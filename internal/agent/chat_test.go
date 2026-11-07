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

package agent

import (
	"context"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/conversation"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

func i32(v int32) *int32 { return &v }

// world is a fake cluster: the warehouse zone with its waypoints, one idle
// robot with navigation and camera_front, a connected adapter, and the
// draft gate served.
func world(t *testing.T, robots ...*swarmv1.Robot) client.Client {
	t.Helper()
	z := &swarmv1.FleetZone{ObjectMeta: metav1.ObjectMeta{Name: "warehouse-a", Namespace: "warehouse-a"}}
	z.Spec.Waypoints = []swarmv1.Waypoint{{Name: "receiving-dock-1", X: 5.1, Y: 2.1}, {Name: "staging-shelf-a", X: 12.1, Y: 8.1}, {Name: "charging-bay", X: 1.1, Y: 1.1}}
	ad := &swarmv1.FleetAdapter{ObjectMeta: metav1.ObjectMeta{Name: "sim-fleet-adapter", Namespace: "warehouse-a"}}
	ad.Status.Phase, ad.Status.Conformance = swarmv1.FleetAdapterPhaseConnected, swarmv1.ConformanceStatePassed
	if len(robots) == 0 {
		r := &swarmv1.Robot{ObjectMeta: metav1.ObjectMeta{Name: "sim-robot-002", Namespace: "warehouse-a"}}
		r.Spec.Adapter.Name, r.Spec.Zone = "sim-fleet-adapter", "warehouse-a"
		r.Status.Phase, r.Status.CurrentZone, r.Status.BatteryPercent, r.Status.EstopState = "Idle", "warehouse-a", i32(85), "Normal"
		r.Status.Capabilities = []swarmv1.CapabilityStatusEntry{{Name: "navigation", Status: "Active"}, {Name: "camera_front", Status: "Active"}}
		robots = append(robots, r)
	}
	fail := admissionv1.Fail
	gate := &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "swarmada"}, Webhooks: []admissionv1.ValidatingWebhook{{
		Name: "vfleettask.swarmada.io", FailurePolicy: &fail, SideEffects: ptr(admissionv1.SideEffectClassNone), AdmissionReviewVersions: []string{"v1"},
		Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Update}, Rule: admissionv1.Rule{APIGroups: []string{"swarmada.io"}, Resources: []string{"fleettasks"}}}},
	}}}
	objs := []client.Object{z, ad, gate}
	for _, r := range robots {
		objs = append(objs, r)
	}
	return fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithObjects(objs...).Build()
}

func ptr[T any](v T) *T { return &v }

func chatFixture(t *testing.T, c client.Client, steps ...llm.Step) (*Chat, *conversation.Conversation, func(string) Trace) {
	t.Helper()
	p, err := policy.Load("../../policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalogue.Load("../../catalogue/warehouse-a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	svc := &tools.Service{C: c, Engine: policy.New(p), Identity: "system:serviceaccount:foc-system:foc-mcp", DefaultNamespace: "warehouse-a"}
	cs, closeFn, err := Connect(context.Background(), mcpserver.New(svc))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	chat := &Chat{Catalogue: cat}
	conv := &conversation.Conversation{Owner: "sam", Backend: &llm.Scripted{Steps: steps}}
	send := func(msg string) Trace {
		tr, err := chat.Turn(context.Background(), cs, conv, "sam", "s-chat-test", msg)
		if err != nil {
			t.Fatalf("%q: %v", msg, err)
		}
		return tr
	}
	return chat, conv, send
}

func requests(t *testing.T, c client.Client) []focv1.ApprovalRequest {
	t.Helper()
	var l focv1.ApprovalRequestList
	if err := c.List(context.Background(), &l); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

// The guided path end to end: Q1 first, the numbered answer read by
// foc-agent, a preview with checks, and the request filed only on "1",
// with the preview's parameters and the checks recorded.
func TestGuidedInspectFiledOnlyAfterConfirmation(t *testing.T) {
	c := world(t)
	_, conv, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "inspect", "fields": map[string]any{"location": "receiving-dock-1"}})}},
	)
	tr := send("inspect the receiving dock")
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomeQuestion || tr.Dialogue.QuestionID != "inspect.what" {
		t.Fatalf("turn 1 %+v", tr.Dialogue)
	}
	if !strings.HasSuffix(tr.Final, "What should the robot inspect?\n1 Damage\n2 Location\n3 Both\n4 Something else — describe it") {
		t.Fatalf("turn 1 text %q", tr.Final)
	}
	tr = send("1")
	if tr.Dialogue.Outcome != OutcomePreview || tr.Dialogue.Blocked || tr.Dialogue.Asked != 1 {
		t.Fatalf("turn 2 %+v\n%s", tr.Dialogue, tr.Final)
	}
	for _, want := range []string{"Checks: likely feasible", "Eligible now: inspect: sim-robot-002", "Checked against current fleet state; Swarmada's own acceptance is checked at execution.", "Swarmada records arrival and completion, not what the robot found", "On failure: retries up to 3 times, then alerts (default)", "1 Confirm · 2 Confirm, and prepare a follow-up request if it fails"} {
		if !strings.Contains(tr.Final, want) {
			t.Errorf("preview lacks %q:\n%s", want, tr.Final)
		}
	}
	if n := len(requests(t, c)); n != 0 {
		t.Fatalf("%d requests filed before the confirmation", n)
	}
	previewID := tr.Dialogue.PreviewID
	tr = send("1")
	if tr.Dialogue.Outcome != OutcomeFiled || tr.Dialogue.PreviewID != previewID {
		t.Fatalf("turn 3 %+v %s", tr.Dialogue, tr.Final)
	}
	rs := requests(t, c)
	if len(rs) != 1 {
		t.Fatalf("%d requests", len(rs))
	}
	r := rs[0]
	if r.Spec.Preview == nil || r.Spec.Preview.ID != previewID || r.Spec.Preview.Kind != "inspect-damage" || r.Spec.Preview.ResultReported || r.Spec.Checks == nil || r.Spec.Checks.Verdict != "likely feasible" {
		t.Fatalf("request spec %+v", r.Spec)
	}
	if loc := r.Spec.DraftTask.Actions[0].Location; loc == nil || loc.Waypoint != "receiving-dock-1" || loc.X != "5.1" {
		t.Fatalf("location %+v", loc)
	}
	if conv.Preview != nil || conv.State.Active() {
		t.Fatal("conversation not reset after filing")
	}
}

// A request without a confirmed preview is never filed: the model is not
// offered the request tools, and a call it makes anyway is not sent.
func TestModelCannotFile(t *testing.T) {
	c := world(t)
	chat, _, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("r1", "request_zone_stop", map[string]any{"zoneName": "warehouse-a", "reason": "spill"})}},
		llm.Step{Final: "Requested."},
	)
	_ = chat
	tr := send("stop warehouse-a")
	if len(tr.Calls) != 1 || !tr.Calls[0].IsError || tr.Calls[0].ErrorText != "tool not offered" {
		t.Fatalf("calls %+v", tr.Calls)
	}
	if n := len(requests(t, c)); n != 0 {
		t.Fatalf("%d requests filed by the model", n)
	}
}

// A blocked preview cannot be confirmed (owner's instruction on phase 0:
// Charge below the battery floor).
func TestBlockedPreviewCannotBeConfirmed(t *testing.T) {
	r := &swarmv1.Robot{ObjectMeta: metav1.ObjectMeta{Name: "sim-robot-001", Namespace: "warehouse-a"}}
	r.Spec.Adapter.Name, r.Spec.Zone = "sim-fleet-adapter", "warehouse-a"
	r.Spec.Constraints = &swarmv1.ClassConstraints{MinBatteryPctForAction: i32(25)}
	r.Status.Phase, r.Status.CurrentZone, r.Status.BatteryPercent, r.Status.EstopState = "Idle", "warehouse-a", i32(21), "Normal"
	c := world(t, r)
	_, _, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "charge", "fields": map[string]any{"robot": "sim-robot-001"}})}},
	)
	tr := send("charge sim-robot-001")
	if tr.Dialogue.Outcome != OutcomePreview || !tr.Dialogue.Blocked || strings.Join(tr.Dialogue.Blockers, ",") != "CHARGE_BELOW_BATTERY_FLOOR" {
		t.Fatalf("preview %+v\n%s", tr.Dialogue, tr.Final)
	}
	if !strings.Contains(tr.Final, "Alternative: charge sim-robot-001 by hand") {
		t.Fatalf("no alternative:\n%s", tr.Final)
	}
	tr = send("1")
	if tr.Dialogue.Outcome != OutcomeBlocked {
		t.Fatalf("confirm %+v", tr.Dialogue)
	}
	if n := len(requests(t, c)); n != 0 {
		t.Fatalf("%d requests filed from a blocked preview", n)
	}
}

// Something else: "4" with a description keeps the words verbatim and the
// model reads them; not possible (work no robot can do, R73) files nothing.
func TestSomethingElseFlows(t *testing.T) {
	c := world(t)
	_, conv, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "inspect"})}},
		llm.Step{Calls: []llm.ToolCall{call("p2", ProposeTool, map[string]any{"somethingElse": map[string]any{"interpretation": "notPossible", "need": "C3", "reason": "it needs an external analysis service"}})}},
	)
	send("inspect something")
	tr := send("4 run the vision service on the camera stream and count pallet 7's boxes")
	if tr.Dialogue.Outcome != OutcomeNotPossible || tr.Dialogue.Instruction != "run the vision service on the camera stream and count pallet 7's boxes" || !strings.Contains(tr.Final, "Nothing was filed") {
		t.Fatalf("not possible %+v %q", tr.Dialogue, tr.Final)
	}
	if conv.State.Active() || len(requests(t, c)) != 0 {
		t.Fatal("state left or request filed")
	}
}

// R73: the finding fallback (a model's "not possible" for a photo, mapped to
// the matching inspect kind) only produces a preview. Nothing is filed on
// the turn that maps it, nor on a reply that is not a confirmation ("3");
// the one
// request is filed on the person's "1", with that preview's id; cancelling
// files nothing.
func TestFindingFallbackOnlyPreviewsUntilConfirmed(t *testing.T) {
	steps := []llm.Step{
		{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "inspect", "fields": map[string]any{"location": "receiving-dock-1"}})}},
		{Calls: []llm.ToolCall{call("p2", ProposeTool, map[string]any{"somethingElse": map[string]any{"interpretation": "notPossible", "need": "C2", "reason": "it needs a photo"}})}},
	}
	for _, last := range []string{"1", "4"} {
		t.Run("then "+last, func(t *testing.T) {
			c := world(t)
			_, conv, send := chatFixture(t, c, steps...)
			send("inspect the receiving dock")
			tr := send("4 check the extinguisher is in its bracket; I need a photo")
			if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomePreview || tr.Dialogue.Kind != "inspect-location" || tr.Dialogue.Blocked {
				t.Fatalf("the fallback did not give an open preview: %+v\n%s", tr.Dialogue, tr.Final)
			}
			if !strings.Contains(tr.Final, "Not reported: photos or other evidence") || !strings.Contains(tr.Final, "1 Confirm") {
				t.Fatalf("preview text:\n%s", tr.Final)
			}
			if conv.Preview == nil || tr.Dialogue.PreviewID == "" {
				t.Fatal("no preview stored to confirm")
			}
			previewID := tr.Dialogue.PreviewID
			if n := len(requests(t, c)); n != 0 {
				t.Fatalf("%d request(s) filed by the fallback itself", n)
			}
			// A reply that is not a confirmation ("3", change something) files
			// nothing and keeps the preview.
			tr = send("3")
			if n := len(requests(t, c)); n != 0 || tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomeQuestion || conv.Preview == nil {
				t.Fatalf("after a non-confirmation: %d requests, %+v", n, tr.Dialogue)
			}
			tr = send(last)
			rs := requests(t, c)
			if last == "4" {
				if tr.Dialogue.Outcome != OutcomeCancelled || len(rs) != 0 {
					t.Fatalf("cancel: %+v, %d requests", tr.Dialogue, len(rs))
				}
				return
			}
			if tr.Dialogue.Outcome != OutcomeFiled || len(rs) != 1 || rs[0].Spec.Preview == nil || rs[0].Spec.Preview.ID != previewID || rs[0].Spec.Preview.Kind != "inspect-location" {
				t.Fatalf("confirm: %+v, %d requests", tr.Dialogue, len(rs))
			}
		})
	}
}
