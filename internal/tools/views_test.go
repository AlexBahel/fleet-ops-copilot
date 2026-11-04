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

package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	swarmv1 "github.com/swarmada/swarmada/api/v1"
)

func TestActionViewBatteryFloorAndDisconnect(t *testing.T) {
	min := int32(30)
	at := metav1.NewTime(time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	a := &swarmv1.FleetAction{}
	a.Name = "deliver-pallet-001"
	a.Spec.MinBatteryPct = &min
	a.Status.DisconnectedAt = &at
	b, _ := json.Marshal(actionView(a))
	for _, want := range []string{`"spec.minBatteryPct":30`, `"status.disconnectedAt":"2026-11-04T03:00:00Z"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("view lacks %s: %s", want, b)
		}
	}
	// Unset fields stay out of the view, so a model never reads a zero floor as a rule.
	b, _ = json.Marshal(actionView(&swarmv1.FleetAction{}))
	if strings.Contains(string(b), "minBatteryPct") || strings.Contains(string(b), "disconnectedAt") {
		t.Errorf("unset fields rendered: %s", b)
	}
}
