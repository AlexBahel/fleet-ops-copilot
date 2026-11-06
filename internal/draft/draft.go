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

// Package draft builds the Swarmada FleetTask a DraftTask request leads to.
// It is the one builder: foc-approvals creates exactly this object, check_task
// and foc activate evaluate it, and the web UI shows it (docs/DESIGN.md D53,
// D77).
package draft

import (
	"encoding/json"
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
)

// Payload keys: the convention this version writes (DESIGN.md §3.10.1). A
// waypoint is resolved to its pose and written as waypoint, x and y; the
// Swarmada quickstart's simulated adapter reads x and y in metres.
const (
	KeyWaypoint    = "waypoint"
	KeyX           = "x"
	KeyY           = "y"
	KeyLookFor     = "lookFor"
	KeyInstruction = "instruction"
	KeyDock        = "dock"
	KeyBounds      = "bounds"
	KeyRoute       = "route"
)

// Task builds the FleetTask, held as a draft, for p in namespace ns. A member
// with a route of more than one stop becomes one member per stop, and a
// repeat or a multi-stop route becomes spec.flow: a loop of p.Repeat.Times
// (default 1) rounds over the members in order. Without either, members keep
// their dependsOn and no flow is set.
func Task(ns string, p *focv1.DraftTaskParams) (*swarmv1.FleetTask, error) {
	if p == nil {
		return nil, fmt.Errorf("no draft task parameters")
	}
	t := &swarmv1.FleetTask{
		TypeMeta:   metav1.TypeMeta{APIVersion: swarmv1.GroupVersion.String(), Kind: "FleetTask"},
		ObjectMeta: metav1.ObjectMeta{Name: p.TaskName, Namespace: ns},
		Spec:       swarmv1.FleetTaskSpec{Draft: true, FailurePolicy: swarmv1.FailurePolicyFailFast},
	}
	useFlow := p.Repeat != nil
	for _, a := range p.Actions {
		if len(a.Route) > 1 {
			useFlow = true
		}
	}
	var order []string
	for _, a := range p.Actions {
		stops := []*focv1.Location{a.Location}
		if len(a.Route) > 0 {
			stops = nil
			for i := range a.Route {
				stops = append(stops, &a.Route[i])
			}
		}
		for i, at := range stops {
			name := a.Name
			if len(stops) > 1 {
				name = fmt.Sprintf("%s-%d", a.Name, i+1)
			}
			spec, err := actionSpec(a, at)
			if err != nil {
				return nil, fmt.Errorf("action %q: %w", a.Name, err)
			}
			m := swarmv1.FleetTaskAction{Name: name, Action: spec}
			if !useFlow {
				m.DependsOn = a.DependsOn
			}
			t.Spec.Actions = append(t.Spec.Actions, m)
			order = append(order, name)
		}
	}
	if useFlow {
		times := int32(1)
		if p.Repeat != nil {
			times = p.Repeat.Times
		}
		seq := make([]swarmv1.FlowNode, 0, len(order))
		for i := range order {
			seq = append(seq, swarmv1.FlowNode{Run: &order[i]})
		}
		t.Spec.Flow = &swarmv1.FlowNode{Loop: &swarmv1.LoopNode{
			Mode: swarmv1.LoopModeTimes, Times: &times, Body: swarmv1.FlowNode{Sequence: seq},
		}}
	}
	return t, nil
}

func actionSpec(a focv1.DraftAction, at *focv1.Location) (swarmv1.FleetActionSpec, error) {
	spec := swarmv1.FleetActionSpec{
		Type: swarmv1.ActionType(a.Type), CustomType: a.CustomType, Zone: a.Zone,
		Priority: swarmv1.ActionPriority(a.Priority), RequiredCapabilities: a.RequiredCapabilities,
		PreferredRobot: a.PreferredRobot, RobotSelector: a.RobotSelector,
		OnFailure: swarmv1.ActionFailureRequeue,
	}
	if a.OnFailure != "" {
		spec.OnFailure = swarmv1.ActionFailurePolicy(a.OnFailure)
	}
	payload := map[string]any{}
	if at != nil {
		if spec.Zone == "" {
			spec.Zone = at.Zone
		}
		if err := putLocation(payload, at); err != nil {
			return spec, err
		}
	}
	if len(a.LookFor) > 0 {
		payload[KeyLookFor] = a.LookFor
	}
	if a.Instruction != "" {
		payload[KeyInstruction] = a.Instruction
	}
	if a.Dock != "" {
		payload[KeyDock] = a.Dock
	}
	if a.Bounds != "" {
		payload[KeyBounds] = a.Bounds
		if spec.Zone == "" {
			spec.Zone = a.Bounds
		}
	}
	if len(payload) > 0 {
		b, err := json.Marshal(payload)
		if err != nil {
			return spec, err
		}
		spec.Payload = &swarmv1.ActionPayload{Raw: b}
	}
	return spec, nil
}

func putLocation(payload map[string]any, at *focv1.Location) error {
	if at.Waypoint != "" {
		payload[KeyWaypoint] = at.Waypoint
	}
	if at.X == "" && at.Y == "" {
		return nil
	}
	x, err := strconv.ParseFloat(at.X, 64)
	if err != nil {
		return fmt.Errorf("location x %q: %w", at.X, err)
	}
	y, err := strconv.ParseFloat(at.Y, 64)
	if err != nil {
		return fmt.Errorf("location y %q: %w", at.Y, err)
	}
	payload[KeyX], payload[KeyY] = x, y
	return nil
}
