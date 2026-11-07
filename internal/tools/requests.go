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
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/draft"
	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tracing"
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

func (s *Service) decide(ctx context.Context, sess Session, tool string, r policy.Request, snap policy.Snapshot) policy.Decision {
	_, span := tracing.OrNoop(s.Tracer).Start(ctx, tracing.SpanPolicy, trace.WithAttributes(
		tracing.AttrToolName.String(tool), tracing.AttrPolicyOp.String(string(r.Op)), tracing.AttrNamespace.String(r.Namespace),
	))
	d := s.Engine.Decide(r, snap)
	span.SetAttributes(tracing.AttrPolicyEffect.String(string(d.Effect)), tracing.AttrPolicyCode.String(string(d.Code)))
	span.End()
	s.record(audit.Entry{Kind: audit.KindPolicyDecision, Actor: sess.Operator, SessionID: sess.ID, Tool: tool, Namespace: r.Namespace, Code: string(d.Code), Message: d.Message, Detail: map[string]any{"effect": string(d.Effect)}})
	return d
}

// outOfScope refuses a request for a namespace the policy does not cover,
// before any cluster state is read for it.
func (s *Service) outOfScope(ctx context.Context, sess Session, tool string, r policy.Request) *policy.Decision {
	if s.Engine.NamespaceAllowed(r.Namespace) {
		return nil
	}
	d := s.decide(ctx, sess, tool, r, policy.Snapshot{})
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
	Name                 string          `json:"name" jsonschema:"member name, unique in the task"`
	Type                 string          `json:"type" jsonschema:"FleetAction spec.type: Navigate, PickUp, DropOff, Patrol, Charge, Clean or Custom"`
	CustomType           string          `json:"customType,omitempty" jsonschema:"the verb of a Custom action"`
	Zone                 string          `json:"zone,omitempty" jsonschema:"FleetZone the action runs in (default: the location's zone)"`
	Location             *LocationInput  `json:"location,omitempty" jsonschema:"where the action takes place"`
	Route                []LocationInput `json:"route,omitempty" jsonschema:"ordered stops (patrol); one member per stop"`
	LookFor              []string        `json:"lookFor,omitempty" jsonschema:"what an inspection is about"`
	Instruction          string          `json:"instruction,omitempty" jsonschema:"the requester's own words, verbatim"`
	Priority             string          `json:"priority,omitempty" jsonschema:"High, Normal or Low (default Normal)"`
	RequiredCapabilities []string        `json:"requiredCapabilities,omitempty" jsonschema:"capability names a robot must have Active"`
	PreferredRobot       string          `json:"preferredRobot,omitempty" jsonschema:"a scheduling preference, not a binding"`
	RobotSelector        string          `json:"robotSelector,omitempty" jsonschema:"pins the action to this robot"`
	Dock                 string          `json:"dock,omitempty" jsonschema:"charging dock or waypoint (Charge)"`
	Bounds               string          `json:"bounds,omitempty" jsonschema:"area of a survey"`
	OnFailure            string          `json:"onFailure,omitempty" jsonschema:"Requeue (default: retries up to 3 times, then alerts) or Alert"`
	DependsOn            []string        `json:"dependsOn,omitempty" jsonschema:"member names that must reach Succeeded first"`
}

// DraftTaskInput is the input of request_draft_task.
type DraftTaskInput struct {
	Namespace string             `json:"namespace,omitempty"`
	Name      string             `json:"name" jsonschema:"FleetTask name (DNS-1123 subdomain)"`
	Purpose   string             `json:"purpose" jsonschema:"one sentence shown to the person who reviews the draft"`
	Actions   []DraftActionInput `json:"actions" jsonschema:"the task's members"`
	Repeat    int32              `json:"repeat,omitempty" jsonschema:"rounds to run the members in order (patrol)"`
	Preview   *PreviewInput      `json:"preview,omitempty"`
}

// confirmed applies the D74 invariant: the request is filed only when the
// client confirmed a preview whose id equals the id of this request.
func confirmed(sess Session, in any) (fleetopsv1.ApprovalRequestSpec, *policy.Decision) {
	_, spec, err := SpecFor(in)
	if err != nil {
		d := policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: err.Error()}
		return spec, &d
	}
	if d := policy.Confirmation(sess.ConfirmedPreview, PreviewID(spec)); d != nil {
		return spec, d
	}
	return spec, nil
}

// checkAtFiling runs check_task's evaluation on current state. A blocked
// request is not filed (DENY_CHECK_FAILED); otherwise the checks it passed
// are recorded in spec.checks (D77).
func (s *Service) checkAtFiling(ctx context.Context, ns string, p feasibility.Proposal) (*fleetopsv1.ChecksRecord, *policy.Decision) {
	snap := feasibility.Load(ctx, s.C, ns, s.now())
	rep := feasibility.Evaluate(p, snap)
	rec := &fleetopsv1.ChecksRecord{Verdict: rep.Verdict, CheckedAt: metav1.NewTime(s.now().UTC().Truncate(time.Second)), Checks: rep.Records()}
	if rep.Verdict == feasibility.Blocked {
		var why []string
		for _, b := range rep.Blockers {
			why = append(why, b.Code+": "+b.Reason)
		}
		d := policy.Decision{Effect: policy.Deny, Code: policy.CodeCheckFailed, Message: "check_task on current state: " + strings.Join(why, "; ")}
		return rec, &d
	}
	return rec, nil
}

func (s *Service) refuse(ctx context.Context, sess Session, tool string, r policy.Request, d policy.Decision) RequestResult {
	_, span := tracing.OrNoop(s.Tracer).Start(ctx, tracing.SpanPolicy, trace.WithAttributes(
		tracing.AttrToolName.String(tool), tracing.AttrPolicyOp.String(string(r.Op)), tracing.AttrNamespace.String(r.Namespace),
		tracing.AttrPolicyEffect.String(string(d.Effect)), tracing.AttrPolicyCode.String(string(d.Code)),
	))
	span.End()
	s.record(audit.Entry{Kind: audit.KindPolicyDecision, Actor: sess.Operator, SessionID: sess.ID, Tool: tool, Namespace: r.Namespace, Code: string(d.Code), Message: d.Message, Detail: map[string]any{"effect": string(d.Effect)}})
	return refused(d)
}

// RequestDraftTask asks a named person to approve creating a FleetTask held
// as a draft (spec.draft: true). fleetops-mcp writes nothing to Swarmada: on
// approval, fleetops-approvals creates the draft (DESIGN.md D25), and a draft
// dispatches nothing until a person activates it. The request is filed only
// with a confirmed preview (D74) and when check_task finds no blocker.
func (s *Service) RequestDraftTask(ctx context.Context, sess Session, in DraftTaskInput) (RequestResult, error) {
	ns := s.ns(in.Namespace)
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "request_draft_task", Namespace: ns, Object: "FleetTask/" + in.Name})
	if d := needSession(sess); d != nil {
		return refused(*d), nil
	}
	pr := policy.Request{Op: policy.OpDraftTask, Namespace: ns, DraftTask: &policy.DraftTaskParams{Name: in.Name, Draft: true}}
	if d := s.outOfScope(ctx, sess, "request_draft_task", pr); d != nil {
		return refused(*d), nil
	}
	reqSpec, d := confirmed(sess, in)
	if d != nil {
		return s.refuse(ctx, sess, "request_draft_task", pr, *d), nil
	}
	if errs := validation.IsDNS1123Subdomain(in.Name); len(errs) > 0 {
		return refused(policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: fmt.Sprintf("task name %q: %v", in.Name, errs)}), nil
	}
	spec := reqSpec.DraftTask
	params := &policy.DraftTaskParams{Name: in.Name, Draft: true}
	for _, a := range spec.Actions {
		params.Actions = append(params.Actions, policy.DraftAction{Name: a.Name, Type: a.Type, Priority: a.Priority, Zone: a.Zone})
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
	d2 := s.decide(ctx, sess, "request_draft_task", policy.Request{Op: policy.OpDraftTask, Namespace: ns, SessionID: sess.ID, DraftTask: params}, snap)
	if d2.Effect != policy.RequireApproval {
		return refused(d2), nil
	}
	task, err := draft.Task(ns, spec)
	if err != nil {
		return refused(policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: err.Error()}), nil
	}
	checks, d := s.checkAtFiling(ctx, ns, feasibility.Proposal{Task: task, Creating: true})
	if d != nil {
		return s.refuse(ctx, sess, "request_draft_task", pr, *d), nil
	}
	reqSpec.Checks = checks
	return s.createRequest(ctx, sess, ns, d2, reqSpec,
		"a person with create on fleettasks runs: fleetops approve <request>; fleetops-approvals then re-checks the fleet and creates the draft, which dispatches nothing until it is activated (request_activate_task)")
}

// ActivateTaskInput is the input of request_activate_task.
type ActivateTaskInput struct {
	Namespace string        `json:"namespace,omitempty"`
	TaskName  string        `json:"taskName" jsonschema:"the draft FleetTask to activate"`
	Preview   *PreviewInput `json:"preview,omitempty"`
}

// RequestActivateTask asks a person to activate a draft.
func (s *Service) RequestActivateTask(ctx context.Context, sess Session, in ActivateTaskInput) (RequestResult, error) {
	ns := s.ns(in.Namespace)
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "request_activate_task", Namespace: ns, Object: "FleetTask/" + in.TaskName})
	if d := needSession(sess); d != nil {
		return refused(*d), nil
	}
	pr := policy.Request{Op: policy.OpActivateTask, Namespace: ns, ActivateTask: &policy.ActivateTaskParams{TaskName: in.TaskName}}
	if d := s.outOfScope(ctx, sess, "request_activate_task", pr); d != nil {
		return refused(*d), nil
	}
	reqSpec, d := confirmed(sess, in)
	if d != nil {
		return s.refuse(ctx, sess, "request_activate_task", pr, *d), nil
	}
	snap, err := fleet.TaskSnapshot(ctx, s.C, ns, in.TaskName)
	if err != nil {
		return RequestResult{}, err
	}
	if snap.DraftGateServed, _, err = fleet.DraftGate(ctx, s.C); err != nil {
		return RequestResult{}, err
	}
	d2 := s.decide(ctx, sess, "request_activate_task", pr, snap)
	if d2.Effect != policy.RequireApproval {
		return refused(d2), nil
	}
	checks, d := s.checkAtFiling(ctx, ns, feasibility.Proposal{ActivateTask: in.TaskName})
	if d != nil {
		return s.refuse(ctx, sess, "request_activate_task", pr, *d), nil
	}
	reqSpec.Checks = checks
	return s.createRequest(ctx, sess, ns, d2, reqSpec,
		"a person with patch and activate on this fleettask runs: fleetops approve <request>; once approved, a person Swarmada allows to activate runs fleetops activate <request>. The copilot never activates; it records the activation when it sees it")
}

// ZoneMaintenanceInput is the input of request_zone_maintenance.
type ZoneMaintenanceInput struct {
	Namespace                     string        `json:"namespace,omitempty"`
	ScopeType                     string        `json:"scopeType,omitempty" jsonschema:"Zone (default) or Namespace"`
	ZoneName                      string        `json:"zoneName,omitempty" jsonschema:"FleetZone name; required for scope Zone"`
	Mode                          string        `json:"mode,omitempty" jsonschema:"Graceful (default: robots finish current actions) or Immediate"`
	Reason                        string        `json:"reason" jsonschema:"why maintenance is needed"`
	ScheduledStart                string        `json:"scheduledStart,omitempty" jsonschema:"RFC 3339 start time; empty means on approval"`
	AutoResumeAfterMinutes        int32         `json:"autoResumeAfterMinutes,omitempty" jsonschema:"must be 0: the copilot never schedules robots to resume"`
	RequireEstopClearBeforeResume *bool         `json:"requireEstopClearBeforeResume,omitempty" jsonschema:"must not be false"`
	Preview                       *PreviewInput `json:"preview,omitempty"`
}

// RequestZoneMaintenance asks a person to approve a ZoneMaintenance.
func (s *Service) RequestZoneMaintenance(ctx context.Context, sess Session, in ZoneMaintenanceInput) (RequestResult, error) {
	ns := s.ns(in.Namespace)
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "request_zone_maintenance", Namespace: ns, Object: "FleetZone/" + in.ZoneName})
	if d := needSession(sess); d != nil {
		return refused(*d), nil
	}
	pr := policy.Request{Op: policy.OpZoneMaintenance, Namespace: ns, ZoneMaintenance: &policy.ZoneMaintenanceParams{ScopeType: in.ScopeType, ZoneName: in.ZoneName, Mode: in.Mode, Reason: in.Reason}}
	if d := s.outOfScope(ctx, sess, "request_zone_maintenance", pr); d != nil {
		return refused(*d), nil
	}
	reqSpec, d := confirmed(sess, in)
	if d != nil {
		if d.Code == policy.CodeMalformed {
			return refused(*d), nil
		}
		return s.refuse(ctx, sess, "request_zone_maintenance", pr, *d), nil
	}
	p := reqSpec.ZoneMaintenance
	snap, err := fleet.ZoneSnapshot(ctx, s.C, ns, p.ZoneName)
	if err != nil {
		return RequestResult{}, err
	}
	d2 := s.decide(ctx, sess, "request_zone_maintenance", policy.Request{Op: policy.OpZoneMaintenance, Namespace: ns, ZoneMaintenance: &policy.ZoneMaintenanceParams{
		ScopeType: p.ScopeType, ZoneName: p.ZoneName, Mode: p.Mode, Reason: p.Reason,
		AutoResumeAfterMinutes: in.AutoResumeAfterMinutes, RequireEstopClearBeforeResume: in.RequireEstopClearBeforeResume,
	}}, snap)
	if d2.Effect != policy.RequireApproval {
		return refused(d2), nil
	}
	checks, d := s.checkAtFiling(ctx, ns, feasibility.Proposal{Maintenance: p})
	if d != nil {
		return s.refuse(ctx, sess, "request_zone_maintenance", pr, *d), nil
	}
	reqSpec.Checks = checks
	return s.createRequest(ctx, sess, ns, d2, reqSpec, "a person with create on zonemaintenances runs: fleetops approve <request>")
}

// ZoneStopInput is the input of request_zone_stop.
type ZoneStopInput struct {
	Namespace string        `json:"namespace,omitempty"`
	ZoneName  string        `json:"zoneName" jsonschema:"FleetZone to stop"`
	Reason    string        `json:"reason" jsonschema:"why the zone must stop; becomes the estop annotation value"`
	Preview   *PreviewInput `json:"preview,omitempty"`
}

// RequestZoneStop asks a person to approve a zone estop.
func (s *Service) RequestZoneStop(ctx context.Context, sess Session, in ZoneStopInput) (RequestResult, error) {
	ns := s.ns(in.Namespace)
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "request_zone_stop", Namespace: ns, Object: "FleetZone/" + in.ZoneName})
	if d := needSession(sess); d != nil {
		return refused(*d), nil
	}
	pr := policy.Request{Op: policy.OpZoneStop, Namespace: ns, ZoneStop: &policy.ZoneStopParams{ZoneName: in.ZoneName, Reason: in.Reason}}
	if d := s.outOfScope(ctx, sess, "request_zone_stop", pr); d != nil {
		return refused(*d), nil
	}
	reqSpec, d := confirmed(sess, in)
	if d != nil {
		return s.refuse(ctx, sess, "request_zone_stop", pr, *d), nil
	}
	snap, err := fleet.ZoneSnapshot(ctx, s.C, ns, in.ZoneName)
	if err != nil {
		return RequestResult{}, err
	}
	d2 := s.decide(ctx, sess, "request_zone_stop", pr, snap)
	if d2.Effect != policy.RequireApproval {
		return refused(d2), nil
	}
	checks, d := s.checkAtFiling(ctx, ns, feasibility.Proposal{ZoneStop: reqSpec.ZoneStop})
	if d != nil {
		return s.refuse(ctx, sess, "request_zone_stop", pr, *d), nil
	}
	reqSpec.Checks = checks
	return s.createRequest(ctx, sess, ns, d2, reqSpec,
		"a person with patch and estop-trigger on this fleetzone runs: fleetops approve <request>. For an emergency, use the physical E-stop or swarmctl estop trigger instead of waiting for approval")
}

func (s *Service) createRequest(ctx context.Context, sess Session, ns string, d policy.Decision, spec fleetopsv1.ApprovalRequestSpec, next string) (RequestResult, error) {
	if spec.Preview != nil {
		now := metav1.NewTime(s.now().UTC().Truncate(time.Second))
		spec.Preview.ID, spec.Preview.ConfirmedAt = PreviewID(spec), &now
	}
	spec.RequestedFor = sess.Operator
	spec.RequestedBy = s.Identity
	spec.SessionID = sess.ID
	spec.ExpiresAt = metav1.NewTime(s.now().Add(s.Engine.P.Approval.TTL.Duration).UTC().Truncate(time.Second))
	spec.Policy = fleetopsv1.PolicyRecord{Effect: string(d.Effect), Code: string(d.Code), Message: d.Message}
	req := &fleetopsv1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "areq-", Namespace: ns, Labels: map[string]string{fleet.SessionLabel: sess.ID, fleet.CreatedByLabel: "fleetops-mcp"}},
		Spec:       spec,
	}
	if err := s.C.Create(ctx, req); err != nil {
		s.record(audit.Entry{Kind: audit.KindExecutionFailed, Actor: sess.Operator, SessionID: sess.ID, Namespace: ns, Message: err.Error(), Detail: map[string]any{"type": string(spec.Type)}})
		return RequestResult{}, fmt.Errorf("creating ApprovalRequest: %w", err)
	}
	detail := map[string]any{"type": string(spec.Type), "requesterVerified": sess.Verified}
	if !sess.Verified {
		detail["unverifiedReason"] = sess.Unverified
	}
	s.record(audit.Entry{Kind: audit.KindRequestCreated, Actor: sess.Operator, SessionID: sess.ID, Namespace: ns, Object: "ApprovalRequest/" + req.Name, Code: string(d.Code), Detail: detail})
	s.event(ctx, req, "RequestCreated", fmt.Sprintf("%s requested for %s; awaiting a person's decision", spec.Type, sess.Operator))
	return RequestResult{
		Effect: string(d.Effect), Code: string(d.Code), Reason: d.Message, Created: "ApprovalRequest/" + req.Name,
		State: string(fleetopsv1.StatePending), NextStep: next,
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
