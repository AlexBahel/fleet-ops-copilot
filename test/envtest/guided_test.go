//go:build envtest

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

package envtest

import (
	"context"
	"strings"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/approval"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

func navigateIn(name, zone string) tools.DraftTaskInput {
	return tools.DraftTaskInput{Name: name, Purpose: "move", Actions: []tools.DraftActionInput{
		{Name: "go", Type: "Navigate", Zone: zone, RequiredCapabilities: []string{"navigation"}},
	}, Preview: &tools.PreviewInput{Kind: "move", ResultReported: true, CatalogueVersion: 1, CatalogueSHA256: "test"}}
}

// R65/D76: conditions change after approval (the only eligible robot is
// gone), so the executor refuses with the reason and writes nothing.
func TestExecutorRefusesWhenConditionsChange(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	ensureZone(t, "recheck-zone")
	r := ensureRobot(t, "recheck-robot", "recheck-zone", 90)
	res, err := h.draft(ctx, sess, navigateIn("recheck-move", "recheck-zone"))
	if err != nil {
		t.Fatal(err)
	}
	name := reqName(t, res)
	filed := waitState(t, name, focv1.StatePending)
	if filed.Spec.Checks == nil || filed.Spec.Checks.Verdict != "likely feasible" || filed.Spec.Preview == nil || filed.Spec.Preview.ID == "" {
		t.Fatalf("filed spec %+v", filed.Spec)
	}
	if err := admin.Delete(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, focv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	got := waitState(t, name, focv1.StateFailed)
	// The only robot that could do it is gone: no eligible robot, or (when no
	// robot is left that declares the capability) no robot with it.
	if !strings.Contains(got.Status.Message, string(policy.CodeCheckFailed)) ||
		(!strings.Contains(got.Status.Message, "NO_ELIGIBLE_ROBOT") && !strings.Contains(got.Status.Message, "NO_ROBOT_WITH_CAPABILITY")) ||
		got.Status.ExecutionChecks == nil || got.Status.ExecutionChecks.Verdict != "blocked" {
		t.Fatalf("status %+v", got.Status)
	}
	var task swarmv1.FleetTask
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "recheck-move"}, &task); err == nil {
		t.Fatal("FleetTask created although the re-check failed")
	}
}

// The happy path records the re-check and Swarmada's dry runs (envtest
// serves none of Swarmada's webhooks: the dry runs reach the CRD schema
// and the copilot's admission policies only).
func TestExecutorRecordsDryRuns(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	ensureZone(t, "dryrun-zone")
	ensureRobot(t, "dryrun-robot", "dryrun-zone", 90)
	res, err := h.draft(ctx, sess, navigateIn("dryrun-move", "dryrun-zone"))
	if err != nil {
		t.Fatal(err)
	}
	name := reqName(t, res)
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, focv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	got := waitState(t, name, focv1.StateExecuted)
	ec := got.Status.ExecutionChecks
	if ec == nil || ec.Verdict != "likely feasible" || len(ec.DryRuns) != 2 {
		t.Fatalf("execution checks %+v", ec)
	}
	for _, d := range ec.DryRuns {
		if d.Outcome != "Passed" {
			t.Fatalf("dry run %+v", d)
		}
	}
	// Nothing was stored by the dry runs: no FleetAction exists.
	var fas swarmv1.FleetActionList
	if err := admin.List(ctx, &fas, client.InNamespace(swarmNS)); err != nil {
		t.Fatal(err)
	}
	for _, fa := range fas.Items {
		if strings.HasPrefix(fa.Name, "dryrun-move") {
			t.Fatalf("dry run stored %s", fa.Name)
		}
	}
}

// R69: a FleetAction create by foc-approvals that is not a dry run is
// refused by the admission policy; a dry run is admitted.
func TestFocApprovalsFleetActionDryRunOnly(t *testing.T) {
	ctx := context.Background()
	c := asSA(t, approvUser)
	fa := func() *swarmv1.FleetAction {
		return &swarmv1.FleetAction{ObjectMeta: metav1.ObjectMeta{Name: "dryrun-only-probe", Namespace: swarmNS},
			Spec: swarmv1.FleetActionSpec{Type: swarmv1.ActionTypeNavigate, Zone: swarmNS}}
	}
	err := c.Create(ctx, fa())
	if err == nil || !strings.Contains(err.Error(), "only as a dry run") {
		t.Fatalf("non-dry-run create: %v", err)
	}
	if err := c.Create(ctx, fa(), client.DryRunAll); err != nil {
		t.Fatalf("dry run refused: %v", err)
	}
	var got swarmv1.FleetAction
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "dryrun-only-probe"}, &got); err == nil {
		t.Fatal("dry run stored the object")
	}
}

// R69 and D25: foc-mcp has no create on any Swarmada resource.
func TestFocMcpHasNoCreateOnSwarmada(t *testing.T) {
	ctx := context.Background()
	lists, err := env.Discovery().ServerResourcesForGroupVersion("swarmada.io/v1")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range lists.APIResources {
		if strings.Contains(r.Name, "/") {
			continue
		}
		for _, verb := range []string{"create", "update", "patch", "delete"} {
			sar := &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{User: mcpUser, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:foc-system"},
				ResourceAttributes: &authzv1.ResourceAttributes{Namespace: swarmNS, Group: "swarmada.io", Resource: r.Name, Verb: verb}}}
			if err := admin.Create(ctx, sar); err != nil {
				t.Fatal(err)
			}
			if sar.Status.Allowed {
				t.Errorf("foc-mcp may %s %s", verb, r.Name)
			}
			n++
		}
	}
	if n < 40 {
		t.Fatalf("only %d checks: discovery returned too few Swarmada resources", n)
	}
}

// D79: the activation patch foc activate sends records the activator, the
// admission policy refuses an annotation naming someone else, and
// foc-approvals records the activator.
func TestActivatedByIsTheAuthenticatedUser(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	ensureZone(t, "act-zone")
	ensureRobot(t, "act-robot", "act-zone", 90)
	res, err := h.draft(ctx, sess, navigateIn("act-move", "act-zone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, reqName(t, res), focv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, reqName(t, res), focv1.StateExecuted)
	act, err := h.activate(ctx, sess, tools.ActivateTaskInput{TaskName: "act-move"})
	if err != nil {
		t.Fatal(err)
	}
	actName := reqName(t, act)
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, actName, focv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, actName, focv1.StateApproved)
	var task swarmv1.FleetTask
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "act-move"}, &task); err != nil {
		t.Fatal(err)
	}
	forged, _ := approval.ActivationPatch("eve", len(task.Annotations) > 0)
	if err := asPerson(t, "dana").Patch(ctx, &task, client.RawPatch(types.JSONPatchType, forged)); err == nil || !strings.Contains(err.Error(), "must name the user who sets it") {
		t.Fatalf("forged activator: %v", err)
	}
	patch, _ := approval.ActivationPatch("dana", len(task.Annotations) > 0)
	if err := asPerson(t, "dana").Patch(ctx, &task, client.RawPatch(types.JSONPatchType, patch)); err != nil {
		t.Fatalf("dana's activation: %v", err)
	}
	got := waitState(t, actName, focv1.StateExecuted)
	if got.Status.ActivatedBy != "dana" {
		t.Fatalf("activatedBy %q", got.Status.ActivatedBy)
	}
	// The JSON patch fails once the task is no longer a draft.
	if err := admin.Get(ctx, client.ObjectKeyFromObject(&task), &task); err != nil {
		t.Fatal(err)
	}
	if err := asPerson(t, "dana").Patch(ctx, &task, client.RawPatch(types.JSONPatchType, patch)); err == nil {
		t.Fatal("second activation patch accepted")
	}
}

// D78: after a failure, a PrepareFollowUp request records the outcome and
// a follow-up proposal; nothing is filed.
func TestOutcomeAndFollowUp(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	ensureZone(t, "fu-zone")
	ensureRobot(t, "fu-robot", "fu-zone", 90)
	in := navigateIn("fu-move", "fu-zone")
	in.Preview.FailureHandling = "PrepareFollowUp"
	res, err := h.draft(ctx, sess, in)
	if err != nil {
		t.Fatal(err)
	}
	name := reqName(t, res)
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, focv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, name, focv1.StateExecuted)
	var task swarmv1.FleetTask
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: "fu-move"}, &task); err != nil {
		t.Fatal(err)
	}
	task.Status.Phase = swarmv1.FleetTaskPhaseFailed
	task.Status.Actions = []swarmv1.FleetTaskActionStatus{{Name: "go", Phase: swarmv1.ActionPhaseFailed}}
	if err := admin.Status().Update(ctx, &task); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var r focv1.ApprovalRequest
	for {
		if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: name}, &r); err != nil {
			t.Fatal(err)
		}
		if r.Status.Outcome != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if r.Status.Outcome == nil || r.Status.Outcome.Phase != "Failed" || r.Status.Outcome.FailedMember != "go" || r.Status.FollowUp == nil || r.Status.FollowUp.Kind != "move" {
		t.Fatalf("status %+v", r.Status)
	}
	var list focv1.ApprovalRequestList
	if err := admin.List(ctx, &list, client.InNamespace(swarmNS)); err != nil {
		t.Fatal(err)
	}
	for _, x := range list.Items {
		if x.Spec.DraftTask != nil && strings.HasPrefix(x.Spec.DraftTask.TaskName, "fu-move-") {
			t.Fatalf("follow-up filed automatically: %s", x.Name)
		}
	}
}
