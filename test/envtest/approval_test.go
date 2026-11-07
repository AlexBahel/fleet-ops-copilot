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
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/approval"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/authn"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// syncBuffer is a goroutine-safe audit sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte{}, s.b.Bytes()...)
}

type harness struct {
	svc      *tools.Service
	auditBuf *syncBuffer
	engine   *policy.Engine
}

// newHarness starts fleetops-approvals (as its ServiceAccount) in a manager and
// returns a tools.Service acting as fleetops-mcp.
func newHarness(t *testing.T, mutate func(*policy.File)) *harness {
	t.Helper()
	p, err := policy.Load(repoRoot + "/policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(p)
	}
	eng := policy.New(p)
	buf := &syncBuffer{}
	aw := audit.NewWriter(buf, redact.New())
	scheme := fleet.NewScheme()

	apCfg := saConfig(approvUser)
	apClient, err := client.New(apCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(apCfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{swarmNS: {}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &approval.Reconciler{
		C: apClient, Engine: eng, Authz: approval.SARAuthorizer{C: apClient}, Audit: aw,
		Events:        &audit.EventSink{C: apClient, Scheme: scheme, Component: "fleetops-approvals", Red: redact.New()},
		ObservePeriod: 200 * time.Millisecond,
	}
	ctrlName := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if err := ctrl.NewControllerManagedBy(mgr).Named(ctrlName).For(&fleetopsv1.ApprovalRequest{}).
		Watches(&fleetopsv1.ApprovalDecision{}, approval.DecisionToRequest()).
		Watches(&swarmv1.FleetTask{}, approval.TaskToRequest()).Complete(rec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	mcpClient := asSA(t, mcpUser)
	svc := &tools.Service{
		C: mcpClient, Engine: eng, Audit: aw, Identity: mcpUser, DefaultNamespace: swarmNS,
		Events: &audit.EventSink{C: mcpClient, Scheme: scheme, Component: "fleetops-mcp", Red: redact.New()},
		Authn:  &authn.TokenReviewer{C: mcpClient},
	}
	return &harness{svc: svc, auditBuf: buf, engine: eng}
}

func approverFor(t *testing.T, person string) *approval.Approver {
	t.Helper()
	a, err := approval.NewApprover(people[person])
	if err != nil {
		t.Fatal(err)
	}
	a.Poll, a.Timeout = 100*time.Millisecond, 20*time.Second
	return a
}

func waitState(t *testing.T, name string, want fleetopsv1.RequestState) *fleetopsv1.ApprovalRequest {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var r fleetopsv1.ApprovalRequest
	for time.Now().Before(deadline) {
		if err := admin.Get(context.Background(), client.ObjectKey{Namespace: swarmNS, Name: name}, &r); err == nil && r.Status.State == want {
			return &r
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("request %s: state %q, message %q; want %q", name, r.Status.State, r.Status.Message, want)
	return nil
}

func reqName(t *testing.T, res tools.RequestResult) string {
	t.Helper()
	if res.Refused || !strings.HasPrefix(res.Created, "ApprovalRequest/") {
		t.Fatalf("expected an ApprovalRequest, got %+v", res)
	}
	return strings.TrimPrefix(res.Created, "ApprovalRequest/")
}

func verifyAudit(t *testing.T, h *harness, kinds ...audit.Kind) {
	t.Helper()
	data := h.auditBuf.Bytes()
	if _, err := audit.Verify(bytes.NewReader(data)); err != nil {
		t.Fatalf("audit chain: %v", err)
	}
	for _, k := range kinds {
		if !bytes.Contains(data, []byte(`"kind":"`+string(k)+`"`)) {
			t.Errorf("audit trail has no %s entry", k)
		}
	}
}

var sess = tools.Session{ID: "s-envtest", Operator: "sam"}

// TestApprovalPathZoneMaintenance: request -> person approves -> exactly one
// ZoneMaintenance created by fleetops-approvals with autoResumeAfterMinutes 0.
func TestApprovalPathZoneMaintenance(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	ensureZone(t, "zm-zone")
	res, err := h.maintenance(ctx, sess, tools.ZoneMaintenanceInput{ZoneName: "zm-zone", Mode: "Graceful", Reason: "floor cleaning"})
	if err != nil {
		t.Fatal(err)
	}
	name := reqName(t, res)
	waitState(t, name, fleetopsv1.StatePending)
	// Nothing written before approval.
	var zms swarmv1.ZoneMaintenanceList
	if err := admin.List(ctx, &zms, client.InNamespace(swarmNS), client.MatchingLabels{fleet.RequestLabel: name}); err != nil || len(zms.Items) != 0 {
		t.Fatalf("ZoneMaintenance exists before approval: %v %d", err, len(zms.Items))
	}
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, "ok"); err != nil {
		t.Fatal(err)
	}
	r := waitState(t, name, fleetopsv1.StateExecuted)
	if r.Status.DecidedBy != "dana" || r.Status.Target == nil || r.Status.Target.Kind != "ZoneMaintenance" {
		t.Fatalf("status %+v", r.Status)
	}
	if err := admin.List(ctx, &zms, client.InNamespace(swarmNS), client.MatchingLabels{fleet.RequestLabel: name}); err != nil || len(zms.Items) != 1 {
		t.Fatalf("want exactly one ZoneMaintenance, got %d (%v)", len(zms.Items), err)
	}
	zm := zms.Items[0]
	if zm.Spec.AutoResumeAfterMinutes != 0 || zm.Spec.Scope.ZoneName != "zm-zone" || zm.Spec.Mode != swarmv1.ZoneMaintenanceModeGraceful {
		t.Fatalf("unexpected ZoneMaintenance spec %+v", zm.Spec)
	}
	var evs corev1.EventList
	if err := admin.List(ctx, &evs, client.InNamespace(swarmNS)); err != nil {
		t.Fatal(err)
	}
	reasons := map[string]bool{}
	for _, e := range evs.Items {
		if e.InvolvedObject.Name == name {
			reasons[e.Reason] = true
		}
	}
	for _, want := range []string{"RequestCreated", "Approved", "Executed"} {
		if !reasons[want] {
			t.Errorf("no %s Event on the request (have %v)", want, reasons)
		}
	}
	verifyAudit(t, h, audit.KindRequestCreated, audit.KindApproved, audit.KindExecuted)
}

// TestApprovalPathZoneStop: request -> approve -> only the estop annotation is added.
func TestApprovalPathZoneStop(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	z := ensureZone(t, "stop-zone")
	res, err := h.stop(ctx, sess, tools.ZoneStopInput{ZoneName: "stop-zone", Reason: "spill in aisle 3"})
	if err != nil {
		t.Fatal(err)
	}
	name := reqName(t, res)
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, name, fleetopsv1.StateExecuted)
	var got swarmv1.FleetZone
	if err := admin.Get(ctx, client.ObjectKeyFromObject(z), &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[fleet.EstopAnnotation] != "spill in aisle 3" || got.Annotations["site/owner"] != "east-wing" {
		t.Fatalf("annotations %v", got.Annotations)
	}
	// With Swarmada's estop-actor mutation in place (stand-in), the stop is
	// attributed to the executor's ServiceAccount; the approver is in the request.
	if got.Annotations[testenv.EstopActorAnn] != approvUser || !slices.Contains(got.Finalizers, testenv.EstopInForceFinz) {
		t.Fatalf("estop actor/finalizer: %v %v", got.Annotations, got.Finalizers)
	}
	// A second request for the same zone is refused: it is already stopped.
	res2, err := h.stop(ctx, sess, tools.ZoneStopInput{ZoneName: "stop-zone", Reason: "again"})
	if err != nil || !res2.Refused || res2.Code != string(policy.CodeAlreadyStopped) {
		t.Fatalf("second stop: %+v %v", res2, err)
	}
	verifyAudit(t, h, audit.KindApproved, audit.KindExecuted)
}

// TestApprovalPathDraftAndActivation: request_draft_task creates only an
// ApprovalRequest; after a named approval fleetops-approvals creates the FleetTask
// held as a draft; request_activate_task is approved and the approver's own
// credentials clear spec.draft.
func TestApprovalPathDraftAndActivation(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	ensureZone(t, swarmNS)
	ensureRobot(t, "envtest-robot-1", swarmNS, 90)
	res, err := h.draft(ctx, sess, tools.DraftTaskInput{Name: "move-to-staging", Purpose: "stage pallet", Actions: []tools.DraftActionInput{
		{Name: "go", Type: "Navigate", Zone: swarmNS, Priority: "Normal", RequiredCapabilities: []string{"navigation"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	draftReq := reqName(t, res)
	var task swarmv1.FleetTask
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "move-to-staging"}, &task); err == nil {
		t.Fatal("FleetTask exists before approval")
	}
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, draftReq, fleetopsv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	r := waitState(t, draftReq, fleetopsv1.StateExecuted)
	if r.Status.Target == nil || r.Status.Target.Kind != "FleetTask" || r.Status.Target.Name != "move-to-staging" {
		t.Fatalf("target %+v", r.Status.Target)
	}
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "move-to-staging"}, &task); err != nil {
		t.Fatal(err)
	}
	if !task.Spec.Draft || task.Labels[fleet.CreatedByLabel] != "fleetops-approvals" || task.Labels[fleet.RequestLabel] != draftReq || len(task.Spec.Actions) != 1 {
		t.Fatalf("draft task draft=%v labels=%v actions=%d", task.Spec.Draft, task.Labels, len(task.Spec.Actions))
	}
	// Neither copilot ServiceAccount can activate it.
	for _, sa := range []string{mcpUser, approvUser} {
		if err := mergePatch(t, asSA(t, sa), &task, map[string]any{"spec": map[string]any{"draft": false}}); err == nil {
			t.Fatalf("%s activated a draft", sa)
		}
	}
	act, err := h.activate(ctx, sess, tools.ActivateTaskInput{TaskName: "move-to-staging"})
	if err != nil {
		t.Fatal(err)
	}
	name := reqName(t, act)
	out, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, "")
	if err != nil {
		t.Fatal(err)
	}
	want := approval.ActivationCommand(swarmNS, name)
	if out.State != fleetopsv1.StateApproved || out.Instruction != want {
		t.Fatalf("outcome %+v, want instruction %q", out, want)
	}
	r = waitState(t, name, fleetopsv1.StateApproved)
	if r.Status.Instruction != want {
		t.Fatalf("status.instruction %q", r.Status.Instruction)
	}
	// The copilot does not activate: after approval the task is still a draft.
	time.Sleep(600 * time.Millisecond)
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "move-to-staging"}, &task); err != nil || !task.Spec.Draft {
		t.Fatalf("task activated by the copilot: draft=%v %v", task.Spec.Draft, err)
	}
	waitState(t, name, fleetopsv1.StateApproved)
	// A person without patch/activate cannot run the instruction.
	if err := mergePatch(t, asPerson(t, "eve"), &task, map[string]any{"spec": map[string]any{"draft": false}}); err == nil {
		t.Fatal("eve activated the draft")
	}
	// dana activates with a plain kubectl-style merge patch, not fleetops activate:
	// it works, and is recorded as such (R72).
	if err := mergePatch(t, asPerson(t, "dana"), &task, map[string]any{"spec": map[string]any{"draft": false}}); err != nil {
		t.Fatalf("dana's activation refused: %v", err)
	}
	r = waitState(t, name, fleetopsv1.StateExecuted)
	if r.Status.DecidedBy != "dana" || r.Status.Target == nil || r.Status.Target.Name != "move-to-staging" {
		t.Fatalf("status %+v", r.Status)
	}
	if r.Status.ActivatedBy != "unknown" || r.Status.ActivationRef != "" || !strings.Contains(r.Status.Message, approval.OutsideFleetopsActivate) {
		t.Fatalf("plain patch: activatedBy %q ref %q message %q", r.Status.ActivatedBy, r.Status.ActivationRef, r.Status.Message)
	}
	// Activating again is refused: the task is no longer a draft.
	again, err := h.activate(ctx, sess, tools.ActivateTaskInput{TaskName: "move-to-staging"})
	if err != nil || !again.Refused || again.Code != string(policy.CodeTaskNotDraft) {
		t.Fatalf("second activation: %+v %v", again, err)
	}
	// A draft request for an existing name is refused before any request is made.
	dup, err := h.draft(ctx, sess, tools.DraftTaskInput{Name: "move-to-staging", Purpose: "p", Actions: []tools.DraftActionInput{{Name: "a", Type: "Navigate"}}})
	if err != nil || !dup.Refused {
		t.Fatalf("duplicate draft: %+v %v", dup, err)
	}
	verifyAudit(t, h, audit.KindRequestCreated, audit.KindApproved, audit.KindExecuted, audit.KindActivationObserved)
}

// TestApprovalRefusals: the requester, a person without Swarmada authority,
// and a stale request cannot lead to a write; a rejection is terminal.
func TestApprovalRefusals(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	ensureZone(t, "refuse-zone")
	res, err := h.maintenance(ctx, sess, tools.ZoneMaintenanceInput{ZoneName: "refuse-zone", Reason: "inspection"})
	if err != nil {
		t.Fatal(err)
	}
	name := reqName(t, res)
	waitState(t, name, fleetopsv1.StatePending)

	// sam asked for the change: self-approval refused.
	if _, err := approverFor(t, "sam").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	// eve holds the approver role but cannot create ZoneMaintenances herself.
	if _, err := approverFor(t, "eve").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var r fleetopsv1.ApprovalRequest
	for time.Now().Before(deadline) {
		_ = admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: name}, &r)
		if len(r.Status.RefusedDecisions) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(r.Status.RefusedDecisions) != 2 || r.Status.State != fleetopsv1.StatePending {
		t.Fatalf("want 2 refused decisions and Pending, got %v %s (%s)", r.Status.RefusedDecisions, r.Status.State, r.Status.Message)
	}
	data := string(h.auditBuf.Bytes())
	for _, code := range []policy.Code{policy.CodeSelfApproval, policy.CodeApproverNotAuthorized} {
		if !strings.Contains(data, string(code)) {
			t.Errorf("audit has no %s", code)
		}
	}
	// dana rejects: terminal, nothing written.
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, fleetopsv1.VerdictReject, "not today"); err != nil {
		t.Fatal(err)
	}
	waitState(t, name, fleetopsv1.StateRejected)
	var zms swarmv1.ZoneMaintenanceList
	if err := admin.List(ctx, &zms, client.InNamespace(swarmNS), client.MatchingLabels{fleet.RequestLabel: name}); err != nil || len(zms.Items) != 0 {
		t.Fatalf("ZoneMaintenance written after refusals/rejection: %d", len(zms.Items))
	}
	// A later approval of a terminal request changes nothing.
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	waitState(t, name, fleetopsv1.StateRejected)
	verifyAudit(t, h, audit.KindDecisionRefused, audit.KindRejected)
}

// TestApprovalExpiry: a request whose TTL passes is Expired and never executed.
func TestApprovalExpiry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, func(p *policy.File) { p.Approval.TTL = policy.Duration{Duration: 2 * time.Second} })
	ensureZone(t, "expiry-zone")
	res, err := h.stop(ctx, sess, tools.ZoneStopInput{ZoneName: "expiry-zone", Reason: "drill"})
	if err != nil {
		t.Fatal(err)
	}
	name := reqName(t, res)
	waitState(t, name, fleetopsv1.StateExpired)
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	var z swarmv1.FleetZone
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "expiry-zone"}, &z); err != nil {
		t.Fatal(err)
	}
	if _, stopped := z.Annotations[fleet.EstopAnnotation]; stopped {
		t.Fatal("expired request was executed")
	}
	verifyAudit(t, h, audit.KindExpired)
}

// TestPolicyRefusalsCreateNothing: out-of-policy requests through the tools
// create no object at all.
func TestPolicyRefusalsCreateNothing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	ensureZone(t, "policy-zone")
	before := countRequests(t)
	f := false
	cases := []struct {
		name string
		run  func() (tools.RequestResult, error)
		code policy.Code
	}{
		{"auto-resume", func() (tools.RequestResult, error) {
			return h.maintenance(ctx, sess, tools.ZoneMaintenanceInput{ZoneName: "policy-zone", Reason: "r", AutoResumeAfterMinutes: 30})
		}, policy.CodeRelease},
		{"estop gate off", func() (tools.RequestResult, error) {
			return h.maintenance(ctx, sess, tools.ZoneMaintenanceInput{ZoneName: "policy-zone", Reason: "r", RequireEstopClearBeforeResume: &f})
		}, policy.CodeField},
		{"unknown zone", func() (tools.RequestResult, error) {
			return h.stop(ctx, sess, tools.ZoneStopInput{ZoneName: "nope", Reason: "r"})
		}, policy.CodeZoneNotFound},
		{"other namespace", func() (tools.RequestResult, error) {
			return h.stop(ctx, sess, tools.ZoneStopInput{Namespace: "default", ZoneName: "z", Reason: "r"})
		}, policy.CodeNamespace},
		{"critical draft", func() (tools.RequestResult, error) {
			return h.draft(ctx, sess, tools.DraftTaskInput{Name: "crit", Purpose: "p", Actions: []tools.DraftActionInput{{Name: "a", Type: "Navigate", Priority: "Critical"}}})
		}, policy.CodeField},
		{"no session identity", func() (tools.RequestResult, error) {
			return h.stop(ctx, tools.Session{}, tools.ZoneStopInput{ZoneName: "policy-zone", Reason: "r"})
		}, policy.CodeMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.run()
			if err != nil || !res.Refused || res.Code != string(c.code) {
				t.Fatalf("got %+v %v, want refusal %s", res, err, c.code)
			}
		})
	}
	if after := countRequests(t); after != before {
		t.Fatalf("refusals created %d ApprovalRequests", after-before)
	}
	var task swarmv1.FleetTask
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "crit"}, &task); err == nil {
		t.Fatal("refused draft was created")
	}
}

func countRequests(t *testing.T) int {
	t.Helper()
	var l fleetopsv1.ApprovalRequestList
	if err := admin.List(context.Background(), &l, client.InNamespace(swarmNS)); err != nil {
		t.Fatal(err)
	}
	return len(l.Items)
}

// TestDraftGateFailsClosed: with no fail-closed webhook covering FleetTask
// updates, no draft is created.
func TestDraftGateFailsClosed(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	gate, name, err := fleet.DraftGate(ctx, h.svc.C)
	if err != nil || !gate {
		t.Fatalf("stand-in gate not detected: %v %v", gate, err)
	}
	// Switch the stand-in to Ignore: it no longer counts as a gate.
	if err := setStandinFailurePolicy(ctx, "Ignore"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = setStandinFailurePolicy(context.Background(), "Fail") })
	res, err := h.draft(ctx, sess, tools.DraftTaskInput{Name: "gate-off", Purpose: "p", Actions: []tools.DraftActionInput{{Name: "a", Type: "Navigate"}}})
	if err != nil || !res.Refused || res.Code != string(policy.CodeDraftGateAbsent) {
		t.Fatalf("got %+v %v (gate was %s)", res, err, name)
	}
}
