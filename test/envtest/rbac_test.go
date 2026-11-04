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
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

func sar(t *testing.T, user string, groups []string, ns, group, resource, sub, verb string) bool {
	t.Helper()
	r := &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{User: user, Groups: groups, ResourceAttributes: &authzv1.ResourceAttributes{
		Namespace: ns, Group: group, Resource: resource, Subresource: sub, Verb: verb,
	}}}
	if err := admin.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r.Status.Allowed
}

// TestRBACMatrix checks every copilot ServiceAccount against the permissions
// docs/DESIGN.md §3.6 grants and the ones it must never hold.
func TestRBACMatrix(t *testing.T) {
	type row struct {
		user, group, resource, sub, verb string
		want                             bool
	}
	sw, fo, core := "swarmada.io", "fleetops.alexbahel.github.io", ""
	rows := []row{
		// foc-agent holds nothing.
		{agentUser, sw, "robots", "", "list", false},
		{agentUser, fo, "approvalrequests", "", "create", false},
		{agentUser, sw, "fleettasks", "", "create", false},

		// foc-mcp: reads, draft create, request create.
		{mcpUser, sw, "robots", "", "list", true},
		{mcpUser, sw, "fleettasks", "", "watch", true},
		{mcpUser, sw, "fleetzones", "", "get", true},
		{mcpUser, sw, "zonemaintenances", "", "list", true},
		{mcpUser, sw, "tasktriggers", "", "list", true},
		{mcpUser, sw, "fleettasks", "", "create", true},
		{mcpUser, fo, "approvalrequests", "", "create", true},
		{mcpUser, core, "events", "", "list", true},
		// foc-mcp never decides, edits, deletes, activates or stops.
		{mcpUser, fo, "approvalrequests", "status", "update", false},
		{mcpUser, fo, "approvalrequests", "status", "patch", false},
		{mcpUser, fo, "approvaldecisions", "", "create", false},
		{mcpUser, fo, "approvalrequests", "", "update", false},
		{mcpUser, fo, "approvalrequests", "", "delete", false},
		{mcpUser, sw, "fleettasks", "", "update", false},
		{mcpUser, sw, "fleettasks", "", "patch", false},
		{mcpUser, sw, "fleettasks", "", "delete", false},
		{mcpUser, sw, "fleettasks", "", "activate", false},
		{mcpUser, sw, "fleettasks", "", "append", false},
		{mcpUser, sw, "fleettasks", "", "cancel", false},
		{mcpUser, sw, "fleetzones", "", "patch", false},
		{mcpUser, sw, "fleetzones", "", "estop-trigger", false},
		{mcpUser, sw, "zonemaintenances", "", "create", false},
		{mcpUser, sw, "robots", "", "patch", false},

		// foc-approvals: the two approved writes and status of requests.
		{approvUser, sw, "zonemaintenances", "", "create", true},
		{approvUser, sw, "fleetzones", "", "patch", true},
		{approvUser, sw, "fleetzones", "", "estop-trigger", true},
		{approvUser, fo, "approvalrequests", "status", "patch", true},
		{approvUser, fo, "approvaldecisions", "", "watch", true},
		// foc-approvals never clears, releases, deletes, activates or edits work.
		{approvUser, sw, "fleetzones", "", "estop-clear", false},
		{approvUser, sw, "robots", "", "estop-clear", false},
		{approvUser, sw, "swarmadaconfigs", "", "estop-clear", false},
		{approvUser, sw, "zonemaintenances", "", "delete", false},
		{approvUser, sw, "zonemaintenances", "", "patch", false},
		{approvUser, sw, "zonemaintenances", "", "update", false},
		{approvUser, sw, "fleetzones", "", "delete", false},
		{approvUser, sw, "fleetzones", "", "update", false},
		{approvUser, sw, "fleettasks", "", "create", false},
		{approvUser, sw, "fleettasks", "", "patch", false},
		{approvUser, sw, "fleettasks", "", "activate", false},
		{approvUser, sw, "fleettasks", "", "cancel", false},
		{approvUser, sw, "fleetactions", "", "cancel", false},
		{approvUser, sw, "fleetactions", "", "patch", false},
		{approvUser, sw, "robots", "", "patch", false},
		{approvUser, sw, "discoveredrobots", "", "admit", false},
		{approvUser, sw, "firmwarerollouts", "", "rollout-resume", false},
		{approvUser, sw, "modelpolicies", "", "policy-reset", false},
		{approvUser, sw, "tasktriggers", "", "trigger-response-approve", false},
		{approvUser, fo, "approvaldecisions", "", "create", false},
		{approvUser, fo, "approvalrequests", "", "create", false},
	}
	for _, r := range rows {
		name := fmt.Sprintf("%s %s %s/%s %s", strings.TrimPrefix(r.user, "system:serviceaccount:foc-system:"), r.verb, r.resource, r.sub, r.group)
		t.Run(name, func(t *testing.T) {
			if got := sar(t, r.user, saGroups(), swarmNS, r.group, r.resource, r.sub, r.verb); got != r.want {
				t.Fatalf("allowed=%v, want %v", got, r.want)
			}
		})
	}
}

func draftTask(name string, draft bool, label bool) *swarmv1.FleetTask {
	t := &swarmv1.FleetTask{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: swarmNS},
		Spec:       swarmv1.FleetTaskSpec{Draft: draft, Actions: []swarmv1.FleetTaskAction{{Name: "go", Action: swarmv1.FleetActionSpec{Type: swarmv1.ActionTypeNavigate, Zone: swarmNS}}}},
	}
	if label {
		t.Labels = map[string]string{fleet.CreatedByLabel: "foc-mcp"}
	}
	return t
}

func ensureZone(t *testing.T, name string) *swarmv1.FleetZone {
	t.Helper()
	z := &swarmv1.FleetZone{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: swarmNS, Annotations: map[string]string{"site/owner": "east-wing"}, Labels: map[string]string{"site": "a"}},
		Spec: swarmv1.FleetZoneSpec{DisplayName: "Receiving and staging area — east wing", Waypoints: []swarmv1.Waypoint{{Name: "staging-shelf-a", X: 12.1, Y: 8.1}}}}
	if err := admin.Create(context.Background(), z); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	return z
}

func mergePatch(t *testing.T, c client.Client, obj client.Object, body map[string]any) error {
	t.Helper()
	b, _ := json.Marshal(body)
	return c.Patch(context.Background(), obj, client.RawPatch(types.MergePatchType, b))
}

// TestAdmissionDraftsOnly: foc-mcp can create a labelled draft and nothing else.
func TestAdmissionDraftsOnly(t *testing.T) {
	ctx := context.Background()
	mcp := asSA(t, mcpUser)
	if err := mcp.Create(ctx, draftTask("vap-draft-ok", true, true)); err != nil {
		t.Fatalf("labelled draft refused: %v", err)
	}
	for name, task := range map[string]*swarmv1.FleetTask{
		"non-draft": draftTask("vap-nondraft", false, true),
		"unlabelled": draftTask("vap-unlabelled", true, false),
	} {
		err := mcp.Create(ctx, task)
		if err == nil || !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
			t.Errorf("%s: want admission refusal, got %v", name, err)
		}
	}
	// No update path either: RBAC refuses before admission.
	err := mergePatch(t, mcp, draftTask("vap-draft-ok", true, true), map[string]any{"spec": map[string]any{"draft": false}})
	if !apierrors.IsForbidden(err) {
		t.Errorf("foc-mcp activation: want Forbidden, got %v", err)
	}
	if err := mcp.Delete(ctx, draftTask("vap-draft-ok", true, true)); !apierrors.IsForbidden(err) {
		t.Errorf("foc-mcp delete: want Forbidden, got %v", err)
	}
}

// TestAdmissionZoneStopOnly: foc-approvals can add the estop annotation and
// make no other change to a FleetZone.
func TestAdmissionZoneStopOnly(t *testing.T) {
	ctx := context.Background()
	z := ensureZone(t, "vap-zone")
	ap := asSA(t, approvUser)
	refused := func(name string, body map[string]any) {
		t.Helper()
		if err := mergePatch(t, ap, z, body); err == nil {
			t.Errorf("%s: patch accepted", name)
		} else if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
	refused("spec change", map[string]any{"spec": map[string]any{"maxConcurrentRobots": 1}})
	refused("label change", map[string]any{"metadata": map[string]any{"labels": map[string]any{"site": "b"}}})
	refused("other annotation", map[string]any{"metadata": map[string]any{"annotations": map[string]any{"site/owner": "x"}}})
	refused("annotation plus other change", map[string]any{"metadata": map[string]any{"annotations": map[string]any{fleet.EstopAnnotation: "drill", "site/owner": "x"}}})
	if err := mergePatch(t, ap, z, map[string]any{"metadata": map[string]any{"annotations": map[string]any{fleet.EstopAnnotation: "spill reported"}}}); err != nil {
		t.Fatalf("adding the estop annotation refused: %v", err)
	}
	refused("change the stop reason", map[string]any{"metadata": map[string]any{"annotations": map[string]any{fleet.EstopAnnotation: "other"}}})
	refused("clear the stop", map[string]any{"metadata": map[string]any{"annotations": map[string]any{fleet.EstopAnnotation: nil}}})
	if err := ap.Delete(ctx, z); !apierrors.IsForbidden(err) {
		t.Errorf("delete: want Forbidden, got %v", err)
	}
	var got swarmv1.FleetZone
	if err := admin.Get(ctx, client.ObjectKeyFromObject(z), &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[fleet.EstopAnnotation] != "spill reported" || got.Annotations["site/owner"] != "east-wing" || got.Spec.MaxConcurrentRobots != 0 {
		t.Fatalf("zone changed beyond the annotation: %+v %+v", got.Annotations, got.Spec)
	}
}

func decision(req, by string, groups []string) *focv1.ApprovalDecision {
	return &focv1.ApprovalDecision{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "dec-", Namespace: swarmNS},
		Spec:       focv1.ApprovalDecisionSpec{RequestRef: req, Verdict: focv1.VerdictApprove, DecidedBy: by, DecidedByGroups: groups},
	}
}

// TestAdmissionDecisions: a decision names its creator, is made by a person,
// and cannot be changed.
func TestAdmissionDecisions(t *testing.T) {
	ctx := context.Background()
	dana := asPerson(t, "dana")
	danaGroups := []string{"fleet-ops", "system:authenticated"}
	if err := dana.Create(ctx, decision("any", "eve", danaGroups)); err == nil {
		t.Error("forged decidedBy accepted")
	}
	if err := dana.Create(ctx, decision("any", "dana", []string{"system:masters", "system:authenticated"})); err == nil {
		t.Error("forged decidedByGroups accepted")
	}
	ok := decision("any", "dana", danaGroups)
	if err := dana.Create(ctx, ok); err != nil {
		t.Fatalf("honest decision refused: %v", err)
	}
	ok.Spec.Verdict = focv1.VerdictReject
	if err := admin.Update(ctx, ok); err == nil {
		t.Error("decision update accepted (even admin must not change a decision)")
	}
	// A copilot ServiceAccount cannot create a decision.
	c := asSA(t, approvUser)
	if err := c.Create(ctx, decision("any", approvUser, saGroups())); err == nil {
		t.Error("ServiceAccount decision accepted")
	}
	// Admin (system:masters) with RBAC bypass: the admission policy still
	// refuses a ServiceAccount identity. Impersonate a SA with admin-level groups.
	cfg := saConfig(approvUser)
	cfg.Impersonate.Groups = append(cfg.Impersonate.Groups, "system:masters")
	privileged, err := client.New(cfg, client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		t.Fatal(err)
	}
	err = privileged.Create(ctx, decision("any", approvUser, cfg.Impersonate.Groups))
	if err == nil || !strings.Contains(err.Error(), "ServiceAccount") {
		t.Errorf("privileged ServiceAccount decision: want admission refusal, got %v", err)
	}
}

// TestAdmissionRequests: a request must name the identity that creates it.
func TestAdmissionRequests(t *testing.T) {
	ctx := context.Background()
	mk := func(by string) *focv1.ApprovalRequest {
		return &focv1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{GenerateName: "areq-", Namespace: swarmNS}, Spec: focv1.ApprovalRequestSpec{
			Type: focv1.RequestZoneStop, RequestedFor: "sam", RequestedBy: by, ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)),
			Policy: focv1.PolicyRecord{Effect: "RequireApproval", Code: "REQUIRE_APPROVAL"}, ZoneStop: &focv1.ZoneStopParams{ZoneName: "z", Reason: "r"},
		}}
	}
	if err := admin.Create(ctx, mk(mcpUser)); err == nil {
		t.Error("request naming another creator accepted")
	}
	if err := asSA(t, mcpUser).Create(ctx, mk(mcpUser)); err != nil {
		t.Fatalf("foc-mcp request refused: %v", err)
	}
	// Spec is immutable (CRD validation rule).
	var list focv1.ApprovalRequestList
	if err := admin.List(ctx, &list, client.InNamespace(swarmNS)); err != nil || len(list.Items) == 0 {
		t.Fatalf("list: %v", err)
	}
	r := list.Items[0]
	r.Spec.RequestedFor = "dana"
	if err := admin.Update(ctx, &r); err == nil {
		t.Error("request spec update accepted")
	}
}
