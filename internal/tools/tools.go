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

// Package tools implements the copilot's MCP tools independently of the MCP
// transport: typed read tools over Swarmada api/v1, and the policy-gated
// request tools. Every call is audited.
package tools

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/authn"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// Session identifies who a tool call is for. It is set by the MCP server
// from request metadata, never by the model.
type Session struct {
	ID string
	// Operator is the requester: the username of the caller's verified bearer
	// token, or "unverified:<asserted name>" (internal/authn, R39).
	Operator string
	Verified bool
	// Unverified is why the requester is not verified; empty when verified.
	Unverified string
	// ConfirmedPreview is the preview id the client says the requester
	// confirmed (_meta, never a tool argument; D74). Empty for a call that
	// carries no confirmation.
	ConfirmedPreview string
}

// Service runs the tools against a Kubernetes client.
type Service struct {
	C                client.Client
	Engine           *policy.Engine
	Audit            *audit.Writer
	Events           *audit.EventSink // optional
	Identity         string           // the principal fleetops-mcp runs as (spec.requestedBy)
	DefaultNamespace string
	Now              func() time.Time
	// Tracer records a span per policy decision and, in internal/mcpserver,
	// per tool call (internal/tracing). nil records nothing.
	Tracer trace.Tracer
	// Authn verifies the caller's bearer token (TokenReview). nil: every
	// requester is unverified.
	Authn authn.Authenticator
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ns(n string) string {
	if n == "" {
		return s.DefaultNamespace
	}
	return n
}

func (s *Service) record(e audit.Entry) {
	if s.Audit == nil {
		return
	}
	if _, err := s.Audit.Append(e); err != nil {
		// The JSONL trail is the record of last resort; failing to write it is
		// surfaced on stderr by the caller's logger. Tool results are not altered.
		fmt.Printf("audit append failed: %v\n", err)
	}
}

func (s *Service) readAudit(sess Session, tool, ns, obj string) {
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: tool, Namespace: ns, Object: obj, Code: string(policy.CodeRead)})
}

// --- Read tools -------------------------------------------------------------

// NameInput selects one object.
type NameInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"Kubernetes namespace (described per deployment by mcpserver.NamespaceDescription)"`
	Name      string `json:"name" jsonschema:"object name"`
}

// ListInput selects a namespace.
type ListInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"Kubernetes namespace (described per deployment by mcpserver.NamespaceDescription)"`
}

// EventsInput selects the Events of one object.
type EventsInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"Kubernetes namespace (described per deployment by mcpserver.NamespaceDescription)"`
	Kind      string `json:"kind" jsonschema:"object kind, e.g. Robot, FleetTask, FleetAction, FleetZone"`
	Name      string `json:"name" jsonschema:"object name"`
}

// Lists wrap slices so every tool output is a JSON object.
type (
	RobotList struct {
		Items []RobotView `json:"items"`
	}
	FleetTaskList struct {
		Items []FleetTaskView `json:"items"`
	}
	FleetActionList struct {
		Items []FleetActionView `json:"items"`
	}
	ZoneList struct {
		Items []ZoneView `json:"items"`
	}
	ZoneMaintenanceList struct {
		Items []ZoneMaintenanceView `json:"items"`
	}
	DiscoveredList struct {
		Items []DiscoveredRobotView `json:"items"`
	}
	AdapterList struct {
		Items []FleetAdapterView `json:"items"`
	}
	TriggerList struct {
		Items []TaskTriggerView `json:"items"`
	}
	EventList struct {
		Items []EventView `json:"items"`
	}
)

func notFound(kind, ns, name string, err error) error {
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%s %q not found in namespace %q", kind, name, ns)
	}
	return err
}

// ListRobots implements list_robots.
func (s *Service) ListRobots(ctx context.Context, sess Session, in ListInput) (RobotList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "list_robots", ns, "")
	var l swarmv1.RobotList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return RobotList{}, err
	}
	out := RobotList{Items: []RobotView{}}
	for i := range l.Items {
		out.Items = append(out.Items, robotView(&l.Items[i]))
	}
	return out, nil
}

// GetRobot implements get_robot.
func (s *Service) GetRobot(ctx context.Context, sess Session, in NameInput) (RobotView, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "get_robot", ns, "Robot/"+in.Name)
	var r swarmv1.Robot
	if err := s.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: in.Name}, &r); err != nil {
		return RobotView{}, notFound("Robot", ns, in.Name, err)
	}
	return robotView(&r), nil
}

// ListFleetTasks implements list_fleet_tasks.
func (s *Service) ListFleetTasks(ctx context.Context, sess Session, in ListInput) (FleetTaskList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "list_fleet_tasks", ns, "")
	var l swarmv1.FleetTaskList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return FleetTaskList{}, err
	}
	out := FleetTaskList{Items: []FleetTaskView{}}
	for i := range l.Items {
		out.Items = append(out.Items, taskView(&l.Items[i], fleet.CreatedByLabel))
	}
	return out, nil
}

// GetFleetTask implements get_fleet_task.
func (s *Service) GetFleetTask(ctx context.Context, sess Session, in NameInput) (FleetTaskView, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "get_fleet_task", ns, "FleetTask/"+in.Name)
	var t swarmv1.FleetTask
	if err := s.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: in.Name}, &t); err != nil {
		return FleetTaskView{}, notFound("FleetTask", ns, in.Name, err)
	}
	return taskView(&t, fleet.CreatedByLabel), nil
}

// ListFleetActions implements list_fleet_actions.
func (s *Service) ListFleetActions(ctx context.Context, sess Session, in ListInput) (FleetActionList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "list_fleet_actions", ns, "")
	var l swarmv1.FleetActionList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return FleetActionList{}, err
	}
	out := FleetActionList{Items: []FleetActionView{}}
	for i := range l.Items {
		out.Items = append(out.Items, actionView(&l.Items[i]))
	}
	return out, nil
}

// GetFleetAction implements get_fleet_action.
func (s *Service) GetFleetAction(ctx context.Context, sess Session, in NameInput) (FleetActionView, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "get_fleet_action", ns, "FleetAction/"+in.Name)
	var a swarmv1.FleetAction
	if err := s.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: in.Name}, &a); err != nil {
		return FleetActionView{}, notFound("FleetAction", ns, in.Name, err)
	}
	return actionView(&a), nil
}

// ListZones implements list_zones.
func (s *Service) ListZones(ctx context.Context, sess Session, in ListInput) (ZoneList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "list_zones", ns, "")
	var l swarmv1.FleetZoneList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return ZoneList{}, err
	}
	out := ZoneList{Items: []ZoneView{}}
	for i := range l.Items {
		out.Items = append(out.Items, zoneView(&l.Items[i], fleet.EstopAnnotation))
	}
	return out, nil
}

// GetZone implements get_zone.
func (s *Service) GetZone(ctx context.Context, sess Session, in NameInput) (ZoneView, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "get_zone", ns, "FleetZone/"+in.Name)
	var z swarmv1.FleetZone
	if err := s.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: in.Name}, &z); err != nil {
		return ZoneView{}, notFound("FleetZone", ns, in.Name, err)
	}
	return zoneView(&z, fleet.EstopAnnotation), nil
}

// ListZoneMaintenances implements list_zone_maintenances.
func (s *Service) ListZoneMaintenances(ctx context.Context, sess Session, in ListInput) (ZoneMaintenanceList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "list_zone_maintenances", ns, "")
	var l swarmv1.ZoneMaintenanceList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return ZoneMaintenanceList{}, err
	}
	out := ZoneMaintenanceList{Items: []ZoneMaintenanceView{}}
	for i := range l.Items {
		out.Items = append(out.Items, maintenanceView(&l.Items[i]))
	}
	return out, nil
}

// ListDiscoveredRobots implements list_discovered_robots.
func (s *Service) ListDiscoveredRobots(ctx context.Context, sess Session, in ListInput) (DiscoveredList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "list_discovered_robots", ns, "")
	var l swarmv1.DiscoveredRobotList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return DiscoveredList{}, err
	}
	out := DiscoveredList{Items: []DiscoveredRobotView{}}
	for _, d := range l.Items {
		out.Items = append(out.Items, DiscoveredRobotView{
			Name: d.Name, Phase: string(d.Status.Phase), Manufacturer: d.Status.Manufacturer, Model: d.Status.Model,
			FirmwareVersion: d.Status.FirmwareVersion, TTLExpiresAt: ts(d.Status.TTLExpiresAt), ReportedCapabilities: d.Status.ReportedCapabilities,
		})
	}
	return out, nil
}

// ListFleetAdapters implements list_fleet_adapters.
func (s *Service) ListFleetAdapters(ctx context.Context, sess Session, in ListInput) (AdapterList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "list_fleet_adapters", ns, "")
	var l swarmv1.FleetAdapterList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return AdapterList{}, err
	}
	out := AdapterList{Items: []FleetAdapterView{}}
	for _, a := range l.Items {
		out.Items = append(out.Items, FleetAdapterView{
			Name: a.Name, Phase: string(a.Status.Phase), Conformance: string(a.Status.Conformance),
			ConnectedRobots: a.Status.ConnectedRobots, LastHeartbeat: ts(a.Status.LastHeartbeat), Message: a.Status.Message,
			Conditions: conds(a.Status.Conditions),
		})
	}
	return out, nil
}

// ListTaskTriggers implements list_task_triggers.
func (s *Service) ListTaskTriggers(ctx context.Context, sess Session, in ListInput) (TriggerList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "list_task_triggers", ns, "")
	var l swarmv1.TaskTriggerList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return TriggerList{}, err
	}
	out := TriggerList{Items: []TaskTriggerView{}}
	for _, t := range l.Items {
		out.Items = append(out.Items, TaskTriggerView{
			Name: t.Name, Suspend: t.Spec.Suspend, LastFiredAt: ts(t.Status.LastFiredAt), LastEventName: t.Status.LastEventName,
			DraftCount: t.Status.DraftCount, PendingDrafts: t.Status.PendingDrafts,
		})
	}
	return out, nil
}

// GetEvents implements get_events.
func (s *Service) GetEvents(ctx context.Context, sess Session, in EventsInput) (EventList, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "get_events", ns, in.Kind+"/"+in.Name)
	var l corev1.EventList
	if err := s.C.List(ctx, &l, client.InNamespace(ns)); err != nil {
		return EventList{}, err
	}
	out := EventList{Items: []EventView{}}
	sort.Slice(l.Items, func(i, j int) bool { return l.Items[i].LastTimestamp.Before(&l.Items[j].LastTimestamp) })
	for _, e := range l.Items {
		if e.InvolvedObject.Name != in.Name || (in.Kind != "" && e.InvolvedObject.Kind != in.Kind) {
			continue
		}
		out.Items = append(out.Items, EventView{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: e.Count, Last: ts(&e.LastTimestamp), Source: e.Source.Component})
	}
	return out, nil
}

// GetRequest implements get_request.
func (s *Service) GetRequest(ctx context.Context, sess Session, in NameInput) (RequestView, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "get_request", ns, "ApprovalRequest/"+in.Name)
	var r fleetopsv1.ApprovalRequest
	if err := s.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: in.Name}, &r); err != nil {
		return RequestView{}, notFound("ApprovalRequest", ns, in.Name, err)
	}
	return requestView(&r), nil
}
