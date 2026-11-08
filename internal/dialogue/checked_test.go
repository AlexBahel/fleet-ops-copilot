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
	"fmt"
	"strings"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// InspectFacts is check_task's facts for an inspection of staging-shelf-a
// on the live healthy-fleet capture: sim-robot-001 is in the zone and
// eligible; sim-robot-002 and sim-robot-003 are Idle with no current zone
// (filter 2), the dominant uncited fact of the v5 record runs (D92).
func inspectFacts() feasibility.Facts {
	caps := map[string]string{"camera_front": "Active", "navigation": "Active"}
	return feasibility.Facts{
		Zones: []feasibility.ZoneFacts{{Name: "warehouse-a", EstopStatus: "Clear", RobotCount: 1}},
		Robots: []feasibility.RobotFacts{
			{Name: "sim-robot-001", Phase: "Idle", Battery: i32(99), CurrentZone: "warehouse-a", Capabilities: caps, Members: []feasibility.MemberFit{{Member: "inspect", Eligible: true}}},
			{Name: "sim-robot-002", Phase: "Idle", Battery: i32(85), Capabilities: caps, Members: []feasibility.MemberFit{{Member: "inspect", Filters: []int{2}}}},
			{Name: "sim-robot-003", Phase: "Idle", Battery: i32(85), Capabilities: caps, Members: []feasibility.MemberFit{{Member: "inspect", Filters: []int{2}}}},
		},
	}
}

func inspectPreview(t *testing.T, robot string) Preview {
	t.Helper()
	e := &Engine{Cat: cat(t)}
	fields := map[string]string{"location": "staging-shelf-a"}
	if robot != "" {
		fields["robot"] = robot
	}
	o := e.Propose(newState("Inspect staging-shelf-a for damage."), Input{Ask: "inspect", Kind: "inspect-damage", Fields: fields}, snap)
	if o.Proposal == nil {
		t.Fatalf("outcome %+v", o)
	}
	chk := tools.CheckTaskResult{Report: feasibility.Report{Verdict: feasibility.LikelyFeasible, Eligible: map[string][]string{"inspect": {"sim-robot-001"}}, Facts: inspectFacts()}}
	return RenderPreview(o.Proposal, chk)
}

// The preview states every robot the check considered, the one it does not
// name as eligible included, with its phase and why it is not eligible.
func TestPreviewStatesWhatTheEngineRead(t *testing.T) {
	pv := inspectPreview(t, "")
	for _, want := range []string{
		"\nChecked:\n",
		"  FleetZone/warehouse-a: status.estopStatus=Clear, status.robotCount=1, no ZoneMaintenance Active or Scheduled over it.",
		"  Robot/sim-robot-001: status.phase=Idle, status.batteryPercent=99, status.currentZone=warehouse-a, camera_front=Active, navigation=Active; eligible now.",
		"  Robot/sim-robot-002: status.phase=Idle, status.batteryPercent=85, no status.currentZone, camera_front=Active, navigation=Active; not eligible now: filter 2 (not in the action's zone).",
	} {
		if !strings.Contains(pv.Text, want) {
			t.Errorf("preview lacks %q:\n%s", want, pv.Text)
		}
	}
	// The choices stay last: they are what the person answers.
	if !strings.HasSuffix(pv.Text, ChoicesOpen) || strings.Index(pv.Text, "Checked:") > strings.Index(pv.Text, feasibility.Statement) {
		t.Fatalf("order:\n%s", pv.Text)
	}
}

// The robot the request names comes first, marked so.
func TestCheckedNamedRobotFirst(t *testing.T) {
	pv := inspectPreview(t, "sim-robot-003")
	i3, i1 := strings.Index(pv.Text, "Robot/sim-robot-003:"), strings.Index(pv.Text, "Robot/sim-robot-001:")
	if i3 < 0 || i1 < 0 || i3 > i1 || !strings.Contains(pv.Text, "navigation=Active (the robot the request names); not eligible now") {
		t.Fatalf("preview:\n%s", pv.Text)
	}
}

// At most five robot lines, then the rest counted.
func TestCheckedAtMostFiveRobots(t *testing.T) {
	var f feasibility.Facts
	for i := 1; i <= 7; i++ {
		f.Robots = append(f.Robots, feasibility.RobotFacts{Name: fmt.Sprintf("sim-robot-%03d", i), Phase: "Idle", Battery: i32(50), Members: []feasibility.MemberFit{{Member: "m", Eligible: true}}})
	}
	lines := CheckedPreview(f, nil)
	if len(lines) != 6 || lines[5] != "and 2 more robots." || !strings.HasPrefix(lines[4], "Robot/sim-robot-005:") {
		t.Fatalf("lines %q", lines)
	}
}

// Members that differ are stated per member; an adapter that excludes a
// robot is named with its phase.
func TestCheckedPerMemberAndAdapter(t *testing.T) {
	r := feasibility.RobotFacts{Name: "sim-robot-002", Phase: "Idle", Adapter: "sim-fleet-adapter", AdapterPhase: "Disconnected",
		Members: []feasibility.MemberFit{{Member: "pickup", Eligible: true}, {Member: "dropoff", Filters: []int{9}}}}
	got := robotLine(r, false)
	want := "Robot/sim-robot-002: status.phase=Idle, status.batteryPercent unreported; member pickup eligible now; member dropoff not eligible now: filter 9 (served by an adapter unfit for dispatch): FleetAdapter/sim-fleet-adapter status.phase=Disconnected."
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// A question states the fleet state the engine read for it, before the
// question; the question's text and choices are unchanged and last.
func TestQuestionStatesTheSnapshot(t *testing.T) {
	e := &Engine{Cat: cat(t)}
	o := e.Propose(newState("Inspect staging-shelf-a."), Input{Ask: "inspect", Fields: map[string]string{"location": "staging-shelf-a"}}, snap)
	if o.Question == nil {
		t.Fatalf("outcome %+v", o)
	}
	text := o.Question.Render()
	for _, want := range []string{
		CheckedSnapshotHeader + "\n  FleetZone/warehouse-a: status.estopStatus=Clear, status.robotCount=1.",
		"  Robot/sim-robot-002: status.phase=Idle, status.batteryPercent=85, no status.currentZone.",
		"  Robot/sim-robot-001: status.phase=InProgress, status.batteryPercent=21, status.currentZone=warehouse-a.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("question lacks %q:\n%s", want, text)
		}
	}
	if !strings.HasSuffix(text, "4 Something else — describe it") {
		t.Fatalf("question:\n%s", text)
	}
}
