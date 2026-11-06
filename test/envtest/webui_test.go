//go:build envtest

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

package envtest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/requestview"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
	"github.com/AlexBahel/fleet-ops-copilot/internal/webui"
)

// uiFixture is foc-agent's HTTP side running as ServiceAccount foc-agent
// (real RBAC), with its agent loop reaching the real tool server (as
// foc-mcp, verifying bearer tokens with TokenReview) over streamable HTTP,
// and foc-approvals running as its own ServiceAccount.
type uiFixture struct {
	h    *harness
	mcp  *httptest.Server
	ts   *httptest.Server
	mu   sync.Mutex
	next []llm.Step
}

func (f *uiFixture) script(steps ...llm.Step) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = steps
}

func newUIFixture(t *testing.T, namespaces []string) *uiFixture {
	t.Helper()
	h := newHarness(t, nil)
	f := &uiFixture{h: h, mcp: httptest.NewServer(mcpserver.Handler(mcpserver.New(h.svc)))}
	t.Cleanup(f.mcp.Close)
	cat, err := catalogue.Load(repoRoot + "/catalogue/warehouse-a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	runner := &webui.AgentRunner{
		BackendName: "scripted",
		Catalogue:   cat,
		Transport:   webui.MCPURL(f.mcp.URL),
		NewBackend: func() (llm.Backend, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return &llm.Scripted{Steps: f.next}, nil
		},
	}
	ui := &webui.Server{Runner: runner, Requests: asSA(t, agentUser), Namespaces: namespaces, Redactor: redact.New()}
	f.ts = httptest.NewServer(ui.Handler())
	t.Cleanup(f.ts.Close)
	return f
}

func (f *uiFixture) get(t *testing.T, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.ts.URL+path, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

type chatOut struct {
	Answer         string `json:"answer"`
	Error          string `json:"error"`
	ConversationID string `json:"conversationId"`
	Dialogue       *struct {
		Outcome string `json:"outcome"`
	} `json:"dialogue"`
	ToolCalls []struct {
		Name, PolicyCode, Created string
		Refused                   bool
	} `json:"toolCalls"`
}

// chat sends one message as the page does (no token) and returns the name
// of the ApprovalRequest it filed.
func (f *uiFixture) chat(t *testing.T, operator, message string, steps ...llm.Step) string {
	t.Helper()
	return f.chatAs(t, "", operator, message, steps...)
}

// chatAs sends one message with a bearer token, as foc chat does, then
// confirms the preview it shows with "1" in the same conversation (D74).
func (f *uiFixture) chatAs(t *testing.T, token, operator, message string, steps ...llm.Step) string {
	t.Helper()
	f.script(steps...)
	post := func(body map[string]string) chatOut {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, f.ts.URL+"/api/chat", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		var out chatOut
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil || res.StatusCode != 200 {
			t.Fatalf("chat: %d %+v %v", res.StatusCode, out, err)
		}
		return out
	}
	out := post(map[string]string{"operator": operator, "message": message})
	if out.Dialogue == nil || out.Dialogue.Outcome != "preview" {
		t.Fatalf("chat showed no preview: %+v", out)
	}
	out = post(map[string]string{"operator": operator, "conversationId": out.ConversationID, "message": "1"})
	for _, c := range out.ToolCalls {
		if strings.HasPrefix(c.Created, "ApprovalRequest/") {
			return strings.TrimPrefix(c.Created, "ApprovalRequest/")
		}
	}
	t.Fatalf("chat filed no request: %+v", out)
	return ""
}

// proposeStop is the model's turn for a zone stop request.
func proposeStop(zone, reason string) llm.Step {
	return llm.Step{Calls: []llm.ToolCall{call("propose_task", map[string]any{"ask": "zone-stop", "fields": map[string]any{"zone": zone, "reason": reason}})}}
}

func (f *uiFixture) views(t *testing.T) map[string]requestview.View {
	t.Helper()
	code, body := f.get(t, "/api/requests")
	if code != 200 {
		t.Fatalf("/api/requests: %d %s", code, body)
	}
	var out struct{ Items []requestview.View }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	m := map[string]requestview.View{}
	for _, v := range out.Items {
		m[v.Name] = v
	}
	return m
}

func call(name string, args map[string]any) llm.ToolCall {
	b, _ := json.Marshal(args)
	return llm.ToolCall{ID: "c-" + name, Name: name, Args: b}
}

// The web UI, as ServiceAccount foc-agent, lists pending requests with the
// exact write and the commands to decide them. After a person approves with
// their own credentials (what `foc approve` runs), the object foc-approvals
// wrote is the one the panel showed, and the panel shows the decision. The
// requests are filed through the tool server as operator sam (not from the
// page, whose requests cannot be approved; see TestWebUIRequestCannotBeApproved).
func TestWebUIShowsTheWriteThatIsExecuted(t *testing.T) {
	ctx := context.Background()
	f := newUIFixture(t, []string{swarmNS})
	h := f.h
	zone := ensureZone(t, "ui-zone")
	ensureZone(t, swarmNS)
	ensureRobot(t, "envtest-robot-1", swarmNS, 90)
	if code, body := f.get(t, "/readyz"); code != 200 {
		t.Fatalf("readyz as foc-agent: %d %s", code, body)
	}
	res, err := h.draft(ctx, sess, tools.DraftTaskInput{Name: "ui-draft-1", Purpose: "stage pallet", Actions: []tools.DraftActionInput{
		{Name: "go", Type: "Navigate", Zone: swarmNS, Priority: "Normal", RequiredCapabilities: []string{"navigation"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	draftName := reqName(t, res)
	res, err = h.stop(ctx, sess, tools.ZoneStopInput{ZoneName: "ui-zone", Reason: "ui: spill in aisle 4"})
	if err != nil {
		t.Fatal(err)
	}
	stopName := reqName(t, res)

	before := f.views(t)
	d, s := before[draftName], before[stopName]
	for _, v := range []requestview.View{d, s} {
		if v.State != "Pending" || v.RequestedFor != "sam" || v.RequestedBy != mcpUser ||
			v.Approve != "foc approve "+v.Name+" -n "+swarmNS || v.Reject != "foc reject "+v.Name+" -n "+swarmNS {
			t.Fatalf("pending view %+v", v)
		}
	}
	if d.Change.Target != "FleetTask "+swarmNS+"/ui-draft-1" || !strings.Contains(d.Change.Body, requestview.ApproverPlaceholder) {
		t.Fatalf("draft change %+v", d.Change)
	}
	if s.Change.Body != `{"metadata":{"annotations":{"swarmada.io/estop-triggered":"ui: spill in aisle 4"}}}` {
		t.Fatalf("zone stop change %+v", s.Change)
	}
	// Nothing is written before a person decides.
	var task swarmv1.FleetTask
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "ui-draft-1"}, &task); !apierrors.IsNotFound(err) {
		t.Fatalf("FleetTask before approval: %v", err)
	}

	for _, n := range []string{draftName, stopName} {
		if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, n, focv1.VerdictApprove, ""); err != nil {
			t.Fatal(err)
		}
		waitState(t, n, focv1.StateExecuted)
	}

	// Every field of the body the panel showed (placeholder replaced by the
	// approver) is in the FleetTask foc-approvals created, unchanged. The API
	// server adds the CRD's defaults for fields the body leaves out; the
	// panel says so and does not list them.
	var shown map[string]any
	if err := yaml.Unmarshal([]byte(strings.ReplaceAll(d.Change.Body, requestview.ApproverPlaceholder, "dana")), &shown); err != nil {
		t.Fatal(err)
	}
	var task2 swarmv1.FleetTask
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "ui-draft-1"}, &task2); err != nil {
		t.Fatal(err)
	}
	task2.APIVersion, task2.Kind = swarmv1.GroupVersion.String(), "FleetTask"
	b, _ := json.Marshal(task2)
	var stored map[string]any
	_ = json.Unmarshal(b, &stored)
	if diff := notIn(shown, stored, ""); len(diff) > 0 {
		t.Fatalf("panel body fields not in the created FleetTask: %v", diff)
	}
	t.Logf("fields the API server added to the shown spec: %v", notIn(stored["spec"], shown["spec"], "spec"))
	var z swarmv1.FleetZone
	if err := admin.Get(ctx, client.ObjectKeyFromObject(zone), &z); err != nil {
		t.Fatal(err)
	}
	if z.Annotations[fleet.EstopAnnotation] != "ui: spill in aisle 4" {
		t.Fatalf("zone annotations %v", z.Annotations)
	}

	after := f.views(t)
	for _, n := range []string{draftName, stopName} {
		v := after[n]
		if v.State != "Executed" || v.DecidedBy != "dana" || v.Approve != "" || v.Reject != "" {
			t.Fatalf("decided view %+v", v)
		}
	}
	if !strings.Contains(after[draftName].Change.Body, "approved-by: dana") {
		t.Fatalf("executed draft body %s", after[draftName].Change.Body)
	}
}

// A request filed from the page records the typed name as an unverified
// requester. foc-approvals refuses every approval of it (R38) and writes
// nothing; a rejection is accepted. The panel offers only the reject command.
func TestWebUIRequestCannotBeApproved(t *testing.T) {
	ctx := context.Background()
	f := newUIFixture(t, []string{swarmNS})
	zone := ensureZone(t, "ui-zone-3")
	name := f.chat(t, "sam", "stop ui-zone-3", proposeStop("ui-zone-3", "typed in the UI"))
	v := f.views(t)[name]
	if v.RequestedFor != policy.UnverifiedPrefix+"sam" || v.Approve != "" || v.Reject != "foc reject "+name+" -n "+swarmNS ||
		!strings.Contains(v.Unapprovable, string(policy.CodeRequesterUnverified)) {
		t.Fatalf("panel view %+v", v)
	}

	// dana holds every permission the stop needs, is not the requester, and
	// approves with their own credentials: refused.
	out, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, focv1.VerdictApprove, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var r focv1.ApprovalRequest
	for {
		if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: name}, &r); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(r.Status.RefusedDecisions, out.Decision) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("decision %s not refused: state %q, message %q", out.Decision, r.Status.State, r.Status.Message)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if r.Status.State != focv1.StatePending || !strings.Contains(r.Status.Message, "never approved") {
		t.Fatalf("after approve: state %q message %q", r.Status.State, r.Status.Message)
	}
	var z swarmv1.FleetZone
	if err := admin.Get(ctx, client.ObjectKeyFromObject(zone), &z); err != nil {
		t.Fatal(err)
	}
	if _, stopped := z.Annotations[fleet.EstopAnnotation]; stopped {
		t.Fatal("zone stopped after a refused approval")
	}
	verifyAudit(t, f.h, audit.KindDecisionRefused)
	if !strings.Contains(string(f.h.auditBuf.Bytes()), string(policy.CodeRequesterUnverified)) {
		t.Fatal("audit trail has no DENY_REQUESTER_UNVERIFIED")
	}

	// A rejection of the same request is accepted.
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, focv1.VerdictReject, "filed from the UI"); err != nil {
		t.Fatal(err)
	}
	waitState(t, name, focv1.StateRejected)
	if v := f.views(t)[name]; v.State != "Rejected" || v.DecidedBy != "dana" || v.Reject != "" || v.Unapprovable != "" {
		t.Fatalf("rejected view %+v", v)
	}
}

// notIn lists the paths of a's leaves that b lacks or holds with another value.
func notIn(a, b any, path string) []string {
	switch av := a.(type) {
	case map[string]any:
		bv, _ := b.(map[string]any)
		var out []string
		for k, x := range av {
			out = append(out, notIn(x, bv[k], path+"."+k)...)
		}
		return out
	case []any:
		bv, _ := b.([]any)
		if len(bv) != len(av) {
			return []string{path + " (length)"}
		}
		var out []string
		for i := range av {
			out = append(out, notIn(av[i], bv[i], path+"[]")...)
		}
		return out
	default:
		if !reflect.DeepEqual(a, b) {
			return []string{path}
		}
		return nil
	}
}

// foc-agent's own credentials cannot decide, change or create anything: real
// requests as its ServiceAccount are refused by the API server.
func TestFocAgentCannotWrite(t *testing.T) {
	ctx := context.Background()
	agent := asSA(t, agentUser)
	toolServer := asSA(t, mcpUser)
	r := &focv1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{GenerateName: "areq-", Namespace: swarmNS}, Spec: focv1.ApprovalRequestSpec{
		Type: focv1.RequestZoneStop, RequestedFor: "sam", RequestedBy: mcpUser, ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)),
		Policy: focv1.PolicyRecord{Effect: "RequireApproval", Code: "REQUIRE_APPROVAL"}, ZoneStop: &focv1.ZoneStopParams{ZoneName: "ui-zone-2", Reason: "r"},
	}}
	if err := toolServer.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	var got focv1.ApprovalRequest
	if err := agent.Get(ctx, client.ObjectKeyFromObject(r), &got); err != nil {
		t.Fatalf("foc-agent cannot read the request: %v", err)
	}
	zone := ensureZone(t, "ui-zone-2")
	for name, try := range map[string]func() error{
		"create ApprovalDecision": func() error {
			return agent.Create(ctx, &focv1.ApprovalDecision{ObjectMeta: metav1.ObjectMeta{GenerateName: "adec-", Namespace: swarmNS},
				Spec: focv1.ApprovalDecisionSpec{RequestRef: r.Name, Verdict: focv1.VerdictApprove, DecidedBy: agentUser}})
		},
		"patch request status": func() error {
			return agent.Status().Patch(ctx, got.DeepCopy(), client.RawPatch("application/merge-patch+json", []byte(`{"status":{"state":"Approved"}}`)))
		},
		"create ApprovalRequest": func() error {
			c := r.DeepCopy()
			c.Name, c.ResourceVersion, c.Spec.RequestedBy = "", "", agentUser
			return agent.Create(ctx, c)
		},
		"delete ApprovalRequest": func() error { return agent.Delete(ctx, got.DeepCopy()) },
		"patch FleetZone": func() error {
			return mergePatch(t, agent, zone, map[string]any{"metadata": map[string]any{"annotations": map[string]string{fleet.EstopAnnotation: "x"}}})
		},
		"create FleetTask": func() error { return agent.Create(ctx, draftTask("ui-agent-draft", true, "foc-approvals")) },
		"list Robots":      func() error { return agent.List(ctx, &swarmv1.RobotList{}, client.InNamespace(swarmNS)) },
	} {
		if err := try(); !apierrors.IsForbidden(err) {
			t.Errorf("foc-agent %s: got %v, want Forbidden", name, err)
		}
	}
}

// Outside the namespaces it is bound in, foc-agent reads nothing: the panel
// reports the refusal and the pod is not ready.
func TestWebUIOutsideGrantedNamespace(t *testing.T) {
	f := newUIFixture(t, []string{focNS})
	if code, body := f.get(t, "/api/requests"); code != http.StatusBadGateway || !strings.Contains(body, "forbidden") {
		t.Fatalf("/api/requests: %d %s", code, body)
	}
	if code, body := f.get(t, "/readyz"); code != http.StatusServiceUnavailable || !strings.Contains(body, `"name":"approvalrequests","ok":false`) {
		t.Fatalf("/readyz: %d %s", code, body)
	}
}
