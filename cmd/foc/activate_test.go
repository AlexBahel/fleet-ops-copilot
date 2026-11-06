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

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

func activationWorld(state focv1.RequestState, withRobot bool) client.Client {
	req := &focv1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Name: "areq-act", Namespace: "warehouse-a"},
		Spec:   focv1.ApprovalRequestSpec{Type: focv1.RequestActivateTask, ActivateTask: &focv1.ActivateTaskParams{TaskName: "move-1"}},
		Status: focv1.ApprovalRequestStatus{State: state}}
	task := &swarmv1.FleetTask{ObjectMeta: metav1.ObjectMeta{Name: "move-1", Namespace: "warehouse-a"},
		Spec: swarmv1.FleetTaskSpec{Draft: true, Actions: []swarmv1.FleetTaskAction{{Name: "go", Action: swarmv1.FleetActionSpec{Type: "Navigate", Zone: "warehouse-a", RequiredCapabilities: []string{"navigation"}}}}}}
	zone := &swarmv1.FleetZone{ObjectMeta: metav1.ObjectMeta{Name: "warehouse-a", Namespace: "warehouse-a"}}
	objs := []client.Object{req, task, zone}
	if withRobot {
		ad := &swarmv1.FleetAdapter{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "warehouse-a"}}
		ad.Status.Phase, ad.Status.Conformance = swarmv1.FleetAdapterPhaseConnected, swarmv1.ConformanceStatePassed
		r := &swarmv1.Robot{ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "warehouse-a"}}
		r.Spec.Adapter.Name = "a"
		r.Status.Phase, r.Status.CurrentZone, r.Status.EstopState = "Idle", "warehouse-a", "Normal"
		r.Status.Capabilities = []swarmv1.CapabilityStatusEntry{{Name: "navigation", Status: "Active"}}
		objs = append(objs, ad, r)
	}
	return fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithObjects(objs...).WithStatusSubresource(req).Build()
}

func who(user string, groups ...string) func(context.Context) (string, []string, error) {
	return func(context.Context) (string, []string, error) { return user, groups, nil }
}

// D79: foc activate checks first, records the activation as an
// ApprovalDecision of type Activation, then sends the plain spec.draft=false
// patch; the FleetTask gains no annotation.
func TestActivate(t *testing.T) {
	ctx := context.Background()
	c := activationWorld(focv1.StateApproved, true)
	var out bytes.Buffer
	if err := activate(ctx, activator{C: c, Identity: who("dana"), Host: "https://127.0.0.1:6443"}, "warehouse-a", "areq-act", "", &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	var task swarmv1.FleetTask
	if err := c.Get(ctx, client.ObjectKey{Namespace: "warehouse-a", Name: "move-1"}, &task); err != nil {
		t.Fatal(err)
	}
	if task.Spec.Draft || len(task.Annotations) != 0 {
		t.Fatalf("task draft=%v annotations=%v: only spec.draft may change", task.Spec.Draft, task.Annotations)
	}
	var ds focv1.ApprovalDecisionList
	if err := c.List(ctx, &ds); err != nil {
		t.Fatal(err)
	}
	if len(ds.Items) != 1 || !ds.Items[0].Spec.IsActivation() || ds.Items[0].Spec.DecidedBy != "dana" || ds.Items[0].Spec.RequestRef != "areq-act" || ds.Items[0].Spec.Verdict != "" {
		t.Fatalf("activation record %+v", ds.Items)
	}
	for _, want := range []string{"API server: https://127.0.0.1:6443", "checks: likely feasible", "Swarmada's own acceptance is checked at execution", "recorded ApprovalDecision areq-act-activation-", "activated FleetTask move-1 as dana"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// Once activated, it is not a draft any more.
	if err := activate(ctx, activator{C: c, Identity: who("dana")}, "warehouse-a", "areq-act", "", &out); err == nil || !strings.Contains(err.Error(), "not a draft") {
		t.Fatalf("second activation: %v", err)
	}
}

func TestActivateRefuses(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		c    client.Client
		id   func(context.Context) (string, []string, error)
		want string
	}{
		"service account": {activationWorld(focv1.StateApproved, true), who("system:serviceaccount:foc-system:foc-approvals", "system:serviceaccounts"), "ServiceAccount"},
		"not approved":    {activationWorld(focv1.StatePending, true), who("dana"), "not Approved"},
		"blocked check":   {activationWorld(focv1.StateApproved, false), who("dana"), "a check is blocked"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := activate(ctx, activator{C: tc.c, Identity: tc.id}, "warehouse-a", "areq-act", "", &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q\n%s", err, tc.want, out.String())
			}
			var task swarmv1.FleetTask
			_ = tc.c.Get(ctx, client.ObjectKey{Namespace: "warehouse-a", Name: "move-1"}, &task)
			if !task.Spec.Draft {
				t.Fatal("activated anyway")
			}
			var ds focv1.ApprovalDecisionList
			if err := tc.c.List(ctx, &ds); err != nil || len(ds.Items) != 0 {
				t.Fatalf("refused, yet recorded %v (%v)", ds.Items, err)
			}
		})
	}
}
