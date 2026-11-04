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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	evalharness "github.com/AlexBahel/fleet-ops-copilot/evals/harness"
)

// TestResetClearsEveryFixtureKind (docs/DESIGN.md D47): the eval harness reset
// leaves no FleetAdapter, DiscoveredRobot or TaskTrigger behind, even one that
// carries a finalizer, so a run never sees objects from the previous fixture.
func TestResetClearsEveryFixtureKind(t *testing.T) {
	ctx := context.Background()
	hold := []string{"test.fleetops.alexbahel.github.io/hold"}
	om := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: swarmNS, Finalizers: hold}
	}
	objs := []client.Object{
		&swarmv1.FleetAdapter{ObjectMeta: om("reset-test-adapter"), Spec: swarmv1.FleetAdapterSpec{Vendor: "simulation"}},
		&swarmv1.DiscoveredRobot{ObjectMeta: om("reset-test-discovered")},
		&swarmv1.TaskTrigger{ObjectMeta: om("reset-test-trigger"), Spec: swarmv1.TaskTriggerSpec{
			Source:       swarmv1.TaskTriggerSourceRef{SemanticEventSource: "reset-test", EventType: "reset-test"},
			TaskTemplate: swarmv1.FleetTaskSpec{Actions: []swarmv1.FleetTaskAction{{Name: "go", Action: swarmv1.FleetActionSpec{Type: swarmv1.ActionTypeNavigate, Zone: swarmNS}}}},
		}},
	}
	for _, o := range objs {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("create %T: %v", o, err)
		}
	}
	if err := evalharness.Reset(ctx, admin); err != nil {
		t.Fatalf("reset: %v", err)
	}
	for _, l := range []client.ObjectList{&swarmv1.FleetAdapterList{}, &swarmv1.DiscoveredRobotList{}, &swarmv1.TaskTriggerList{}} {
		if err := admin.List(ctx, l, client.InNamespace(swarmNS)); err != nil {
			t.Fatal(err)
		}
		if n := meta.LenList(l); n != 0 {
			t.Errorf("%T: %d object(s) left after reset", l, n)
		}
	}
}
