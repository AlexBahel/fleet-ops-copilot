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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

func request(spec fleetopsv1.ApprovalRequestSpec) *fleetopsv1.ApprovalRequest {
	spec.RequestedFor, spec.SessionID = "sam", "s1"
	return &fleetopsv1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Name: "areq-1", Namespace: "warehouse-a"}, Spec: spec}
}

// The builders are what the executor writes and what the web UI shows; they
// must hold the draft flag, the zero auto-resume and the single annotation.
func TestDraftTaskObject(t *testing.T) {
	req := request(fleetopsv1.ApprovalRequestSpec{Type: fleetopsv1.RequestDraftTask, DraftTask: &fleetopsv1.DraftTaskParams{
		TaskName: "stage-pallet-a", Purpose: "stage", Actions: []fleetopsv1.DraftAction{{Name: "go", Type: "Navigate", Zone: "warehouse-a", Priority: "Normal", RequiredCapabilities: []string{"navigation"}}},
	}})
	o, err := DraftTaskObject(req, "pat")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Spec.Draft || o.Name != "stage-pallet-a" || o.Namespace != "warehouse-a" || o.Kind != "FleetTask" {
		t.Fatalf("object %+v", o)
	}
	if o.Labels[fleet.CreatedByLabel] != "fleetops-approvals" || o.Labels[fleet.RequestLabel] != "areq-1" || o.Labels[fleet.SessionLabel] != "s1" {
		t.Fatalf("labels %v", o.Labels)
	}
	if o.Annotations["fleetops.alexbahel.github.io/approved-by"] != "pat" || o.Annotations["fleetops.alexbahel.github.io/requested-for"] != "sam" {
		t.Fatalf("annotations %v", o.Annotations)
	}
	if len(o.Spec.Actions) != 1 || string(o.Spec.Actions[0].Action.Type) != "Navigate" || o.Spec.Actions[0].Action.RequiredCapabilities[0] != "navigation" {
		t.Fatalf("actions %+v", o.Spec.Actions)
	}
}

func TestMaintenanceObject(t *testing.T) {
	req := request(fleetopsv1.ApprovalRequestSpec{Type: fleetopsv1.RequestZoneMaintenance, ZoneMaintenance: &fleetopsv1.ZoneMaintenanceParams{ScopeType: "Zone", ZoneName: "aisle-b", Mode: "Graceful", Reason: "inspection"}})
	o := MaintenanceObject(req, "pat")
	if o.Name != "fleetops-areq-1" || o.Spec.AutoResumeAfterMinutes != 0 || string(o.Spec.Mode) != "Graceful" || o.Spec.Scope.ZoneName != "aisle-b" || o.Kind != "ZoneMaintenance" {
		t.Fatalf("object %+v", o)
	}
}

func TestZoneStopPatchAndActivationCommand(t *testing.T) {
	req := request(fleetopsv1.ApprovalRequestSpec{Type: fleetopsv1.RequestZoneStop, ZoneStop: &fleetopsv1.ZoneStopParams{ZoneName: "aisle-b", Reason: "spill"}})
	b, err := ZoneStopPatch(req)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"metadata":{"annotations":{"swarmada.io/estop-triggered":"spill"}}}` {
		t.Fatalf("patch %s", b)
	}
	if got := ActivationCommand("warehouse-a", "areq-1"); got != "fleetops activate areq-1 -n warehouse-a" {
		t.Fatalf("command %s", got)
	}
	if ActivationPatch != `{"spec":{"draft":false}}` {
		t.Fatalf("activation patch %s", ActivationPatch)
	}
}
