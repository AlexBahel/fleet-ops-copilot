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

package feasibility

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"
)

// Load reads the snapshot for namespace ns with c. A list that fails (for
// example a person without list permission running fleetops activate) is recorded
// in NotChecked instead of failing the whole check. SwarmadaConfig is read
// and projected at once (ProjectConfig); the objects are not kept.
func Load(ctx context.Context, c client.Reader, ns string, now time.Time) Snapshot {
	s := Snapshot{Namespace: ns, Now: now}
	read := func(what string, l client.ObjectList) bool {
		if err := c.List(ctx, l, client.InNamespace(ns)); err != nil {
			s.NotChecked = append(s.NotChecked, fmt.Sprintf("%s (could not be read: %s)", what, short(err)))
			return false
		}
		return true
	}
	var robots swarmv1.RobotList
	if read("robots and their eligibility", &robots) {
		s.Robots = robots.Items
	}
	var zones swarmv1.FleetZoneList
	if read("zones, their stops and restrictions", &zones) {
		s.Zones = zones.Items
	}
	var zms swarmv1.ZoneMaintenanceList
	if read("zone maintenance windows", &zms) {
		s.Maintenances = zms.Items
	}
	var ads swarmv1.FleetAdapterList
	if read("fleet adapters and their action catalogs", &ads) {
		s.Adapters = ads.Items
	}
	var tasks swarmv1.FleetTaskList
	if read("existing FleetTasks", &tasks) {
		s.Tasks = tasks.Items
	}
	var actions swarmv1.FleetActionList
	if read("Pending FleetActions per zone", &actions) {
		s.Actions = actions.Items
	}
	var cfg swarmv1.SwarmadaConfigList
	if err := c.List(ctx, &cfg, client.InNamespace(ns)); err == nil {
		s.Config = ProjectConfig(cfg.Items)
	}
	return s
}

func short(err error) string {
	m := err.Error()
	if len(m) > 160 {
		m = m[:160] + "..."
	}
	return m
}
