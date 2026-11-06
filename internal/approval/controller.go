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

// Package approval turns a person's ApprovalDecision into at most one
// Swarmada write. It runs in foc-approvals under its own ServiceAccount.
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/draft"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// Authorizer answers whether a person may perform an operation themselves.
type Authorizer interface {
	Allowed(ctx context.Context, user string, groups []string, attrs authzv1.ResourceAttributes) (bool, error)
}

// SARAuthorizer asks the API server with a SubjectAccessReview.
type SARAuthorizer struct{ C client.Client }

// Allowed implements Authorizer.
func (a SARAuthorizer) Allowed(ctx context.Context, user string, groups []string, attrs authzv1.ResourceAttributes) (bool, error) {
	sar := &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{User: user, Groups: groups, ResourceAttributes: &attrs}}
	if err := a.C.Create(ctx, sar); err != nil {
		return false, err
	}
	return sar.Status.Allowed, nil
}

// RequiredAttributes lists the Swarmada permissions a person needs to perform
// a request's write themselves. All must be allowed.
func RequiredAttributes(r *focv1.ApprovalRequest) []authzv1.ResourceAttributes {
	ns := r.Namespace
	switch r.Spec.Type {
	case focv1.RequestDraftTask:
		return []authzv1.ResourceAttributes{{Namespace: ns, Group: fleet.SwarmadaGroup, Resource: "fleettasks", Verb: "create"}}
	case focv1.RequestZoneMaintenance:
		return []authzv1.ResourceAttributes{{Namespace: ns, Group: fleet.SwarmadaGroup, Resource: "zonemaintenances", Verb: "create"}}
	case focv1.RequestZoneStop:
		z := r.Spec.ZoneStop.ZoneName
		return []authzv1.ResourceAttributes{
			{Namespace: ns, Group: fleet.SwarmadaGroup, Resource: "fleetzones", Name: z, Verb: "patch"},
			{Namespace: ns, Group: fleet.SwarmadaGroup, Resource: "fleetzones", Name: z, Verb: "estop-trigger"},
		}
	case focv1.RequestActivateTask:
		t := r.Spec.ActivateTask.TaskName
		return []authzv1.ResourceAttributes{
			{Namespace: ns, Group: fleet.SwarmadaGroup, Resource: "fleettasks", Name: t, Verb: "patch"},
			{Namespace: ns, Group: fleet.SwarmadaGroup, Resource: "fleettasks", Name: t, Verb: "activate"},
		}
	}
	return nil
}

// Reconciler processes ApprovalRequests and their decisions.
type Reconciler struct {
	C      client.Client
	Engine *policy.Engine
	Authz  Authorizer
	Audit  *audit.Writer
	Events *audit.EventSink
	Now    func() time.Time
	// ObservePeriod is how often an approved activation is re-checked.
	ObservePeriod time.Duration
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) log(e audit.Entry) {
	if r.Audit != nil {
		if _, err := r.Audit.Append(e); err != nil {
			ctrl.Log.Error(err, "audit append failed")
		}
	}
}

func (r *Reconciler) event(ctx context.Context, obj client.Object, typ, reason, msg string) {
	if r.Events != nil {
		if err := r.Events.Emit(ctx, obj, typ, reason, msg); err != nil {
			ctrl.Log.Error(err, "event write failed", "reason", reason)
		}
	}
}

// SetupWithManager registers the reconciler: it runs on ApprovalRequest
// changes and on every ApprovalDecision, mapped to the request it names.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&focv1.ApprovalRequest{}).
		Watches(&focv1.ApprovalDecision{}, DecisionToRequest()).
		Watches(&swarmv1.FleetTask{}, TaskToRequest()).
		Complete(r)
}

// DecisionToRequest maps an ApprovalDecision event to the request it names.
func DecisionToRequest() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		d, ok := o.(*focv1.ApprovalDecision)
		if !ok {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: d.Namespace, Name: d.Spec.RequestRef}}}
	})
}

// Reconcile implements reconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, key reconcile.Request) (reconcile.Result, error) {
	var req focv1.ApprovalRequest
	if err := r.C.Get(ctx, key.NamespacedName, &req); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	st := req.Status.State
	if st == focv1.StateExecuted && req.Spec.Type == focv1.RequestDraftTask && req.Status.Outcome == nil && req.Status.Target != nil {
		return reconcile.Result{}, r.observeOutcome(ctx, &req)
	}
	if st.IsTerminal() {
		return reconcile.Result{}, nil
	}
	if st == "" {
		return reconcile.Result{Requeue: true}, r.setStatus(ctx, &req, func(s *focv1.ApprovalRequestStatus) { s.State = focv1.StatePending })
	}
	if st == focv1.StateApproved {
		return r.execute(ctx, &req)
	}
	// Pending.
	if r.now().After(req.Spec.ExpiresAt.Time) {
		r.log(audit.Entry{Kind: audit.KindExpired, Namespace: req.Namespace, Object: "ApprovalRequest/" + req.Name, SessionID: req.Spec.SessionID})
		r.event(ctx, &req, corev1.EventTypeNormal, "Expired", "request expired without an accepted decision")
		return reconcile.Result{}, r.setStatus(ctx, &req, func(s *focv1.ApprovalRequestStatus) {
			s.State, s.Message = focv1.StateExpired, "expired without an accepted decision"
		})
	}
	decisions, err := r.decisionsFor(ctx, &req)
	if err != nil {
		return reconcile.Result{}, err
	}
	for i := range decisions {
		d := &decisions[i]
		if slices.Contains(req.Status.RefusedDecisions, d.Name) {
			continue
		}
		accepted, err := r.consider(ctx, &req, d)
		if err != nil || accepted {
			return reconcile.Result{Requeue: accepted}, err
		}
	}
	// Re-check expiry when it is due.
	return reconcile.Result{RequeueAfter: time.Until(req.Spec.ExpiresAt.Time) + time.Second}, nil
}

func (r *Reconciler) decisionsFor(ctx context.Context, req *focv1.ApprovalRequest) ([]focv1.ApprovalDecision, error) {
	var list focv1.ApprovalDecisionList
	if err := r.C.List(ctx, &list, client.InNamespace(req.Namespace)); err != nil {
		return nil, err
	}
	var out []focv1.ApprovalDecision
	for _, d := range list.Items {
		if d.Spec.RequestRef == req.Name {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreationTimestamp.Equal(&out[j].CreationTimestamp) {
			return out[i].Name < out[j].Name
		}
		return out[i].CreationTimestamp.Before(&out[j].CreationTimestamp)
	})
	return out, nil
}

// consider accepts or refuses one decision. It returns true when the
// decision moved the request to Approved or Rejected.
func (r *Reconciler) consider(ctx context.Context, req *focv1.ApprovalRequest, d *focv1.ApprovalDecision) (bool, error) {
	approve := d.Spec.Verdict == focv1.VerdictApprove
	authorized := false
	if approve {
		authorized = true
		for _, attrs := range RequiredAttributes(req) {
			ok, err := r.Authz.Allowed(ctx, d.Spec.DecidedBy, d.Spec.DecidedByGroups, attrs)
			if err != nil {
				return false, err
			}
			if !ok {
				authorized = false
				break
			}
		}
	}
	// The admission policy guarantees decidedBy is the creator; the Decider
	// principal is therefore the decision's own spec.
	check := policy.ApprovalCheck{
		Decider: policy.Principal{Username: d.Spec.DecidedBy, Groups: d.Spec.DecidedByGroups}, DecidedBy: d.Spec.DecidedBy,
		RequestedFor: req.Spec.RequestedFor, RequestedBy: req.Spec.RequestedBy, State: string(req.Status.State),
		Expired: r.now().After(req.Spec.ExpiresAt.Time), DeciderAuthorized: authorized, Approve: approve,
	}
	verdict := r.Engine.DecideApproval(check)
	obj := "ApprovalRequest/" + req.Name
	if verdict.Effect != policy.Allow {
		r.log(audit.Entry{Kind: audit.KindDecisionRefused, Actor: d.Spec.DecidedBy, Namespace: req.Namespace, Object: obj, SessionID: req.Spec.SessionID, Code: string(verdict.Code), Message: verdict.Message, Detail: map[string]any{"decision": d.Name}})
		r.event(ctx, req, corev1.EventTypeWarning, "DecisionRefused", fmt.Sprintf("decision %s by %s refused: %s", d.Name, d.Spec.DecidedBy, verdict.Message))
		return false, r.setStatus(ctx, req, func(s *focv1.ApprovalRequestStatus) {
			s.RefusedDecisions = append(s.RefusedDecisions, d.Name)
			s.Message = verdict.Message
		})
	}
	now := metav1.NewTime(r.now())
	if !approve {
		r.log(audit.Entry{Kind: audit.KindRejected, Actor: d.Spec.DecidedBy, Namespace: req.Namespace, Object: obj, SessionID: req.Spec.SessionID, Message: d.Spec.Comment})
		r.event(ctx, req, corev1.EventTypeNormal, "Rejected", "rejected by "+d.Spec.DecidedBy)
		return true, r.setStatus(ctx, req, func(s *focv1.ApprovalRequestStatus) {
			s.State, s.DecidedBy, s.DecisionRef, s.DecidedAt, s.Message = focv1.StateRejected, d.Spec.DecidedBy, d.Name, &now, "rejected"
		})
	}
	// Fresh policy check before recording the approval.
	fresh, err := r.freshDecision(ctx, req)
	if err != nil {
		return false, err
	}
	if fresh.Effect != policy.RequireApproval {
		r.log(audit.Entry{Kind: audit.KindExecutionFailed, Actor: d.Spec.DecidedBy, Namespace: req.Namespace, Object: obj, SessionID: req.Spec.SessionID, Code: string(fresh.Code), Message: "policy re-check at approval: " + fresh.Message})
		r.event(ctx, req, corev1.EventTypeWarning, "PolicyDenied", fresh.Message)
		return true, r.setStatus(ctx, req, func(s *focv1.ApprovalRequestStatus) {
			s.State, s.DecidedBy, s.DecisionRef, s.DecidedAt = focv1.StateFailed, d.Spec.DecidedBy, d.Name, &now
			s.Message = "approved, but the policy re-check on current state refused it: " + fresh.Message
		})
	}
	// Record before effect.
	instruction := ""
	msg := "approved"
	if req.Spec.Type == focv1.RequestActivateTask {
		instruction = ActivationCommand(req.Namespace, req.Name)
		msg = "approved; Swarmada refuses activate to every ServiceAccount, so " + d.Spec.DecidedBy + " runs: " + instruction
	}
	r.log(audit.Entry{Kind: audit.KindApproved, Actor: d.Spec.DecidedBy, Namespace: req.Namespace, Object: obj, SessionID: req.Spec.SessionID, Detail: map[string]any{"decision": d.Name, "type": string(req.Spec.Type), "instruction": instruction}})
	r.event(ctx, req, corev1.EventTypeNormal, "Approved", "approved by "+d.Spec.DecidedBy)
	return true, r.setStatus(ctx, req, func(s *focv1.ApprovalRequestStatus) {
		s.State, s.DecidedBy, s.DecisionRef, s.DecidedAt, s.Message, s.Instruction = focv1.StateApproved, d.Spec.DecidedBy, d.Name, &now, msg, instruction
	})
}

func (r *Reconciler) freshDecision(ctx context.Context, req *focv1.ApprovalRequest) (policy.Decision, error) {
	ns := req.Namespace
	switch req.Spec.Type {
	case focv1.RequestDraftTask:
		p := req.Spec.DraftTask
		snap, err := fleet.TaskSnapshot(ctx, r.C, ns, p.TaskName)
		if err != nil {
			return policy.Decision{}, err
		}
		if snap.TaskExists {
			return policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: fmt.Sprintf("a FleetTask named %q already exists", p.TaskName)}, nil
		}
		if snap.DraftGateServed, _, err = fleet.DraftGate(ctx, r.C); err != nil {
			return policy.Decision{}, err
		}
		params := &policy.DraftTaskParams{Name: p.TaskName, Draft: true}
		for _, a := range p.Actions {
			params.Actions = append(params.Actions, policy.DraftAction{Name: a.Name, Type: a.Type, Priority: a.Priority, Zone: a.Zone})
		}
		// The per-session draft limit applies when requests are made, not here.
		return r.Engine.Decide(policy.Request{Op: policy.OpDraftTask, Namespace: ns, DraftTask: params}, snap), nil
	case focv1.RequestZoneMaintenance:
		p := req.Spec.ZoneMaintenance
		snap, err := fleet.ZoneSnapshot(ctx, r.C, ns, p.ZoneName)
		if err != nil {
			return policy.Decision{}, err
		}
		return r.Engine.Decide(policy.Request{Op: policy.OpZoneMaintenance, Namespace: ns, ZoneMaintenance: &policy.ZoneMaintenanceParams{
			ScopeType: p.ScopeType, ZoneName: p.ZoneName, Mode: p.Mode, Reason: p.Reason,
		}}, snap), nil
	case focv1.RequestZoneStop:
		p := req.Spec.ZoneStop
		snap, err := fleet.ZoneSnapshot(ctx, r.C, ns, p.ZoneName)
		if err != nil {
			return policy.Decision{}, err
		}
		return r.Engine.Decide(policy.Request{Op: policy.OpZoneStop, Namespace: ns, ZoneStop: &policy.ZoneStopParams{ZoneName: p.ZoneName, Reason: p.Reason}}, snap), nil
	case focv1.RequestActivateTask:
		snap, err := fleet.TaskSnapshot(ctx, r.C, ns, req.Spec.ActivateTask.TaskName)
		if err != nil {
			return policy.Decision{}, err
		}
		if snap.DraftGateServed, _, err = fleet.DraftGate(ctx, r.C); err != nil {
			return policy.Decision{}, err
		}
		return r.Engine.Decide(policy.Request{Op: policy.OpActivateTask, Namespace: ns, ActivateTask: &policy.ActivateTaskParams{TaskName: req.Spec.ActivateTask.TaskName}}, snap), nil
	}
	return policy.Decision{Effect: policy.Deny, Code: policy.CodeMalformed, Message: "unknown request type"}, nil
}

// ActivationCommand is the exact command a person runs to activate the draft
// an approved ActivateTask request names, with their own kubeconfig (D79).
func ActivationCommand(ns, request string) string {
	return fmt.Sprintf("foc activate %s -n %s", request, ns)
}

func (r *Reconciler) observeActivation(ctx context.Context, req *focv1.ApprovalRequest) (reconcile.Result, error) {
	var t swarmv1.FleetTask
	if err := r.C.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Spec.ActivateTask.TaskName}, &t); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if t.Spec.Draft {
		period := r.ObservePeriod
		if period == 0 {
			period = 10 * time.Second
		}
		return reconcile.Result{RequeueAfter: period}, nil
	}
	now := metav1.NewTime(r.now())
	target := focv1.TargetRef{APIVersion: swarmv1.GroupVersion.String(), Kind: "FleetTask", Name: t.Name}
	// foc activate records the activator in an annotation that the
	// fleetops-activated-by admission policy binds to the authenticated user
	// (D79); an activation made another way leaves it unknown.
	activator := t.Annotations[ActivatedByAnnotation]
	if activator == "" {
		activator = "unknown"
	}
	r.log(audit.Entry{Kind: audit.KindActivationObserved, Actor: activator, Namespace: req.Namespace, Object: "ApprovalRequest/" + req.Name, SessionID: req.Spec.SessionID,
		Message: "spec.draft is false; activation was performed outside the copilot", Detail: map[string]any{"target": "FleetTask/" + t.Name, "phase": string(t.Status.Phase), "approvedBy": req.Status.DecidedBy, "activatedBy": activator}})
	r.event(ctx, req, corev1.EventTypeNormal, "ActivationObserved", fmt.Sprintf("FleetTask/%s left Draft after approval by %s, activated by %s; the copilot did not perform the activation", t.Name, req.Status.DecidedBy, activator))
	return reconcile.Result{}, r.setStatus(ctx, req, func(s *focv1.ApprovalRequestStatus) {
		s.State, s.ExecutedAt, s.Target, s.ActivatedBy = focv1.StateExecuted, &now, &target, activator
		s.Message = "activation observed (performed by " + activator + ", not by the copilot)"
	})
}

// MaintenanceName is the deterministic ZoneMaintenance name for a request, so
// a retried execution returns AlreadyExists instead of creating a second window.
func MaintenanceName(req *focv1.ApprovalRequest) string { return "foc-" + req.Name }

func (r *Reconciler) execute(ctx context.Context, req *focv1.ApprovalRequest) (reconcile.Result, error) {
	obj := "ApprovalRequest/" + req.Name
	var target focv1.TargetRef
	var err error
	var checks *focv1.ChecksRecord
	switch req.Spec.Type {
	case focv1.RequestDraftTask:
		var t *swarmv1.FleetTask
		t, err = DraftTaskObject(req, req.Status.DecidedBy)
		if err != nil {
			break
		}
		var existing swarmv1.FleetTask
		gerr := r.C.Get(ctx, client.ObjectKeyFromObject(t), &existing)
		if gerr == nil && existing.Labels[fleet.RequestLabel] == req.Name {
			// Created by this request on an earlier attempt: no re-check.
			target, err = r.createDraft(ctx, req)
			break
		}
		var refusal *policy.Decision
		checks, refusal, err = r.recheckDraft(ctx, req, t)
		if err != nil {
			return reconcile.Result{}, err
		}
		if refusal != nil {
			return reconcile.Result{}, r.refuseExecution(ctx, req, checks, *refusal)
		}
		target, err = r.createDraft(ctx, req)
	case focv1.RequestZoneMaintenance:
		target, err = r.createMaintenance(ctx, req)
	case focv1.RequestZoneStop:
		target, err = r.stopZone(ctx, req)
	case focv1.RequestActivateTask:
		// The copilot never activates: Swarmada's FleetTask webhook refuses
		// clearing spec.draft to every ServiceAccount (authorizeActivate,
		// internal/webhook/fleettask_webhook.go). The approver runs
		// status.instruction with their own credentials; this only observes
		// the task leaving Draft and records it (DESIGN.md D28).
		return r.observeActivation(ctx, req)
	default:
		err = fmt.Errorf("unknown request type %q", req.Spec.Type)
	}
	if err != nil {
		r.log(audit.Entry{Kind: audit.KindExecutionFailed, Namespace: req.Namespace, Object: obj, SessionID: req.Spec.SessionID, Message: err.Error()})
		r.event(ctx, req, corev1.EventTypeWarning, "ExecutionFailed", err.Error())
		if apierrors.IsForbidden(err) || apierrors.IsInvalid(err) || apierrors.IsAlreadyExists(err) {
			return reconcile.Result{}, r.setStatus(ctx, req, func(s *focv1.ApprovalRequestStatus) {
				s.State, s.Message = focv1.StateFailed, "execution refused by the API server: "+err.Error()
			})
		}
		return reconcile.Result{}, err
	}
	now := metav1.NewTime(r.now())
	r.log(audit.Entry{Kind: audit.KindExecuted, Actor: req.Status.DecidedBy, Namespace: req.Namespace, Object: obj, SessionID: req.Spec.SessionID, Detail: map[string]any{"target": target.Kind + "/" + target.Name}})
	r.event(ctx, req, corev1.EventTypeNormal, "Executed", fmt.Sprintf("%s/%s written after approval by %s", target.Kind, target.Name, req.Status.DecidedBy))
	return reconcile.Result{}, r.setStatus(ctx, req, func(s *focv1.ApprovalRequestStatus) {
		s.State, s.ExecutedAt, s.Target, s.Message = focv1.StateExecuted, &now, &target, "executed"
		if checks != nil {
			s.ExecutionChecks = checks
		}
	})
}

// DraftTaskObject is the FleetTask foc-approvals creates for an approved
// DraftTask request, held as a draft (internal/draft builds it). approvedBy is
// recorded in an annotation. The web UI shows this same object before approval.
func DraftTaskObject(req *focv1.ApprovalRequest, approvedBy string) (*swarmv1.FleetTask, error) {
	p := req.Spec.DraftTask
	t, err := draft.Task(req.Namespace, p)
	if err != nil {
		return nil, err
	}
	t.Labels = map[string]string{fleet.CreatedByLabel: "foc-approvals", fleet.RequestLabel: req.Name, fleet.SessionLabel: req.Spec.SessionID}
	t.Annotations = map[string]string{
		"fleetops.alexbahel.github.io/approved-by":   approvedBy,
		"fleetops.alexbahel.github.io/requested-for": req.Spec.RequestedFor,
		"fleetops.alexbahel.github.io/purpose":       p.Purpose,
	}
	return t, nil
}

// createDraft creates the approved FleetTask held as a draft. Swarmada
// generates no child action for it, and only a person holding activate can
// clear spec.draft (internal/webhook/fleettask_webhook.go in Swarmada).
func (r *Reconciler) createDraft(ctx context.Context, req *focv1.ApprovalRequest) (focv1.TargetRef, error) {
	t, err := DraftTaskObject(req, req.Status.DecidedBy)
	if err != nil {
		return focv1.TargetRef{}, err
	}
	ref := focv1.TargetRef{APIVersion: swarmv1.GroupVersion.String(), Kind: "FleetTask", Name: t.Name}
	if err := r.C.Create(ctx, t); err != nil {
		if apierrors.IsAlreadyExists(err) {
			var existing swarmv1.FleetTask
			if gerr := r.C.Get(ctx, client.ObjectKeyFromObject(t), &existing); gerr == nil && existing.Labels[fleet.RequestLabel] == req.Name {
				return ref, nil
			}
		}
		return ref, err
	}
	return ref, nil
}

// MaintenanceObject is the ZoneMaintenance foc-approvals creates for an
// approved ZoneMaintenance request. autoResumeAfterMinutes is always 0.
func MaintenanceObject(req *focv1.ApprovalRequest, approvedBy string) *swarmv1.ZoneMaintenance {
	p := req.Spec.ZoneMaintenance
	return &swarmv1.ZoneMaintenance{
		TypeMeta: metav1.TypeMeta{APIVersion: swarmv1.GroupVersion.String(), Kind: "ZoneMaintenance"},
		ObjectMeta: metav1.ObjectMeta{
			Name: MaintenanceName(req), Namespace: req.Namespace,
			Labels:      map[string]string{fleet.CreatedByLabel: "foc-approvals", fleet.RequestLabel: req.Name},
			Annotations: map[string]string{"fleetops.alexbahel.github.io/approved-by": approvedBy},
		},
		Spec: swarmv1.ZoneMaintenanceSpec{
			Scope:                  swarmv1.ZoneMaintenanceScope{Type: swarmv1.MaintenanceScopeType(p.ScopeType), ZoneName: p.ZoneName},
			Mode:                   swarmv1.ZoneMaintenanceMode(p.Mode),
			Reason:                 p.Reason,
			ScheduledStart:         p.ScheduledStart,
			AutoResumeAfterMinutes: 0,
		},
	}
}

func (r *Reconciler) createMaintenance(ctx context.Context, req *focv1.ApprovalRequest) (focv1.TargetRef, error) {
	zm := MaintenanceObject(req, req.Status.DecidedBy)
	ref := focv1.TargetRef{APIVersion: swarmv1.GroupVersion.String(), Kind: "ZoneMaintenance", Name: zm.Name}
	if err := r.C.Create(ctx, zm); err != nil {
		if apierrors.IsAlreadyExists(err) {
			var existing swarmv1.ZoneMaintenance
			if gerr := r.C.Get(ctx, client.ObjectKeyFromObject(zm), &existing); gerr == nil && existing.Labels[fleet.RequestLabel] == req.Name {
				return ref, nil
			}
		}
		return ref, err
	}
	return ref, nil
}

func (r *Reconciler) stopZone(ctx context.Context, req *focv1.ApprovalRequest) (focv1.TargetRef, error) {
	p := req.Spec.ZoneStop
	ref := focv1.TargetRef{APIVersion: swarmv1.GroupVersion.String(), Kind: "FleetZone", Name: p.ZoneName}
	var z swarmv1.FleetZone
	if err := r.C.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: p.ZoneName}, &z); err != nil {
		return ref, err
	}
	if _, present := z.Annotations[fleet.EstopAnnotation]; present {
		// Already stopped (by this request on a previous attempt, or by someone else).
		return ref, nil
	}
	patch, err := ZoneStopPatch(req)
	if err != nil {
		return ref, err
	}
	return ref, r.C.Patch(ctx, &z, client.RawPatch(types.MergePatchType, patch))
}

// ZoneStopPatch is the JSON merge patch foc-approvals sends to the FleetZone
// for an approved ZoneStop request: it sets only the
// swarmada.io/estop-triggered annotation. The executor sends nothing when
// the annotation is already present.
func ZoneStopPatch(req *focv1.ApprovalRequest) ([]byte, error) {
	return json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{fleet.EstopAnnotation: req.Spec.ZoneStop.Reason}}})
}

// ActivatedByAnnotation records who activated a draft. foc activate sets it
// in the activation patch, and the fleetops-activated-by admission policy
// admits it only with the authenticated user's name (D79).
const ActivatedByAnnotation = "fleetops.alexbahel.github.io/activated-by"

// ActivationPatch is the JSON patch foc activate sends with the person's own
// credentials: it fails unless the task is still a draft, clears spec.draft
// and records the activator. hasAnnotations says whether the task already
// has metadata.annotations (a JSON patch cannot add a key to a missing map).
func ActivationPatch(user string, hasAnnotations bool) ([]byte, error) {
	ops := []map[string]any{
		{"op": "test", "path": "/spec/draft", "value": true},
		{"op": "replace", "path": "/spec/draft", "value": false},
	}
	if hasAnnotations {
		ops = append(ops, map[string]any{"op": "add", "path": "/metadata/annotations/" + strings.ReplaceAll(ActivatedByAnnotation, "/", "~1"), "value": user})
	} else {
		ops = append(ops, map[string]any{"op": "add", "path": "/metadata/annotations", "value": map[string]string{ActivatedByAnnotation: user}})
	}
	return json.Marshal(ops)
}

func (r *Reconciler) setStatus(ctx context.Context, req *focv1.ApprovalRequest, mutate func(*focv1.ApprovalRequestStatus)) error {
	base := req.DeepCopy()
	mutate(&req.Status)
	return r.C.Status().Patch(ctx, req, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}
