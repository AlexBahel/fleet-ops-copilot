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

package dialogue

import (
	"os"
	"strings"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

func cat(t *testing.T, edit ...string) *catalogue.Catalogue {
	t.Helper()
	b, err := os.ReadFile("../../catalogue/warehouse-a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for i := 0; i+1 < len(edit); i += 2 {
		if !strings.Contains(s, edit[i]) {
			t.Fatalf("edit %q not found", edit[i])
		}
		s = strings.Replace(s, edit[i], edit[i+1], 1)
	}
	c, err := catalogue.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func i32(v int32) *int32 { return &v }

var snap = Snapshot{
	Zones: []string{"warehouse-a"},
	Waypoints: []Waypoint{
		{"warehouse-a", "receiving-dock-1", "5.1", "2.1"},
		{"warehouse-a", "staging-shelf-a", "12.1", "8.1"},
		{"warehouse-a", "charging-bay", "1.1", "1.1"},
	},
	Robots: []Robot{{"sim-robot-002", i32(85), "Idle"}, {"sim-robot-001", i32(21), "InProgress"}, {"sim-robot-003", i32(60), "Idle"}},
}

func newState(msg string) *State { return &State{Message: msg, Fields: map[string]string{}} }

func draftInput(t *testing.T, o Outcome) tools.DraftTaskInput {
	t.Helper()
	if o.Proposal == nil {
		t.Fatalf("no proposal: %+v", o)
	}
	in, ok := o.Proposal.Input.(tools.DraftTaskInput)
	if !ok {
		t.Fatalf("input %T", o.Proposal.Input)
	}
	return in
}

// A complete request needs no question (scenario: complete request → 0).
func TestCompleteInspectAsksNothing(t *testing.T) {
	e := &Engine{Cat: cat(t)}
	st := newState("Inspect receiving-dock-1 for damage")
	o := e.Propose(st, Input{Ask: "inspect", Kind: "inspect-damage", Fields: map[string]string{"location": "receiving-dock-1"}}, snap)
	in := draftInput(t, o)
	if len(st.Asked) != 0 {
		t.Fatalf("asked %v", st.Asked)
	}
	a := in.Actions[0]
	if a.Type != "Navigate" || a.Location.X != "5.1" || a.Location.Zone != "warehouse-a" || strings.Join(a.LookFor, ",") != "damage" || strings.Join(a.RequiredCapabilities, ",") != "navigation,camera_front" {
		t.Fatalf("action %+v", a)
	}
	if in.Preview.Kind != "inspect-damage" || in.Preview.ResultReported || in.Preview.CatalogueSHA256 == "" {
		t.Fatalf("preview %+v", in.Preview)
	}
}

// An ambiguous inspect asks Q1 first, exactly.
func TestAmbiguousInspectAsksQ1First(t *testing.T) {
	e := &Engine{Cat: cat(t)}
	st := newState("inspect the receiving dock")
	o := e.Propose(st, Input{Ask: "inspect", Fields: map[string]string{"location": "receiving-dock-1"}}, snap)
	if o.Question == nil || o.Question.ID != "inspect.what" {
		t.Fatalf("outcome %+v", o)
	}
	want := "What should the robot inspect?\n1 Damage\n2 Location\n3 Both\n4 Something else — describe it"
	if got := o.Question.Render(); got != want {
		t.Fatalf("Q1 =\n%s\nwant\n%s", got, want)
	}
	for n, kind := range map[int]string{1: "inspect-damage", 2: "inspect-location", 3: "inspect-both"} {
		st := newState("inspect the receiving dock")
		e.Propose(st, Input{Ask: "inspect", Fields: map[string]string{"location": "receiving-dock-1"}}, snap)
		o, needModel, ok := e.Answer(st, n, "", snap)
		if !ok || needModel || draftInput(t, o).Preview.Kind != kind || len(st.Asked) != 1 {
			t.Fatalf("answer %d: %+v asked %v", n, o, st.Asked)
		}
	}
}

// Two questions, and where's choices put the waypoint the person named first.
func TestInspectTwoQuestionsLikeliestFirst(t *testing.T) {
	e := &Engine{Cat: cat(t)}
	st := newState("have a robot look at the staging shelf")
	e.Propose(st, Input{Ask: "inspect"}, snap)
	o, _, _ := e.Answer(st, 2, "", snap)
	if o.Question == nil || o.Question.ID != "inspect.where" || !strings.HasPrefix(o.Question.Choices[0].Label, "staging-shelf-a") {
		t.Fatalf("where %+v", o.Question)
	}
	last := o.Question.Choices[len(o.Question.Choices)-1]
	if last.Label != catalogue.SomethingElse || !last.SomethingElse {
		t.Fatalf("last choice %+v", last)
	}
	o, _, _ = e.Answer(st, 1, "", snap)
	if in := draftInput(t, o); in.Actions[0].Location.Waypoint != "staging-shelf-a" || len(st.Asked) != 2 {
		t.Fatalf("input %+v asked %v", in, st.Asked)
	}
}

// Something else: "4" alone asks the open question; "4 <text>" keeps the text
// verbatim and needs the model; known, composed and not possible.
func TestSomethingElse(t *testing.T) {
	var logged []ReviewEntry
	e := &Engine{Cat: cat(t), Review: func(r ReviewEntry) { logged = append(logged, r) }}
	st := newState("inspect the dock")
	e.Propose(st, Input{Ask: "inspect", Fields: map[string]string{"location": "receiving-dock-1"}}, snap)
	o, needModel, _ := e.Answer(st, 4, "", snap)
	if needModel || o.Question == nil || !o.Question.Open || len(st.Asked) != 2 {
		t.Fatalf("open question %+v asked %v", o, st.Asked)
	}
	st.Describe("Check that the fire extinguisher by receiving-dock-1 is there, and tell me if it is missing")
	// Known kind.
	o = e.Propose(st, Input{SomethingElse: &SomethingElseInput{Interpretation: Known, Kind: "inspect-location"}}, snap)
	in := draftInput(t, o)
	if in.Actions[0].Instruction != st.SomethingElse.Text || in.Preview.Instruction != st.SomethingElse.Text || in.Preview.Kind != "inspect-location" {
		t.Fatalf("known %+v", in)
	}
	// Composed.
	st2 := newState("x")
	_, needModel, _ = func() (Outcome, bool, bool) {
		e.Propose(st2, Input{Ask: "inspect"}, snap)
		return e.Answer(st2, 4, "go to the charging bay, then charge sim-robot-003", snap)
	}()
	if !needModel || st2.SomethingElse.Text != "go to the charging bay, then charge sim-robot-003" {
		t.Fatalf("verbatim %+v", st2.SomethingElse)
	}
	o = e.Propose(st2, Input{SomethingElse: &SomethingElseInput{Interpretation: Composed, Steps: []StepInput{
		{Kind: "move", Fields: map[string]string{"location": "charging-bay"}}, {Kind: "charge", Fields: map[string]string{"robot": "sim-robot-003"}},
	}}}, snap)
	in = draftInput(t, o)
	if o.Proposal.Interpretation != Composed || in.Preview.Interpretation != "Composed" || len(in.Actions) != 2 || in.Actions[1].DependsOn[0] != "step1-move" || in.Actions[1].RobotSelector != "sim-robot-003" {
		t.Fatalf("composed %+v", in)
	}
	// Not possible.
	st3 := newState("x")
	st3.Describe("take a photo of the damaged pallet and send it to me")
	o = e.Propose(st3, Input{SomethingElse: &SomethingElseInput{Interpretation: NotPossible, Need: "C2", Reason: "it needs a photo attached to the result"}}, snap)
	if o.NotPossible == "" || !strings.Contains(o.NotPossible, "Nothing was filed") || !strings.Contains(o.NotPossible, "no place for evidence") {
		t.Fatalf("not possible %+v", o)
	}
	if len(logged) != 3 || logged[2].Interpretation != NotPossible || logged[0].Text == "" {
		t.Fatalf("review log %+v", logged)
	}
}

// At most two questions before the preview: past the cap, a missing item is
// shown in a preview that cannot be confirmed.
func TestCapShowsMissingInPreview(t *testing.T) {
	e := &Engine{Cat: cat(t)}
	st := newState("inspect something")
	e.Propose(st, Input{Ask: "inspect"}, snap)
	e.Answer(st, 1, "", snap)
	o, _, _ := e.Answer(st, 4, "", snap) // something else on "where", no text
	if o.Proposal == nil || len(st.Asked) != 2 || strings.Join(o.Proposal.Missing, ",") != "location" {
		t.Fatalf("outcome %+v asked %v", o, st.Asked)
	}
	pv := RenderPreview(o.Proposal, tools.CheckTaskResult{Report: feasibility.Report{Verdict: feasibility.LikelyFeasible}})
	if !pv.Blocked || !strings.HasSuffix(pv.Text, ChoicesBlocked) {
		t.Fatalf("preview %+v", pv)
	}
}

func TestOtherAsks(t *testing.T) {
	e := &Engine{Cat: cat(t)}
	// Charge: robots by lowest battery first.
	st := newState("charge a robot")
	o := e.Propose(st, Input{Ask: "charge"}, snap)
	if o.Question == nil || !strings.HasPrefix(o.Question.Choices[0].Label, "sim-robot-001 (battery 21%") {
		t.Fatalf("charge %+v", o)
	}
	o, _, _ = e.Answer(st, 1, "", snap)
	in := draftInput(t, o)
	if in.Actions[0].Type != "Charge" || in.Actions[0].RobotSelector != "sim-robot-001" || in.Actions[0].Dock != "charging-bay" {
		t.Fatalf("charge input %+v", in.Actions[0])
	}
	// Deliver: one question with pairs.
	st = newState("bring a pallet from the receiving dock to the staging shelf")
	o = e.Propose(st, Input{Ask: "deliver"}, snap)
	if o.Question == nil || o.Question.Choices[0].Label != "receiving-dock-1 → staging-shelf-a" {
		t.Fatalf("deliver %+v", o.Question)
	}
	o, _, _ = e.Answer(st, 1, "", snap)
	in = draftInput(t, o)
	if in.Actions[0].Type != "PickUp" || in.Actions[1].DependsOn[0] != "pickup" || in.Actions[1].Location.Waypoint != "staging-shelf-a" {
		t.Fatalf("deliver input %+v", in)
	}
	// Patrol: route then rounds.
	st = newState("patrol the warehouse")
	e.Propose(st, Input{Ask: "patrol"}, snap)
	o, _, _ = e.Answer(st, 1, "", snap)
	if o.Question == nil || o.Question.ID != "patrol.times" {
		t.Fatalf("patrol %+v", o)
	}
	o, _, _ = e.Answer(st, 2, "", snap)
	in = draftInput(t, o)
	if in.Repeat != 3 || len(in.Actions[0].Route) != 3 {
		t.Fatalf("patrol input %+v", in)
	}
	// Zone stop: one zone, inferred; the reason defaults to the person's words.
	st = newState("stop warehouse-a, there is a spill")
	o = e.Propose(st, Input{Ask: "zone-stop"}, snap)
	zs, ok := o.Proposal.Input.(tools.ZoneStopInput)
	if !ok || zs.ZoneName != "warehouse-a" || zs.Reason != "stop warehouse-a, there is a spill" || len(st.Asked) != 0 {
		t.Fatalf("zone stop %+v", o)
	}
	// Activate with no draft: not possible now.
	st = newState("activate my draft")
	if o = e.Propose(st, Input{Ask: "activate"}, snap); o.NotPossible == "" {
		t.Fatalf("activate %+v", o)
	}
}

// R67: flipping resultReported (when Swarmada can report a result, C1)
// changes one preview line and nothing in the dialogue.
func TestResultReportedFlipNeedsNoDialogueChange(t *testing.T) {
	render := func(c *catalogue.Catalogue) ([]string, string) {
		e := &Engine{Cat: c}
		st := newState("inspect the receiving dock")
		o := e.Propose(st, Input{Ask: "inspect", Fields: map[string]string{"location": "receiving-dock-1"}}, snap)
		q := o.Question.Render()
		o, _, _ = e.Answer(st, 1, "", snap)
		return []string{q}, RenderPreview(o.Proposal, tools.CheckTaskResult{Report: feasibility.Report{Verdict: feasibility.LikelyFeasible}}).Text
	}
	q1, p1 := render(cat(t))
	q2, p2 := render(cat(t, "name: inspect-damage\n    title: Inspect for damage\n    class: b\n    resultReported: false", "name: inspect-damage\n    title: Inspect for damage\n    class: a\n    resultReported: true"))
	if strings.Join(q1, "") != strings.Join(q2, "") {
		t.Fatal("questions changed")
	}
	l1, l2 := strings.Split(p1, "\n"), strings.Split(p2, "\n")
	var removed []string
	for _, l := range l1 {
		if !strings.Contains(p2, l) {
			removed = append(removed, l)
		}
	}
	if len(l1) != len(l2)+1 || len(removed) != 1 || strings.TrimSpace(removed[0]) != ResultNotReported {
		t.Fatalf("preview diff %q", removed)
	}
}

func TestParseReply(t *testing.T) {
	for msg, want := range map[string]Reply{"1": ReplyConfirm, "confirm": ReplyConfirm, "2": ReplyConfirmFollowUp, "4": ReplyCancel, "cancel": ReplyCancel, "3 use sim-robot-003": ReplyChange, "go ahead maybe": ReplyOther} {
		if got, _ := ParseReply(msg); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}
	if _, rest := ParseReply("3 alert on first failure"); rest != "alert on first failure" {
		t.Errorf("change text %q", rest)
	}
}
