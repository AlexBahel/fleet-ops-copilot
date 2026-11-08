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
	"strings"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/dialogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

func i32p(v int32) *int32 { return &v }

// enginePreview renders the preview fleetops-agent shows for an inspection
// of staging-shelf-a on the live healthy-fleet capture, with facts as
// check_task returns them (none at 528d8de: the field did not exist).
func enginePreview(t *testing.T, facts feasibility.Facts) string {
	t.Helper()
	cat, err := catalogue.Load("../../catalogue/warehouse-a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e := &dialogue.Engine{Cat: cat}
	snap := dialogue.Snapshot{Zones: []string{"warehouse-a"}, Waypoints: []dialogue.Waypoint{{Zone: "warehouse-a", Name: "staging-shelf-a", X: "12.1", Y: "8.1"}}}
	o := e.Propose(&dialogue.State{Message: "Inspect staging-shelf-a for damage."}, dialogue.Input{Ask: "inspect", Kind: "inspect-damage", Fields: map[string]string{"location": "staging-shelf-a"}}, snap)
	if o.Proposal == nil {
		t.Fatalf("outcome %+v", o)
	}
	chk := tools.CheckTaskResult{Report: feasibility.Report{Verdict: feasibility.LikelyFeasible, Eligible: map[string][]string{"inspect": {"sim-robot-001"}}, Facts: facts}}
	return dialogue.RenderPreview(o.Proposal, chk).Text
}

func healthyFacts(filter int) feasibility.Facts {
	caps := map[string]string{"camera_front": "Active", "navigation": "Active"}
	return feasibility.Facts{
		Zones: []feasibility.ZoneFacts{{Name: "warehouse-a", EstopStatus: "Clear", RobotCount: 1}},
		Robots: []feasibility.RobotFacts{
			{Name: "sim-robot-001", Phase: "Idle", Battery: i32p(99), CurrentZone: "warehouse-a", Capabilities: caps, Members: []feasibility.MemberFit{{Member: "inspect", Eligible: true}}},
			{Name: "sim-robot-002", Phase: "Idle", Battery: i32p(85), Capabilities: caps, Adapter: "sim-fleet-adapter", AdapterPhase: "Disconnected", Members: []feasibility.MemberFit{{Member: "inspect", Filters: []int{filter}}}},
		},
	}
}

// D92, the dominant case of the v5 record runs: the model writes no text,
// the required fact is the phase of a robot the preview does not name as
// eligible. The judge grades the engine's "Checked:" block like any other
// text the person saw; without the block (528d8de) the fact is not cited.
func TestJudgeCreditsTheEnginesCheckedBlock(t *testing.T) {
	s := Scenario{ID: "t", Category: "guided", Expect: Expect{Facts: []Fact{
		{Ref: "Robot/sim-robot-002", Field: "status.phase", Value: "Idle"},
		{Ref: "Robot/sim-robot-001", Field: "status.batteryPercent", Value: "99"},
	}}}
	ob := Observed{
		FactValues: map[string]string{"Robot/sim-robot-002 status.phase": "Idle", "Robot/sim-robot-001 status.batteryPercent": "99"},
		Existing:   map[string]bool{"Robot/sim-robot-001": true, "Robot/sim-robot-002": true, "FleetZone/warehouse-a": true, "FleetAdapter/sim-fleet-adapter": true},
	}
	with := agent.Trace{Final: enginePreview(t, healthyFacts(2))}
	if c := judgeExplanation(s, with, ob); !c.Pass {
		t.Fatalf("Checked block not credited: %v\n%s", c.Detail, with.Final)
	}
	without := agent.Trace{Final: enginePreview(t, feasibility.Facts{})}
	if c := judgeExplanation(s, without, ob); c.Pass {
		t.Fatalf("a preview without the block was credited:\n%s", without.Final)
	}
}

// The block states a robot's state; it never reads as a refusal of the
// request, whichever filter excludes a robot.
func TestCheckedBlockIsNoRefusal(t *testing.T) {
	for _, f := range feasibility.Filters {
		text := enginePreview(t, healthyFacts(f.Number))
		block := text[strings.Index(text, "Checked:"):strings.Index(text, feasibility.Statement)]
		for _, w := range refusalWords {
			if containsFold(block, w) {
				t.Errorf("filter %d: the block says %q:\n%s", f.Number, w, block)
			}
		}
		tr := agent.Trace{Final: text, Conversation: []agent.TurnRecord{{Reply: text, Dialogue: &agent.DialogueEvent{Outcome: agent.OutcomePreview}}}}
		if statesRefusal(tr) {
			t.Errorf("filter %d: a likely-feasible preview reads as a refusal:\n%s", f.Number, text)
		}
	}
}
