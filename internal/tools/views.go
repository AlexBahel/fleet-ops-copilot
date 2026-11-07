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
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
)

// The view types below are what read tools return. Field names follow the
// Swarmada JSON paths (spec.*, status.*) so an answer can cite them exactly.
// Times are RFC 3339 strings.

func ts(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ConditionView is a metav1.Condition.
type ConditionView struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

func conds(in []metav1.Condition) []ConditionView {
	out := make([]ConditionView, 0, len(in))
	for _, c := range in {
		out = append(out, ConditionView{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message})
	}
	return out
}

// CapabilityView is one status.capabilities[] entry.
type CapabilityView struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Paused bool   `json:"paused,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// HardwareView is one status.hardware[] entry.
type HardwareView struct {
	Name              string `json:"name"`
	Status            string `json:"status"`
	DegradationReason string `json:"degradationReason,omitempty"`
	LastHealthyAt     string `json:"lastHealthyAt,omitempty"`
}

// RobotStatusView is the subset of Robot status the copilot surfaces.
type RobotStatusView struct {
	Phase           string           `json:"phase,omitempty"`
	EstopState      string           `json:"estopState,omitempty"`
	HealthStatus    string           `json:"health.status,omitempty"`
	HealthMessage   string           `json:"health.message,omitempty"`
	BatteryPercent  *int32           `json:"batteryPercent,omitempty"`
	CurrentZone     string           `json:"currentZone,omitempty"`
	AssignedAction  string           `json:"assignedAction,omitempty"`
	LastSeenAt      string           `json:"connectivity.lastSeenAt,omitempty"`
	OfflineSince    string           `json:"offlineSince,omitempty"`
	FirmwareVersion string           `json:"firmwareVersion,omitempty"`
	Capabilities    []CapabilityView `json:"capabilities,omitempty"`
	Hardware        []HardwareView   `json:"hardware,omitempty"`
	Conditions      []ConditionView  `json:"conditions,omitempty"`
}

// RobotView is a Robot.
type RobotView struct {
	Name         string          `json:"name"`
	Namespace    string          `json:"namespace"`
	Zone         string          `json:"spec.zone"`
	Manufacturer string          `json:"spec.manufacturer"`
	Model        string          `json:"spec.model"`
	Adapter      string          `json:"spec.adapter.name"`
	Status       RobotStatusView `json:"status"`
}

func robotView(r *swarmv1.Robot) RobotView {
	s := r.Status
	v := RobotView{
		Name: r.Name, Namespace: r.Namespace, Zone: r.Spec.Zone,
		Manufacturer: r.Spec.Manufacturer, Model: r.Spec.Model, Adapter: r.Spec.Adapter.Name,
		Status: RobotStatusView{
			Phase: string(s.Phase), EstopState: string(s.EstopState), BatteryPercent: s.BatteryPercent,
			CurrentZone: s.CurrentZone, AssignedAction: s.AssignedAction, OfflineSince: ts(s.OfflineSince),
			FirmwareVersion: s.FirmwareVersion, Conditions: conds(s.Conditions),
		},
	}
	if s.Health != nil {
		v.Status.HealthStatus, v.Status.HealthMessage = string(s.Health.Status), s.Health.Message
	}
	if s.Connectivity != nil {
		v.Status.LastSeenAt = ts(s.Connectivity.LastSeenAt)
	}
	for _, c := range s.Capabilities {
		v.Status.Capabilities = append(v.Status.Capabilities, CapabilityView{Name: c.Name, Status: string(c.Status), Paused: c.Paused, Reason: c.Reason})
	}
	for _, h := range s.Hardware {
		v.Status.Hardware = append(v.Status.Hardware, HardwareView{Name: h.Name, Status: string(h.Status), DegradationReason: h.DegradationReason, LastHealthyAt: ts(h.LastHealthyAt)})
	}
	return v
}

// TaskMemberView is one FleetTask member with its projected status.
type TaskMemberView struct {
	Name          string   `json:"name"`
	Type          string   `json:"action.type"`
	Zone          string   `json:"action.zone,omitempty"`
	Priority      string   `json:"action.priority,omitempty"`
	Required      []string `json:"action.requiredCapabilities,omitempty"`
	DependsOn     []string `json:"dependsOn,omitempty"`
	Phase         string   `json:"status.phase,omitempty"`
	ActionRef     string   `json:"status.actionRef,omitempty"`
	AssignedRobot string   `json:"status.assignedRobot,omitempty"`
}

// FleetTaskView is a FleetTask.
type FleetTaskView struct {
	Name          string           `json:"name"`
	Namespace     string           `json:"namespace"`
	Draft         bool             `json:"spec.draft"`
	DesiredState  string           `json:"spec.desiredState,omitempty"`
	FailurePolicy string           `json:"spec.failurePolicy,omitempty"`
	Phase         string           `json:"status.phase,omitempty"`
	ActionSummary string           `json:"status.actionSummary,omitempty"`
	CreatedBy     string           `json:"createdBy,omitempty"`
	CreatedAt     string           `json:"metadata.creationTimestamp,omitempty"`
	Members       []TaskMemberView `json:"members"`
	Conditions    []ConditionView  `json:"status.conditions,omitempty"`
}

func taskView(t *swarmv1.FleetTask, createdByLabel string) FleetTaskView {
	v := FleetTaskView{
		Name: t.Name, Namespace: t.Namespace, Draft: t.Spec.Draft, DesiredState: string(t.Spec.DesiredState),
		FailurePolicy: string(t.Spec.FailurePolicy), Phase: string(t.Status.Phase), ActionSummary: t.Status.ActionSummary,
		CreatedBy: t.Labels[createdByLabel], Conditions: conds(t.Status.Conditions),
		CreatedAt: ts(&t.CreationTimestamp),
	}
	st := map[string]swarmv1.FleetTaskActionStatus{}
	for _, s := range t.Status.Actions {
		st[s.Name] = s
	}
	for _, a := range t.Spec.Actions {
		s := st[a.Name]
		v.Members = append(v.Members, TaskMemberView{
			Name: a.Name, Type: string(a.Action.Type), Zone: a.Action.Zone, Priority: string(a.Action.Priority),
			Required: a.Action.RequiredCapabilities, DependsOn: a.DependsOn,
			Phase: string(s.Phase), ActionRef: s.ActionRef, AssignedRobot: s.AssignedRobot,
		})
	}
	return v
}

// FleetActionView is a FleetAction.
type FleetActionView struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Type      string   `json:"spec.type"`
	Zone      string   `json:"spec.zone,omitempty"`
	Priority  string   `json:"spec.priority,omitempty"`
	Required  []string `json:"spec.requiredCapabilities,omitempty"`
	// MinBatteryPct is the battery floor a candidate robot must meet (nil: none set).
	MinBatteryPct *int32 `json:"spec.minBatteryPct,omitempty"`
	Phase         string `json:"status.phase,omitempty"`
	AssignedRobot string `json:"status.assignedRobot,omitempty"`
	FailureReason string `json:"status.failureReason,omitempty"`
	Message       string `json:"status.message,omitempty"`
	RetryCount    int32  `json:"status.retryCount,omitempty"`
	// DisconnectedAt is when the assigned robot was lost (RFC 3339; empty when not set).
	DisconnectedAt string          `json:"status.disconnectedAt,omitempty"`
	Conditions     []ConditionView `json:"status.conditions,omitempty"`
}

func actionView(a *swarmv1.FleetAction) FleetActionView {
	return FleetActionView{
		Name: a.Name, Namespace: a.Namespace, Type: string(a.Spec.Type), Zone: a.Spec.Zone, Priority: string(a.Spec.Priority),
		Required: a.Spec.RequiredCapabilities, Phase: string(a.Status.Phase), AssignedRobot: a.Status.AssignedRobot,
		FailureReason: a.Status.FailureReason, Message: a.Status.Message, RetryCount: a.Status.RetryCount,
		Conditions: conds(a.Status.Conditions), MinBatteryPct: a.Spec.MinBatteryPct,
		DisconnectedAt: ts(a.Status.DisconnectedAt),
	}
}

// ZoneView is a FleetZone.
type ZoneView struct {
	Name                string         `json:"name"`
	Namespace           string         `json:"namespace"`
	DisplayName         string         `json:"spec.displayName,omitempty"`
	ParentZone          string         `json:"spec.parentZone,omitempty"`
	MaxConcurrentRobots int32          `json:"spec.maxConcurrentRobots"`
	Waypoints           []WaypointView `json:"spec.waypoints,omitempty"`
	EstopStatus         string         `json:"status.estopStatus"`
	EstopAnnotation     string         `json:"estopTriggeredAnnotation,omitempty"`
	LastEstopAt         string         `json:"status.lastEstopAt,omitempty"`
	RobotCount          int32          `json:"status.robotCount"`
	IsLeaf              bool           `json:"status.isLeaf"`
	ChildZones          []string       `json:"status.childZones,omitempty"`
}

func zoneView(z *swarmv1.FleetZone, estopAnnotation string) ZoneView {
	v := ZoneView{
		Name: z.Name, Namespace: z.Namespace, DisplayName: z.Spec.DisplayName, ParentZone: z.Spec.ParentZone,
		MaxConcurrentRobots: z.Spec.MaxConcurrentRobots, EstopStatus: string(z.Status.EstopStatus),
		EstopAnnotation: z.Annotations[estopAnnotation], LastEstopAt: ts(z.Status.LastEstopAt),
		RobotCount: z.Status.RobotCount, IsLeaf: z.Status.IsLeaf, ChildZones: z.Status.ChildZones,
	}
	if v.EstopStatus == "" {
		v.EstopStatus = "Clear" // RFC-0001: empty is treated as Clear (fleetzone_types.go)
	}
	for _, w := range z.Spec.Waypoints {
		v.Waypoints = append(v.Waypoints, WaypointView{Name: w.Name, X: strconv.FormatFloat(w.X, 'f', -1, 64), Y: strconv.FormatFloat(w.Y, 'f', -1, 64)})
	}
	return v
}

// WaypointView is a named pose of a FleetZone (spec.waypoints[]), with its
// coordinates as decimal strings, as a request carries them.
type WaypointView struct {
	Name string `json:"name"`
	X    string `json:"x"`
	Y    string `json:"y"`
}

// ZoneMaintenanceView is a ZoneMaintenance.
type ZoneMaintenanceView struct {
	Name                   string   `json:"name"`
	ScopeType              string   `json:"spec.scope.type"`
	ZoneName               string   `json:"spec.scope.zoneName,omitempty"`
	Mode                   string   `json:"spec.mode,omitempty"`
	DesiredState           string   `json:"spec.desiredState,omitempty"`
	Reason                 string   `json:"spec.reason,omitempty"`
	AutoResumeAfterMinutes int32    `json:"spec.autoResumeAfterMinutes"`
	Phase                  string   `json:"status.phase,omitempty"`
	PausedRobots           []string `json:"status.pausedRobots,omitempty"`
	WindingDownRobots      []string `json:"status.windingDownRobots,omitempty"`
}

func maintenanceView(m *swarmv1.ZoneMaintenance) ZoneMaintenanceView {
	v := ZoneMaintenanceView{
		Name: m.Name, ScopeType: string(m.Spec.Scope.Type), ZoneName: m.Spec.Scope.ZoneName, Mode: string(m.Spec.Mode),
		DesiredState: string(m.Spec.DesiredState), Reason: m.Spec.Reason, AutoResumeAfterMinutes: m.Spec.AutoResumeAfterMinutes,
		Phase: string(m.Status.Phase),
	}
	for _, p := range m.Status.PausedRobots {
		v.PausedRobots = append(v.PausedRobots, p.Name)
	}
	for _, w := range m.Status.WindingDownRobots {
		v.WindingDownRobots = append(v.WindingDownRobots, w.Name)
	}
	return v
}

// DiscoveredRobotView is a DiscoveredRobot.
type DiscoveredRobotView struct {
	Name                 string   `json:"name"`
	Phase                string   `json:"status.phase"`
	Manufacturer         string   `json:"status.manufacturer"`
	Model                string   `json:"status.model"`
	FirmwareVersion      string   `json:"status.firmwareVersion,omitempty"`
	TTLExpiresAt         string   `json:"status.ttlExpiresAt,omitempty"`
	ReportedCapabilities []string `json:"status.reportedCapabilities,omitempty"`
}

// FleetAdapterView is a FleetAdapter.
type FleetAdapterView struct {
	Name            string          `json:"name"`
	Phase           string          `json:"status.phase,omitempty"`
	Conformance     string          `json:"status.conformance,omitempty"`
	ConnectedRobots int32           `json:"status.connectedRobots"`
	LastHeartbeat   string          `json:"status.lastHeartbeat,omitempty"`
	Message         string          `json:"status.message,omitempty"`
	Conditions      []ConditionView `json:"status.conditions,omitempty"`
}

// TaskTriggerView is a TaskTrigger (RFC-0003).
type TaskTriggerView struct {
	Name          string   `json:"name"`
	Suspend       bool     `json:"spec.suspend"`
	LastFiredAt   string   `json:"status.lastFiredAt,omitempty"`
	LastEventName string   `json:"status.lastEventName,omitempty"`
	DraftCount    int64    `json:"status.draftCount"`
	PendingDrafts []string `json:"status.pendingDrafts,omitempty"`
}

// EventView is a core/v1 Event.
type EventView struct {
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int32  `json:"count"`
	Last    string `json:"lastTimestamp,omitempty"`
	Source  string `json:"source,omitempty"`
}

// RequestView is a copilot ApprovalRequest.
type RequestView struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Type         string `json:"spec.type"`
	RequestedFor string `json:"spec.requestedFor"`
	Target       string `json:"target"`
	ExpiresAt    string `json:"spec.expiresAt"`
	State        string `json:"status.state"`
	DecidedBy    string `json:"status.decidedBy,omitempty"`
	Message      string `json:"status.message,omitempty"`
}

func requestView(r *fleetopsv1.ApprovalRequest) RequestView {
	v := RequestView{
		Name: r.Name, Namespace: r.Namespace, Type: string(r.Spec.Type), RequestedFor: r.Spec.RequestedFor,
		ExpiresAt: ts(&r.Spec.ExpiresAt), State: string(r.Status.State), DecidedBy: r.Status.DecidedBy, Message: r.Status.Message,
	}
	if v.State == "" {
		v.State = string(fleetopsv1.StatePending)
	}
	switch {
	case r.Spec.ActivateTask != nil:
		v.Target = "FleetTask/" + r.Spec.ActivateTask.TaskName
	case r.Spec.ZoneMaintenance != nil:
		v.Target = "ZoneMaintenance on FleetZone/" + r.Spec.ZoneMaintenance.ZoneName
	case r.Spec.ZoneStop != nil:
		v.Target = "FleetZone/" + r.Spec.ZoneStop.ZoneName
	}
	return v
}
