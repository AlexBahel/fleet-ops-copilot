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

// Package feasibility answers, from reads only, whether a proposed request is
// likely to run: an eligible robot now, zone state, location, dependencies,
// timing (docs/DESIGN.md §3.10.4, D73). It never says "guaranteed": the
// adapter's own per-instance check, and everything that changes between now
// and dispatch, are outside what a read can see. fleetops-mcp (check_task),
// fleetops-approvals (the execution re-check) and fleetops activate share it.
package feasibility

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
)

// Verdicts. There is deliberately no stronger one.
const (
	LikelyFeasible = "likely feasible"
	Blocked        = "blocked"
)

// Statement is shown with every verdict (R65).
const Statement = "Checked against current fleet state; Swarmada's own acceptance is checked at execution."

// Check outcomes (api/v1alpha1 CheckRecord).
const (
	Passed     = "Passed"
	Warning    = "Warning"
	BlockedOut = "Blocked"
	NotChecked = "NotChecked"
)

// Stable codes.
const (
	CodeNoEligibleRobot       = "NO_ELIGIBLE_ROBOT"
	CodeRobotsBusy            = "ROBOTS_BUSY"
	CodeNoCarryingRobot       = "NO_CARRYING_ROBOT"
	CodeNoRobotWithCapability = "NO_ROBOT_WITH_CAPABILITY"
	CodeChargeBelowFloor      = "CHARGE_BELOW_BATTERY_FLOOR"
	CodeRobotNotFound         = "ROBOT_NOT_FOUND"
	CodeZoneNotFound          = "ZONE_NOT_FOUND"
	CodeZoneStopped           = "ZONE_STOPPED"
	CodeZoneEntryDenied       = "ZONE_ENTRY_DENIED"
	CodeZoneCapabilityDenied  = "ZONE_CAPABILITY_DENIED"
	CodeNamespaceStopped      = "NAMESPACE_STOPPED"
	CodeMaintenanceActive     = "MAINTENANCE_ACTIVE"
	CodeMaintenanceScheduled  = "MAINTENANCE_SCHEDULED"
	CodeWaypointNotFound      = "WAYPOINT_NOT_FOUND"
	CodeBadDependency         = "BAD_DEPENDENCY"
	CodeTaskExists            = "TASK_EXISTS"
	CodeTaskNotFound          = "TASK_NOT_FOUND"
	CodeTaskNotDraft          = "TASK_NOT_DRAFT"
	CodeStartInPast           = "START_IN_PAST"
	CodeTypeNotInCatalog      = "TYPE_NOT_IN_CATALOG"
	CodePendingCap            = "PENDING_CAP_REACHED"
	CodeAlreadyStopped        = "ZONE_ALREADY_STOPPED"
)

// Swarmada markers read here (fleet.EstopAnnotation is the same string).
const (
	estopTriggered = "swarmada.io/estop-triggered"
	estopProcessed = "swarmada.io/estop-processed"
)

// Filter is one Swarmada candidate filter as RFC-0001 §9.4.2 numbers it, with
// the phrase Swarmada's own no-candidate tally uses
// (internal/controller/dispatch_filters.go filterClass). TestFilterListPinned
// fails when either changes at a re-pin.
type Filter struct {
	Number int
	Const  string
	Phrase string
	// Read says whether a read can evaluate it (false: listed as not checked).
	Read bool
}

// Filters is the list check_task was written against (Swarmada 558da47).
var Filters = []Filter{
	{1, "scheduler.FilterPhaseIdle", "not Idle", true},
	{2, "filterZoneLeafSet", "not in the action's zone", true},
	{3, "scheduler.FilterRequiredCapability", "missing a required capability", true},
	{4, "filterMinBattery", "below the action's battery floor", true},
	{5, "scheduler.FilterParametricConstr", "unable to meet a parametric constraint", true},
	{6, "scheduler.FilterRobotSelector", "not the robot the action pins", true},
	{7, "filterModelNotUpdating", "mid model update", true},
	{8, "filterZoneMaintenance", "held by an active maintenance window", true},
	{9, "filterAdapterReady", "served by an adapter unfit for dispatch", true},
	{10, "filterEstop", "under an active emergency stop", true},
	{12, "filterOperatingConstraint", "served by an adapter that does not honour a zone operating rule", true},
	{0, "filterResumeWindow", "in the post-clear estop resume window", true},
	{-1, "filterAdapterRefusedAction", "served by an adapter that refused this action as invalid", false},
	{-2, "scheduler.FilterTerminating", "being deleted", true},
	{-3, "filterRobotIDConflict", "sharing its robot-id with another Robot", false},
}

func phrase(n int) string {
	for _, f := range Filters {
		if f.Number == n {
			return f.Phrase
		}
	}
	return fmt.Sprintf("filter %d", n)
}

// AlwaysNotChecked are checks no read can make (DESIGN.md §3.10.4).
var AlwaysNotChecked = []string{
	"the adapter's validate_action for the concrete action (no client can see it before assignment; Swarmada draft 5, C5)",
	"filter 9: the adapter's contract version range and SafetyStream",
	"filter 10: stops reported by adapters, and duplicate wire robot ids",
}

// ConfigNotChecked is listed when SwarmadaConfig could not be read (R66).
const ConfigNotChecked = "namespace-wide stop and Pending cap not checked"

// Config is the projection of the namespace's SwarmadaConfig: the stop
// markers and the Pending cap, nothing else (R66, R69). The object itself is
// never kept, logged or traced.
type Config struct {
	Read          bool
	NamespaceStop bool
	PendingCap    int32
}

// ProjectConfig keeps only what check_task needs from a SwarmadaConfig list.
func ProjectConfig(items []swarmv1.SwarmadaConfig) Config {
	c := Config{Read: true}
	for _, it := range items {
		_, t := it.Annotations[estopTriggered]
		_, p := it.Annotations[estopProcessed]
		c.NamespaceStop = c.NamespaceStop || t || p
		if it.Spec.Scheduling.MaxPendingActionsPerZone > c.PendingCap {
			c.PendingCap = it.Spec.Scheduling.MaxPendingActionsPerZone
		}
	}
	return c
}

// Snapshot is the fleet state a proposal is checked against.
type Snapshot struct {
	Namespace    string
	Now          time.Time
	Robots       []swarmv1.Robot
	Zones        []swarmv1.FleetZone
	Maintenances []swarmv1.ZoneMaintenance
	Adapters     []swarmv1.FleetAdapter
	Tasks        []swarmv1.FleetTask
	Actions      []swarmv1.FleetAction
	Config       Config
	// NotChecked lists reads that failed (for example a person without list
	// permission running fleetops activate).
	NotChecked []string
}

// Proposal is what is checked: a FleetTask to create (Creating) or a draft
// to activate, a maintenance window, or a zone stop.
type Proposal struct {
	Task         *swarmv1.FleetTask
	Creating     bool
	ActivateTask string
	Maintenance  *fleetopsv1.ZoneMaintenanceParams
	ZoneStop     *fleetopsv1.ZoneStopParams
}

// Finding is one blocker or warning.
type Finding struct {
	Code   string `json:"code"`
	Member string `json:"member,omitempty"`
	Reason string `json:"reason"`
	Filter *int   `json:"filter,omitempty"`
}

// Check is one check made, with its outcome.
type Check struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Code    string `json:"code,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Report is the result.
type Report struct {
	Verdict      string              `json:"verdict"`
	Statement    string              `json:"statement"`
	Blockers     []Finding           `json:"blockers,omitempty"`
	Warnings     []Finding           `json:"warnings,omitempty"`
	Alternatives []string            `json:"alternatives,omitempty"`
	Eligible     map[string][]string `json:"eligibleNow,omitempty"`
	Checks       []Check             `json:"checks"`
	NotChecked   []string            `json:"notChecked"`
}

// Records converts the checks for an ApprovalRequest.
func (r Report) Records() []fleetopsv1.CheckRecord {
	out := make([]fleetopsv1.CheckRecord, 0, len(r.Checks)+len(r.NotChecked))
	for _, c := range r.Checks {
		out = append(out, fleetopsv1.CheckRecord{Name: c.Name, Outcome: c.Outcome, Code: c.Code, Detail: c.Detail})
	}
	for _, n := range r.NotChecked {
		out = append(out, fleetopsv1.CheckRecord{Name: n, Outcome: NotChecked})
	}
	return out
}

type eval struct {
	s       Snapshot
	r       Report
	zones   map[string]*swarmv1.FleetZone
	robots  map[string]*swarmv1.Robot
	adapter map[string]*swarmv1.FleetAdapter
}

func (e *eval) block(code, member, format string, a ...any) {
	e.r.Blockers = append(e.r.Blockers, Finding{Code: code, Member: member, Reason: fmt.Sprintf(format, a...)})
	e.r.Checks = append(e.r.Checks, Check{Name: checkName(code, member), Outcome: BlockedOut, Code: code, Detail: fmt.Sprintf(format, a...)})
}

func (e *eval) warn(code, member, format string, a ...any) {
	e.r.Warnings = append(e.r.Warnings, Finding{Code: code, Member: member, Reason: fmt.Sprintf(format, a...)})
	e.r.Checks = append(e.r.Checks, Check{Name: checkName(code, member), Outcome: Warning, Code: code, Detail: fmt.Sprintf(format, a...)})
}

func (e *eval) pass(name, format string, a ...any) {
	e.r.Checks = append(e.r.Checks, Check{Name: name, Outcome: Passed, Detail: fmt.Sprintf(format, a...)})
}

func (e *eval) alt(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if !slices.Contains(e.r.Alternatives, s) {
		e.r.Alternatives = append(e.r.Alternatives, s)
	}
}

func checkName(code, member string) string {
	if member == "" {
		return strings.ToLower(code)
	}
	return member + ": " + strings.ToLower(code)
}

// Evaluate checks p against s.
func Evaluate(p Proposal, s Snapshot) Report {
	e := &eval{s: s, r: Report{Statement: Statement}, zones: map[string]*swarmv1.FleetZone{}, robots: map[string]*swarmv1.Robot{}, adapter: map[string]*swarmv1.FleetAdapter{}}
	for i := range s.Zones {
		e.zones[s.Zones[i].Name] = &s.Zones[i]
	}
	for i := range s.Robots {
		e.robots[s.Robots[i].Name] = &s.Robots[i]
	}
	for i := range s.Adapters {
		e.adapter[s.Adapters[i].Name] = &s.Adapters[i]
	}
	e.r.NotChecked = append(append([]string{}, s.NotChecked...), AlwaysNotChecked...)
	if !s.Config.Read {
		e.r.NotChecked = append(e.r.NotChecked, ConfigNotChecked)
	}
	switch {
	case p.ActivateTask != "":
		e.activate(p.ActivateTask)
	case p.Task != nil:
		e.task(p.Task, p.Creating)
	case p.Maintenance != nil:
		e.maintenance(p.Maintenance)
	case p.ZoneStop != nil:
		e.zoneStop(p.ZoneStop)
	}
	e.r.Verdict = LikelyFeasible
	if len(e.r.Blockers) > 0 {
		e.r.Verdict = Blocked
	}
	return e.r
}

func (e *eval) activate(name string) {
	for i := range e.s.Tasks {
		t := &e.s.Tasks[i]
		if t.Name != name {
			continue
		}
		if !t.Spec.Draft {
			e.block(CodeTaskNotDraft, "", "FleetTask %s is not a draft (status.phase=%s)", name, t.Status.Phase)
			return
		}
		e.task(t, false)
		return
	}
	e.block(CodeTaskNotFound, "", "FleetTask %s not found in %s", name, e.s.Namespace)
}

func (e *eval) task(t *swarmv1.FleetTask, creating bool) {
	if creating {
		for _, x := range e.s.Tasks {
			if x.Name == t.Name {
				e.block(CodeTaskExists, "", "a FleetTask named %s already exists", t.Name)
				e.alt("choose another draft name")
			}
		}
	}
	e.dependencies(t)
	if e.s.Config.NamespaceStop {
		e.block(CodeNamespaceStopped, "", "namespace %s is under a namespace-wide stop (SwarmadaConfig); Swarmada gives no robot new work", e.s.Namespace)
		e.alt("a person clears the namespace stop; the copilot cannot")
	}
	pendingByZone := map[string]int{}
	for _, a := range e.s.Actions {
		if a.Status.Phase == swarmv1.ActionPhasePending && a.Spec.Zone != "" {
			pendingByZone[a.Spec.Zone]++
		}
	}
	e.r.Eligible = map[string][]string{}
	for _, m := range t.Spec.Actions {
		e.member(m.Name, m.Action)
		if cap := e.s.Config.PendingCap; cap > 0 && m.Action.Zone != "" && pendingByZone[m.Action.Zone] >= int(cap) {
			e.warn(CodePendingCap, m.Name, "zone %s already has %d Pending FleetActions, at the namespace cap of %d; Swarmada refuses more until some are assigned", m.Action.Zone, pendingByZone[m.Action.Zone], cap)
		}
	}
}

func (e *eval) dependencies(t *swarmv1.FleetTask) {
	names := map[string]bool{}
	for _, m := range t.Spec.Actions {
		names[m.Name] = true
	}
	deps := map[string][]string{}
	bad := false
	for _, m := range t.Spec.Actions {
		for _, d := range m.DependsOn {
			if !names[d] {
				e.block(CodeBadDependency, m.Name, "dependsOn names %s, which is not a member", d)
				bad = true
			}
		}
		deps[m.Name] = m.DependsOn
	}
	// Cycle check (Swarmada checks only after activation, inventory C7).
	state := map[string]int{}
	var visit func(string) bool
	visit = func(n string) bool {
		switch state[n] {
		case 1:
			return true
		case 2:
			return false
		}
		state[n] = 1
		for _, d := range deps[n] {
			if visit(d) {
				return true
			}
		}
		state[n] = 2
		return false
	}
	for _, m := range t.Spec.Actions {
		if visit(m.Name) {
			e.block(CodeBadDependency, m.Name, "dependsOn forms a cycle")
			bad = true
			break
		}
	}
	if !bad {
		e.pass("dependencies", "every dependsOn names a member and the graph is acyclic")
	}
}

func payloadOf(a swarmv1.FleetActionSpec) map[string]any {
	if a.Payload == nil || len(a.Payload.Raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(a.Payload.Raw, &m) != nil {
		return nil
	}
	return m
}

func (e *eval) member(name string, a swarmv1.FleetActionSpec) {
	explained := a.Zone != "" && !e.zoneState(name, a)
	if wp, _ := payloadOf(a)["waypoint"].(string); wp != "" && a.Zone != "" {
		if !e.waypointExists(a.Zone, wp) {
			e.block(CodeWaypointNotFound, name, "waypoint %s is not declared in FleetZone %s or its ancestors", wp, a.Zone)
			if names := e.waypointNames(); len(names) > 0 {
				e.alt("waypoints that exist: %s", strings.Join(names, ", "))
			}
			explained = true
		} else {
			e.pass(name+": location", "waypoint %s is declared in FleetZone %s", wp, a.Zone)
		}
	}
	if !e.inCatalog(name, a) {
		explained = true
	}
	if a.Type == swarmv1.ActionTypeCharge && a.RobotSelector != "" {
		if e.chargeFloor(name, a) {
			explained = true
		}
	}
	if a.RobotSelector != "" {
		if _, ok := e.robots[a.RobotSelector]; !ok {
			e.block(CodeRobotNotFound, name, "Robot %s, which the action pins, does not exist", a.RobotSelector)
			explained = true
		}
	}
	if len(a.RequiredCapabilities) > 0 && a.RobotSelector == "" && !e.anyRobotDeclares(a.RequiredCapabilities) {
		code := CodeNoRobotWithCapability
		if a.Type == swarmv1.ActionTypePickUp || a.Type == swarmv1.ActionTypeDropOff {
			code = CodeNoCarryingRobot
			e.alt("move a robot there instead (no load is carried)")
		}
		e.block(code, name, "no robot in %s declares every required capability %v", e.s.Namespace, a.RequiredCapabilities)
		explained = true
	}
	e.eligibility(name, a, explained)
}

// zoneState checks the target zone; false when it found a blocker.
func (e *eval) zoneState(member string, a swarmv1.FleetActionSpec) bool {
	z, ok := e.zones[a.Zone]
	if !ok {
		e.block(CodeZoneNotFound, member, "FleetZone %s does not exist in %s", a.Zone, e.s.Namespace)
		if names := e.zoneNames(); len(names) > 0 {
			e.alt("zones that exist: %s", strings.Join(names, ", "))
		}
		return false
	}
	okAll := true
	for _, anc := range e.chain(z.Name) {
		zz := e.zones[anc]
		if _, t := zz.Annotations[estopTriggered]; t || zz.Status.EstopStatus == swarmv1.ZoneEstopTriggered {
			e.block(CodeZoneStopped, member, "FleetZone %s has an emergency stop in force (status.estopStatus=%s); Swarmada holds work for it Pending", anc, zz.Status.EstopStatus)
			e.alt("a person clears the stop on %s; the copilot cannot", anc)
			okAll = false
		}
	}
	for _, r := range effective(z) {
		if r.Mode != swarmv1.ZoneRestrictionDenied {
			continue
		}
		switch r.Axis {
		case swarmv1.ZoneAxisEntry:
			e.block(CodeZoneEntryDenied, member, "FleetZone %s denies entry (declared on %s: %s)", z.Name, r.InheritedFrom, r.Reason)
			okAll = false
		case swarmv1.ZoneAxisCapability:
			if slices.Contains(a.RequiredCapabilities, r.Subject) {
				e.block(CodeZoneCapabilityDenied, member, "FleetZone %s denies capability %s (declared on %s: %s)", z.Name, r.Subject, r.InheritedFrom, r.Reason)
				okAll = false
			}
		}
	}
	for _, m := range e.s.Maintenances {
		covers := m.Spec.Scope.Type == "Namespace" || slices.Contains(e.chain(z.Name), m.Spec.Scope.ZoneName)
		if !covers {
			continue
		}
		switch m.Status.Phase {
		case "Active":
			e.warn(CodeMaintenanceActive, member, "ZoneMaintenance %s is Active over %s; the robots it pauses are not given work", m.Name, a.Zone)
		case "Scheduled", "":
			if m.Spec.ScheduledStart != nil {
				e.warn(CodeMaintenanceScheduled, member, "ZoneMaintenance %s over %s starts at %s", m.Name, a.Zone, m.Spec.ScheduledStart.UTC().Format(time.RFC3339))
			}
		}
	}
	if okAll {
		e.pass(member+": zone", "FleetZone %s exists, no stop in force on it or its ancestors, no restriction denies the work", a.Zone)
	}
	return okAll
}

// chain is the zone and its ancestors (bounded like Swarmada's walk).
func (e *eval) chain(zone string) []string {
	var out []string
	for i := 0; i < 65 && zone != ""; i++ {
		z, ok := e.zones[zone]
		if !ok || slices.Contains(out, zone) {
			break
		}
		out = append(out, zone)
		zone = z.Spec.ParentZone
	}
	return out
}

// leaves is filter 2's leaf set of zone: zones at or below it with no
// children, without entry-denied ones; a zone that does not exist resolves
// to itself.
func (e *eval) leaves(zone string) []string {
	if _, ok := e.zones[zone]; !ok {
		return []string{zone}
	}
	children := map[string]bool{}
	for _, z := range e.s.Zones {
		if z.Spec.ParentZone != "" {
			children[z.Spec.ParentZone] = true
		}
	}
	var out []string
	for _, z := range e.s.Zones {
		if children[z.Name] || !slices.Contains(e.chain(z.Name), zone) {
			continue
		}
		denied := false
		for _, r := range effective(&z) {
			denied = denied || (r.Axis == swarmv1.ZoneAxisEntry && r.Mode == swarmv1.ZoneRestrictionDenied)
		}
		if !denied {
			out = append(out, z.Name)
		}
	}
	return out
}

func effective(z *swarmv1.FleetZone) []swarmv1.EffectiveZoneRestriction {
	if z.Status.Restrictions == nil {
		return nil
	}
	return z.Status.Restrictions.Effective
}

func (e *eval) waypointExists(zone, wp string) bool {
	for _, anc := range e.chain(zone) {
		for _, w := range e.zones[anc].Spec.Waypoints {
			if w.Name == wp {
				return true
			}
		}
	}
	// A waypoint on a zone below the target is also inside it.
	for _, z := range e.s.Zones {
		if slices.Contains(e.chain(z.Name), zone) {
			for _, w := range z.Spec.Waypoints {
				if w.Name == wp {
					return true
				}
			}
		}
	}
	return false
}

func (e *eval) waypointNames() []string {
	var out []string
	for _, z := range e.s.Zones {
		for _, w := range z.Spec.Waypoints {
			out = append(out, w.Name)
		}
	}
	return out
}

func (e *eval) zoneNames() []string {
	var out []string
	for _, z := range e.s.Zones {
		out = append(out, z.Name)
	}
	sort.Strings(out)
	return out
}

// inCatalog is Swarmada's admission pre-filter: when any adapter in the
// namespace advertises a catalog, one of them must list the type (and, for
// a Custom verb, the verb). It fails open when none does.
func (e *eval) inCatalog(member string, a swarmv1.FleetActionSpec) bool {
	anyCatalog := false
	for _, ad := range e.s.Adapters {
		for _, sa := range ad.Status.SupportedActions {
			anyCatalog = true
			if sa.ActionType == string(a.Type) && (a.CustomType == "" || sa.CustomType == "" || sa.CustomType == a.CustomType) {
				e.pass(member+": adapter catalog", "FleetAdapter %s lists %s%s", ad.Name, a.Type, verb(a.CustomType))
				return true
			}
		}
	}
	if !anyCatalog {
		e.pass(member+": adapter catalog", "no adapter advertises a catalog; Swarmada's admission check passes")
		return true
	}
	e.block(CodeTypeNotInCatalog, member, "no FleetAdapter in %s lists action type %s%s; Swarmada refuses it at admission", e.s.Namespace, a.Type, verb(a.CustomType))
	return false
}

func verb(v string) string {
	if v == "" {
		return ""
	}
	return " (verb " + v + ")"
}

func floorOf(r *swarmv1.Robot, a swarmv1.FleetActionSpec) *int32 {
	var f *int32
	if r.Spec.Constraints != nil {
		f = r.Spec.Constraints.MinBatteryPctForAction
	}
	if a.MinBatteryPct != nil && (f == nil || *a.MinBatteryPct > *f) {
		f = a.MinBatteryPct
	}
	return f
}

// chargeFloor reports the owner-named blocker: filter 4 has no Charge
// exemption, so a robot below its floor cannot be sent to charge.
func (e *eval) chargeFloor(member string, a swarmv1.FleetActionSpec) bool {
	r, ok := e.robots[a.RobotSelector]
	if !ok {
		return false
	}
	f := floorOf(r, a)
	if f == nil {
		return false
	}
	if r.Status.BatteryPercent == nil || *r.Status.BatteryPercent < *f {
		battery := "unreported"
		if r.Status.BatteryPercent != nil {
			battery = fmt.Sprintf("%d%%", *r.Status.BatteryPercent)
		}
		e.block(CodeChargeBelowFloor, member, "Robot %s battery is %s, below its floor of %d%% (spec.constraints.minBatteryPctForAction); Swarmada gives no new action, a Charge included, to a robot below its floor (candidate filter 4)", r.Name, battery, *f)
		e.alt("charge %s by hand or with the robot's own application", r.Name)
		e.alt("a person lowers %s's minBatteryPctForAction", r.Name)
		return true
	}
	return false
}

func (e *eval) anyRobotDeclares(caps []string) bool {
	for _, r := range e.s.Robots {
		have := map[string]bool{}
		for _, c := range r.Status.Capabilities {
			have[c.Name] = true
		}
		all := true
		for _, c := range caps {
			all = all && have[c]
		}
		if all {
			return true
		}
	}
	return false
}

// exclusions is the candidate-filter evaluation of one robot for one action.
func (e *eval) exclusions(r *swarmv1.Robot, a swarmv1.FleetActionSpec, leaves []string) []int {
	var out []int
	add := func(n int) { out = append(out, n) }
	if r.DeletionTimestamp != nil {
		add(-2)
	}
	pinned := a.RobotSelector != ""
	if pinned && r.Name != a.RobotSelector {
		return []int{6}
	}
	if r.Status.Phase != swarmv1.RobotPhase("Idle") {
		add(1)
	}
	if !pinned && a.Zone != "" && (r.Status.CurrentZone == "" || !slices.Contains(leaves, r.Status.CurrentZone)) {
		add(2)
	}
	if !pinned {
		for _, c := range a.RequiredCapabilities {
			ok := false
			for _, rc := range r.Status.Capabilities {
				ok = ok || (rc.Name == c && rc.Status == swarmv1.CapabilityStatusActive)
			}
			if !ok {
				add(3)
				break
			}
		}
	}
	if f := floorOf(r, a); f != nil && (r.Status.BatteryPercent == nil || *r.Status.BatteryPercent < *f) {
		add(4)
	}
	if e.modelUpdating(r, a) {
		add(7)
	}
	for _, m := range e.s.Maintenances {
		if m.Status.Phase != "Active" {
			continue
		}
		held := false
		for _, p := range m.Status.PausedRobots {
			held = held || p.Name == r.Name
		}
		for _, w := range m.Status.WindingDownRobots {
			held = held || w.Name == r.Name
		}
		if held {
			add(8)
			break
		}
	}
	if !e.adapterReady(r) {
		add(9)
	}
	if e.underStop(r) {
		add(10)
	}
	if e.operatingUnmet(r) {
		add(12)
	}
	if r.Status.EstopState == swarmv1.RobotEstopState("Resuming") {
		add(0)
	}
	return out
}

func (e *eval) modelUpdating(r *swarmv1.Robot, a swarmv1.FleetActionSpec) bool {
	if len(a.RequiredCapabilities) == 0 {
		return false
	}
	grants := map[string]bool{}
	for _, g := range r.Status.ModelGrantedCapabilities {
		for _, c := range g.Capabilities {
			if slices.Contains(a.RequiredCapabilities, c) {
				grants[g.ModelName] = true
			}
		}
	}
	for _, m := range r.Status.InstalledModels {
		if grants[m.Name] && string(m.Status) == "Updating" {
			return true
		}
	}
	return false
}

func (e *eval) adapterReady(r *swarmv1.Robot) bool {
	ad, ok := e.adapter[r.Spec.Adapter.Name]
	if !ok {
		return false
	}
	if ad.Status.Phase != swarmv1.FleetAdapterPhaseConnected || ad.Status.Conformance != swarmv1.ConformanceStatePassed {
		return false
	}
	if len(ad.Spec.ServesRobotClasses) > 0 && !slices.Contains(ad.Spec.ServesRobotClasses, r.Spec.RobotClass) {
		return false
	}
	return true
}

func (e *eval) underStop(r *swarmv1.Robot) bool {
	switch string(r.Status.EstopState) {
	case "Stopping", "Stopped", "Failed":
		return true
	}
	if e.s.Config.NamespaceStop {
		return true
	}
	for _, z := range []string{r.Status.CurrentZone, r.Spec.Zone} {
		for _, anc := range e.chain(z) {
			zz := e.zones[anc]
			if _, t := zz.Annotations[estopTriggered]; t || zz.Status.EstopStatus == swarmv1.ZoneEstopTriggered {
				return true
			}
		}
	}
	return false
}

func (e *eval) operatingUnmet(r *swarmv1.Robot) bool {
	zones := append([]string{r.Status.CurrentZone}, r.Status.ContainingZones...)
	for _, zn := range zones {
		z, ok := e.zones[zn]
		if !ok {
			continue
		}
		for _, x := range effective(z) {
			if x.Axis == swarmv1.ZoneAxisOperating && x.Subject != "" && !slices.Contains(r.Status.HonouredOperatingConstraints, x.Subject) {
				return true
			}
		}
	}
	return false
}

func (e *eval) eligibility(member string, a swarmv1.FleetActionSpec, explained bool) {
	leaves := e.leaves(a.Zone)
	var eligible, busyOnly []string
	tally := map[int][]string{}
	for i := range e.s.Robots {
		r := &e.s.Robots[i]
		ex := e.exclusions(r, a, leaves)
		if len(ex) == 0 {
			eligible = append(eligible, r.Name)
			continue
		}
		if len(ex) == 1 && ex[0] == 1 {
			busyOnly = append(busyOnly, r.Name)
		}
		if a.RobotSelector != "" && r.Name != a.RobotSelector {
			continue
		}
		for _, n := range ex {
			tally[n] = append(tally[n], r.Name)
		}
	}
	sort.Strings(eligible)
	e.r.Eligible[member] = eligible
	switch {
	case len(eligible) > 0:
		e.pass(member+": eligible robot now", "%s pass every candidate filter a read can evaluate", strings.Join(eligible, ", "))
	case len(busyOnly) > 0:
		sort.Strings(busyOnly)
		e.warn(CodeRobotsBusy, member, "no robot is eligible now; %s would be once idle (filter 1: %s)", strings.Join(busyOnly, ", "), phrase(1))
	case explained:
		// A named blocker already says why; no second, vaguer one.
	default:
		var parts []string
		var nums []int
		for n := range tally {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		for _, n := range nums {
			parts = append(parts, fmt.Sprintf("%s (%s)", phrase(n), strings.Join(tally[n], ", ")))
		}
		e.block(CodeNoEligibleRobot, member, "no robot passes Swarmada's candidate filters now: %s", strings.Join(parts, "; "))
	}
}

func (e *eval) maintenance(p *fleetopsv1.ZoneMaintenanceParams) {
	if p.ScopeType == "Zone" || p.ScopeType == "" {
		if _, ok := e.zones[p.ZoneName]; !ok {
			e.block(CodeZoneNotFound, "", "FleetZone %s does not exist in %s", p.ZoneName, e.s.Namespace)
			if names := e.zoneNames(); len(names) > 0 {
				e.alt("zones that exist: %s", strings.Join(names, ", "))
			}
		} else {
			e.pass("zone", "FleetZone %s exists", p.ZoneName)
		}
	}
	if p.ScheduledStart != nil && p.ScheduledStart.Time.Before(e.s.Now) {
		e.block(CodeStartInPast, "", "scheduledStart %s is in the past (now %s)", p.ScheduledStart.UTC().Format(time.RFC3339), e.s.Now.UTC().Format(time.RFC3339))
		e.alt("start on approval instead")
	} else {
		e.pass("timing", "start is on approval or in the future")
	}
	for _, m := range e.s.Maintenances {
		if (m.Status.Phase == "Active" || m.Status.Phase == "Scheduled") && m.Spec.Scope.ZoneName == p.ZoneName {
			e.warn(CodeMaintenanceActive, "", "ZoneMaintenance %s over %s is already %s", m.Name, p.ZoneName, m.Status.Phase)
		}
	}
}

func (e *eval) zoneStop(p *fleetopsv1.ZoneStopParams) {
	z, ok := e.zones[p.ZoneName]
	if !ok {
		e.block(CodeZoneNotFound, "", "FleetZone %s does not exist in %s", p.ZoneName, e.s.Namespace)
		if names := e.zoneNames(); len(names) > 0 {
			e.alt("zones that exist: %s", strings.Join(names, ", "))
		}
		return
	}
	if _, t := z.Annotations[estopTriggered]; t || z.Status.EstopStatus == swarmv1.ZoneEstopTriggered {
		e.block(CodeAlreadyStopped, "", "FleetZone %s already has an emergency stop in force (status.estopStatus=%s)", z.Name, z.Status.EstopStatus)
		return
	}
	e.pass("zone", "FleetZone %s exists and has no stop in force", p.ZoneName)
}
