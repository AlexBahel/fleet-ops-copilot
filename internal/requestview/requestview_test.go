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

package requestview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/approval"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

var t0 = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

func req(name string, created time.Time, spec focv1.ApprovalRequestSpec) *focv1.ApprovalRequest {
	spec.RequestedFor, spec.RequestedBy, spec.SessionID = "sam", "system:serviceaccount:foc-system:foc-mcp", "web-1"
	spec.ExpiresAt = metav1.NewTime(created.Add(30 * time.Minute))
	spec.Policy = focv1.PolicyRecord{Effect: "RequireApproval", Code: "REQUIRE_APPROVAL"}
	return &focv1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "warehouse-a", CreationTimestamp: metav1.NewTime(created)}, Spec: spec}
}

func draftReq() *focv1.ApprovalRequest {
	return req("areq-draft", t0, focv1.ApprovalRequestSpec{Type: focv1.RequestDraftTask, DraftTask: &focv1.DraftTaskParams{
		TaskName: "stage-pallet-a", Purpose: "stage pallets",
		Actions: []focv1.DraftAction{{Name: "go", Type: "Navigate", Zone: "warehouse-a", Priority: "High", RequiredCapabilities: []string{"navigation"}}},
	}})
}

// The body shown for a draft is the executor's object: parsed back, it has
// the same metadata and spec that foc-approvals creates.
func TestDraftViewIsTheExecutorObject(t *testing.T) {
	r := draftReq()
	v := Of(r)
	if v.State != "Pending" || v.Approve != "foc approve areq-draft -n warehouse-a" || v.Reject != "foc reject areq-draft -n warehouse-a" {
		t.Fatalf("view %+v", v)
	}
	c := v.Change
	if c.Operation != "create" || c.Target != "FleetTask warehouse-a/stage-pallet-a" || !strings.HasPrefix(c.Writer, "foc-approvals") {
		t.Fatalf("change %+v", c)
	}
	var got swarmv1.FleetTask
	if err := yaml.UnmarshalStrict([]byte(c.Body), &got); err != nil {
		t.Fatalf("body is not a FleetTask: %v\n%s", err, c.Body)
	}
	want, err := approval.DraftTaskObject(r, ApproverPlaceholder)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Spec, want.Spec) || !reflect.DeepEqual(got.ObjectMeta, want.ObjectMeta) || got.TypeMeta != want.TypeMeta {
		t.Fatalf("body differs from executor object:\n%s", c.Body)
	}
	if !got.Spec.Draft || strings.Contains(c.Body, "status") || strings.Contains(c.Body, "creationTimestamp") {
		t.Fatalf("body %s", c.Body)
	}
	if len(c.Diff) != strings.Count(c.Body, "\n") {
		t.Fatalf("%d diff lines for %d body lines", len(c.Diff), strings.Count(c.Body, "\n"))
	}
	for _, l := range c.Diff {
		if l.Op != "+" {
			t.Fatalf("create diff line %+v", l)
		}
	}
}

func TestDecidedDraftShowsApproverAndNoCommands(t *testing.T) {
	r := draftReq()
	r.Status = focv1.ApprovalRequestStatus{State: focv1.StateExecuted, DecidedBy: "pat"}
	v := Of(r)
	if v.Approve != "" || v.Reject != "" || v.State != "Executed" {
		t.Fatalf("view %+v", v)
	}
	if !strings.Contains(v.Change.Body, "approved-by: pat") || strings.Contains(v.Change.Body, ApproverPlaceholder) {
		t.Fatalf("body %s", v.Change.Body)
	}
}

// A request whose requester was typed into the web UI offers no approve
// command, only reject, and says why (R38).
func TestUnverifiedRequesterHasNoApproveCommand(t *testing.T) {
	r := draftReq()
	r.Spec.RequestedFor = "unverified:sam"
	v := Of(r)
	if v.Approve != "" || v.Reject != "foc reject areq-draft -n warehouse-a" || !strings.Contains(v.Unapprovable, "DENY_REQUESTER_UNVERIFIED") {
		t.Fatalf("view %+v", v)
	}
	r.Status.State = focv1.StateRejected
	if v := Of(r); v.Unapprovable != "" || v.Reject != "" {
		t.Fatalf("decided view %+v", v)
	}
	a := req("areq-act", t0, focv1.ApprovalRequestSpec{Type: focv1.RequestActivateTask, ActivateTask: &focv1.ActivateTaskParams{TaskName: "t"}})
	a.Spec.RequestedFor = "unverified:sam"
	if v := Of(a); v.Approve != "" || v.Unapprovable == "" {
		t.Fatalf("activate view %+v", v)
	}
}

func TestMaintenanceView(t *testing.T) {
	r := req("areq-zm", t0, focv1.ApprovalRequestSpec{Type: focv1.RequestZoneMaintenance, ZoneMaintenance: &focv1.ZoneMaintenanceParams{ScopeType: "Zone", ZoneName: "aisle-b", Mode: "Graceful", Reason: "inspection"}})
	c := Of(r).Change
	var got swarmv1.ZoneMaintenance
	if err := yaml.UnmarshalStrict([]byte(c.Body), &got); err != nil {
		t.Fatal(err)
	}
	want := approval.MaintenanceObject(r, ApproverPlaceholder)
	if c.Target != "ZoneMaintenance warehouse-a/foc-areq-zm" || !reflect.DeepEqual(got.Spec, want.Spec) || got.Spec.AutoResumeAfterMinutes != 0 {
		t.Fatalf("change %+v", c)
	}
}

// A zone stop shows the exact merge patch and the one annotation it adds,
// with the reason quoted as YAML would.
func TestZoneStopView(t *testing.T) {
	r := req("areq-stop", t0, focv1.ApprovalRequestSpec{Type: focv1.RequestZoneStop, ZoneStop: &focv1.ZoneStopParams{ZoneName: "aisle-b", Reason: "spill: aisle B"}})
	c := Of(r).Change
	want, _ := approval.ZoneStopPatch(r)
	if c.Body != string(want) || c.Target != "FleetZone warehouse-a/aisle-b" || !strings.HasPrefix(c.Operation, "JSON merge patch") {
		t.Fatalf("change %+v", c)
	}
	added := 0
	for _, l := range c.Diff {
		if l.Op == "+" {
			added++
			if l.Text != "    "+fleet.EstopAnnotation+": 'spill: aisle B'" {
				t.Fatalf("added line %q", l.Text)
			}
		}
		if l.Op == "-" {
			t.Fatalf("zone stop removes %q", l.Text)
		}
	}
	if added != 1 {
		t.Fatalf("%d added lines", added)
	}
}

func TestActivateView(t *testing.T) {
	r := req("areq-act", t0, focv1.ApprovalRequestSpec{Type: focv1.RequestActivateTask, ActivateTask: &focv1.ActivateTaskParams{TaskName: "stage-pallet-a"}})
	c := Of(r).Change
	if c.Command != "foc activate areq-act -n warehouse-a" || c.Body != approval.ActivationPatch || !strings.HasPrefix(c.Writer, "a person Swarmada allows") {
		t.Fatalf("change %+v", c)
	}
	if !strings.Contains(c.Notes[0], "ApprovalDecision of type Activation") || !strings.Contains(c.Notes[0], "Nothing is added to the FleetTask") {
		t.Fatalf("notes %q", c.Notes)
	}
	want := []Line{{" ", "spec:"}, {"-", "  draft: true"}, {"+", "  draft: false"}}
	if !reflect.DeepEqual(c.Diff, want) {
		t.Fatalf("diff %+v", c.Diff)
	}
}

func TestUnknownOrEmptyTypeHasNoChange(t *testing.T) {
	r := req("areq-x", t0, focv1.ApprovalRequestSpec{Type: focv1.RequestZoneStop})
	if c := Of(r).Change; c.Body != "" || len(c.Diff) != 0 || len(c.Notes) != 1 {
		t.Fatalf("change %+v", c)
	}
}

func TestListOrdersPendingFirstAcrossNamespaces(t *testing.T) {
	old := draftReq()
	old.Name, old.CreationTimestamp = "areq-old", metav1.NewTime(t0.Add(-time.Hour))
	newer := draftReq()
	newer.Name, newer.CreationTimestamp = "areq-new", metav1.NewTime(t0)
	done := draftReq()
	done.Name, done.CreationTimestamp = "areq-done", metav1.NewTime(t0.Add(time.Hour))
	done.Status.State = focv1.StateRejected
	other := draftReq()
	other.Name, other.Namespace = "areq-other", "warehouse-b"
	other.CreationTimestamp = metav1.NewTime(t0.Add(-2 * time.Hour))
	c := fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithObjects(old, newer, done, other).Build()
	vs, err := List(context.Background(), c, []string{"warehouse-a", "warehouse-b"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range vs {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != "areq-new,areq-old,areq-other,areq-done" {
		t.Fatalf("order %v", names)
	}
	if only, _ := List(context.Background(), c, []string{"warehouse-b"}); len(only) != 1 {
		t.Fatalf("namespace filter: %d", len(only))
	}
}

func TestListReportsReadErrors(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("forbidden")
		},
	}).Build()
	if _, err := List(context.Background(), c, []string{"warehouse-a"}); err == nil || !strings.Contains(err.Error(), "warehouse-a") {
		t.Fatalf("err %v", err)
	}
}
