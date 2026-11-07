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

package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
)

// PreviewInput is the preview context a request carries. fleetops-agent fills it
// from the preview the person confirmed; it is part of the preview id, so a
// request whose context differs from what was shown is refused (D74).
type PreviewInput struct {
	Kind             string `json:"kind" jsonschema:"site catalogue kind"`
	Interpretation   string `json:"interpretation,omitempty" jsonschema:"Known (default) or Composed"`
	Instruction      string `json:"instruction,omitempty" jsonschema:"the requester's own words, verbatim"`
	ResultReported   bool   `json:"resultReported"`
	FailureHandling  string `json:"failureHandling,omitempty" jsonschema:"ReportOnly (default) or PrepareFollowUp"`
	CatalogueVersion int32  `json:"catalogueVersion"`
	CatalogueSHA256  string `json:"catalogueSHA256"`
}

func (p *PreviewInput) record() *fleetopsv1.PreviewRecord {
	if p == nil {
		return nil
	}
	r := &fleetopsv1.PreviewRecord{
		CatalogueVersion: p.CatalogueVersion, CatalogueSHA256: p.CatalogueSHA256, Kind: p.Kind,
		Interpretation: fleetopsv1.Interpretation(p.Interpretation), Instruction: p.Instruction,
		ResultReported: p.ResultReported, FailureHandling: fleetopsv1.FailureHandling(p.FailureHandling),
	}
	if r.Interpretation == "" {
		r.Interpretation = fleetopsv1.InterpretationKnown
	}
	if r.FailureHandling == "" {
		r.FailureHandling = fleetopsv1.FailureReportOnly
	}
	return r
}

// LocationInput is a waypoint resolved to its pose.
type LocationInput struct {
	Zone     string `json:"zone" jsonschema:"FleetZone"`
	Waypoint string `json:"waypoint,omitempty" jsonschema:"waypoint name from the zone's spec.waypoints"`
	X        string `json:"x,omitempty" jsonschema:"decimal metres"`
	Y        string `json:"y,omitempty" jsonschema:"decimal metres"`
}

func (l *LocationInput) record() *fleetopsv1.Location {
	if l == nil {
		return nil
	}
	return &fleetopsv1.Location{Zone: l.Zone, Waypoint: l.Waypoint, X: l.X, Y: l.Y}
}

// DraftSpec is the ApprovalRequest parameters a DraftTaskInput leads to.
func DraftSpec(in DraftTaskInput) *fleetopsv1.DraftTaskParams {
	spec := &fleetopsv1.DraftTaskParams{TaskName: in.Name, Purpose: in.Purpose}
	if in.Repeat > 0 {
		spec.Repeat = &fleetopsv1.Repeat{Times: in.Repeat}
	}
	for _, a := range in.Actions {
		d := fleetopsv1.DraftAction{
			Name: a.Name, Type: a.Type, Zone: a.Zone, Priority: a.Priority,
			RequiredCapabilities: a.RequiredCapabilities, PreferredRobot: a.PreferredRobot, DependsOn: a.DependsOn,
			CustomType: a.CustomType, Location: a.Location.record(), LookFor: a.LookFor, Instruction: a.Instruction,
			RobotSelector: a.RobotSelector, Dock: a.Dock, Bounds: a.Bounds, OnFailure: a.OnFailure,
		}
		for i := range a.Route {
			d.Route = append(d.Route, *a.Route[i].record())
		}
		spec.Actions = append(spec.Actions, d)
	}
	return spec
}

// DraftInputFrom is the tool input that leads to p (the inverse of
// DraftSpec), for a follow-up built from a filed request.
func DraftInputFrom(p *fleetopsv1.DraftTaskParams) DraftTaskInput {
	in := DraftTaskInput{Name: p.TaskName, Purpose: p.Purpose}
	if p.Repeat != nil {
		in.Repeat = p.Repeat.Times
	}
	loc := func(l *fleetopsv1.Location) *LocationInput {
		if l == nil {
			return nil
		}
		return &LocationInput{Zone: l.Zone, Waypoint: l.Waypoint, X: l.X, Y: l.Y}
	}
	for _, a := range p.Actions {
		d := DraftActionInput{
			Name: a.Name, Type: a.Type, CustomType: a.CustomType, Zone: a.Zone, Location: loc(a.Location), LookFor: a.LookFor,
			Instruction: a.Instruction, Priority: a.Priority, RequiredCapabilities: a.RequiredCapabilities, PreferredRobot: a.PreferredRobot,
			RobotSelector: a.RobotSelector, Dock: a.Dock, Bounds: a.Bounds, OnFailure: a.OnFailure, DependsOn: a.DependsOn,
		}
		for i := range a.Route {
			d.Route = append(d.Route, *loc(&a.Route[i]))
		}
		in.Actions = append(in.Actions, d)
	}
	return in
}

func maintenanceSpec(in ZoneMaintenanceInput) (*fleetopsv1.ZoneMaintenanceParams, error) {
	if in.ScopeType == "" {
		in.ScopeType = "Zone"
	}
	if in.Mode == "" {
		in.Mode = "Graceful"
	}
	p := &fleetopsv1.ZoneMaintenanceParams{ScopeType: in.ScopeType, ZoneName: in.ZoneName, Mode: in.Mode, Reason: in.Reason}
	if in.ScheduledStart != "" {
		t, err := time.Parse(time.RFC3339, in.ScheduledStart)
		if err != nil {
			return nil, fmt.Errorf("scheduledStart must be RFC 3339")
		}
		mt := metav1.NewTime(t.UTC())
		p.ScheduledStart = &mt
	}
	return p, nil
}

// SpecFor is the request a tool input leads to, without the fields fleetops-mcp
// sets when it files it (requester, expiry, policy, checks). It returns the
// tool name too. The preview id is computed from it.
func SpecFor(in any) (string, fleetopsv1.ApprovalRequestSpec, error) {
	switch v := in.(type) {
	case DraftTaskInput:
		return "request_draft_task", fleetopsv1.ApprovalRequestSpec{Type: fleetopsv1.RequestDraftTask, DraftTask: DraftSpec(v), Preview: v.Preview.record()}, nil
	case ActivateTaskInput:
		return "request_activate_task", fleetopsv1.ApprovalRequestSpec{Type: fleetopsv1.RequestActivateTask, ActivateTask: &fleetopsv1.ActivateTaskParams{TaskName: v.TaskName}, Preview: v.Preview.record()}, nil
	case ZoneMaintenanceInput:
		p, err := maintenanceSpec(v)
		if err != nil {
			return "", fleetopsv1.ApprovalRequestSpec{}, err
		}
		return "request_zone_maintenance", fleetopsv1.ApprovalRequestSpec{Type: fleetopsv1.RequestZoneMaintenance, ZoneMaintenance: p, Preview: v.Preview.record()}, nil
	case ZoneStopInput:
		return "request_zone_stop", fleetopsv1.ApprovalRequestSpec{Type: fleetopsv1.RequestZoneStop, ZoneStop: &fleetopsv1.ZoneStopParams{ZoneName: v.ZoneName, Reason: v.Reason}, Preview: v.Preview.record()}, nil
	}
	return "", fleetopsv1.ApprovalRequestSpec{}, fmt.Errorf("not a request input: %T", in)
}

// PreviewID is the id of the preview that shows spec: the SHA-256 of the
// request type, its parameters and its preview record (id and confirmation
// time left out) (D74).
func PreviewID(spec fleetopsv1.ApprovalRequestSpec) string {
	var pr *fleetopsv1.PreviewRecord
	if spec.Preview != nil {
		c := *spec.Preview
		c.ID, c.ConfirmedAt = "", nil
		pr = &c
	}
	b, _ := json.Marshal(struct {
		Type            fleetopsv1.RequestType            `json:"type"`
		DraftTask       *fleetopsv1.DraftTaskParams       `json:"draftTask,omitempty"`
		ActivateTask    *fleetopsv1.ActivateTaskParams    `json:"activateTask,omitempty"`
		ZoneMaintenance *fleetopsv1.ZoneMaintenanceParams `json:"zoneMaintenance,omitempty"`
		ZoneStop        *fleetopsv1.ZoneStopParams        `json:"zoneStop,omitempty"`
		Preview         *fleetopsv1.PreviewRecord         `json:"preview,omitempty"`
	}{spec.Type, spec.DraftTask, spec.ActivateTask, spec.ZoneMaintenance, spec.ZoneStop, pr})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// PreviewIDFor is PreviewID of the request a tool input leads to.
func PreviewIDFor(in any) (string, error) {
	_, spec, err := SpecFor(in)
	if err != nil {
		return "", err
	}
	return PreviewID(spec), nil
}
