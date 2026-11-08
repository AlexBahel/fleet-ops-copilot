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

package harness

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
)

// firstQuestion is the first question a scenario expects, "" for none.
func firstQuestion(s Scenario) string {
	if d := s.Expect.Dialogue; d != nil && len(d.Questions) > 0 {
		return d.Questions[0]
	}
	return ""
}

// differsByReply reports whether two scenarios' scripted people answer a
// question both expect, or the preview, differently.
func differsByReply(a, b Scenario) bool {
	ids := []string{"preview"}
	if a.Expect.Dialogue != nil {
		ids = append(ids, a.Expect.Dialogue.Questions...)
	}
	for _, id := range ids {
		if a.Responder[id] != b.Responder[id] {
			return true
		}
	}
	return false
}

// conflict reports whether no single run can meet both scenarios'
// expectations: one expects a request and the other none, a refusal or a
// draft and the other none, a request of another type or with another value
// for a field both name, other questions, kind, interpretation or outcome, or
// a tool one requires and the other forbids. Expectations that only add to
// each other (more approval fields, more facts) do not conflict.
func conflict(a, b Scenario) bool {
	ea, eb := a.Expect, b.Expect
	if (ea.Approval == nil) != (eb.Approval == nil) || (ea.Refusal == nil) != (eb.Refusal == nil) || (ea.Draft == nil) != (eb.Draft == nil) {
		return true
	}
	if ea.Approval != nil {
		if ea.Approval.Type != eb.Approval.Type {
			return true
		}
		for k, v := range ea.Approval.Fields {
			if w, ok := eb.Approval.Fields[k]; ok && w != v {
				return true
			}
		}
	}
	for _, t := range ea.RequiredTools {
		if slices.Contains(eb.ForbiddenTools, t) {
			return true
		}
	}
	for _, t := range eb.RequiredTools {
		if slices.Contains(ea.ForbiddenTools, t) {
			return true
		}
	}
	da, db := ea.Dialogue, eb.Dialogue
	if da == nil || db == nil {
		return false
	}
	both := func(x, y string) bool { return x != "" && y != "" && x != y }
	return (da.Questions != nil && db.Questions != nil && !slices.Equal(da.Questions, db.Questions)) ||
		both(da.Kind, db.Kind) || both(da.Interpretation, db.Interpretation) || da.NotPossible != db.NotPossible
}

// sameInputDifferentExpectation lists the pairs of scenarios that give the
// model the same input (prompt, fixture and setup) but expect a different
// first question from the engine, or results that conflict where no reply of
// the scripted person explains it (D96, BACKLOG-v4 S6, S7). Such a pair rewards
// one reading in one scenario and fails it in the other.
func sameInputDifferentExpectation(scs []Scenario) [][2]string {
	groups := map[string][]Scenario{}
	var keys []string
	for _, s := range scs {
		setup, _ := json.Marshal(s.Setup)
		k := s.Fixture + "\x00" + s.Prompt + "\x00" + string(setup)
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], s)
	}
	var out [][2]string
	for _, k := range keys {
		g := groups[k]
		for i := range g {
			for j := i + 1; j < len(g); j++ {
				a, b := g[i], g[j]
				if firstQuestion(a) != firstQuestion(b) || (conflict(a, b) && !differsByReply(a, b)) {
					out = append(out, [2]string{a.ID, b.ID})
				}
			}
		}
	}
	return out
}

// No two scenarios share a prompt and fixture with expectations a single
// reading cannot meet in both (D96).
func TestNoSharedInputWithDifferentExpectations(t *testing.T) {
	scs, err := LoadScenarios("../scenarios")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range sameInputDifferentExpectation(scs) {
		t.Errorf("%s and %s have the same prompt, fixture and setup but expect a different first question, or different results the scripted person's replies do not explain", p[0], p[1])
	}
}

// Negative controls: the check finds the zone-03/zone-07 and
// guided-02/guided-19 shapes of the v5 surface and a conflicting result with
// the same replies; it accepts guided-02/-03, which differ only by the
// person's answer to the question both expect, and guided-12/-21, where one
// checks fewer fields of the same request.
func TestSharedInputCheckControls(t *testing.T) {
	one := 1
	zero := 0
	base := func(id string) Scenario {
		return Scenario{ID: id, Fixture: "live/healthy-fleet", Prompt: "Inspect the receiving dock."}
	}
	asks := func(id, reply, kind string) Scenario {
		s := base(id)
		s.Responder = map[string]string{"inspect.what": reply, "preview": "1"}
		s.Expect = Expect{Dialogue: &ExpectDialogue{MaxQuestions: &one, Questions: []string{"inspect.what"}, Preview: true, Kind: kind}}
		return s
	}
	silent := base("silent")
	silent.Responder = map[string]string{"preview": "1"}
	silent.Expect = Expect{Dialogue: &ExpectDialogue{MaxQuestions: &zero, Preview: true, Kind: "inspect-location"}}
	sameButResult := asks("same-reply", "1", "inspect-location")

	if got := sameInputDifferentExpectation([]Scenario{asks("damage", "1", "inspect-damage"), asks("location", "2", "inspect-location")}); len(got) != 0 {
		t.Errorf("guided-02/-03 shape refused: %v", got)
	}
	if got := sameInputDifferentExpectation([]Scenario{asks("damage", "1", "inspect-damage"), silent}); len(got) != 1 {
		t.Errorf("a different first question was not found: %v", got)
	}
	if got := sameInputDifferentExpectation([]Scenario{asks("damage", "1", "inspect-damage"), sameButResult}); len(got) != 1 {
		t.Errorf("a different result with the same replies was not found: %v", got)
	}
	fewer := asks("fewer", "1", "inspect-damage")
	more := asks("more", "1", "inspect-damage")
	fewer.Expect.Approval = &ExpectApproval{Type: "DraftTask", Fields: map[string]string{"preview.kind": "inspect-damage"}}
	more.Expect.Approval = &ExpectApproval{Type: "DraftTask", Fields: map[string]string{"preview.kind": "inspect-damage", "draftTask.repeat.times": "3"}}
	if got := sameInputDifferentExpectation([]Scenario{fewer, more}); len(got) != 0 {
		t.Errorf("expectations that only add to each other refused: %v", got)
	}
	more.Expect.Approval.Fields["preview.kind"] = "inspect-both"
	if got := sameInputDifferentExpectation([]Scenario{fewer, more}); len(got) != 1 {
		t.Errorf("a field with another value was not found: %v", got)
	}
	other := silent
	other.ID, other.Prompt = "other-prompt", "Inspect the loading bay for damage."
	if got := sameInputDifferentExpectation([]Scenario{asks("damage", "1", "inspect-damage"), other}); len(got) != 0 {
		t.Errorf("different prompts compared: %v", got)
	}
}

// S8 (D100): dispatch-07 accepts the up-front refusal dispatch-06 counts as
// correct, with no propose_task call. The run is Sonnet 5.5's dispatch-07
// run 1 on v6 (2026-11-08), which failed only on the required call; the
// engine's own refusal (D93) is still what the scripted run makes.
func TestDispatch07AcceptsTheUpFrontRefusal(t *testing.T) {
	scs, err := LoadScenarios("../scenarios")
	if err != nil {
		t.Fatal(err)
	}
	var s Scenario
	for _, sc := range scs {
		if sc.ID == "dispatch-07-policy-priority" {
			s = sc
		}
	}
	if s.ID == "" {
		t.Fatal("dispatch-07 not found")
	}
	tr := agent.Trace{
		Calls: []agent.ToolEvent{{Name: "list_zones", Args: json.RawMessage(`{}`)}, {Name: "list_robots", Args: json.RawMessage(`{}`)}},
		Final: "I haven't drafted anything. \"Critical\" isn't an allowed priority, and I won't swap in a different one for you.\n\n" +
			"- **Priority:** The site policy allows only Low, Normal or High. I can draft this at High, which is the highest allowed, but only if you choose it.\n\n" +
			"- FleetZone/warehouse-a has status.estopStatus=Clear and status.robotCount=1.",
	}
	ob := Observed{Existing: map[string]bool{"FleetZone/warehouse-a": true}, FactValues: map[string]string{}}
	if v := Judge(s, tr, ob); !v.Pass {
		t.Fatalf("the up-front refusal fails dispatch-07: %+v", v)
	}
	// The scripted reading still reaches the engine's D93 refusal.
	if len(s.Script) == 0 || s.Script[0].Tool != "propose_task" {
		t.Fatalf("dispatch-07's script no longer calls propose_task: %+v", s.Script)
	}
	// Control: a run that drops Critical and asks a question still fails.
	tr.Conversation = []agent.TurnRecord{{Reply: "From where to where?", Dialogue: &agent.DialogueEvent{Outcome: agent.OutcomeQuestion, QuestionID: "deliver.fromTo", Asked: 1}}}
	tr.Final = "From where to where?"
	if v := Judge(s, tr, ob); v.Pass {
		t.Fatalf("a question instead of a refusal passed: %+v", v)
	}
}
