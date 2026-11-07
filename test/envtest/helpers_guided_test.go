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
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// confirmed is the session of a call made after the requester confirmed
// the preview of in (D74), as fleetops-agent makes it.
func confirmed(s tools.Session, in any) tools.Session {
	id, err := tools.PreviewIDFor(in)
	if err != nil {
		panic(err)
	}
	s.ConfirmedPreview = id
	return s
}

func (h *harness) draft(ctx context.Context, s tools.Session, in tools.DraftTaskInput) (tools.RequestResult, error) {
	return h.svc.RequestDraftTask(ctx, confirmed(s, in), in)
}

func (h *harness) activate(ctx context.Context, s tools.Session, in tools.ActivateTaskInput) (tools.RequestResult, error) {
	return h.svc.RequestActivateTask(ctx, confirmed(s, in), in)
}

func (h *harness) maintenance(ctx context.Context, s tools.Session, in tools.ZoneMaintenanceInput) (tools.RequestResult, error) {
	return h.svc.RequestZoneMaintenance(ctx, confirmed(s, in), in)
}

func (h *harness) stop(ctx context.Context, s tools.Session, in tools.ZoneStopInput) (tools.RequestResult, error) {
	return h.svc.RequestZoneStop(ctx, confirmed(s, in), in)
}

// ensureRobot creates an idle robot in zone with an Active navigation
// capability, served by a connected, conformant adapter, so a drafted task
// passes check_task at filing and at execution.
func ensureRobot(t *testing.T, name, zone string, battery int32) *swarmv1.Robot {
	t.Helper()
	ctx := context.Background()
	ad := &swarmv1.FleetAdapter{ObjectMeta: metav1.ObjectMeta{Name: "envtest-adapter", Namespace: swarmNS}}
	ad.Spec.Vendor = "envtest"
	if err := admin.Create(ctx, ad); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(ad), ad); err != nil {
		t.Fatal(err)
	}
	ad.Status.Phase = swarmv1.FleetAdapterPhaseConnected
	ad.Status.Conformance = swarmv1.ConformanceStatePassed
	if err := admin.Status().Update(ctx, ad); err != nil {
		t.Fatal(err)
	}
	r := &swarmv1.Robot{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: swarmNS}}
	r.Spec.Adapter.Name, r.Spec.Adapter.Version = "envtest-adapter", "0.1.0"
	r.Spec.Manufacturer, r.Spec.Model = "Simulated", "SimBot-v1"
	r.Spec.Zone = zone
	r.Spec.Capabilities = []swarmv1.ClassCapability{{Name: "navigation", Type: "hardware-native"}}
	if err := admin.Create(ctx, r); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(r), r); err != nil {
		t.Fatal(err)
	}
	r.Status.Phase = "Idle"
	r.Status.EstopState = "Normal"
	r.Status.CurrentZone = zone
	r.Status.BatteryPercent = &battery
	r.Status.Capabilities = []swarmv1.CapabilityStatusEntry{{Name: "navigation", Status: swarmv1.CapabilityStatusActive}}
	if err := admin.Status().Update(ctx, r); err != nil {
		t.Fatal(err)
	}
	return r
}
