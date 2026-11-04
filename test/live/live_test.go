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
//	FOC_LIVE_ADMIN_CONTEXT        cluster-admin context (setup reads, tokens, kubectl exec)
//	FOC_LIVE_APPROVER_CONTEXT     context of a person who holds fleetops:approver and swarmada:fleet-manager
//	FOC_LIVE_MCP_URL              foc-mcp streamable HTTP endpoint, e.g. http://127.0.0.1:18080/mcp
//	FOC_LIVE_FOC                  path to the foc binary
//	FOC_LIVE_PHASE                "no-webhooks" (Swarmada serves no admission webhooks) or "webhooks"
//	FOC_LIVE_EVIDENCE_DIR         optional; the executor's audit file is copied there
package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

const (
	ns       = "warehouse-a"
	zone     = "warehouse-a"
	operator = "sam" // the fictional shift supervisor who asks for the change
	execSA   = "system:serviceaccount:foc-system:foc-approvals"
)

type env struct {
	adminCtx, approverCtx, mcpURL, focBin, evidence string
	admin                                           client.Client
	adminCS                                         *kubernetes.Clientset
	adminCfg                                        *rest.Config
	approverCfg                                     *rest.Config
	approverUser                                    string
	mcp                                             *mcp.ClientSession
	session                                         string
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
	if os.Getenv("FOC_LIVE_PHASE") != phase {
		t.Skipf("FOC_LIVE_PHASE is not %q", phase)
	}
	e := &env{
		adminCtx: mustEnv(t, "FOC_LIVE_ADMIN_CONTEXT"), approverCtx: mustEnv(t, "FOC_LIVE_APPROVER_CONTEXT"),
		mcpURL: mustEnv(t, "FOC_LIVE_MCP_URL"), focBin: mustEnv(t, "FOC_LIVE_FOC"), evidence: os.Getenv("FOC_LIVE_EVIDENCE_DIR"),
		session: fmt.Sprintf("live-%d", time.Now().Unix()),
	}
	var err error
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
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "foc-live-test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: e.mcpURL}, nil)
	if err != nil {
		t.Fatalf("connecting to foc-mcp at %s: %v", e.mcpURL, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	e.mcp = cs
	return e
}

// call invokes a request tool on the deployed foc-mcp as the given operator.
func (e *env) call(t *testing.T, op, tool string, args map[string]any) tools.RequestResult {
	t.Helper()
	res, err := e.mcp.CallTool(context.Background(), &mcp.CallToolParams{
		Meta: mcp.Meta{mcpserver.MetaSession: e.session, mcpserver.MetaOperator: op}, Name: tool, Arguments: args,
	})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var out tools.RequestResult
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: decoding %s: %v", tool, b, err)
	}
	t.Logf("%s by %s -> %s", tool, op, b)
	return out
}

func requestName(t *testing.T, r tools.RequestResult) string {
	t.Helper()
	if r.Refused || !strings.HasPrefix(r.Created, "ApprovalRequest/") {
		t.Fatalf("expected an ApprovalRequest, got %+v", r)
	}
	return strings.TrimPrefix(r.Created, "ApprovalRequest/")
}

// foc runs the foc CLI as the approver (a person, with their own kubeconfig context).
func (e *env) foc(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(e.focBin, args...)
	out, err := cmd.CombinedOutput()
	t.Logf("$ foc %s\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("foc %v: %v", args, err)
	}
	return string(out)
}

func (e *env) waitState(t *testing.T, name string, want focv1.RequestState, d time.Duration) *focv1.ApprovalRequest {
	t.Helper()
	var r focv1.ApprovalRequest
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
	cmd := exec.Command("kubectl", "--context", e.adminCtx, "-n", "foc-system", "exec", "deploy/foc-approvals", "--",
		"/busybox/cat", "/var/lib/fleetops/audit/foc-approvals.jsonl")
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
	name := fmt.Sprintf("foc-live-nogate-%d", time.Now().Unix())
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
	draft := fmt.Sprintf("foc-live-draft-%d", stamp)
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
		e.foc(t, "approve", req, "-n", ns, "--context", e.approverCtx, "-comment", "live check")
		ar := e.waitState(t, req, focv1.StateExecuted, 60*time.Second)
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
		if ft.Labels[fleet.CreatedByLabel] != "foc-approvals" || ft.Labels[fleet.RequestLabel] != req {
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
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "foc-live-probe", Namespace: ns}}
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "foc-live-probe-fleet-manager", Namespace: ns},
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
		for _, who := range []struct{ ns, sa string }{{ns, "foc-live-probe"}, {"foc-system", "foc-approvals"}, {"foc-system", "foc-mcp"}, {"foc-system", "foc-agent"}} {
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
		out := e.foc(t, "approve", req, "-n", ns, "--context", e.approverCtx)
		host := "API server: " + e.approverCfg.Host
		iHost, iNS := strings.Index(out, host), strings.Index(out, "namespace:  "+ns)
		iCmd := strings.Index(out, "\n  kubectl --context "+e.approverCtx+" patch fleettask "+draft)
		if iHost < 0 || iNS < 0 || iCmd < 0 || iHost > iCmd || iNS > iCmd {
			t.Fatalf("foc approve must show %q and the namespace above the activation command; got:\n%s", host, out)
		}
		line := strings.TrimSpace(strings.SplitN(out[iCmd+1:], "\n", 2)[0])
		// The person runs the printed command with their own credentials.
		cmd := exec.Command("sh", "-c", line)
		b, err := cmd.CombinedOutput()
		t.Logf("$ %s\n%s", line, b)
		if err != nil {
			t.Fatalf("the approver's activation command failed: %v", err)
		}
		e.waitState(t, req, focv1.StateExecuted, 60*time.Second)
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
	})

	t.Run("zone-maintenance-by-executor", func(t *testing.T) {
		r := e.call(t, operator, "request_zone_maintenance", map[string]any{
			"zoneName": zone, "mode": "Graceful", "reason": "live check: floor cleaning after the wave (fictional)",
		})
		req := requestName(t, r)
		e.foc(t, "approve", req, "-n", ns, "--context", e.approverCtx)
		ar := e.waitState(t, req, focv1.StateExecuted, 60*time.Second)
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
		e.foc(t, "approve", req, "-n", ns, "--context", e.approverCtx)
		e.waitState(t, req, focv1.StateExecuted, 60*time.Second)
		time.Sleep(5 * time.Second)
		var z swarmv1.FleetZone
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: zone}, &z); err != nil {
			t.Fatal(err)
		}
		t.Logf("FleetZone %s annotations=%v finalizers=%v status.estopStatus=%v", zone, z.Annotations, z.Finalizers, z.Status.EstopStatus)
		if z.Annotations[fleet.EstopAnnotation] != stopReason {
			t.Errorf("%s = %q", fleet.EstopAnnotation, z.Annotations[fleet.EstopAnnotation])
		}
		if z.Annotations["swarmada.io/estop-actor"] != execSA {
			t.Errorf("swarmada.io/estop-actor = %q, want %q", z.Annotations["swarmada.io/estop-actor"], execSA)
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
		for _, sa := range []string{"foc-agent", "foc-mcp", "foc-approvals"} {
			c := e.saClient(t, "foc-system", sa)
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

	t.Run("executor-writes-only-after-named-approval", func(t *testing.T) {
		// No decision: nothing is written.
		pendingDraft := fmt.Sprintf("foc-live-pending-%d", stamp)
		req := requestName(t, e.call(t, operator, "request_draft_task", map[string]any{
			"name": pendingDraft, "purpose": "live check: no decision, no write",
			"actions": []map[string]any{{"name": "inspect", "type": "Navigate", "zone": zone}},
		}))
		time.Sleep(15 * time.Second)
		var ar focv1.ApprovalRequest
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: req}, &ar); err != nil {
			t.Fatal(err)
		}
		var ft swarmv1.FleetTask
		err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: pendingDraft}, &ft)
		t.Logf("request %s with no decision after 15s: state=%s; FleetTask/%s: %v", req, ar.Status.State, pendingDraft, err)
		if !apierrors.IsNotFound(err) {
			t.Errorf("FleetTask %s exists without a decision", pendingDraft)
		}
		e.foc(t, "reject", req, "-n", ns, "--context", e.approverCtx, "-comment", "live check cleanup")
		e.waitState(t, req, focv1.StateRejected, 60*time.Second)
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: pendingDraft}, &ft); !apierrors.IsNotFound(err) {
			t.Errorf("FleetTask %s exists after rejection: %v", pendingDraft, err)
		}

		// The requester approving their own request: refused, nothing written.
		selfDraft := fmt.Sprintf("foc-live-self-%d", stamp)
		req = requestName(t, e.call(t, e.approverUser, "request_draft_task", map[string]any{
			"name": selfDraft, "purpose": "live check: self-approval is refused",
			"actions": []map[string]any{{"name": "inspect", "type": "Navigate", "zone": zone}},
		}))
		e.foc(t, "approve", req, "-n", ns, "--context", e.approverCtx)
		time.Sleep(10 * time.Second)
		if err := e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: req}, &ar); err != nil {
			t.Fatal(err)
		}
		err = e.admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: selfDraft}, &ft)
		t.Logf("self-approved request %s: state=%s refusedDecisions=%v message=%q; FleetTask/%s: %v", req, ar.Status.State, ar.Status.RefusedDecisions, ar.Status.Message, selfDraft, err)
		if len(ar.Status.RefusedDecisions) == 0 || ar.Status.State == focv1.StateApproved || ar.Status.State == focv1.StateExecuted {
			t.Error("self-approval was not refused")
		}
		if !apierrors.IsNotFound(err) {
			t.Errorf("FleetTask %s exists after a self-approval", selfDraft)
		}

		// Every object the executor created traces to an approval by a person.
		var reqs focv1.ApprovalRequestList
		if err := e.admin.List(ctx, &reqs, client.InNamespace(ns)); err != nil {
			t.Fatal(err)
		}
		byName := map[string]focv1.ApprovalRequest{}
		for _, r := range reqs.Items {
			byName[r.Name] = r
		}
		var fts swarmv1.FleetTaskList
		var zms swarmv1.ZoneMaintenanceList
		_ = e.admin.List(ctx, &fts, client.InNamespace(ns), client.MatchingLabels{fleet.CreatedByLabel: "foc-approvals"})
		_ = e.admin.List(ctx, &zms, client.InNamespace(ns), client.MatchingLabels{fleet.CreatedByLabel: "foc-approvals"})
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
		t.Logf("foc-approvals audit file: %d entries, chain intact", n)
		for _, en := range auditEntries(t, b) {
			if en.Kind == audit.KindExecuted && strings.HasPrefix(en.Object, "FleetZone/") {
				line, _ := json.Marshal(en)
				t.Logf("audit record (zone stop executed): %s", line)
				break
			}
		}
		if e.evidence != "" {
			if err := os.WriteFile(filepath.Join(e.evidence, "foc-approvals-audit.jsonl"), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})
}
