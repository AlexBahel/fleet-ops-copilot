//go:build live

/*
Copyright 2026 Alex Bahel

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package live runs the copilot's approval paths against a live cluster that
// runs Swarmada v0.4 with its admission webhooks served, and the copilot
// deployed from config/ (make deploy-kind). Unlike test/envtest, nothing here
// is a stand-in: Swarmada's own webhooks admit or refuse every write.
//
// Required environment (hack/live-approval-path.sh sets it):
//
//	KUBECONFIG                    kubeconfig holding both contexts below
//	FLEETOPS_LIVE_ADMIN_CONTEXT        cluster-admin context (setup reads, tokens, kubectl exec)
//	FLEETOPS_LIVE_APPROVER_CONTEXT     context of a person who authenticates with a bearer token (dana) and holds fleetops:approver and swarmada:fleet-manager
//	FLEETOPS_LIVE_TOKENS_FILE          the people's static token file (hack/kind-people.sh gen): requests are filed with the
//	                              requester's own bearer token, so the requester is verified (D57, R43)
//	FLEETOPS_LIVE_MCP_URL              fleetops-mcp streamable HTTP endpoint, e.g. http://127.0.0.1:18080/mcp
//	FLEETOPS_LIVE_FLEETOPS                  path to the fleetops binary
//	FLEETOPS_LIVE_PHASE                "no-webhooks" (Swarmada serves no admission webhooks) or "webhooks"
//	FLEETOPS_LIVE_EVIDENCE_DIR         optional; the executor's audit file is copied there
package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

const (
	ns       = "warehouse-a"
	zone     = "warehouse-a"
	operator = "sam" // the fictional shift supervisor who asks for the change
	execSA   = "system:serviceaccount:fleetops-system:fleetops-approvals"
)

type env struct {
	adminCtx, approverCtx, mcpURL, fleetopsBin, evidence string
	tokens                                               map[string]string // person -> bearer token (never logged)
	sessions                                             map[string]*mcp.ClientSession
	admin                                                client.Client
	adminCS                                              *kubernetes.Clientset
	adminCfg                                             *rest.Config
	approverCfg                                          *rest.Config
	approverUser                                         string
	session                                              string
}

// readTokens parses a kube-apiserver static token file: token,user,uid,"groups".
func readTokens(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.SplitN(l, ",", 4)
		if len(f) < 3 || f[0] == "" || f[1] == "" {
			return nil, fmt.Errorf("%s: line is not token,user,uid,groups", path)
		}
		out[f[1]] = f[0]
	}
	return out, nil
}

func mustEnv(t *testing.T, k string) string {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s not set", k)
	}
	return v
}

func setup(t *testing.T, phase string) *env {
	t.Helper()
	if os.Getenv("FLEETOPS_LIVE_PHASE") != phase {
		t.Skipf("FLEETOPS_LIVE_PHASE is not %q", phase)
	}
	e := &env{
		adminCtx: mustEnv(t, "FLEETOPS_LIVE_ADMIN_CONTEXT"), approverCtx: mustEnv(t, "FLEETOPS_LIVE_APPROVER_CONTEXT"),
		mcpURL: mustEnv(t, "FLEETOPS_LIVE_MCP_URL"), fleetopsBin: mustEnv(t, "FLEETOPS_LIVE_FLEETOPS"), evidence: os.Getenv("FLEETOPS_LIVE_EVIDENCE_DIR"),
		session:  fmt.Sprintf("live-%d", time.Now().Unix()),
		sessions: map[string]*mcp.ClientSession{},
	}
	var err error
	if e.tokens, err = readTokens(mustEnv(t, "FLEETOPS_LIVE_TOKENS_FILE")); err != nil {
		t.Fatal(err)
	}
	if e.adminCfg, err = config.GetConfigWithContext(e.adminCtx); err != nil {
		t.Fatal(err)
	}
	if e.admin, err = client.New(e.adminCfg, client.Options{Scheme: fleet.NewScheme()}); err != nil {
		t.Fatal(err)
	}
	if e.adminCS, err = kubernetes.NewForConfig(e.adminCfg); err != nil {
		t.Fatal(err)
	}
	if e.approverCfg, err = config.GetConfigWithContext(e.approverCtx); err != nil {
		t.Fatal(err)
	}
	acs, err := kubernetes.NewForConfig(e.approverCfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := acs.AuthenticationV1().SelfSubjectReviews().Create(context.Background(), &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.approverUser = r.Status.UserInfo.Username
	t.Logf("approver identity (SelfSubjectReview): %s groups=%v", e.approverUser, r.Status.UserInfo.Groups)
	if strings.HasPrefix(e.approverUser, "system:serviceaccount:") {
		t.Fatal("the approver context must be a person, not a ServiceAccount")
	}
	return e
}

// mcpSession returns the MCP session that sends person's bearer token with
// every request ("" sends none, so the requester is unverified).
func (e *env) mcpSession(t *testing.T, person string) *mcp.ClientSession {
	t.Helper()
	if cs, ok := e.sessions[person]; ok {
		return cs
	}
	token := ""
	if person != "" {
		if token = e.tokens[person]; token == "" {
			t.Fatalf("no token for %q in FLEETOPS_LIVE_TOKENS_FILE", person)
		}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "fleetops-live-test", Version: "0"}, nil).
		Connect(context.Background(), mcpserver.ClientTransport(e.mcpURL, token), nil)
	if err != nil {
		t.Fatalf("connecting to fleetops-mcp at %s as %q: %v", e.mcpURL, person, err)
	}
	// The session belongs to the (sub)test that opened it: closed when that
	// test ends and dropped from the cache, so a later subtest reconnects
	// instead of reusing a closed session.
	t.Cleanup(func() { _ = cs.Close(); delete(e.sessions, person) })
	e.sessions[person] = cs
	return cs
}

// callAs invokes a request tool on the deployed fleetops-mcp with person's bearer
// token ("" for none) and the operator name asserted in _meta, which fleetops-mcp
// ignores for a verified requester (D57).
func (e *env) callAs(t *testing.T, person, asserted, tool string, args map[string]any) tools.RequestResult {
	t.Helper()
	res, err := e.mcpSession(t, person).CallTool(context.Background(), &mcp.CallToolParams{
		Meta: mcp.Meta{mcpserver.MetaSession: e.session, mcpserver.MetaOperator: asserted, mcpserver.MetaConfirmedPreview: confirmFor(t, tool, args)},
		Name: tool, Arguments: args,
	})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var out tools.RequestResult
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: decoding %s: %v", tool, b, err)
	}
	t.Logf("%s by %s (token of %q, asserted %q) -> %s", tool, asserted, person, asserted, b)
	return out
}

// confirmFor is the id of the preview a person confirmed for this request
// (D74): the test acts as the person's own client, which sends it.
func confirmFor(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(args)
	var in any
	switch tool {
	case "request_draft_task":
		var v tools.DraftTaskInput
		_ = json.Unmarshal(b, &v)
		in = v
	case "request_activate_task":
		var v tools.ActivateTaskInput
		_ = json.Unmarshal(b, &v)
		in = v
	case "request_zone_maintenance":
		var v tools.ZoneMaintenanceInput
		_ = json.Unmarshal(b, &v)
		in = v
	case "request_zone_stop":
		var v tools.ZoneStopInput
		_ = json.Unmarshal(b, &v)
		in = v
	default:
		return ""
	}
	id, err := tools.PreviewIDFor(in)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// requestedFor returns spec.requestedFor of an ApprovalRequest.
func (e *env) requestedFor(t *testing.T, name string) string {
	t.Helper()
	var r fleetopsv1.ApprovalRequest
	if err := e.admin.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &r); err != nil {
		t.Fatal(err)
	}
	return r.Spec.RequestedFor
}

// call files a request as the person op: with op's own bearer token, so the
// requester is verified. A request that was created must name op as its
// requester, not "unverified:op".
func (e *env) call(t *testing.T, op, tool string, args map[string]any) tools.RequestResult {
	t.Helper()
	out := e.callAs(t, op, op, tool, args)
	if name, ok := strings.CutPrefix(out.Created, "ApprovalRequest/"); ok {
		if got := e.requestedFor(t, name); got != op {
			t.Fatalf("ApprovalRequest %s: requestedFor %q, want %q (the token must make the requester verified)", name, got, op)
		}
	}
	return out
}

func requestName(t *testing.T, r tools.RequestResult) string {
	t.Helper()
	if r.Refused || !strings.HasPrefix(r.Created, "ApprovalRequest/") {
		t.Fatalf("expected an ApprovalRequest, got %+v", r)
	}
	return strings.TrimPrefix(r.Created, "ApprovalRequest/")
}

// fleetops runs the fleetops CLI as the approver (a person, with their own kubeconfig context).
func (e *env) fleetops(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(e.fleetopsBin, args...)
	out, err := cmd.CombinedOutput()
	t.Logf("$ fleetops %s\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("fleetops %v: %v", args, err)
	}
	return string(out)
}

func (e *env) waitState(t *testing.T, name string, want fleetopsv1.RequestState, d time.Duration) *fleetopsv1.ApprovalRequest {
	t.Helper()
	var r fleetopsv1.ApprovalRequest
	deadline := time.Now().Add(d)
	for {
		if err := e.admin.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &r); err != nil {
			t.Fatal(err)
		}
		if r.Status.State == want {
			b, _ := json.Marshal(r.Status)
			t.Logf("ApprovalRequest %s status: %s", name, b)
			return &r
		}
		if time.Now().After(deadline) {
			b, _ := json.Marshal(r.Status)
			t.Fatalf("ApprovalRequest %s: state %q after %s, want %q; status %s", name, r.Status.State, d, want, b)
		}
		time.Sleep(time.Second)
	}
}

// saClient returns a client that authenticates with a real token of the
// ServiceAccount (TokenRequest API), not by impersonation. The token is never logged.
func (e *env) saClient(t *testing.T, saNS, sa string) client.Client {
	t.Helper()
	exp := int64(600)
	tr, err := e.adminCS.CoreV1().ServiceAccounts(saNS).CreateToken(context.Background(), sa,
		&authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{ExpirationSeconds: &exp}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := rest.AnonymousClientConfig(e.adminCfg)
	cfg.BearerToken = tr.Status.Token
	c, err := client.New(cfg, client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) auditFile(t *testing.T) []byte {
	t.Helper()
	cmd := exec.Command("kubectl", "--context", e.adminCtx, "-n", "fleetops-system", "exec", "deploy/fleetops-approvals", "--",
		"/busybox/cat", "/var/lib/fleetops/audit/fleetops-approvals.jsonl")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("reading the executor audit file: %v: %s", err, errb.String())
	}
	return out.Bytes()
}

func auditEntries(t *testing.T, b []byte) []audit.Entry {
	t.Helper()
	var out []audit.Entry
	for _, l := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var en audit.Entry
		if err := json.Unmarshal(l, &en); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		out = append(out, en)
	}
	return out
}

func logYAML(t *testing.T, label string, v any) {
	t.Helper()
	b, _ := json.MarshalIndent(v, "", "  ")
	t.Logf("%s:\n%s", label, b)
}

// TestLiveDraftGateAbsent runs against Swarmada installed without admission
// webhooks (the quickstart's overlay). There the draft flag is not an
// authorization boundary (RFC-0003 #resources-tasktrigger), so the copilot must
// refuse to request a draft (DESIGN.md D22).
func TestLiveDraftGateAbsent(t *testing.T) {
	e := setup(t, "no-webhooks")
	var vwc admissionv1.ValidatingWebhookConfigurationList
	if err := e.admin.List(context.Background(), &vwc); err != nil {
		t.Fatal(err)
	}
	for _, c := range vwc.Items {
		for _, w := range c.Webhooks {
			t.Logf("validating webhook served: %s/%s", c.Name, w.Name)
		}
	}
	name := fmt.Sprintf("fleetops-live-nogate-%d", time.Now().Unix())
	r := e.call(t, operator, "request_draft_task", map[string]any{
		"name": name, "purpose": "live check: draft gate absent",
		"actions": []map[string]any{{"name": "inspect", "type": "Navigate", "zone": zone}},
	})
	if !r.Refused || r.Code != "DENY_DRAFT_GATE_ABSENT" {
		t.Fatalf("want refusal DENY_DRAFT_GATE_ABSENT, got %+v", r)
	}
	var ft swarmv1.FleetTask
	if err := e.admin.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &ft); !apierrors.IsNotFound(err) {
		t.Fatalf("FleetTask %s: want NotFound, got %v", name, err)
	}
}

// TestLiveApprovalPaths runs every approval path against Swarmada v0.4 with
// its admission webhooks served. Subtests run in order and share state.
func TestLiveApprovalPaths(t *testing.T) {
	e := setup(t, "webhooks")
	ctx := context.Background()
	stamp := time.Now().Unix()
	draft := fmt.Sprintf("fleetops-live-draft-%d", stamp)
	var zmName string
	stopReason := fmt.Sprintf("live check %d: spill reported in aisle (fictional)", stamp)

	ok := t.Run("swarmada-webhooks-served", func(t *testing.T) {
		var vwc admissionv1.ValidatingWebhookConfigurationList
		if err := e.admin.List(ctx, &vwc); err != nil {
			t.Fatal(err)
		}
		gate := false
		for _, c := range vwc.Items {
			for _, w := range c.Webhooks {
				t.Logf("validating: %s/%s failurePolicy=%s", c.Name, w.Name, *w.FailurePolicy)
				if w.Name == "vfleettask.swarmada.io" && *w.FailurePolicy == admissionv1.Fail {
					gate = true
				}
			}
		}
		var mwc admissionv1.MutatingWebhookConfigurationList
		if err := e.admin.List(ctx, &mwc); err != nil {
			t.Fatal(err)
		}
		for _, c := range mwc.Items {
			for _, w := range c.Webhooks {
				t.Logf("mutating: %s/%s failurePolicy=%s", c.Name, w.Name, *w.FailurePolicy)
			}
		}
		if !gate {
			t.Fatal("vfleettask.swarmada.io with failurePolicy Fail is not configured")
		}
	})
	if !ok {
		t.FailNow()
	}

	t.Run("draft-created-by-executor", func(t *testing.T) {
		r := e.call(t, operator, "request_draft_task", map[string]any{
			"name": draft, "purpose": "live check: held draft created by the executor after a named approval",
			"actions": []map[string]any{{"name": "inspect", "type": "Navigate", "zone": zone}},
		})
		req := requestName(t, r)
		e.fleetops(t, "approve", req, "-n", ns, "--context", e.approverCtx, "-comment", "live check")
		ar := e.waitState(t, req, fleetopsv1.StateExecuted, 60*time.Second)
		if ar.Status.DecidedBy != e.approverUser {
			t.Errorf("decidedBy %q, want %q", ar.Status.DecidedBy, e.approverUser)
		}
		var ft swarmv1.FleetTask
		deadline := time.Now().Add(30 * time.Second)
		for {
			if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: draft}, &ft); err != nil {
				t.Fatal(err)
			}
			if ft.Status.Phase != "" || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}
		ft.ManagedFields = nil
		logYAML(t, "FleetTask "+draft, ft)
		if !ft.Spec.Draft {
			t.Error("spec.draft is not true")
		}
		if string(ft.Status.Phase) != "Draft" {
			t.Errorf("status.phase %q, want Draft", ft.Status.Phase)
		}
		if ft.Labels[fleet.CreatedByLabel] != "fleetops-approvals" || ft.Labels[fleet.RequestLabel] != req {
			t.Errorf("labels %v", ft.Labels)
		}
		time.Sleep(10 * time.Second)
		var fas swarmv1.FleetActionList
		if err := e.admin.List(ctx, &fas, client.InNamespace(ns)); err != nil {
			t.Fatal(err)
		}
		for _, a := range fas.Items {
			for _, o := range a.OwnerReferences {
				if o.Name == draft {
					t.Errorf("FleetAction %s generated for the draft", a.Name)
				}
			}
		}
	})

	t.Run("activate-refused-to-serviceaccounts", func(t *testing.T) {
		// A ServiceAccount that RBAC allows to activate (swarmada:fleet-manager)
		// is still refused by Swarmada's FleetTask webhook.
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "fleetops-live-probe", Namespace: ns}}
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "fleetops-live-probe-fleet-manager", Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "swarmada:fleet-manager"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: ns}},
		}
		for _, o := range []client.Object{sa, rb} {
			if err := e.admin.Create(ctx, o); err != nil && !apierrors.IsAlreadyExists(err) {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { _ = e.admin.Delete(ctx, rb); _ = e.admin.Delete(ctx, sa) })
		time.Sleep(2 * time.Second)
		patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"draft":false}}`))
		for _, who := range []struct{ ns, sa string }{{ns, "fleetops-live-probe"}, {"fleetops-system", "fleetops-approvals"}, {"fleetops-system", "fleetops-mcp"}, {"fleetops-system", "fleetops-agent"}} {
			c := e.saClient(t, who.ns, who.sa)
			err := c.Patch(ctx, &swarmv1.FleetTask{ObjectMeta: metav1.ObjectMeta{Name: draft, Namespace: ns}}, patch)
			t.Logf("activate as system:serviceaccount:%s:%s -> %v", who.ns, who.sa, err)
			if err == nil {
				t.Errorf("%s activated the draft", who.sa)
			}
		}
		var ft swarmv1.FleetTask
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: draft}, &ft); err != nil {
			t.Fatal(err)
		}
		if !ft.Spec.Draft {
			t.Fatal("the draft was activated by a ServiceAccount")
		}
	})

	t.Run("activation-by-person-observed", func(t *testing.T) {
		r := e.call(t, operator, "request_activate_task", map[string]any{"taskName": draft})
		req := requestName(t, r)
		out := e.fleetops(t, "approve", req, "-n", ns, "--context", e.approverCtx)
		host := "API server: " + e.approverCfg.Host
		iHost, iNS := strings.Index(out, host), strings.Index(out, "namespace:  "+ns)
		want := "\n  fleetops activate " + req + " -n " + ns + " --context " + e.approverCtx
		iCmd := strings.Index(out, want)
		if iHost < 0 || iNS < 0 || iCmd < 0 || iHost > iCmd || iNS > iCmd {
			t.Fatalf("fleetops approve must show %q and the namespace above %q; got:\n%s", host, want, out)
		}
		var before swarmv1.FleetTask
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: draft}, &before); err != nil {
			t.Fatal(err)
		}
		// The person runs the printed command with their own credentials:
		// fleetops activate records an Activation ApprovalDecision, then sends the
		// plain spec.draft=false patch through Swarmada's real webhook (R71).
		act := e.fleetops(t, strings.Fields(strings.TrimSpace(out[iCmd+1 : iCmd+len(want)]))[1:]...)
		if !strings.Contains(act, "recorded ApprovalDecision "+req+"-activation-") || !strings.Contains(act, "activated FleetTask "+draft) {
			t.Fatalf("fleetops activate output:\n%s", act)
		}
		e.waitState(t, req, fleetopsv1.StateExecuted, 60*time.Second)
		var evs corev1.EventList
		if err := e.admin.List(ctx, &evs, client.InNamespace(ns)); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, ev := range evs.Items {
			if ev.InvolvedObject.Name == req && ev.Reason == "ActivationObserved" {
				found = true
				t.Logf("Event %s/%s: reason=%s source=%s message=%q", ev.InvolvedObject.Kind, ev.InvolvedObject.Name, ev.Reason, ev.Source.Component, ev.Message)
			}
		}
		if !found {
			t.Error("no ActivationObserved Event on the request")
		}
		found = false
		for _, en := range auditEntries(t, e.auditFile(t)) {
			if en.Kind == audit.KindActivationObserved && strings.Contains(en.Object+fmt.Sprint(en.Detail), req) {
				found = true
				b, _ := json.Marshal(en)
				t.Logf("audit: %s", b)
			}
		}
		if !found {
			t.Error("no activation_observed audit record for the request")
		}
		var ft swarmv1.FleetTask
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: draft}, &ft); err != nil {
			t.Fatal(err)
		}
		t.Logf("FleetTask %s after activation: spec.draft=%v status.phase=%s", draft, ft.Spec.Draft, ft.Status.Phase)
		if ft.Spec.Draft {
			t.Error("still a draft")
		}
		if !maps.Equal(ft.Annotations, before.Annotations) {
			t.Errorf("activation changed the FleetTask's annotations: %v -> %v", before.Annotations, ft.Annotations)
		}
		// The activation record: our own API, bound to the authenticated
		// person, linked by fleetops-approvals.
		var got fleetopsv1.ApprovalRequest
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: req}, &got); err != nil {
			t.Fatal(err)
		}
		var rec fleetopsv1.ApprovalDecision
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: got.Status.ActivationRef}, &rec); err != nil {
			t.Fatalf("activationRef %q: %v", got.Status.ActivationRef, err)
		}
		rec.ManagedFields = nil
		logYAML(t, "ApprovalDecision "+rec.Name, rec)
		if !rec.Spec.IsActivation() || rec.Spec.RequestRef != req || rec.Spec.DecidedBy != got.Status.ActivatedBy || strings.HasPrefix(rec.Spec.DecidedBy, "system:serviceaccount:") {
			t.Errorf("activation record %+v, request activatedBy %q", rec.Spec, got.Status.ActivatedBy)
		}
		if got.Status.ActivatedBy != got.Status.DecidedBy {
			t.Logf("note: activated by %q, approved by %q", got.Status.ActivatedBy, got.Status.DecidedBy)
		}
	})

	// R43/D57 on the live API server: the requester is the person behind the
	// bearer token, whatever name _meta asserts; without a token the requester
	// is unverified:<name> and no one can approve the request.
	t.Run("requester-is-authenticated-not-asserted", func(t *testing.T) {
		draftArgs := func(name string) map[string]any {
			return map[string]any{
				"name": name, "purpose": "live check: the requester is the token's person",
				"actions": []map[string]any{{"name": "inspect", "type": "Navigate", "zone": zone}},
			}
		}
		// sam's token, _meta asserts dana: requestedFor is sam.
		forged := requestName(t, e.callAs(t, "sam", "dana", "request_draft_task", draftArgs(fmt.Sprintf("fleetops-live-forged-%d", stamp))))
		if got := e.requestedFor(t, forged); got != "sam" {
			t.Errorf("requestedFor %q, want sam (the asserted name dana must have no effect)", got)
		}
		// No token, _meta asserts dana: unverified, and dana's own approval is refused.
		anon := fmt.Sprintf("fleetops-live-unverified-%d", stamp)
		unverified := requestName(t, e.callAs(t, "", "dana", "request_draft_task", draftArgs(anon)))
		if got := e.requestedFor(t, unverified); got != "unverified:dana" {
			t.Errorf("requestedFor %q, want unverified:dana", got)
		}
		e.fleetops(t, "approve", unverified, "-n", ns, "--context", e.approverCtx)
		time.Sleep(10 * time.Second)
		var ar fleetopsv1.ApprovalRequest
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: unverified}, &ar); err != nil {
			t.Fatal(err)
		}
		var ft swarmv1.FleetTask
		err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: anon}, &ft)
		t.Logf("approved unverified request %s: state=%s refusedDecisions=%v message=%q; FleetTask/%s: %v", unverified, ar.Status.State, ar.Status.RefusedDecisions, ar.Status.Message, anon, err)
		if len(ar.Status.RefusedDecisions) == 0 || ar.Status.State == fleetopsv1.StateApproved || ar.Status.State == fleetopsv1.StateExecuted {
			t.Error("the approval of an unverified request was not refused")
		}
		if !apierrors.IsNotFound(err) {
			t.Errorf("FleetTask %s exists after approving an unverified request", anon)
		}
		for _, r := range []string{forged, unverified} {
			e.fleetops(t, "reject", r, "-n", ns, "--context", e.approverCtx, "-comment", "live check cleanup")
		}
	})

	t.Run("executor-writes-only-after-named-approval", func(t *testing.T) {
		// No decision: nothing is written.
		pendingDraft := fmt.Sprintf("fleetops-live-pending-%d", stamp)
		req := requestName(t, e.call(t, operator, "request_draft_task", map[string]any{
			"name": pendingDraft, "purpose": "live check: no decision, no write",
			"actions": []map[string]any{{"name": "inspect", "type": "Navigate", "zone": zone}},
		}))
		time.Sleep(15 * time.Second)
		var ar fleetopsv1.ApprovalRequest
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: req}, &ar); err != nil {
			t.Fatal(err)
		}
		var ft swarmv1.FleetTask
		err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: pendingDraft}, &ft)
		t.Logf("request %s with no decision after 15s: state=%s; FleetTask/%s: %v", req, ar.Status.State, pendingDraft, err)
		if !apierrors.IsNotFound(err) {
			t.Errorf("FleetTask %s exists without a decision", pendingDraft)
		}
		e.fleetops(t, "reject", req, "-n", ns, "--context", e.approverCtx, "-comment", "live check cleanup")
		e.waitState(t, req, fleetopsv1.StateRejected, 60*time.Second)
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: pendingDraft}, &ft); !apierrors.IsNotFound(err) {
			t.Errorf("FleetTask %s exists after rejection: %v", pendingDraft, err)
		}

		// The requester approving their own request: refused, nothing written.
		selfDraft := fmt.Sprintf("fleetops-live-self-%d", stamp)
		req = requestName(t, e.call(t, e.approverUser, "request_draft_task", map[string]any{
			"name": selfDraft, "purpose": "live check: self-approval is refused",
			"actions": []map[string]any{{"name": "inspect", "type": "Navigate", "zone": zone}},
		}))
		e.fleetops(t, "approve", req, "-n", ns, "--context", e.approverCtx)
		time.Sleep(10 * time.Second)
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: req}, &ar); err != nil {
			t.Fatal(err)
		}
		err = e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: selfDraft}, &ft)
		t.Logf("self-approved request %s: state=%s refusedDecisions=%v message=%q; FleetTask/%s: %v", req, ar.Status.State, ar.Status.RefusedDecisions, ar.Status.Message, selfDraft, err)
		if len(ar.Status.RefusedDecisions) == 0 || ar.Status.State == fleetopsv1.StateApproved || ar.Status.State == fleetopsv1.StateExecuted {
			t.Error("self-approval was not refused")
		}
		if !apierrors.IsNotFound(err) {
			t.Errorf("FleetTask %s exists after a self-approval", selfDraft)
		}
	})

	t.Run("zone-maintenance-by-executor", func(t *testing.T) {
		r := e.call(t, operator, "request_zone_maintenance", map[string]any{
			"zoneName": zone, "mode": "Graceful", "reason": "live check: floor cleaning after the wave (fictional)",
		})
		req := requestName(t, r)
		e.fleetops(t, "approve", req, "-n", ns, "--context", e.approverCtx)
		ar := e.waitState(t, req, fleetopsv1.StateExecuted, 60*time.Second)
		if ar.Status.Target == nil || ar.Status.Target.Kind != "ZoneMaintenance" {
			t.Fatalf("target %+v", ar.Status.Target)
		}
		zmName = ar.Status.Target.Name
		time.Sleep(5 * time.Second)
		var zm swarmv1.ZoneMaintenance
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: zmName}, &zm); err != nil {
			t.Fatal(err)
		}
		zm.ManagedFields = nil
		logYAML(t, "ZoneMaintenance "+zmName, zm)
		if zm.Spec.AutoResumeAfterMinutes != 0 || zm.Spec.Scope.ZoneName != zone {
			t.Errorf("spec %+v", zm.Spec)
		}
	})

	t.Run("zone-stop-by-executor", func(t *testing.T) {
		r := e.call(t, operator, "request_zone_stop", map[string]any{"zoneName": zone, "reason": stopReason})
		req := requestName(t, r)
		e.fleetops(t, "approve", req, "-n", ns, "--context", e.approverCtx)
		e.waitState(t, req, fleetopsv1.StateExecuted, 60*time.Second)
		time.Sleep(5 * time.Second)
		var z swarmv1.FleetZone
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: zone}, &z); err != nil {
			t.Fatal(err)
		}
		t.Logf("FleetZone %s annotations=%v finalizers=%v status.estopStatus=%v", zone, z.Annotations, z.Finalizers, z.Status.EstopStatus)
		if z.Annotations[fleet.EstopAnnotation] != stopReason {
			t.Errorf("%s = %q", fleet.EstopAnnotation, z.Annotations[fleet.EstopAnnotation])
		}
		// Swarmada's FleetZone mutator stamps swarmada.io/estop-actor at admission; its
		// zone-estop controller seals the actor into the safety audit log and removes the
		// annotation in the same patch that sets swarmada.io/estop-processed
		// (consumeEstopActor, internal/controller/estop_actor.go). So the annotation is
		// either still the executor (not yet processed) or gone once processed; the sealed
		// actor is checked in TestLiveSwarmadaAuditActor.
		actor, hasActor := z.Annotations["swarmada.io/estop-actor"]
		processed := z.Annotations["swarmada.io/estop-processed"]
		switch {
		case hasActor && actor != execSA:
			t.Errorf("swarmada.io/estop-actor = %q, want %q", actor, execSA)
		case !hasActor && processed != stopReason:
			t.Errorf("no swarmada.io/estop-actor and swarmada.io/estop-processed = %q: the stop was not processed", processed)
		case !hasActor:
			t.Logf("swarmada.io/estop-actor consumed by Swarmada's controller after sealing (estop-processed = trigger value)")
		}
		hasFin := false
		for _, f := range z.Finalizers {
			hasFin = hasFin || f == "swarmada.io/estop-in-force"
		}
		if !hasFin {
			t.Error("finalizer swarmada.io/estop-in-force missing")
		}
	})

	t.Run("guardrails-api-server-refuses-serviceaccounts", func(t *testing.T) {
		clear := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"swarmada.io/estop-triggered":null}}}`))
		for _, sa := range []string{"fleetops-agent", "fleetops-mcp", "fleetops-approvals"} {
			c := e.saClient(t, "fleetops-system", sa)
			err := c.Delete(ctx, &swarmv1.FleetTask{ObjectMeta: metav1.ObjectMeta{Name: draft, Namespace: ns}})
			t.Logf("delete FleetTask/%s as %s -> %v", draft, sa, err)
			if err == nil || !(apierrors.IsForbidden(err) || apierrors.IsInvalid(err)) {
				t.Errorf("%s: delete not refused: %v", sa, err)
			}
			if zmName != "" {
				err = c.Delete(ctx, &swarmv1.ZoneMaintenance{ObjectMeta: metav1.ObjectMeta{Name: zmName, Namespace: ns}})
				t.Logf("delete ZoneMaintenance/%s as %s -> %v", zmName, sa, err)
				if err == nil {
					t.Errorf("%s: delete ZoneMaintenance not refused", sa)
				}
			}
			err = c.Patch(ctx, &swarmv1.FleetZone{ObjectMeta: metav1.ObjectMeta{Name: zone, Namespace: ns}}, clear)
			t.Logf("estop-clear (remove %s) on FleetZone/%s as %s -> %v", fleet.EstopAnnotation, zone, sa, err)
			if err == nil {
				t.Errorf("%s: estop-clear not refused", sa)
			}
		}
		var z swarmv1.FleetZone
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: zone}, &z); err != nil {
			t.Fatal(err)
		}
		if _, ok := z.Annotations[fleet.EstopAnnotation]; !ok {
			t.Fatal("the zone stop was cleared")
		}
	})

	// Last before the audit record: the zone stop above leaves the zone stopped,
	// so every subtest that files a draft into it runs before (R65 check).
	t.Run("every-write-traces-to-a-named-approval", func(t *testing.T) {
		// Every object the executor created traces to an approval by a person.
		var reqs fleetopsv1.ApprovalRequestList
		if err := e.admin.List(ctx, &reqs, client.InNamespace(ns)); err != nil {
			t.Fatal(err)
		}
		byName := map[string]fleetopsv1.ApprovalRequest{}
		for _, r := range reqs.Items {
			byName[r.Name] = r
		}
		var fts swarmv1.FleetTaskList
		var zms swarmv1.ZoneMaintenanceList
		_ = e.admin.List(ctx, &fts, client.InNamespace(ns), client.MatchingLabels{fleet.CreatedByLabel: "fleetops-approvals"})
		_ = e.admin.List(ctx, &zms, client.InNamespace(ns), client.MatchingLabels{fleet.CreatedByLabel: "fleetops-approvals"})
		objs := []client.Object{}
		for i := range fts.Items {
			objs = append(objs, &fts.Items[i])
		}
		for i := range zms.Items {
			objs = append(objs, &zms.Items[i])
		}
		for _, o := range objs {
			r, ok := byName[o.GetLabels()[fleet.RequestLabel]]
			t.Logf("%T %s <- request %s state=%s decidedBy=%s requestedFor=%s", o, o.GetName(), r.Name, r.Status.State, r.Status.DecidedBy, r.Spec.RequestedFor)
			if !ok || r.Status.DecidedBy == "" || strings.HasPrefix(r.Status.DecidedBy, "system:serviceaccount:") || r.Status.DecidedBy == r.Spec.RequestedFor {
				t.Errorf("%s has no approval by a named person", o.GetName())
			}
		}
	})

	t.Run("audit-record", func(t *testing.T) {
		b := e.auditFile(t)
		n, err := audit.Verify(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("audit chain: %v after %d entries", err, n)
		}
		t.Logf("fleetops-approvals audit file: %d entries, chain intact", n)
		for _, en := range auditEntries(t, b) {
			if en.Kind == audit.KindExecuted && strings.HasPrefix(en.Object, "FleetZone/") {
				line, _ := json.Marshal(en)
				t.Logf("audit record (zone stop executed): %s", line)
				break
			}
		}
		if e.evidence != "" {
			if err := os.WriteFile(filepath.Join(e.evidence, "fleetops-approvals-audit.jsonl"), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// TestLiveSwarmadaAuditActor checks that Swarmada's own safety audit log
// attributes every zone stop the executor performed to the executor's
// ServiceAccount, with the approved reason. FLEETOPS_LIVE_SWARMADA_AUDIT_FILE is a
// copy of the manager's audit log (hack/live-approval-path.sh fetches it after
// TestLiveApprovalPaths).
func TestLiveSwarmadaAuditActor(t *testing.T) {
	path := mustEnv(t, "FLEETOPS_LIVE_SWARMADA_AUDIT_FILE")
	e := setup(t, "webhooks")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		EventType string `json:"event_type"`
		Actor     struct {
			Type, Identity string
		} `json:"actor"`
		Resource struct{ Kind, Name string } `json:"resource"`
		Outcome  string                      `json:"outcome"`
		Detail   map[string]string           `json:"detail"`
	}
	var entries []entry
	for _, l := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var en entry
		if err := json.Unmarshal(l, &en); err != nil {
			t.Fatalf("Swarmada audit line %q: %v", l, err)
		}
		entries = append(entries, en)
	}
	var reqs fleetopsv1.ApprovalRequestList
	if err := e.admin.List(context.Background(), &reqs, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range reqs.Items {
		if r.Spec.Type != fleetopsv1.RequestZoneStop || r.Status.State != fleetopsv1.StateExecuted {
			continue
		}
		n++
		found := false
		for _, en := range entries {
			if en.EventType == "ESTOP_TRIGGERED" && en.Resource.Kind == "FleetZone" && en.Resource.Name == r.Spec.ZoneStop.ZoneName &&
				en.Detail["reason"] == r.Spec.ZoneStop.Reason {
				found = true
				t.Logf("request %s (approved by %s) -> Swarmada ESTOP_TRIGGERED actor=%s/%s outcome=%s detail=%v",
					r.Name, r.Status.DecidedBy, en.Actor.Type, en.Actor.Identity, en.Outcome, en.Detail)
				if en.Actor.Identity != execSA {
					t.Errorf("request %s: Swarmada recorded actor %q, want %q", r.Name, en.Actor.Identity, execSA)
				}
			}
		}
		if !found {
			t.Errorf("request %s: no ESTOP_TRIGGERED entry for FleetZone/%s with reason %q", r.Name, r.Spec.ZoneStop.ZoneName, r.Spec.ZoneStop.Reason)
		}
	}
	if n == 0 {
		t.Fatal("no executed ZoneStop request to check")
	}
}
