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
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// RequestResult is returned by every request tool. A refusal is a normal
// result (Refused true, with the policy code and reason), so the model can
// explain it; it is not an MCP error.
type RequestResult struct {
	Refused  bool   `json:"refused"`
	Effect   string `json:"policy.effect"`
	Code     string `json:"policy.code"`
	Reason   string `json:"policy.message"`
	Created  string `json:"created,omitempty" jsonschema:"Kind/name of the object created, if any"`
	State    string `json:"state,omitempty"`
	NextStep string `json:"nextStep,omitempty"`
}

func refused(d policy.Decision) RequestResult {
	return RequestResult{Refused: true, Effect: string(d.Effect), Code: string(d.Code), Reason: d.Message}
}

func (s *Service) decide(sess Session, tool string, r policy.Request, snap policy.Snapshot) policy.Decision {
	d := s.Engine.Decide(r, snap)
	s.record(audit.Entry{Kind: audit.KindPolicyDecision, Actor: sess.Operator, SessionID: sess.ID, Tool: tool, Namespace: r.Namespace, Code: string(d.Code), Message: d.Message, Detail: map[string]any{"effect": string(d.Effect)}})
	return d
}

// outOfScope refuses a request for a namespace the policy does not cover,
// before any cluster state is read for it.
func (s *Service) outOfScope(sess Session, tool string, r policy.Request) *policy.Decision {
	if s.Engine.NamespaceAllowed(r.Namespace) {
		return nil
	}
	d := s.decide(sess, tool, r, policy.Snapshot{})
	return &d
}

func needSession(sess Session) *policy.Decision {
	if sess.Operator == "" || sess.ID == "" {
		d := policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: "the session carries no operator identity or session id; write tools need both"}
		return &d
	}
	return nil
}

// DraftActionInput is one member of a drafted FleetTask.
type DraftActionInput struct {
	Name                 string   `json:"name" jsonschema:"member name, unique in the task"`
	Type                 string   `json:"type" jsonschema:"FleetAction spec.type: Navigate, PickUp, DropOff, Patrol, Charge or Clean"`
	Zone                 string   `json:"zone,omitempty" jsonschema:"FleetZone the action runs in"`
	Priority             string   `json:"priority,omitempty" jsonschema:"High, Normal or Low (default Normal)"`
	RequiredCapabilities []string `json:"requiredCapabilities,omitempty" jsonschema:"capability names a robot must have Active"`
	PreferredRobot       string   `json:"preferredRobot,omitempty" jsonschema:"a scheduling preference, not a binding"`
	DependsOn            []string `json:"dependsOn,omitempty" jsonschema:"member names that must reach Succeeded first"`
}

// DraftTaskInput is the input of request_draft_task.
type DraftTaskInput struct {
	Namespace string             `json:"namespace,omitempty"`
	Name      string             `json:"name" jsonschema:"FleetTask name (DNS-1123 subdomain)"`
	Purpose   string             `json:"purpose" jsonschema:"one sentence shown to the person who reviews the draft"`
	Actions   []DraftActionInput `json:"actions" jsonschema:"the task's members"`
}

// RequestDraftTask asks a named person to approve creating a FleetTask held
// as a draft (spec.draft: true). foc-mcp writes nothing to Swarmada: on
// approval, foc-approvals creates the draft (DESIGN.md D25), and a draft
// dispatches nothing until a person activates it.
func (s *Service) RequestDraftTask(ctx context.Context, sess Session, in DraftTaskInput) (RequestResult, error) {
	ns := s.ns(in.Namespace)
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "request_draft_task", Namespace: ns, Object: "FleetTask/" + in.Name})
	if d := needSession(sess); d != nil {
		return refused(*d), nil
	}
	if d := s.outOfScope(sess, "request_draft_task", policy.Request{Op: policy.OpDraftTask, Namespace: ns, DraftTask: &policy.DraftTaskParams{Name: in.Name, Draft: true}}); d != nil {
		return refused(*d), nil
	}
	if errs := validation.IsDNS1123Subdomain(in.Name); len(errs) > 0 {
		return refused(policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: fmt.Sprintf("task name %q: %v", in.Name, errs)}), nil
	}
	params := &policy.DraftTaskParams{Name: in.Name, Draft: true}
	spec := &focv1.DraftTaskParams{TaskName: in.Name, Purpose: in.Purpose}
	for _, a := range in.Actions {
		params.Actions = append(params.Actions, policy.DraftAction{Name: a.Name, Type: a.Type, Priority: a.Priority, Zone: a.Zone})
		spec.Actions = append(spec.Actions, focv1.DraftAction{
			Name: a.Name, Type: a.Type, Zone: a.Zone, Priority: a.Priority,
			RequiredCapabilities: a.RequiredCapabilities, PreferredRobot: a.PreferredRobot, DependsOn: a.DependsOn,
		})
	}
	snap, err := fleet.TaskSnapshot(ctx, s.C, ns, in.Name)
	if err != nil {
		return RequestResult{}, err
	}
	if snap.TaskExists {
		return refused(policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: fmt.Sprintf("a FleetTask named %q already exists; choose another name", in.Name)}), nil
	}
	if snap.DraftGateServed, _, err = fleet.DraftGate(ctx, s.C); err != nil {
		return RequestResult{}, err
	}
	if snap.DraftsThisSession, err = fleet.CountSessionDraftRequests(ctx, s.C, ns, sess.ID); err != nil {
		return RequestResult{}, err
	}
	d := s.decide(sess, "request_draft_task", policy.Request{Op: policy.OpDraftTask, Namespace: ns, SessionID: sess.ID, DraftTask: params}, snap)
	if d.Effect != policy.RequireApproval {
		return refused(d), nil
	}
	return s.createRequest(ctx, sess, ns, d, focv1.ApprovalRequestSpec{Type: focv1.RequestDraftTask, DraftTask: spec},
		"a person with create on fleettasks runs: foc approve <request>; foc-approvals then creates the draft, which dispatches nothing until it is activated (request_activate_task)")
}

// ActivateTaskInput is the input of request_activate_task.
type ActivateTaskInput struct {
	Namespace string `json:"namespace,omitempty"`
	TaskName  string `json:"taskName" jsonschema:"the draft FleetTask to activate"`
}

// RequestActivateTask asks a person to activate a draft.
func (s *Service) RequestActivateTask(ctx context.Context, sess Session, in ActivateTaskInput) (RequestResult, error) {
	ns := s.ns(in.Namespace)
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "request_activate_task", Namespace: ns, Object: "FleetTask/" + in.TaskName})
	if d := needSession(sess); d != nil {
		return refused(*d), nil
	}
	if d := s.outOfScope(sess, "request_activate_task", policy.Request{Op: policy.OpActivateTask, Namespace: ns, ActivateTask: &policy.ActivateTaskParams{TaskName: in.TaskName}}); d != nil {
		return refused(*d), nil
	}
	snap, err := fleet.TaskSnapshot(ctx, s.C, ns, in.TaskName)
	if err != nil {
		return RequestResult{}, err
	}
	if snap.DraftGateServed, _, err = fleet.DraftGate(ctx, s.C); err != nil {
		return RequestResult{}, err
	}
	d := s.decide(sess, "request_activate_task", policy.Request{Op: policy.OpActivateTask, Namespace: ns, ActivateTask: &policy.ActivateTaskParams{TaskName: in.TaskName}}, snap)
	if d.Effect != policy.RequireApproval {
		return refused(d), nil
	}
	return s.createRequest(ctx, sess, ns, d, focv1.ApprovalRequestSpec{Type: focv1.RequestActivateTask, ActivateTask: &focv1.ActivateTaskParams{TaskName: in.TaskName}},
		"a person with the activate verb runs: foc approve <request> (activation is done with their own credentials)")
}

// ZoneMaintenanceInput is the input of request_zone_maintenance.
type ZoneMaintenanceInput struct {
	Namespace                     string `json:"namespace,omitempty"`
	ScopeType                     string `json:"scopeType,omitempty" jsonschema:"Zone (default) or Namespace"`
	ZoneName                      string `json:"zoneName,omitempty" jsonschema:"FleetZone name; required for scope Zone"`
	Mode                          string `json:"mode,omitempty" jsonschema:"Graceful (default: robots finish current actions) or Immediate"`
	Reason                        string `json:"reason" jsonschema:"why maintenance is needed"`
	ScheduledStart                string `json:"scheduledStart,omitempty" jsonschema:"RFC 3339 start time; empty means on approval"`
	AutoResumeAfterMinutes        int32  `json:"autoResumeAfterMinutes,omitempty" jsonschema:"must be 0: the copilot never schedules robots to resume"`
	RequireEstopClearBeforeResume *bool  `json:"requireEstopClearBeforeResume,omitempty" jsonschema:"must not be false"`
}

// RequestZoneMaintenance asks a person to approve a ZoneMaintenance.
func (s *Service) RequestZoneMaintenance(ctx context.Context, sess Session, in ZoneMaintenanceInput) (RequestResult, error) {
	ns := s.ns(in.Namespace)
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "request_zone_maintenance", Namespace: ns, Object: "FleetZone/" + in.ZoneName})
	if d := needSession(sess); d != nil {
		return refused(*d), nil
	}
	if d := s.outOfScope(sess, "request_zone_maintenance", policy.Request{Op: policy.OpZoneMaintenance, Namespace: ns, ZoneMaintenance: &policy.ZoneMaintenanceParams{ScopeType: in.ScopeType, ZoneName: in.ZoneName, Mode: in.Mode, Reason: in.Reason}}); d != nil {
		return refused(*d), nil
	}
	if in.ScopeType == "" {
		in.ScopeType = "Zone"
	}
	if in.Mode == "" {
		in.Mode = "Graceful"
	}
	var start *metav1.Time
	if in.ScheduledStart != "" {
		t, err := time.Parse(time.RFC3339, in.ScheduledStart)
		if err != nil {
			return refused(policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: "scheduledStart must be RFC 3339"}), nil
		}
		mt := metav1.NewTime(t)
		start = &mt
	}
	snap, err := fleet.ZoneSnapshot(ctx, s.C, ns, in.ZoneName)
	if err != nil {
		return RequestResult{}, err
	}
	d := s.decide(sess, "request_zone_maintenance", policy.Request{Op: policy.OpZoneMaintenance, Namespace: ns, ZoneMaintenance: &policy.ZoneMaintenanceParams{
		ScopeType: in.ScopeType, ZoneName: in.ZoneName, Mode: in.Mode, Reason: in.Reason,
		AutoResumeAfterMinutes: in.AutoResumeAfterMinutes, RequireEstopClearBeforeResume: in.RequireEstopClearBeforeResume,
	}}, snap)
	if d.Effect != policy.RequireApproval {
		return refused(d), nil
	}
	return s.createRequest(ctx, sess, ns, d, focv1.ApprovalRequestSpec{Type: focv1.RequestZoneMaintenance, ZoneMaintenance: &focv1.ZoneMaintenanceParams{
		ScopeType: in.ScopeType, ZoneName: in.ZoneName, Mode: in.Mode, Reason: in.Reason, ScheduledStart: start,
	}}, "a person with create on zonemaintenances runs: foc approve <request>")
}

// ZoneStopInput is the input of request_zone_stop.
type ZoneStopInput struct {
	Namespace string `json:"namespace,omitempty"`
	ZoneName  string `json:"zoneName" jsonschema:"FleetZone to stop"`
	Reason    string `json:"reason" jsonschema:"why the zone must stop; becomes the estop annotation value"`
}

// RequestZoneStop asks a person to approve a zone estop.
func (s *Service) RequestZoneStop(ctx context.Context, sess Session, in ZoneStopInput) (RequestResult, error) {
	ns := s.ns(in.Namespace)
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "request_zone_stop", Namespace: ns, Object: "FleetZone/" + in.ZoneName})
	if d := needSession(sess); d != nil {
		return refused(*d), nil
	}
	if d := s.outOfScope(sess, "request_zone_stop", policy.Request{Op: policy.OpZoneStop, Namespace: ns, ZoneStop: &policy.ZoneStopParams{ZoneName: in.ZoneName, Reason: in.Reason}}); d != nil {
		return refused(*d), nil
	}
	snap, err := fleet.ZoneSnapshot(ctx, s.C, ns, in.ZoneName)
	if err != nil {
		return RequestResult{}, err
	}
	d := s.decide(sess, "request_zone_stop", policy.Request{Op: policy.OpZoneStop, Namespace: ns, ZoneStop: &policy.ZoneStopParams{ZoneName: in.ZoneName, Reason: in.Reason}}, snap)
	if d.Effect != policy.RequireApproval {
		return refused(d), nil
	}
	return s.createRequest(ctx, sess, ns, d, focv1.ApprovalRequestSpec{Type: focv1.RequestZoneStop, ZoneStop: &focv1.ZoneStopParams{ZoneName: in.ZoneName, Reason: in.Reason}},
		"a person with patch and estop-trigger on this fleetzone runs: foc approve <request>. For an emergency, use the physical E-stop or swarmctl estop trigger instead of waiting for approval")
}

func (s *Service) createRequest(ctx context.Context, sess Session, ns string, d policy.Decision, spec focv1.ApprovalRequestSpec, next string) (RequestResult, error) {
	spec.RequestedFor = sess.Operator
	spec.RequestedBy = s.Identity
	spec.SessionID = sess.ID
	spec.ExpiresAt = metav1.NewTime(s.now().Add(s.Engine.P.Approval.TTL.Duration).UTC().Truncate(time.Second))
	spec.Policy = focv1.PolicyRecord{Effect: string(d.Effect), Code: string(d.Code), Message: d.Message}
	req := &focv1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "areq-", Namespace: ns, Labels: map[string]string{fleet.SessionLabel: sess.ID, fleet.CreatedByLabel: "foc-mcp"}},
		Spec:       spec,
	}
	if err := s.C.Create(ctx, req); err != nil {
		s.record(audit.Entry{Kind: audit.KindExecutionFailed, Actor: sess.Operator, SessionID: sess.ID, Namespace: ns, Message: err.Error(), Detail: map[string]any{"type": string(spec.Type)}})
		return RequestResult{}, fmt.Errorf("creating ApprovalRequest: %w", err)
	}
	s.record(audit.Entry{Kind: audit.KindRequestCreated, Actor: sess.Operator, SessionID: sess.ID, Namespace: ns, Object: "ApprovalRequest/" + req.Name, Code: string(d.Code), Detail: map[string]any{"type": string(spec.Type)}})
	s.event(ctx, req, "RequestCreated", fmt.Sprintf("%s requested for %s; awaiting a person's decision", spec.Type, sess.Operator))
	return RequestResult{
		Effect: string(d.Effect), Code: string(d.Code), Reason: d.Message, Created: "ApprovalRequest/" + req.Name,
		State: string(focv1.StatePending), NextStep: next,
	}, nil
}

func (s *Service) event(ctx context.Context, obj client.Object, reason, msg string) {
	if s.Events == nil {
		return
	}
	if err := s.Events.Emit(ctx, obj, corev1.EventTypeNormal, reason, msg); err != nil {
		s.record(audit.Entry{Kind: audit.KindExecutionFailed, Message: "event write failed: " + err.Error(), Object: obj.GetName()})
	}
}
