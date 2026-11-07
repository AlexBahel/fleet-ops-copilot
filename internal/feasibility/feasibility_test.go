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

package feasibility

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/draft"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
)

func i32(v int32) *int32 { return &v }

var now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func robot(name, phase string, battery int32, caps ...string) swarmv1.Robot {
	r := swarmv1.Robot{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "warehouse-a"}}
	r.Spec.Adapter.Name = "sim-fleet-adapter"
	r.Spec.Zone = "warehouse-a"
	r.Status.Phase = swarmv1.RobotPhase(phase)
	r.Status.BatteryPercent = i32(battery)
	r.Status.CurrentZone = "warehouse-a"
	r.Status.EstopState = "Normal"
	for _, c := range caps {
		r.Status.Capabilities = append(r.Status.Capabilities, swarmv1.CapabilityStatusEntry{Name: c, Status: swarmv1.CapabilityStatusActive})
	}
	return r
}

func fleet() Snapshot {
	z := swarmv1.FleetZone{ObjectMeta: metav1.ObjectMeta{Name: "warehouse-a", Namespace: "warehouse-a"}}
	z.Spec.Waypoints = []swarmv1.Waypoint{{Name: "receiving-dock-1", X: 5.1, Y: 2.1}, {Name: "charging-bay", X: 1.1, Y: 1.1}}
	ad := swarmv1.FleetAdapter{ObjectMeta: metav1.ObjectMeta{Name: "sim-fleet-adapter", Namespace: "warehouse-a"}}
	ad.Status.Phase = swarmv1.FleetAdapterPhaseConnected
	ad.Status.Conformance = swarmv1.ConformanceStatePassed
	for _, t := range []string{"Navigate", "PickUp", "DropOff", "Patrol", "Charge"} {
		ad.Status.SupportedActions = append(ad.Status.SupportedActions, swarmv1.SupportedAction{ActionType: t})
	}
	ad.Status.SupportedActions = append(ad.Status.SupportedActions, swarmv1.SupportedAction{ActionType: "Custom", CustomType: "explore-floor"})
	return Snapshot{
		Namespace: "warehouse-a", Now: now, Zones: []swarmv1.FleetZone{z}, Adapters: []swarmv1.FleetAdapter{ad},
		Robots: []swarmv1.Robot{
			robot("sim-robot-001", "InProgress", 21, "navigation", "camera_front"),
			robot("sim-robot-002", "Idle", 85, "navigation", "camera_front"),
			robot("sim-robot-003", "Idle", 85, "navigation"),
		},
		Config: Config{Read: true},
	}
}

func task(t *testing.T, actions ...fleetopsv1.DraftAction) *swarmv1.FleetTask {
	t.Helper()
	ft, err := draft.Task("warehouse-a", &fleetopsv1.DraftTaskParams{TaskName: "t1", Actions: actions})
	if err != nil {
		t.Fatal(err)
	}
	return ft
}

var dock = &fleetopsv1.Location{Zone: "warehouse-a", Waypoint: "receiving-dock-1", X: "5.1", Y: "2.1"}

func codes(fs []Finding) string {
	var c []string
	for _, f := range fs {
		c = append(c, f.Code)
	}
	return strings.Join(c, ",")
}

func TestInspectLikelyFeasible(t *testing.T) {
	r := Evaluate(Proposal{Creating: true, Task: task(t, fleetopsv1.DraftAction{Name: "inspect", Type: "Navigate", RequiredCapabilities: []string{"navigation", "camera_front"}, Location: dock})}, fleet())
	if r.Verdict != LikelyFeasible || len(r.Blockers) != 0 || strings.Join(r.Eligible["inspect"], ",") != "sim-robot-002" {
		t.Fatalf("report %+v", r)
	}
	if r.Statement != Statement {
		t.Fatalf("statement %q", r.Statement)
	}
}

// Never "guaranteed" (DESIGN.md §3.10.4).
func TestNeverGuaranteed(t *testing.T) {
	r := Evaluate(Proposal{Creating: true, Task: task(t, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", Location: dock})}, fleet())
	b, _ := json.Marshal(r)
	if strings.Contains(strings.ToLower(string(b)), "guarantee") {
		t.Fatalf("report says guaranteed: %s", b)
	}
}

// Owner's instruction on phase 0: a Charge for a robot below its battery
// floor is a named blocker with its reason and alternatives.
func TestChargeBelowFloor(t *testing.T) {
	s := fleet()
	s.Robots[0].Spec.Constraints = &swarmv1.ClassConstraints{MinBatteryPctForAction: i32(25)}
	r := Evaluate(Proposal{Creating: true, Task: task(t, fleetopsv1.DraftAction{Name: "charge", Type: "Charge", RobotSelector: "sim-robot-001", Dock: "charging-bay"})}, s)
	if r.Verdict != Blocked || codes(r.Blockers) != CodeChargeBelowFloor {
		t.Fatalf("blockers %s", codes(r.Blockers))
	}
	if !strings.Contains(r.Blockers[0].Reason, "21%, below its floor of 25%") || len(r.Alternatives) != 2 {
		t.Fatalf("report %+v", r)
	}
	// Without a floor the same Charge is only waiting for the robot to be idle.
	r = Evaluate(Proposal{Creating: true, Task: task(t, fleetopsv1.DraftAction{Name: "charge", Type: "Charge", RobotSelector: "sim-robot-001"})}, fleet())
	if r.Verdict != LikelyFeasible || codes(r.Warnings) != CodeRobotsBusy {
		t.Fatalf("no floor: %+v", r)
	}
}

// Owner's instruction on phase 0: a deliver no robot can carry.
func TestDeliverWithoutCarryingRobot(t *testing.T) {
	r := Evaluate(Proposal{Creating: true, Task: task(t,
		fleetopsv1.DraftAction{Name: "pickup", Type: "PickUp", RequiredCapabilities: []string{"navigation", "transport.payload"}, Location: dock},
		fleetopsv1.DraftAction{Name: "dropoff", Type: "DropOff", DependsOn: []string{"pickup"}, RequiredCapabilities: []string{"navigation", "transport.payload"}, Location: dock},
	)}, fleet())
	if r.Verdict != Blocked || codes(r.Blockers) != CodeNoCarryingRobot+","+CodeNoCarryingRobot {
		t.Fatalf("blockers %s", codes(r.Blockers))
	}
	if !strings.Contains(strings.Join(r.Alternatives, "|"), "move a robot there instead") {
		t.Fatalf("alternatives %v", r.Alternatives)
	}
}

func TestBlockers(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Snapshot)
		act  fleetopsv1.DraftAction
		want string
	}{
		{"zone stopped", func(s *Snapshot) { s.Zones[0].Annotations = map[string]string{estopTriggered: "spill"} }, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", Location: dock}, CodeZoneStopped},
		{"namespace stop", func(s *Snapshot) { s.Config.NamespaceStop = true }, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", Location: dock}, CodeNamespaceStopped},
		{"unknown zone", nil, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", Location: &fleetopsv1.Location{Zone: "aisle-b", X: "1", Y: "1"}}, CodeZoneNotFound},
		{"unknown waypoint", nil, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", Location: &fleetopsv1.Location{Zone: "warehouse-a", Waypoint: "dock-9", X: "1", Y: "1"}}, CodeWaypointNotFound},
		{"type not in catalog", nil, fleetopsv1.DraftAction{Name: "m", Type: "Custom", CustomType: "shelf-audit", Zone: "warehouse-a"}, CodeTypeNotInCatalog},
		{"no robot with capability", nil, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", RequiredCapabilities: []string{"lidar"}, Location: dock}, CodeNoRobotWithCapability},
		{"pinned robot missing", nil, fleetopsv1.DraftAction{Name: "m", Type: "Charge", RobotSelector: "sim-robot-009"}, CodeRobotNotFound},
		{"every robot paused", func(s *Snapshot) {
			s.Maintenances = []swarmv1.ZoneMaintenance{{ObjectMeta: metav1.ObjectMeta{Name: "zm"}, Status: swarmv1.ZoneMaintenanceStatus{Phase: "Active", PausedRobots: []swarmv1.PausedRobotEntry{{Name: "sim-robot-002"}, {Name: "sim-robot-003"}}}}}
			s.Robots = s.Robots[1:]
		}, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", Location: dock}, CodeNoEligibleRobot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fleet()
			if tc.mut != nil {
				tc.mut(&s)
			}
			r := Evaluate(Proposal{Creating: true, Task: task(t, tc.act)}, s)
			if r.Verdict != Blocked || !strings.Contains(codes(r.Blockers), tc.want) {
				t.Fatalf("blockers %s, want %s: %+v", codes(r.Blockers), tc.want, r)
			}
		})
	}
}

func TestNoEligibleRobotNamesFilters(t *testing.T) {
	s := fleet()
	s.Robots = s.Robots[2:] // sim-robot-003's camera is declared but Degraded
	s.Robots[0].Status.Capabilities = append(s.Robots[0].Status.Capabilities, swarmv1.CapabilityStatusEntry{Name: "camera_front", Status: swarmv1.CapabilityStatusDegraded})
	r := Evaluate(Proposal{Creating: true, Task: task(t, fleetopsv1.DraftAction{Name: "inspect", Type: "Navigate", RequiredCapabilities: []string{"navigation", "camera_front"}, Location: dock})}, s)
	if codes(r.Blockers) != CodeNoEligibleRobot || !strings.Contains(r.Blockers[0].Reason, "missing a required capability (sim-robot-003)") {
		t.Fatalf("report %+v", r.Blockers)
	}
}

func TestDependencyCycleAndTaskExists(t *testing.T) {
	s := fleet()
	s.Tasks = []swarmv1.FleetTask{{ObjectMeta: metav1.ObjectMeta{Name: "t1"}}}
	ft := task(t, fleetopsv1.DraftAction{Name: "a", Type: "Navigate", DependsOn: []string{"b"}, Location: dock}, fleetopsv1.DraftAction{Name: "b", Type: "Navigate", DependsOn: []string{"a"}, Location: dock})
	r := Evaluate(Proposal{Creating: true, Task: ft}, s)
	if !strings.Contains(codes(r.Blockers), CodeTaskExists) || !strings.Contains(codes(r.Blockers), CodeBadDependency) {
		t.Fatalf("blockers %s", codes(r.Blockers))
	}
}

func TestActivateAndMaintenanceAndStop(t *testing.T) {
	s := fleet()
	draftTask := task(t, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", Location: dock})
	s.Tasks = []swarmv1.FleetTask{*draftTask}
	if r := Evaluate(Proposal{ActivateTask: "t1"}, s); r.Verdict != LikelyFeasible {
		t.Fatalf("activate %+v", r)
	}
	s.Tasks[0].Spec.Draft = false
	if r := Evaluate(Proposal{ActivateTask: "t1"}, s); codes(r.Blockers) != CodeTaskNotDraft {
		t.Fatalf("activate non-draft %+v", r)
	}
	if r := Evaluate(Proposal{ActivateTask: "nope"}, s); codes(r.Blockers) != CodeTaskNotFound {
		t.Fatalf("activate missing %+v", r)
	}
	past := metav1.NewTime(now.Add(-time.Hour))
	if r := Evaluate(Proposal{Maintenance: &fleetopsv1.ZoneMaintenanceParams{ScopeType: "Zone", ZoneName: "warehouse-a", ScheduledStart: &past}}, s); codes(r.Blockers) != CodeStartInPast {
		t.Fatalf("maintenance %+v", r)
	}
	s.Zones[0].Status.EstopStatus = swarmv1.ZoneEstopTriggered
	if r := Evaluate(Proposal{ZoneStop: &fleetopsv1.ZoneStopParams{ZoneName: "warehouse-a"}}, s); codes(r.Blockers) != CodeAlreadyStopped {
		t.Fatalf("stop %+v", r)
	}
}

// R66/R69: only the stop markers and the Pending cap survive the projection,
// and without the read the report says what was not checked.
func TestConfigProjectionAndNotChecked(t *testing.T) {
	cfg := swarmv1.SwarmadaConfig{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{estopProcessed: "1"}}}
	cfg.Spec.Scheduling.MaxPendingActionsPerZone = 7
	cfg.Spec.Telemetry.Sink.Endpoint = "https://user:secret@example.invalid"
	c := ProjectConfig([]swarmv1.SwarmadaConfig{cfg})
	b, _ := json.Marshal(c)
	if !c.NamespaceStop || c.PendingCap != 7 || strings.Contains(string(b), "secret") {
		t.Fatalf("projection %s", b)
	}
	s := fleet()
	s.Config = Config{}
	r := Evaluate(Proposal{Creating: true, Task: task(t, fleetopsv1.DraftAction{Name: "m", Type: "Navigate", Location: dock})}, s)
	if !strings.Contains(strings.Join(r.NotChecked, "|"), ConfigNotChecked) {
		t.Fatalf("notChecked %v", r.NotChecked)
	}
}

// TestFilterListPinned fails when Swarmada's documented candidate filters
// (RFC-0001 §9.4.2) or the phrases of its no-candidate tally
// (internal/controller/dispatch_filters.go filterClass) differ from the list
// check_task was written against (D73). Revisit Filters and Evaluate first.
func TestFilterListPinned(t *testing.T) {
	dir, err := testenv.SwarmadaDir(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "internal", "controller", "dispatch_filters.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "var filterClass = map[int]string{")
	end := strings.Index(body[start:], "\n}")
	if start < 0 || end < 0 {
		t.Fatal("filterClass not found in dispatch_filters.go")
	}
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*([\w.]+):\s*"([^"]+)",`).FindAllStringSubmatch(body[start:start+end], -1) {
		got[m[1]] = m[2]
	}
	want := map[string]string{}
	for _, f := range Filters {
		want[f.Const] = f.Phrase
	}
	if len(got) != len(want) {
		t.Errorf("Swarmada's filterClass has %d entries, the pinned list %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("filter %s: Swarmada says %q, pinned %q", k, got[k], v)
		}
	}
	rfc, err := os.ReadFile(filepath.Join(dir, "rfcs", "dist", "RFC-0001-core-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	r := string(rfc)
	i := strings.Index(r, "**Filter phase (hard constraints).**")
	j := strings.Index(r, "**Resume window (named, not numbered).**")
	if i < 0 || j < i {
		t.Fatal("RFC §9.4.2 filter list not found")
	}
	items := regexp.MustCompile(`(?m)^(\d+)\. (.{0,48})`).FindAllStringSubmatch(r[i:j], -1)
	pinned := []string{
		"`Robot.status.phase` MUST be `Idle`.",
		"For an action that names `spec.zone`: `Robot.sta",
		"For each capability in `FleetAction.spec.require",
		"`Robot.status.batteryPercent` MUST be ≥ `FleetAc",
		"If `FleetAction.spec.constraints.maxPayloadKg` is",
		"If `FleetAction.spec.robotSelector` is set: only",
		"`Robot.status.installedModels[*].status` MUST NO",
		"The robot MUST NOT be in `status.pausedRobots[]`",
		"The `FleetAdapter` the robot's `spec.adapter.nam",
		"The robot MUST NOT be **under a stop**, the term",
		"The action's target zone MUST NOT forbid the wor",
		"`Robot.status.honouredOperatingConstraints[]` MU",
	}
	if len(items) != len(pinned) {
		t.Fatalf("RFC §9.4.2 lists %d filters, pinned %d", len(items), len(pinned))
	}
	for n, it := range items {
		if !strings.HasPrefix(pinned[n], strings.TrimSpace(it[2])) && !strings.HasPrefix(it[2], pinned[n]) {
			t.Errorf("RFC filter %s starts %q, pinned %q", it[1], it[2], pinned[n])
		}
	}
}
