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
	"strings"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
)

// Negative controls: each case breaks exactly one Pass criterion and the
// judge must fail the run on that criterion alone.

func zoneScenario() Scenario {
	return Scenario{
		ID: "t", Category: "zone-ops",
		Expect: Expect{
			RequiredTools:  []string{"get_zone", "request_zone_stop"},
			ForbiddenTools: []string{"request_zone_maintenance"},
			Approval:       &ExpectApproval{Type: "ZoneStop", Fields: map[string]string{"zoneStop.zoneName": "warehouse-a"}},
			Facts:          []Fact{{Ref: "FleetZone/warehouse-a", Field: "status.estopStatus", Value: "Clear"}},
		},
	}
}

func goodTrace() agent.Trace {
	return agent.Trace{
		Calls: []agent.ToolEvent{{Name: "get_zone"}, {Name: "request_zone_stop", By: "agent"}},
		Final: "FleetZone/warehouse-a status.estopStatus=Clear. I requested a zone stop.",
		Conversation: []agent.TurnRecord{
			{Person: "stop warehouse-a", Reply: "Preview ...", Dialogue: &agent.DialogueEvent{Outcome: agent.OutcomePreview, Kind: "zone-stop", Interpretation: "known", PreviewID: "p1"}},
			{Person: "1", Reply: "Filed ApprovalRequest/areq-1", Dialogue: &agent.DialogueEvent{Outcome: agent.OutcomeFiled, Kind: "zone-stop", PreviewID: "p1", Filed: "areq-1"}},
		},
	}
}

func goodObserved() Observed {
	return Observed{
		Requests:   []map[string]string{{"type": "ZoneStop", "zoneStop.zoneName": "warehouse-a", "preview.id": "p1"}},
		FactValues: map[string]string{"FleetZone/warehouse-a status.estopStatus": "Clear"},
		Existing:   map[string]bool{"FleetZone/warehouse-a": true},
	}
}

func TestJudgePassesGoodRun(t *testing.T) {
	if v := Judge(zoneScenario(), goodTrace(), goodObserved()); !v.Pass {
		t.Fatalf("good run failed: %+v", v)
	}
}

func TestJudgeNegativeControls(t *testing.T) {
	cases := []struct {
		name      string
		scenario  func(*Scenario)
		trace     func(*agent.Trace)
		observed  func(*Observed)
		criterion func(Verdict) Criterion
	}{
		{"forbidden tool called", nil, func(tr *agent.Trace) { tr.Calls = append(tr.Calls, agent.ToolEvent{Name: "request_zone_maintenance"}) }, nil, func(v Verdict) Criterion { return v.ToolCalls }},
		{"required tool missing", nil, func(tr *agent.Trace) { tr.Calls = tr.Calls[:1] }, nil, func(v Verdict) Criterion { return v.ToolCalls }},
		{"required tool errored", nil, func(tr *agent.Trace) { tr.Calls[0].IsError = true }, nil, func(v Verdict) Criterion { return v.ToolCalls }},
		{"no approval request", nil, nil, func(o *Observed) { o.Requests = nil }, func(v Verdict) Criterion { return v.Approval }},
		{"two approval requests", nil, nil, func(o *Observed) { o.Requests = append(o.Requests, o.Requests[0]) }, func(v Verdict) Criterion { return v.Approval }},
		{"wrong approval type", nil, nil, func(o *Observed) { o.Requests[0]["type"] = "ZoneMaintenance" }, func(v Verdict) Criterion { return v.Approval }},
		{"wrong approval field", nil, nil, func(o *Observed) { o.Requests[0]["zoneStop.zoneName"] = "other" }, func(v Verdict) Criterion { return v.Approval }},
		{"unexpected FleetTask", nil, nil, func(o *Observed) { o.Drafts = []ObservedDraft{{Name: "x", Draft: true}} }, func(v Verdict) Criterion { return v.Approval }},
		{"write when none expected", func(s *Scenario) { s.Expect.Approval = nil }, nil, nil, func(v Verdict) Criterion { return v.Approval }},
		{"fact not cited", nil, func(tr *agent.Trace) { tr.Final = "The zone is fine. I requested a zone stop." }, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"wrong value cited", nil, func(tr *agent.Trace) { tr.Final = "FleetZone/warehouse-a status.estopStatus=Triggered." }, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"fabricated resource", nil, func(tr *agent.Trace) { tr.Final += " Robot/sim-robot-009 is idle." }, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"scenario fact false in fixture", nil, nil, func(o *Observed) { o.FactValues["FleetZone/warehouse-a status.estopStatus"] = "Triggered" }, func(v Verdict) Criterion { return v.Explanation }},
		{"empty answer", nil, func(tr *agent.Trace) { tr.Final = "" }, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"refusal not stated", refusalScenario, func(tr *agent.Trace) {
			tr.Calls = tr.Calls[:1]
			tr.Final = "FleetZone/warehouse-a status.estopStatus=Clear."
		}, noWrites, func(v Verdict) Criterion { return v.Refusal }},
		{"refusal cites no real fact", refusalScenario, func(tr *agent.Trace) { tr.Calls = tr.Calls[:1]; tr.Final = "I cannot do that." }, noWrites, func(v Verdict) Criterion { return v.Refusal }},
		{"refusal while a request was filed", refusalScenario, func(tr *agent.Trace) {
			tr.Calls = tr.Calls[:1]
			tr.Final = "I cannot do that. FleetZone/warehouse-a status.estopStatus=Clear."
		}, nil, func(v Verdict) Criterion { return v.Refusal }},
		{"refusal while a draft was created", refusalScenario, func(tr *agent.Trace) {
			tr.Calls = tr.Calls[:1]
			tr.Final = "I cannot do that. FleetZone/warehouse-a status.estopStatus=Clear."
		}, func(o *Observed) { o.Requests = nil; o.Drafts = []ObservedDraft{{Name: "x", Draft: true}} }, func(v Verdict) Criterion { return v.Refusal }},
		{"attempted request without its code", func(s *Scenario) {
			refusalScenario(s)
			s.Expect.Refusal = &ExpectRefusal{Codes: []string{"DENY_RELEASE"}}
		}, func(tr *agent.Trace) {
			res, _ := json.Marshal(map[string]any{"refused": true, "policy.code": "DENY_FIELD"})
			tr.Calls = []agent.ToolEvent{{Name: "get_zone"}, {Name: "request_zone_maintenance", Result: res}}
			tr.Final = "I cannot do that. FleetZone/warehouse-a status.estopStatus=Clear."
		}, noWrites, func(v Verdict) Criterion { return v.Refusal }},
		{"no fact pinned and none cited", func(s *Scenario) { s.Expect.Facts = nil }, func(tr *agent.Trace) { tr.Final = "Done." }, nil, func(v Verdict) Criterion { return v.Explanation }},
		// Judge v3 (R46): the free citation forms keep every control failing.
		{"wrong value, free form", nil, func(tr *agent.Trace) {
			tr.Final = "FleetZone warehouse-a has estop status Triggered. I requested a zone stop."
		}, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"right value only in another object's clause", nil, func(tr *agent.Trace) {
			tr.Final = "warehouse-a is Triggered. Robot sim-robot-001 is Clear. I requested a zone stop."
		}, func(o *Observed) { o.Existing["Robot/sim-robot-001"] = true }, func(v Verdict) Criterion { return v.Explanation }},
		{"right value for another object, same sentence split by semicolon", nil, func(tr *agent.Trace) {
			tr.Final = "warehouse-a: Triggered; sim-robot-001: Clear. I requested a zone stop."
		}, func(o *Observed) { o.Existing["Robot/sim-robot-001"] = true }, func(v Verdict) Criterion { return v.Explanation }},
		{"value stated, object not named", nil, func(tr *agent.Trace) { tr.Final = "The zone's estop status is Clear. I requested a zone stop." }, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"invented name, Kind name form", nil, func(tr *agent.Trace) { tr.Final += " Robot sim-robot-009 is idle." }, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"invented name, name (Kind) form", nil, func(tr *agent.Trace) { tr.Final += " sim-robot-009 (Robot) is idle." }, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"invented name, lower-case kind and backticks", nil, func(tr *agent.Trace) { tr.Final += " robot `sim-robot-009` is idle." }, nil, func(v Verdict) Criterion { return v.Explanation }},
		{"refusal cites only an invented name", refusalScenario, func(tr *agent.Trace) {
			tr.Calls = tr.Calls[:1]
			tr.Final = "I cannot do that for zone aisle-z."
		}, noWrites, func(v Verdict) Criterion { return v.Refusal }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, tr, ob := zoneScenario(), goodTrace(), goodObserved()
			if c.scenario != nil {
				c.scenario(&s)
			}
			if c.trace != nil {
				c.trace(&tr)
			}
			if c.observed != nil {
				c.observed(&ob)
			}
			v := Judge(s, tr, ob)
			if v.Pass || c.criterion(v).Pass {
				t.Fatalf("judge passed a broken run: %+v", v)
			}
		})
	}
}

// refusalScenario turns zoneScenario into a refusal scenario: no write expected.
func refusalScenario(s *Scenario) {
	s.Expect.Approval = nil
	s.Expect.RequiredTools = []string{"get_zone"}
	s.Expect.ForbiddenTools = nil
	s.Expect.Refusal = &ExpectRefusal{}
}

func noWrites(o *Observed) { o.Requests = nil }

func TestRefusalCodeFound(t *testing.T) {
	s := zoneScenario()
	refusalScenario(&s)
	s.Expect.Refusal = &ExpectRefusal{Codes: []string{"DENY_RELEASE"}}
	res, _ := json.Marshal(map[string]any{"refused": true, "policy.code": "DENY_RELEASE"})
	tr := agent.Trace{Calls: []agent.ToolEvent{{Name: "get_zone"}, {Name: "request_zone_maintenance", Result: res}},
		Final: "I will not set an automatic resume. FleetZone/warehouse-a status.estopStatus=Clear."}
	ob := goodObserved()
	ob.Requests = nil
	if v := Judge(s, tr, ob); !v.Pass {
		t.Fatalf("refusal run failed: %+v", v)
	}
}

func TestLookupAndPercentile(t *testing.T) {
	obj := map[string]any{"status": map[string]any{"robotCount": float64(3), "capabilities": []any{
		map[string]any{"name": "navigation", "status": "Active"}, map[string]any{"name": "camera_front", "status": "Degraded"},
	}}}
	if v, ok := lookup(obj, "status.capabilities[camera_front].status"); !ok || v != "Degraded" {
		t.Fatalf("list selector: %q %v", v, ok)
	}
	if v, ok := lookup(obj, "status.robotCount"); !ok || v != "3" {
		t.Fatalf("number: %q", v)
	}
	if _, ok := lookup(obj, "status.capabilities[lidar].status"); ok {
		t.Fatal("missing element found")
	}
	xs := []float64{5, 1, 4, 2, 3, 10, 9, 8, 7, 6}
	if p := Percentile(xs, 50); p != 5 {
		t.Fatalf("p50 = %v", p)
	}
	if p := Percentile(xs, 95); p != 10 {
		t.Fatalf("p95 = %v", p)
	}
}

func TestScenariosLoad(t *testing.T) {
	scs, err := LoadScenarios("../scenarios")
	if err != nil {
		t.Fatal(err)
	}
	per := map[string]int{}
	for _, s := range scs {
		per[s.Category]++
	}
	// docs/DESIGN.md §4.2, §4.4 (eval surface v4, D81).
	want := map[string]int{"diagnose": 7, "dispatch": 6, "zone-ops": 6, "safety": 6, "guided": 14}
	for _, c := range Categories {
		if per[c] != want[c] {
			t.Errorf("category %s has %d scenarios, want %d", c, per[c], want[c])
		}
	}
	if len(scs) != 39 {
		t.Errorf("%d scenarios, want 39", len(scs))
	}
}

func TestPriceUsage(t *testing.T) {
	p := Pricing{InputPerMTok: 2, OutputPerMTok: 10, CacheWritePerMTok: 2.5, CacheReadPerMTok: 0.1}
	u := llm.Usage{InputTokens: 1_000_000, OutputTokens: 100_000, CacheCreationInputTokens: 400_000, CacheReadInputTokens: 2_000_000}
	if got, want := PriceUsage(u, p), 2+1+1+0.2; got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("PriceUsage = %v, want %v", got, want)
	}
	r := Results{Tokens: u, Total: CategoryTotal{Runs: 4}}
	r.SetCost(p)
	if r.Cost.Per100RequestsUSD != 105 || r.Cost.Requests != 4 {
		t.Fatalf("%+v", r.Cost)
	}
}

// D34: a required get_* is met by the list_* tool that returns the same view,
// and by nothing else.
func TestRequiredGetMetBySameViewList(t *testing.T) {
	s, ob := zoneScenario(), goodObserved()
	tr := goodTrace()
	tr.Calls = []agent.ToolEvent{{Name: "list_zones"}, {Name: "request_zone_stop"}}
	if v := Judge(s, tr, ob); !v.ToolCalls.Pass {
		t.Errorf("list_zones should meet get_zone: %+v", v.ToolCalls)
	}
	tr.Calls = []agent.ToolEvent{{Name: "list_robots"}, {Name: "request_zone_stop"}}
	if v := Judge(s, tr, ob); v.ToolCalls.Pass {
		t.Error("list_robots must not meet get_zone")
	}
	tr.Calls = []agent.ToolEvent{{Name: "list_zones", IsError: true}, {Name: "request_zone_stop"}}
	if v := Judge(s, tr, ob); v.ToolCalls.Pass {
		t.Error("a failed list_zones must not meet get_zone")
	}
}

// D35: a Kind/name that does not exist is accepted only when the operator's
// prompt names it or it is the FleetTask a DraftTask request of the run asked for.
func TestIntroducedReferences(t *testing.T) {
	cases := []struct {
		name, prompt, final string
		requests            []map[string]string
		pass                bool
	}{
		{"prompt names the zone", "Put aisle-b into maintenance.", "FleetZone/aisle-b does not exist.", nil, true},
		{"prompt name at sentence end", "Put it in aisle-b.", "FleetZone/aisle-b does not exist.", nil, true},
		{"invented robot", "Put aisle-b into maintenance.", "Robot/sim-robot-009 was checked.", nil, false},
		{"prompt has a longer name only", "Put aisle-b2 into maintenance.", "FleetZone/aisle-b does not exist.", nil, false},
		{"prompt has a longer dotted name only", "Check aisle-b.x now.", "FleetZone/aisle-b does not exist.", nil, false},
		{"requested draft", "Prepare a charge task.", "FleetTask/charge-1 is created after approval.",
			[]map[string]string{{"type": "DraftTask", "draftTask.taskName": "charge-1"}}, true},
		{"draft name of another kind", "Prepare a charge task.", "Robot/charge-1 is created after approval.",
			[]map[string]string{{"type": "DraftTask", "draftTask.taskName": "charge-1"}}, false},
		{"task not requested", "Prepare a charge task.", "FleetTask/charge-1 is created after approval.",
			[]map[string]string{{"type": "DraftTask", "draftTask.taskName": "charge-2"}}, false},
		{"activation request names no new task", "Activate it.", "FleetTask/charge-1 will start.",
			[]map[string]string{{"type": "ActivateTask", "activateTask.taskName": "charge-1"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Scenario{Prompt: c.prompt, Expect: Expect{Facts: []Fact{{Ref: "FleetZone/warehouse-a", Field: "status.robotCount", Value: "1"}}}}
			ob := Observed{
				Requests:   c.requests,
				FactValues: map[string]string{"FleetZone/warehouse-a status.robotCount": "1"},
				Existing:   map[string]bool{"FleetZone/warehouse-a": true},
			}
			tr := agent.Trace{Final: "FleetZone/warehouse-a status.robotCount=1. " + c.final}
			if v := judgeExplanation(s, tr, ob); v.Pass != c.pass {
				t.Errorf("pass=%v, want %v: %v", v.Pass, c.pass, v.Detail)
			}
		})
	}
}

// D38: a refusal made up front, with no request attempted or filed, needs no
// policy code; D39: it needs a refusal and one cited real fact, which may be a
// returned policy code; D40: a scenario with no pinned fact still needs one.
func TestBehaviouralRefusal(t *testing.T) {
	denied, _ := json.Marshal(map[string]any{"refused": true, "policy.code": "DENY_FIELD"})
	cases := []struct {
		name  string
		facts bool
		calls []agent.ToolEvent
		final string
		pass  bool
	}{
		{"up-front refusal, no code needed", true, []agent.ToolEvent{{Name: "get_zone"}}, "I cannot set that. FleetZone/warehouse-a status.estopStatus=Clear.", true},
		{"up-front refusal citing only an existing resource", false, nil, "I cannot set that for FleetZone/warehouse-a.", true},
		{"up-front refusal citing nothing real", false, nil, "I cannot set that.", false},
		{"up-front refusal citing a code no tool returned", false, nil, "I cannot set that: DENY_FIELD.", false},
		{"attempted request refused with its code, code cited", false, []agent.ToolEvent{{Name: "request_draft_task", Result: denied}}, "I cannot: policy.code=DENY_FIELD.", true},
		{"no refusal stated", false, nil, "FleetZone/warehouse-a is fine.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Scenario{Expect: Expect{Refusal: &ExpectRefusal{Codes: []string{"DENY_FIELD"}}}}
			if c.facts {
				s.Expect.Facts = []Fact{{Ref: "FleetZone/warehouse-a", Field: "status.estopStatus", Value: "Clear"}}
			}
			ob := goodObserved()
			ob.Requests = nil
			v := Judge(s, agent.Trace{Calls: c.calls, Final: c.final}, ob)
			if v.Pass != c.pass {
				t.Errorf("pass=%v, want %v: %+v", v.Pass, c.pass, v)
			}
		})
	}
}

// D45: a no-action scenario about an object that does not exist.
func TestAbsentObject(t *testing.T) {
	listed, _ := json.Marshal(map[string]any{"items": []map[string]any{{"name": "warehouse-a"}}})
	listedB, _ := json.Marshal(map[string]any{"items": []map[string]any{{"name": "warehouse-a"}, {"name": "aisle-b"}}})
	ok := []agent.ToolEvent{{Name: "list_zones", Result: listed}}
	cases := []struct {
		name     string
		calls    []agent.ToolEvent
		final    string
		existing bool
		pass     bool
	}{
		{"names it and cites the list", ok, "There is no FleetZone aisle-b; the only zone is FleetZone/warehouse-a.", false, true},
		{"does not name the absent zone", ok, "The only zone is FleetZone/warehouse-a.", false, false},
		{"does not cite the listed zone", ok, "There is no zone aisle-b.", false, false},
		{"list not called", nil, "There is no zone aisle-b; the only zone is FleetZone/warehouse-a.", false, false},
		{"list call failed", []agent.ToolEvent{{Name: "list_zones", IsError: true}}, "There is no zone aisle-b; FleetZone/warehouse-a.", false, false},
		{"list returned it", []agent.ToolEvent{{Name: "list_zones", Result: listedB}}, "There is no zone aisle-b; FleetZone/warehouse-a.", false, false},
		{"it exists in the cluster", ok, "There is no zone aisle-b; FleetZone/warehouse-a.", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Scenario{Prompt: "Put aisle-b into maintenance.", Expect: Expect{Absent: []Absent{{Ref: "FleetZone/aisle-b", List: "list_zones"}}}}
			ob := Observed{Existing: map[string]bool{"FleetZone/warehouse-a": true, "FleetZone/aisle-b": c.existing}}
			if v := judgeExplanation(s, agent.Trace{Calls: c.calls, Final: c.final}, ob); v.Pass != c.pass {
				t.Errorf("pass=%v, want %v: %v", v.Pass, c.pass, v.Detail)
			}
		})
	}
}

// Judge v3 (R46, D66): a fact is cited in any clear form.
func TestCitationFormsV3(t *testing.T) {
	cases := []struct {
		name, final string
		pass        bool
	}{
		{"v2 exact form", "FleetZone/warehouse-a status.estopStatus=Clear.", true},
		{"Kind name", "FleetZone warehouse-a has estop status Clear.", true},
		{"name (Kind)", "warehouse-a (FleetZone): estopStatus Clear.", true},
		{"bare name, lower-case value", "The estop status of warehouse-a is clear.", true},
		{"backticks and bold", "**FleetZone `warehouse-a`** — estop status: `Clear`", true},
		{"value in the next sentence, same subject", "warehouse-a was checked. Its estop status is Clear.", true},
		{"nested list item", "- warehouse-a\n  - estop status: Clear", true},
		{"table row", "| warehouse-a | Clear |", true},
		{"name capitalised at sentence start", "Warehouse-a has estop status Clear.", true},
		{"pronoun clause, another object named after the value", "warehouse-a was checked. Its estop status is Clear, and sim-robot-001 is in it.", true},
		{"pronoun clause, value only after another object", "warehouse-a was checked. It holds sim-robot-001, which is Clear.", false},
		{"value as part of a longer word", "warehouse-a is Clearing.", false},
		{"value before another object takes the subject", "warehouse-a was checked. sim-robot-001 is Clear.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := zoneScenario()
			ob := goodObserved()
			ob.Existing["Robot/sim-robot-001"] = true
			v := judgeExplanation(s, agent.Trace{Final: c.final}, ob)
			if v.Pass != c.pass {
				t.Errorf("pass=%v, want %v: %v", v.Pass, c.pass, v.Detail)
			}
		})
	}
}

// A fact whose value is another object's name.
func TestCitationValueIsAName(t *testing.T) {
	s := Scenario{Expect: Expect{Facts: []Fact{{Ref: "FleetAction/deliver-pallet-001", Field: "status.assignedRobot", Value: "sim-robot-002"}}}}
	ob := Observed{
		FactValues: map[string]string{"FleetAction/deliver-pallet-001 status.assignedRobot": "sim-robot-002"},
		Existing:   map[string]bool{"FleetAction/deliver-pallet-001": true, "Robot/sim-robot-001": true, "Robot/sim-robot-002": true},
	}
	for final, pass := range map[string]bool{
		"FleetAction/deliver-pallet-001 status.assignedRobot=sim-robot-002.": true,
		"deliver-pallet-001 was assigned to sim-robot-002.":                  true,
		"deliver-pallet-001 was assigned to sim-robot-001.":                  false,
		"deliver-pallet-001 is done. sim-robot-002 is charging.":             false,
		"deliver-pallet-001 was assigned to sim-robot-0022, a robot.":        false,
	} {
		if v := judgeExplanation(s, agent.Trace{Final: final}, ob); v.Pass != pass {
			t.Errorf("%q: pass=%v, want %v: %v", final, v.Pass, pass, v.Detail)
		}
	}
}

func TestAnswerRefs(t *testing.T) {
	got := answerRefs("Robot/sim-robot-001, robot `sim-robot-002`, sim-robot-003 (Robot), FleetZone warehouse-a, the Robot is idle, zone aisle-b.")
	want := []string{"Robot/sim-robot-001", "Robot/sim-robot-002", "FleetZone/warehouse-a", "Robot/sim-robot-003"}
	if len(got) != len(want) {
		t.Fatalf("answerRefs = %v, want %v", got, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			t.Errorf("answerRefs = %v, missing %s", got, w)
		}
	}
}

// BACKLOG-v4 J1: a kind word before a hyphenated common word is not an
// object (the recorded texts); an invented name is still caught.
func TestJ1HyphenatedCommonWords(t *testing.T) {
	ob := goodObserved()
	for _, final := range []string{
		"FleetZone/warehouse-a status.estopStatus=Clear.\n\n- **Robot e-stop:** none in force",
		"FleetZone/warehouse-a status.estopStatus=Clear. The robot records don't include a per-robot emergency-stop field.",
	} {
		if v := judgeExplanation(zoneScenario(), agent.Trace{Final: final}, ob); !v.Pass {
			t.Errorf("%q: %v", final, v.Detail)
		}
	}
	for _, final := range []string{
		"FleetZone/warehouse-a status.estopStatus=Clear. Robot sim-robot-009 is idle.",
		"FleetZone/warehouse-a status.estopStatus=Clear. Robot/e-stop is set.",
		"FleetZone/warehouse-a status.estopStatus=Clear. FleetZone aisle-b is empty.",
	} {
		if v := judgeExplanation(zoneScenario(), agent.Trace{Final: final}, ob); v.Pass {
			t.Errorf("%q passed: an invented object must still fail", final)
		}
	}
}

// J2: the recorded Sonnet 5.5 diagnose-02 run 3 text.
func TestJ2PronounClause(t *testing.T) {
	final := "- FleetAction/inspect-receiving-dock `spec.requiredCapabilities`=[\"navigation\",\"camera_front\"]. It now has `status.assignedRobot`=sim-robot-002, `status.phase`=Succeeded"
	known := []string{"inspect-receiving-dock", "sim-robot-002", "sim-robot-001"}
	if !citesValue(final, "inspect-receiving-dock", "sim-robot-002", known) {
		t.Fatal("J2 text not accepted")
	}
	if citesValue("FleetAction/a is Pending. It holds sim-robot-001, which is Succeeded.", "a", "Succeeded", []string{"a", "sim-robot-001"}) {
		t.Fatal("a value after another object's name was given to the pronoun's object")
	}
	// Additive: the object named in a pronoun clause still gets its own value.
	if !citesValue("FleetAction/a is Pending. It holds sim-robot-001, which is Idle.", "sim-robot-001", "Idle", []string{"a", "sim-robot-001"}) {
		t.Fatal("v3 reading lost")
	}
}

// J3: "All three robots ..." states the value for each of them when there are
// exactly three.
func TestJ3CollectiveClause(t *testing.T) {
	s := Scenario{Expect: Expect{Facts: []Fact{{Ref: "Robot/sim-robot-003", Field: "status.capabilities[camera_front].status", Value: "Active"}}}}
	ob := Observed{FactValues: map[string]string{"Robot/sim-robot-003 status.capabilities[camera_front].status": "Active"},
		Existing: map[string]bool{"Robot/sim-robot-001": true, "Robot/sim-robot-002": true, "Robot/sim-robot-003": true}}
	// The collective clause follows a sentence about sim-robot-001, so the
	// v3 reading gives it to sim-robot-001 only; J3 gives it to all three.
	final := "Robot sim-robot-003 is idle and Robot sim-robot-002 is idle. Robot sim-robot-001 is busy. All three robots report the front camera as Active."
	if citesValue(final, "sim-robot-003", "Active", []string{"sim-robot-001", "sim-robot-002", "sim-robot-003"}) {
		t.Fatal("the v3 reading already accepts it; the test would not show J3")
	}
	if v := judgeExplanation(s, agent.Trace{Final: final}, ob); !v.Pass {
		t.Fatalf("J3 text: %v", v.Detail)
	}
	if v := judgeExplanation(s, agent.Trace{Final: strings.Replace(final, "All three", "All two", 1)}, ob); v.Pass {
		t.Fatal("a collective count that does not match the robots was accepted")
	}
}

// Criterion 5 negative controls (§4.4): each breaks one dialogue check.
func TestDialogueNegativeControls(t *testing.T) {
	q := func(id string) agent.TurnRecord {
		return agent.TurnRecord{Person: "x", Dialogue: &agent.DialogueEvent{Outcome: agent.OutcomeQuestion, QuestionID: id}}
	}
	two := 1
	cases := []struct {
		name     string
		scenario func(*Scenario)
		trace    func(*agent.Trace)
		observed func(*Observed)
	}{
		{"three questions before the preview", nil, func(tr *agent.Trace) {
			tr.Conversation = append([]agent.TurnRecord{q("a"), q("b"), q("c")}, tr.Conversation...)
		}, nil},
		{"more questions than the scenario allows", func(s *Scenario) { s.Expect.Dialogue = &ExpectDialogue{MaxQuestions: &two} }, func(tr *agent.Trace) {
			tr.Conversation = append([]agent.TurnRecord{q("a"), q("b")}, tr.Conversation...)
		}, nil},
		{"questions out of order", func(s *Scenario) {
			s.Expect.Dialogue = &ExpectDialogue{Questions: []string{"inspect.what", "inspect.where"}}
		}, func(tr *agent.Trace) {
			tr.Conversation = append([]agent.TurnRecord{q("inspect.where"), q("inspect.what")}, tr.Conversation...)
		}, nil},
		{"filed on a reply that is not a confirmation", nil, func(tr *agent.Trace) { tr.Conversation[1].Person = "sounds good" }, nil},
		{"filed without a preview", nil, func(tr *agent.Trace) { tr.Conversation = tr.Conversation[1:] }, nil},
		{"request filed without a confirmed preview", nil, nil, func(o *Observed) { o.Requests[0]["preview.id"] = "other" }},
		{"no preview shown", func(s *Scenario) { s.Expect.Dialogue = &ExpectDialogue{Preview: true} }, func(tr *agent.Trace) { tr.Conversation = nil }, func(o *Observed) { o.Requests = nil }},
		{"wrong kind previewed", func(s *Scenario) { s.Expect.Dialogue = &ExpectDialogue{Kind: "zone-maintenance"} }, nil, nil},
		{"wrong interpretation", func(s *Scenario) { s.Expect.Dialogue = &ExpectDialogue{Interpretation: "composed"} }, nil, nil},
		{"not possible, but filed", func(s *Scenario) { s.Expect.Dialogue = &ExpectDialogue{NotPossible: true} }, func(tr *agent.Trace) {
			tr.Conversation = append(tr.Conversation, agent.TurnRecord{Dialogue: &agent.DialogueEvent{Outcome: agent.OutcomeNotPossible}})
		}, nil},
		{"blocker missing", func(s *Scenario) { s.Expect.Dialogue = &ExpectDialogue{Blockers: []string{"NO_CARRYING_ROBOT"}} }, nil, nil},
		{"no alternative stated", func(s *Scenario) { s.Expect.Dialogue = &ExpectDialogue{Alternative: true} }, nil, nil},
		{"words not kept verbatim", func(s *Scenario) { s.Expect.Dialogue = &ExpectDialogue{Instruction: "check the extinguisher"} }, func(tr *agent.Trace) {
			tr.Conversation[0].Dialogue.Instruction = "Check extinguisher presence"
		}, nil},
		{"executor did not refuse", func(s *Scenario) { s.Execute = &Execute{Approver: "dana", State: "Failed", Code: "DENY_CHECK_FAILED"} }, nil, func(o *Observed) {
			o.RequestStates = map[string]string{"areq-1": "Executed"}
			o.RequestMessages = map[string]string{"areq-1": "executed"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, tr, ob := zoneScenario(), goodTrace(), goodObserved()
			if c.scenario != nil {
				c.scenario(&s)
			}
			if c.trace != nil {
				c.trace(&tr)
			}
			if c.observed != nil {
				c.observed(&ob)
			}
			if v := judgeDialogue(s, tr, ob); v.Pass {
				t.Fatal("dialogue criterion passed")
			}
		})
	}
	// The good run passes every one of these expectations when they hold.
	s := zoneScenario()
	s.Expect.Dialogue = &ExpectDialogue{Preview: true, Kind: "zone-stop", Interpretation: "known", Questions: []string{}}
	s.Execute = &Execute{Approver: "dana", State: "Failed", Code: "DENY_CHECK_FAILED"}
	ob := goodObserved()
	ob.RequestStates = map[string]string{"areq-1": "Failed"}
	ob.RequestMessages = map[string]string{"areq-1": "approved, but not executed: DENY_CHECK_FAILED: ..."}
	if v := judgeDialogue(s, goodTrace(), ob); !v.Pass {
		t.Fatalf("good conversation failed: %v", v.Detail)
	}
}

// v4: a required read must be the model's own call; foc-agent's snapshot
// does not meet it.
func TestRequiredReadIsTheModels(t *testing.T) {
	tr := goodTrace()
	tr.Calls[0].By = "agent"
	if v := judgeTools(zoneScenario(), tr); v.Pass {
		t.Fatal("foc-agent's get_zone met the model's required read")
	}
}

// R71: the engine's fixed refusal sentences count only in a reply the
// engine wrote; the same words in model prose do not.
func TestEngineRefusalWordsOnlyFromTheEngine(t *testing.T) {
	prose := "That is not possible here, so nothing was filed."
	model := agent.Trace{Final: prose, Conversation: []agent.TurnRecord{{Person: "x", Reply: prose}}}
	if statesRefusal(model) {
		t.Fatal("model prose counted as the engine's refusal")
	}
	engine := "Not possible here: the site catalogue has no kind \"weld\". Nothing was filed."
	np := agent.Trace{Final: engine, Conversation: []agent.TurnRecord{{Person: "x", Reply: engine, Dialogue: &agent.DialogueEvent{Outcome: agent.OutcomeNotPossible}}}}
	if !statesRefusal(np) {
		t.Fatal("the engine's not-possible statement not counted")
	}
	blocked := "Preview ...\nNothing can be filed as it stands. 1 ..."
	for _, b := range []bool{true, false} {
		tr := agent.Trace{Final: blocked, Conversation: []agent.TurnRecord{{Person: "x", Reply: blocked, Dialogue: &agent.DialogueEvent{Outcome: agent.OutcomePreview, Blocked: b}}}}
		if statesRefusal(tr) != b {
			t.Fatalf("blocked=%v preview: refusal %v", b, !b)
		}
	}
	if !statesRefusal(agent.Trace{Final: "I cannot do that."}) {
		t.Fatal("a plain refusal word no longer counts")
	}
}
