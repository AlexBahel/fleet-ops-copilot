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

package approval

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// recheckDraft runs immediately before an approved draft is created (R65,
// D76): check_task's evaluation on fresh state, then Swarmada's own
// admission as dry runs, of the FleetTask exactly as it will be created and
// of each member as a FleetAction (the catalog check and the Pending cap run
// only there). It returns the record and, when the write must not happen,
// the refusal. A transient error (no answer from the API server) is
// returned as err, so the request is retried.
func (r *Reconciler) recheckDraft(ctx context.Context, req *fleetopsv1.ApprovalRequest, t *swarmv1.FleetTask) (*fleetopsv1.ChecksRecord, *policy.Decision, error) {
	now := r.now()
	rep := feasibility.Evaluate(feasibility.Proposal{Task: t, Creating: true}, feasibility.Load(ctx, r.C, req.Namespace, now))
	rec := &fleetopsv1.ChecksRecord{Verdict: rep.Verdict, CheckedAt: metav1.NewTime(now.UTC().Truncate(time.Second)), Checks: rep.Records()}
	if rep.Verdict == feasibility.Blocked {
		var why []string
		for _, b := range rep.Blockers {
			why = append(why, b.Code+": "+b.Reason)
		}
		d := policy.Decision{Effect: policy.Deny, Code: policy.CodeCheckFailed, Message: "conditions changed since the request was filed: " + strings.Join(why, "; ")}
		return rec, &d, nil
	}
	var refusals []string
	dry := func(name string, obj client.Object) error {
		err := r.C.Create(ctx, obj, client.DryRunAll)
		switch {
		case err == nil:
			rec.DryRuns = append(rec.DryRuns, fleetopsv1.CheckRecord{Name: name, Outcome: feasibility.Passed, Detail: "accepted by the API server (dry run; nothing stored)"})
			return nil
		case isRefusal(err):
			rec.DryRuns = append(rec.DryRuns, fleetopsv1.CheckRecord{Name: name, Outcome: feasibility.BlockedOut, Code: string(policy.CodeSwarmadaRefused), Detail: err.Error()})
			refusals = append(refusals, name+": "+err.Error())
			return nil
		default:
			return err
		}
	}
	if err := dry("FleetTask/"+t.Name, t.DeepCopy()); err != nil {
		return rec, nil, err
	}
	for _, m := range t.Spec.Actions {
		fa := &swarmv1.FleetAction{
			TypeMeta:   metav1.TypeMeta{APIVersion: swarmv1.GroupVersion.String(), Kind: "FleetAction"},
			ObjectMeta: metav1.ObjectMeta{Name: t.Name + "-" + m.Name, Namespace: t.Namespace},
			Spec:       *m.Action.DeepCopy(),
		}
		if err := dry("FleetAction/"+fa.Name, fa); err != nil {
			return rec, nil, err
		}
	}
	if len(refusals) > 0 {
		d := policy.Decision{Effect: policy.Deny, Code: policy.CodeSwarmadaRefused, Message: "Swarmada refused the dry run: " + strings.Join(refusals, "; ")}
		return rec, &d, nil
	}
	return rec, nil, nil
}

// isRefusal tells the API server's answer "no" (an admission or validation
// refusal) from not getting an answer.
func isRefusal(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) ||
		apierrors.IsAlreadyExists(err) || apierrors.IsConflict(err) || apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err)
}

// refuseExecution moves an approved request to Failed without a write.
func (r *Reconciler) refuseExecution(ctx context.Context, req *fleetopsv1.ApprovalRequest, rec *fleetopsv1.ChecksRecord, d policy.Decision) error {
	r.log(audit.Entry{Kind: audit.KindExecutionFailed, Actor: req.Status.DecidedBy, Namespace: req.Namespace, Object: "ApprovalRequest/" + req.Name,
		SessionID: req.Spec.SessionID, Code: string(d.Code), Message: d.Message})
	r.event(ctx, req, corev1.EventTypeWarning, "ExecutionRefused", d.Message)
	return r.setStatus(ctx, req, func(s *fleetopsv1.ApprovalRequestStatus) {
		s.State, s.ExecutionChecks = fleetopsv1.StateFailed, rec
		s.Message = "approved, but not executed: " + string(d.Code) + ": " + d.Message
	})
}

// TaskToRequest maps a FleetTask event to the DraftTask request that created
// it, so its outcome is recorded when it reaches a terminal phase (D78).
func TaskToRequest() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		name := o.GetLabels()[fleet.RequestLabel]
		if name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: name}}}
	})
}

func terminalTask(p swarmv1.FleetTaskPhase) bool {
	switch p {
	case swarmv1.FleetTaskPhaseSucceeded, swarmv1.FleetTaskPhaseFailed, swarmv1.FleetTaskPhaseCancelled, swarmv1.FleetTaskPhaseCompensated:
		return true
	}
	return false
}

// observeOutcome records a drafted task's terminal result once, and with
// PrepareFollowUp after a failure, a follow-up proposal that is never filed
// without a new preview and confirmation (D78).
func (r *Reconciler) observeOutcome(ctx context.Context, req *fleetopsv1.ApprovalRequest) error {
	var t swarmv1.FleetTask
	if err := r.C.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Status.Target.Name}, &t); err != nil {
		return client.IgnoreNotFound(err)
	}
	if t.Labels[fleet.RequestLabel] != req.Name || !terminalTask(t.Status.Phase) {
		return nil
	}
	out := &fleetopsv1.Outcome{Phase: string(t.Status.Phase), ObservedAt: metav1.NewTime(r.now().UTC().Truncate(time.Second))}
	for _, a := range t.Status.Actions {
		if a.Phase != swarmv1.ActionPhaseFailed {
			continue
		}
		out.FailedMember = a.Name
		if a.ActionRef != "" {
			var fa swarmv1.FleetAction
			if err := r.C.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: a.ActionRef}, &fa); err == nil {
				out.FailureReason = fa.Status.FailureReason
			}
		}
		break
	}
	var fu *fleetopsv1.FollowUp
	if t.Status.Phase == swarmv1.FleetTaskPhaseFailed && req.Spec.Preview != nil && req.Spec.Preview.FailureHandling == fleetopsv1.FailurePrepareFollowUp && req.Spec.DraftTask != nil {
		fu = &fleetopsv1.FollowUp{Kind: req.Spec.Preview.Kind, DraftTask: req.Spec.DraftTask.DeepCopy(),
			Summary: fmt.Sprintf("the same %s as %s, as a new draft; start it with fleetops chat --follow-up %s", req.Spec.Preview.Kind, req.Name, req.Name)}
	}
	msg := fmt.Sprintf("FleetTask/%s ended %s", t.Name, t.Status.Phase)
	if out.FailedMember != "" {
		msg += fmt.Sprintf(" (member %s: %s)", out.FailedMember, out.FailureReason)
	}
	if fu != nil {
		msg += "; a follow-up request is prepared for the requester to review"
	}
	r.log(audit.Entry{Kind: audit.KindTaskOutcome, Namespace: req.Namespace, Object: "ApprovalRequest/" + req.Name, SessionID: req.Spec.SessionID, Message: msg,
		Detail: map[string]any{"phase": out.Phase, "failedMember": out.FailedMember, "followUp": fu != nil}})
	typ := corev1.EventTypeNormal
	if t.Status.Phase != swarmv1.FleetTaskPhaseSucceeded {
		typ = corev1.EventTypeWarning
	}
	r.event(ctx, req, typ, "TaskOutcome", msg)
	return r.setStatus(ctx, req, func(s *fleetopsv1.ApprovalRequestStatus) { s.Outcome, s.FollowUp = out, fu })
}
