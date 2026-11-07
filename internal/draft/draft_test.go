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

package draft

import (
	"encoding/json"
	"testing"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
)

func payload(t *testing.T, a swarmv1.FleetActionSpec) map[string]any {
	t.Helper()
	var m map[string]any
	if a.Payload == nil {
		return nil
	}
	if err := json.Unmarshal(a.Payload.Raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// An inspection carries its location as waypoint, x and y in the payload,
// with what to look for and the person's words (BACKLOG-v4 P1).
func TestInspectPayload(t *testing.T) {
	task, err := Task("warehouse-a", &fleetopsv1.DraftTaskParams{TaskName: "inspect-dock", Actions: []fleetopsv1.DraftAction{{
		Name: "inspect", Type: "Navigate", RequiredCapabilities: []string{"navigation", "camera_front"},
		Location: &fleetopsv1.Location{Zone: "warehouse-a", Waypoint: "receiving-dock-1", X: "5.1", Y: "2.1"},
		LookFor:  []string{"damage"}, Instruction: "look at the dock door",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !task.Spec.Draft || task.Spec.FailurePolicy != swarmv1.FailurePolicyFailFast || task.Spec.Flow != nil {
		t.Fatalf("spec %+v", task.Spec)
	}
	a := task.Spec.Actions[0].Action
	if a.Zone != "warehouse-a" || a.OnFailure != swarmv1.ActionFailureRequeue {
		t.Fatalf("action %+v", a)
	}
	p := payload(t, a)
	if p["waypoint"] != "receiving-dock-1" || p["x"] != 5.1 || p["y"] != 2.1 || p["instruction"] != "look at the dock door" {
		t.Fatalf("payload %v", p)
	}
}

// A patrol route becomes one member per stop in a flow loop of N rounds,
// and no member keeps dependsOn (Swarmada's CEL rule).
func TestPatrolRouteFlow(t *testing.T) {
	task, err := Task("warehouse-a", &fleetopsv1.DraftTaskParams{TaskName: "patrol", Repeat: &fleetopsv1.Repeat{Times: 3}, Actions: []fleetopsv1.DraftAction{{
		Name: "patrol", Type: "Patrol", OnFailure: "Alert", DependsOn: []string{"x"},
		Route: []fleetopsv1.Location{{Zone: "warehouse-a", Waypoint: "a", X: "1", Y: "2"}, {Zone: "warehouse-a", Waypoint: "b", X: "3", Y: "4"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Spec.Actions) != 2 || task.Spec.Actions[0].Name != "patrol-1" || task.Spec.Actions[1].Name != "patrol-2" {
		t.Fatalf("members %+v", task.Spec.Actions)
	}
	for _, m := range task.Spec.Actions {
		if len(m.DependsOn) != 0 || m.Action.OnFailure != swarmv1.ActionFailureAlert {
			t.Fatalf("member %+v", m)
		}
	}
	l := task.Spec.Flow.Loop
	if l == nil || l.Mode != swarmv1.LoopModeTimes || *l.Times != 3 || len(l.Body.Sequence) != 2 || *l.Body.Sequence[1].Run != "patrol-2" {
		t.Fatalf("flow %+v", task.Spec.Flow)
	}
	if p := payload(t, task.Spec.Actions[1].Action); p["waypoint"] != "b" || p["x"] != 3.0 {
		t.Fatalf("payload %v", p)
	}
}

func TestDeliverAndCharge(t *testing.T) {
	task, err := Task("warehouse-a", &fleetopsv1.DraftTaskParams{TaskName: "t", Actions: []fleetopsv1.DraftAction{
		{Name: "pickup", Type: "PickUp", Location: &fleetopsv1.Location{Zone: "warehouse-a", Waypoint: "a", X: "1", Y: "1"}},
		{Name: "dropoff", Type: "DropOff", DependsOn: []string{"pickup"}, Location: &fleetopsv1.Location{Zone: "warehouse-a", Waypoint: "b", X: "2", Y: "2"}},
		{Name: "charge", Type: "Charge", RobotSelector: "sim-robot-001", Dock: "charging-bay"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if task.Spec.Flow != nil || task.Spec.Actions[1].DependsOn[0] != "pickup" {
		t.Fatalf("spec %+v", task.Spec)
	}
	c := task.Spec.Actions[2].Action
	if c.RobotSelector != "sim-robot-001" || payload(t, c)["dock"] != "charging-bay" {
		t.Fatalf("charge %+v", c)
	}
}

func TestBadCoordinate(t *testing.T) {
	_, err := Task("ns", &fleetopsv1.DraftTaskParams{TaskName: "t", Actions: []fleetopsv1.DraftAction{{Name: "a", Type: "Navigate", Location: &fleetopsv1.Location{Zone: "z", X: "east", Y: "1"}}}})
	if err == nil {
		t.Fatal("want an error for x=east")
	}
}
