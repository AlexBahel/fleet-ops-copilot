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
	"path/filepath"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	evalharness "github.com/AlexBahel/fleet-ops-copilot/evals/harness"
)

// Positive control for the unsafe-write containment metric (D67): the
// snapshot sees a fixture's objects, and a change to a Swarmada object made
// during a run shows up in the diff.
func TestSwarmadaSnapshotDetectsChanges(t *testing.T) {
	ctx := context.Background()
	if err := evalharness.Reset(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := evalharness.LoadFixture(ctx, admin, filepath.Join(repoRoot, "evals", "fixtures", "estop-drill.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evalharness.Reset(ctx, admin) })
	before, err := evalharness.SwarmadaSnapshot(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("snapshot after loading a fixture is empty")
	}
	if got := evalharness.SwarmadaChanges(before, before); len(got) != 0 {
		t.Fatalf("no change, but %v", got)
	}

	var zones swarmv1.FleetZoneList
	if err := admin.List(ctx, &zones, client.InNamespace(swarmNS)); err != nil || len(zones.Items) == 0 {
		t.Fatalf("fixture zones: %v (%d)", err, len(zones.Items))
	}
	z := zones.Items[0]
	patch := client.MergeFrom(z.DeepCopy())
	if z.Annotations == nil {
		z.Annotations = map[string]string{}
	}
	z.Annotations["test.fleetops.alexbahel.github.io/touched"] = "1"
	if err := admin.Patch(ctx, &z, patch); err != nil {
		t.Fatal(err)
	}
	zm := &swarmv1.ZoneMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "containment-test", Namespace: swarmNS},
		Spec:       swarmv1.ZoneMaintenanceSpec{Scope: swarmv1.ZoneMaintenanceScope{Type: swarmv1.MaintenanceScopeZone, ZoneName: z.Name}, Reason: "containment test"},
	}
	if err := admin.Create(ctx, zm); err != nil {
		t.Fatal(err)
	}
	after, err := evalharness.SwarmadaSnapshot(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	got := evalharness.SwarmadaChanges(before, after)
	for _, want := range []string{"modified FleetZone/" + z.Name, "created ZoneMaintenance/containment-test"} {
		if !slices.Contains(got, want) {
			t.Errorf("changes %v do not include %q", got, want)
		}
	}
}
