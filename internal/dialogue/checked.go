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
	"slices"
	"sort"
	"strings"

	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// The "Checked:" block (D92) states what fleetops-agent read itself, never
// what the model wrote: a question's and a refusal's from the engine's fleet
// snapshot, a preview's from check_task's facts. Each line names one object
// as Kind/name and gives the fields as field=value, so the person (and the
// judge) reads each value next to its object.

// MaxCheckedRobots bounds the robot lines of a block; the rest are counted.
const MaxCheckedRobots = 5

// CheckedHeader opens a preview's block; CheckedSnapshotHeader a question's
// or a refusal's, which no eligibility check has run for.
const (
	CheckedHeader         = "Checked:"
	CheckedSnapshotHeader = "Checked (fleet state read now; eligibility is checked for the preview):"
)

// filterText overrides the phrase of a filter whose RFC phrase reads like a
// refusal of the request ("unable to", "refused"): the block states a
// robot's state, it does not refuse anything.
var filterText = map[int]string{
	5:  "a parametric constraint not met",
	-1: "served by an adapter that rejected this action as invalid",
}

func filterPhrase(n int) string {
	if t, ok := filterText[n]; ok {
		return t
	}
	return feasibility.FilterPhrase(n)
}

// renderChecked is the block's text: the header, then one indented line each.
func renderChecked(header string, lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(header)
	for _, l := range lines {
		b.WriteString("\n  " + l)
	}
	return b.String()
}

func batteryField(b *int32) string {
	if b == nil {
		return "status.batteryPercent unreported"
	}
	return fmt.Sprintf("status.batteryPercent=%d", *b)
}

func phaseField(p string) string {
	if p == "" {
		return "no status.phase"
	}
	return "status.phase=" + p
}

// robotsLines keeps at most MaxCheckedRobots lines and counts the rest.
func robotsLines(lines []string) []string {
	if len(lines) <= MaxCheckedRobots {
		return lines
	}
	n := len(lines) - MaxCheckedRobots
	more := fmt.Sprintf("and %d more robots.", n)
	if n == 1 {
		more = "and 1 more robot."
	}
	return append(lines[:MaxCheckedRobots:MaxCheckedRobots], more)
}

// CheckedPreview is a preview's block from check_task's facts. named are
// the robots the request names (pinned or preferred); they come first.
func CheckedPreview(f feasibility.Facts, named []string) []string {
	var out []string
	for _, z := range f.Zones {
		out = append(out, zoneLine(z))
	}
	for _, t := range f.Tasks {
		l := fmt.Sprintf("FleetTask/%s: spec.draft=%t", t.Name, t.Draft)
		if t.Phase != "" {
			l += ", status.phase=" + t.Phase
		}
		out = append(out, l+".")
	}
	rs := slices.Clone(f.Robots)
	sort.SliceStable(rs, func(i, j int) bool {
		ni, nj := slices.Contains(named, rs[i].Name), slices.Contains(named, rs[j].Name)
		if ni != nj {
			return ni
		}
		return rs[i].Name < rs[j].Name
	})
	var robots []string
	for _, r := range rs {
		robots = append(robots, robotLine(r, slices.Contains(named, r.Name)))
	}
	return append(out, robotsLines(robots)...)
}

func zoneLine(z feasibility.ZoneFacts) string {
	parts := []string{"status.estopStatus=" + z.EstopStatus}
	if z.EstopTriggered {
		parts = append(parts, "annotation swarmada.io/estop-triggered set")
	}
	parts = append(parts, fmt.Sprintf("status.robotCount=%d", z.RobotCount))
	if len(z.Maintenances) == 0 {
		parts = append(parts, "no ZoneMaintenance Active or Scheduled over it")
	}
	for _, m := range z.Maintenances {
		name, phase, _ := strings.Cut(m, " ")
		parts = append(parts, fmt.Sprintf("ZoneMaintenance/%s status.phase=%s over it", name, phase))
	}
	return fmt.Sprintf("FleetZone/%s: %s.", z.Name, strings.Join(parts, ", "))
}

func robotLine(r feasibility.RobotFacts, named bool) string {
	parts := []string{phaseField(r.Phase), batteryField(r.Battery)}
	if r.Floor != nil {
		parts = append(parts, fmt.Sprintf("battery floor %d%%", *r.Floor))
	}
	zoneUsed := false
	for _, m := range r.Members {
		zoneUsed = zoneUsed || slices.Contains(m.Filters, 2)
	}
	switch {
	case r.CurrentZone != "":
		parts = append(parts, "status.currentZone="+r.CurrentZone)
	case zoneUsed:
		parts = append(parts, "no status.currentZone")
	}
	if r.EstopState != "" {
		parts = append(parts, "status.estopState="+r.EstopState)
	}
	caps := make([]string, 0, len(r.Capabilities))
	for c := range r.Capabilities {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	for _, c := range caps {
		if st := r.Capabilities[c]; st != "" {
			parts = append(parts, c+"="+st)
		} else {
			parts = append(parts, c+" not declared")
		}
	}
	l := fmt.Sprintf("Robot/%s: %s", r.Name, strings.Join(parts, ", "))
	if named {
		l += " (the robot the request names)"
	}
	if fit := fitText(r); fit != "" {
		l += "; " + fit
	}
	return l + "."
}

// fitText says why a robot is eligible now or not, per member when the
// members differ.
func fitText(r feasibility.RobotFacts) string {
	if len(r.Members) == 0 {
		return "in the zone the request names"
	}
	one := func(m feasibility.MemberFit) string {
		if m.Eligible {
			return "eligible now"
		}
		var why []string
		for _, n := range m.Filters {
			w := fmt.Sprintf("filter %d (%s)", n, filterPhrase(n))
			if n == 0 || n < 0 {
				w = filterPhrase(n)
			}
			if n == 9 && r.Adapter != "" {
				w += fmt.Sprintf(": FleetAdapter/%s status.phase=%s", r.Adapter, r.AdapterPhase)
			}
			why = append(why, w)
		}
		return "not eligible now: " + strings.Join(why, ", ")
	}
	same := true
	for _, m := range r.Members[1:] {
		same = same && one(m) == one(r.Members[0])
	}
	if same {
		return one(r.Members[0])
	}
	var parts []string
	for _, m := range r.Members {
		parts = append(parts, "member "+m.Member+" "+one(m))
	}
	return strings.Join(parts, "; ")
}

// Checked is a question's or a refusal's block from the engine's fleet
// snapshot: every zone, every robot (at most MaxCheckedRobots), and the
// drafts when the ask is an activation.
func (s Snapshot) Checked(ask string) []string {
	var out []string
	for _, z := range s.ZoneStates {
		out = append(out, fmt.Sprintf("FleetZone/%s: status.estopStatus=%s, status.robotCount=%d.", z.Name, z.EstopStatus, z.RobotCount))
	}
	if ask == "activate" {
		for _, d := range s.Drafts {
			l := fmt.Sprintf("FleetTask/%s: spec.draft=true", d.Name)
			if d.Phase != "" {
				l += ", status.phase=" + d.Phase
			}
			out = append(out, l+".")
		}
	}
	rs := slices.Clone(s.Robots)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
	var robots []string
	for _, r := range rs {
		parts := []string{phaseField(r.Phase), batteryField(r.Battery)}
		if r.CurrentZone != "" {
			parts = append(parts, "status.currentZone="+r.CurrentZone)
		} else {
			parts = append(parts, "no status.currentZone")
		}
		if r.EstopState != "" {
			parts = append(parts, "status.estopState="+r.EstopState)
		}
		robots = append(robots, fmt.Sprintf("Robot/%s: %s.", r.Name, strings.Join(parts, ", ")))
	}
	return append(out, robotsLines(robots)...)
}

// namedRobots are the robots a request input pins or prefers.
func namedRobots(in any) []string {
	var out []string
	if d, ok := in.(tools.DraftTaskInput); ok {
		for _, a := range d.Actions {
			for _, n := range []string{a.RobotSelector, a.PreferredRobot} {
				if n != "" && !slices.Contains(out, n) {
					out = append(out, n)
				}
			}
		}
	}
	return out
}
