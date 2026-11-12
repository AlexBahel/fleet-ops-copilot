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
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/conversation"
	"github.com/AlexBahel/fleet-ops-copilot/internal/dialogue"
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
func world(t *testing.T, robots ...*swarmv1.Robot) client.WithWatch {
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
	svc := &tools.Service{C: c, Engine: policy.New(p), Identity: "system:serviceaccount:fleetops-system:fleetops-mcp", DefaultNamespace: "warehouse-a"}
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

func requests(t *testing.T, c client.Client) []fleetopsv1.ApprovalRequest {
	t.Helper()
	var l fleetopsv1.ApprovalRequestList
	if err := c.List(context.Background(), &l); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

// The guided path end to end: Q1 first, the numbered answer read by
// fleetops-agent, a preview with checks, and the request filed only on "1",
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

// A blocked preview cannot be confirmed (D73:
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
// model reads them; not possible (work no robot can do, D72) files nothing.
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

// D72: the finding fallback (a model's "not possible" for a photo, mapped to
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

// D84: text the model wrote before the response that calls propose_task is
// shown with the engine's preview, before it, and recorded.
func TestEarlierResponseTextShownWithPreview(t *testing.T) {
	c := world(t)
	_, _, send := chatFixture(t, c,
		llm.Step{Final: "Robot/sim-robot-002 status.phase=Idle.", Calls: []llm.ToolCall{call("r1", "get_robot", map[string]any{"name": "sim-robot-002"})}},
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "inspect", "kind": "inspect-damage", "fields": map[string]any{"location": "receiving-dock-1"}})}},
	)
	tr := send("inspect receiving-dock-1 for damage")
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomePreview {
		t.Fatalf("dialogue %+v", tr.Dialogue)
	}
	if !strings.HasPrefix(tr.Final, "Robot/sim-robot-002 status.phase=Idle.\n\nPreview") {
		t.Fatalf("final does not open with the earlier text, then the preview:\n%s", tr.Final)
	}
	if len(tr.Responses) != 2 || tr.Responses[0] != "Robot/sim-robot-002 status.phase=Idle." {
		t.Fatalf("responses %q", tr.Responses)
	}
}

// D85: propose_task's schema lists the catalogue's asks, kinds and field
// names as enums, and no other field name.
func TestProposeSchemaEnumeratesTheCatalogue(t *testing.T) {
	cat, err := catalogue.Load("../../catalogue/warehouse-a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties map[string]struct {
			Enum                 []string                   `json:"enum"`
			Properties           map[string]json.RawMessage `json:"properties"`
			AdditionalProperties json.RawMessage            `json:"additionalProperties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(ProposeSpec(cat, nil).InputSchema, &s); err != nil {
		t.Fatal(err)
	}
	var asks, kinds []string
	for _, a := range cat.Asks {
		asks = append(asks, a.Name)
	}
	for _, k := range cat.Kinds {
		kinds = append(kinds, k.Name)
	}
	if got, want := strings.Join(s.Properties["ask"].Enum, ","), strings.Join(append(asks, "something-else"), ","); got != want {
		t.Errorf("ask enum %q, want %q", got, want)
	}
	if got, want := strings.Join(s.Properties["kind"].Enum, ","), strings.Join(kinds, ","); got != want {
		t.Errorf("kind enum %q, want %q", got, want)
	}
	f := s.Properties["fields"]
	if string(f.AdditionalProperties) != "false" || len(f.Properties) != len(catalogue.Fields) {
		t.Fatalf("fields: additionalProperties %s, %d properties, want false and %d", f.AdditionalProperties, len(f.Properties), len(catalogue.Fields))
	}
	for _, name := range catalogue.Fields {
		if _, ok := f.Properties[name]; !ok {
			t.Errorf("field %s not in the schema", name)
		}
	}
}

// D85: an unknown field name is refused with a question naming the allowed
// ones, nothing of that call is applied, and the model's next call with the
// right name is used (the recorded guided-12 reading: repeat for times).
func TestUnknownFieldNameRefused(t *testing.T) {
	c := world(t)
	_, conv, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "patrol", "fields": map[string]any{"repeat": "3", "route": "receiving-dock-1,charging-bay"}})}},
		llm.Step{Calls: []llm.ToolCall{call("p2", ProposeTool, map[string]any{"ask": "patrol", "fields": map[string]any{"times": "3", "route": "receiving-dock-1,charging-bay"}})}},
	)
	tr := send("Patrol the dock and the charging bay three times.")
	var first *ToolEvent
	for i := range tr.Calls {
		if tr.Calls[i].Name == ProposeTool {
			first = &tr.Calls[i]
			break
		}
	}
	if first == nil || !first.IsError || !strings.Contains(first.ErrorText, `"repeat"`) || !strings.Contains(first.ErrorText, "times") {
		t.Fatalf("first propose_task not refused with the allowed names: %+v", first)
	}
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomePreview || !strings.Contains(tr.Final, "Times: 3 round(s)") {
		t.Fatalf("dialogue %+v\n%s", tr.Dialogue, tr.Final)
	}
	if _, ok := conv.State.Fields["repeat"]; ok {
		t.Fatal("the unknown field was kept")
	}
}

// D86: a location that is not a waypoint of the catalogue's fleet is caught by
// check_task before the preview is shown: the preview is blocked with
// WAYPOINT_NOT_FOUND, never "likely feasible", and cannot be confirmed.
func TestUnknownLocationBlockedBeforeFiling(t *testing.T) {
	c := world(t)
	_, _, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "inspect", "kind": "inspect-location", "fields": map[string]any{"location": "receiving-dock"}})}},
	)
	tr := send("Inspect the receiving dock.")
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomePreview || !tr.Dialogue.Blocked || !slices.Contains(tr.Dialogue.Blockers, "WAYPOINT_NOT_FOUND") {
		t.Fatalf("preview %+v\n%s", tr.Dialogue, tr.Final)
	}
	if strings.Contains(tr.Final, "likely feasible") {
		t.Fatalf("an unknown location was called likely feasible:\n%s", tr.Final)
	}
	if tr = send("1"); tr.Dialogue.Outcome != OutcomeBlocked {
		t.Fatalf("confirm %+v", tr.Dialogue)
	}
	if n := len(requests(t, c)); n != 0 {
		t.Fatalf("%d requests filed", n)
	}
}

// D86: when fleetops-mcp refuses the filing with an error (here the API server
// refusing the ApprovalRequest), the person gets the engine's fixed reply
// saying what was refused and why, never an empty one.
func TestRefusedFilingIsNeverAnEmptyReply(t *testing.T) {
	c := interceptor.NewClient(world(t), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*fleetopsv1.ApprovalRequest); ok {
				return errors.New(`ApprovalRequest.fleetops.alexbahel.github.io "areq-x" is invalid: spec.draftTask.actions[0].location.zone: Invalid value: "": should be at least 1 chars long`)
			}
			return cl.Create(ctx, obj, opts...)
		},
	})
	_, conv, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "inspect", "kind": "inspect-damage", "fields": map[string]any{"location": "receiving-dock-1"}})}},
	)
	if tr := send("inspect receiving-dock-1 for damage"); tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomePreview || tr.Dialogue.Blocked {
		t.Fatalf("preview %+v", tr.Dialogue)
	}
	tr := send("1")
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomeRefused {
		t.Fatalf("dialogue %+v", tr.Dialogue)
	}
	for _, want := range []string{"Not filed", "Inspect for damage", "location.zone", "Nothing was filed"} {
		if !strings.Contains(tr.Final, want) {
			t.Errorf("reply lacks %q: %q", want, tr.Final)
		}
	}
	if conv.Preview != nil {
		t.Fatal("the refused preview is still open")
	}
}

// D88 end to end, a zone-03 reading: start "on approval" is not sent to check_task
// (which refused it, and kept refusing later calls); the person is asked
// again and the request is filed after the answer and the confirmation.
func TestInvalidStartAskedAgainThenFiled(t *testing.T) {
	c := world(t)
	_, _, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "zone-maintenance", "kind": "zone-maintenance", "fields": map[string]any{"zone": "warehouse-a", "mode": "Immediate", "start": "on approval", "reason": "forklift crossing"}})}},
	)
	tr := send("A forklift has to cross warehouse-a. Pause all robots there right away.")
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomeQuestion || tr.Dialogue.QuestionID != "maintenance.mode" {
		t.Fatalf("dialogue %+v\n%s", tr.Dialogue, tr.Final)
	}
	for _, e := range tr.Calls {
		if e.Name == "check_task" {
			t.Fatalf("check_task was called with the invalid start: %s", e.Args)
		}
	}
	if tr = send("2"); tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomePreview || tr.Dialogue.Blocked {
		t.Fatalf("preview %+v\n%s", tr.Dialogue, tr.Final)
	}
	if tr = send("1"); tr.Dialogue.Outcome != OutcomeFiled {
		t.Fatalf("filing %+v %s", tr.Dialogue, tr.Final)
	}
	rs := requests(t, c)
	if len(rs) != 1 || rs[0].Spec.ZoneMaintenance == nil || rs[0].Spec.ZoneMaintenance.Mode != "Immediate" || rs[0].Spec.ZoneMaintenance.ScheduledStart != nil {
		t.Fatalf("requests %+v", rs)
	}
}

// D93: the schema offers only the catalogue values the site policy
// allows: no priority Critical under policies/default.yaml.
func TestProposeSchemaOffersOnlyWhatThePolicyAllows(t *testing.T) {
	cat, err := catalogue.Load("../../catalogue/warehouse-a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Load("../../policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties map[string]struct {
			Properties map[string]struct {
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(ProposeSpec(cat, dialogue.AllowedBy(p)).InputSchema, &s); err != nil {
		t.Fatal(err)
	}
	prio := s.Properties["fields"].Properties["priority"]
	if strings.Join(prio.Enum, ",") != "Low,Normal,High" || strings.Contains(prio.Description, "Critical") {
		t.Fatalf("priority %+v", prio)
	}
	if err := json.Unmarshal(ProposeSpec(cat, nil).InputSchema, &s); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Properties["fields"].Properties["priority"].Enum, ","); got != "Low,Normal,High,Critical" {
		t.Fatalf("no policy: %s", got)
	}
}

// D98: Qwen2.5-7B's dispatch-04 run 1 in the v6 record (trace
// e89e129a47afc33ef68d8488e51b748f) made the same propose_task call eleven
// times, each refused by D94 for zone "Zone/zone-001", and ended with no
// reply. The model now sees the refusal twice; the third call is applied
// without the field, and the person gets the preview, which says what was
// left out (no field of the kind can hold the value).
func TestRefusalLoopEndsAfterTwoRefusals(t *testing.T) {
	c := world(t)
	args := map[string]any{"ask": "inspect", "kind": "inspect-location",
		"fields":        map[string]any{"location": "receiving-dock-1", "robot": "Robot/sim-robot-002", "zone": "Zone/zone-001"},
		"somethingElse": map[string]any{"interpretation": "known", "kind": "inspect-location", "lookFor": []string{"location-state"}}}
	var steps []llm.Step
	for i := 0; i < 11; i++ {
		steps = append(steps, llm.Step{Calls: []llm.ToolCall{call("p", ProposeTool, args)}})
	}
	_, conv, send := chatFixture(t, c, steps...)
	tr := send("Draft an inspection named inspect-dock-camera of receiving-dock-1 that needs the front camera. Keep it off any robot whose camera is degraded.")
	refused, applied := 0, 0
	for _, e := range tr.Calls {
		if e.Name == ProposeTool {
			if e.IsError {
				refused++
			} else {
				applied++
			}
		}
	}
	if refused != dialogue.MaxRefusals || applied != 1 {
		t.Fatalf("propose_task refused %d, applied %d; want %d and 1", refused, applied, dialogue.MaxRefusals)
	}
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomePreview || !strings.Contains(tr.Final, `Left out: zone "Zone/zone-001"`) {
		t.Fatalf("dialogue %+v\n%s", tr.Dialogue, tr.Final)
	}
	if len(requests(t, c)) != 0 || conv.Preview == nil {
		t.Fatal("want a preview and nothing filed")
	}
}

// D98: Qwen2.5-7B's first call in guided-23 run 3 of the v6 record
// (trace 24b31da3e740e0cb5a70d474dd06d56d), dock "receiving-dock-1" on a
// move, repeated. After two refusals the person is asked which of the
// move's fields holds it, with numbered choices ending with "Something else
// — describe it"; the numbered answer is applied by fleetops-agent, without
// the model, and the request is filed only on the preview's confirmation.
func TestRefusedFieldAskedOfThePerson(t *testing.T) {
	c := world(t)
	dock := map[string]any{"ask": "move", "kind": "move", "fields": map[string]any{"dock": "receiving-dock-1"}}
	_, _, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, dock)}},
		llm.Step{Calls: []llm.ToolCall{call("p2", ProposeTool, dock)}},
		llm.Step{Calls: []llm.ToolCall{call("p3", ProposeTool, dock)}},
	)
	tr := send("Move a robot over to receiving-dock-1.")
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomeQuestion || tr.Dialogue.QuestionID != dialogue.UnusedFieldQuestion || tr.Dialogue.Asked != 1 {
		t.Fatalf("dialogue %+v\n%s", tr.Dialogue, tr.Final)
	}
	for _, want := range []string{`dock "receiving-dock-1"`, "\n1 Location: \"receiving-dock-1\"", "\n2 " + catalogue.SomethingElse, dialogue.CheckedSnapshotHeader} {
		if !strings.Contains(tr.Final, want) {
			t.Errorf("question lacks %q:\n%s", want, tr.Final)
		}
	}
	tr = send("1")
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomePreview || !strings.Contains(tr.Final, "Location: receiving-dock-1") {
		t.Fatalf("after the answer: %+v\n%s", tr.Dialogue, tr.Final)
	}
	if len(requests(t, c)) != 0 {
		t.Fatal("filed before the confirmation")
	}
	if tr = send("1"); tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomeFiled || len(requests(t, c)) != 1 {
		t.Fatalf("confirmation: %+v\n%s", tr.Dialogue, tr.Final)
	}
}

// D93 through fleetops-agent: a value the site policy forbids is refused by
// the engine, before any question, with DENY_FIELD, the allowed values and
// the "Checked:" block, and nothing is filed. dispatch-07 no longer requires
// the call (D100); this test and the scripted dispatch-07 run keep the
// engine's refusal covered.
func TestPolicyForbiddenValueRefusedThroughChat(t *testing.T) {
	c := world(t)
	chat, conv, send := chatFixture(t, c,
		llm.Step{Calls: []llm.ToolCall{call("p1", ProposeTool, map[string]any{"ask": "deliver", "kind": "deliver", "fields": map[string]any{"from": "receiving-dock-1", "priority": "Critical"}})}},
	)
	p, err := policy.Load("../../policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	chat.Allowed = dialogue.AllowedBy(p)
	tr := send("Draft a pickup from receiving-dock-1 at Critical priority.")
	if tr.Dialogue == nil || tr.Dialogue.Outcome != OutcomeRefused || tr.Dialogue.Code != string(policy.CodeField) || tr.Dialogue.Asked != 0 {
		t.Fatalf("dialogue %+v\n%s", tr.Dialogue, tr.Final)
	}
	for _, want := range []string{`priority "Critical"`, "DENY_FIELD", "High, Normal, Low", "Nothing was filed", dialogue.CheckedReadHeader} {
		if !strings.Contains(tr.Final, want) {
			t.Errorf("refusal lacks %q:\n%s", want, tr.Final)
		}
	}
	if len(requests(t, c)) != 0 || conv.Preview != nil || conv.State.Active() {
		t.Fatal("something was kept or filed after the refusal")
	}
}
