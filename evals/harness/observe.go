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

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
)

// gvkFor maps the Kind in a Kind/name reference to its API group/version.
func gvkFor(kind string) (schema.GroupVersionKind, bool) {
	switch kind {
	case "Robot", "FleetTask", "FleetAction", "FleetZone", "ZoneMaintenance", "DiscoveredRobot", "FleetAdapter", "TaskTrigger":
		return swarmv1.GroupVersion.WithKind(kind), true
	case "ApprovalRequest":
		return focv1.GroupVersion.WithKind(kind), true
	}
	return schema.GroupVersionKind{}, false
}

// resetKinds are cleared before every run: every kind a fixture can hold or a
// run can create in the namespace. Each entry pairs a kind with its list type.
var resetKinds = []struct {
	obj  client.Object
	list client.ObjectList
}{
	{&focv1.ApprovalDecision{}, &focv1.ApprovalDecisionList{}},
	{&focv1.ApprovalRequest{}, &focv1.ApprovalRequestList{}},
	{&swarmv1.ZoneMaintenance{}, &swarmv1.ZoneMaintenanceList{}},
	{&swarmv1.FleetTask{}, &swarmv1.FleetTaskList{}},
	{&swarmv1.FleetAction{}, &swarmv1.FleetActionList{}},
	{&swarmv1.Robot{}, &swarmv1.RobotList{}},
	{&swarmv1.FleetZone{}, &swarmv1.FleetZoneList{}},
	{&corev1.Event{}, &corev1.EventList{}},
	{&swarmv1.FleetAdapter{}, &swarmv1.FleetAdapterList{}},
	{&swarmv1.DiscoveredRobot{}, &swarmv1.DiscoveredRobotList{}},
	{&swarmv1.TaskTrigger{}, &swarmv1.TaskTriggerList{}},
}

// Reset deletes every object of resetKinds in the namespace and waits until
// none is left (docs/DESIGN.md D47). Finalizers are removed first, from every
// kind (Swarmada's estop-in-force on a stopped FleetZone, or any other), so
// no object stays behind in deletion and collides with the next fixture.
func Reset(ctx context.Context, admin client.Client) error {
	for _, k := range resetKinds {
		if err := admin.List(ctx, k.list, client.InNamespace(testenv.SwarmNS)); err != nil {
			return fmt.Errorf("listing %T: %w", k.list, err)
		}
		items, err := meta.ExtractList(k.list)
		if err != nil {
			return err
		}
		for _, it := range items {
			o, ok := it.(client.Object)
			if !ok || len(o.GetFinalizers()) == 0 {
				continue
			}
			o.SetFinalizers(nil)
			if err := admin.Update(ctx, o); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("removing finalizers from %T %s: %w", o, o.GetName(), err)
			}
		}
	}
	for _, k := range resetKinds {
		if err := admin.DeleteAllOf(ctx, k.obj, client.InNamespace(testenv.SwarmNS)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("clearing %T: %w", k.obj, err)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var left []string
		for _, k := range resetKinds {
			if err := admin.List(ctx, k.list, client.InNamespace(testenv.SwarmNS)); err != nil {
				return err
			}
			if n := meta.LenList(k.list); n > 0 {
				left = append(left, fmt.Sprintf("%d %T", n, k.obj))
			}
		}
		if len(left) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("objects left after reset: %s", strings.Join(left, ", "))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// LoadFixture creates every object in a fixture file, then writes its status
// through the status subresource (fixtures include status; no Swarmada
// controller runs to produce it).
func LoadFixture(ctx context.Context, admin client.Client, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	objs, err := testenv.DecodeYAML(b)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, u := range objs {
		status, hasStatus := u.Object["status"]
		delete(u.Object, "status")
		if u.GetNamespace() == "" {
			u.SetNamespace(testenv.SwarmNS)
		}
		if err := admin.Create(ctx, u); err != nil {
			return fmt.Errorf("%s: creating %s/%s: %w", path, u.GetKind(), u.GetName(), err)
		}
		if hasStatus {
			u.Object["status"] = status
			if err := admin.Status().Update(ctx, u); err != nil {
				return fmt.Errorf("%s: status of %s/%s: %w", path, u.GetKind(), u.GetName(), err)
			}
		}
	}
	return nil
}

type pathSeg struct {
	key, sel string
}

// splitPath parses "status.capabilities[camera_front].status".
func splitPath(p string) []pathSeg {
	var out []pathSeg
	for _, part := range strings.Split(p, ".") {
		seg := pathSeg{key: part}
		if i := strings.Index(part, "["); i > 0 && strings.HasSuffix(part, "]") {
			seg = pathSeg{key: part[:i], sel: part[i+1 : len(part)-1]}
		}
		out = append(out, seg)
	}
	return out
}

// lookup resolves a path in an object; list elements are selected by name.
func lookup(obj map[string]any, path string) (string, bool) {
	var cur any = obj
	for _, seg := range splitPath(path) {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[seg.key]
		if !ok {
			return "", false
		}
		if seg.sel != "" {
			list, ok := cur.([]any)
			if !ok {
				return "", false
			}
			found := false
			for _, el := range list {
				if em, ok := el.(map[string]any); ok && fmt.Sprint(em["name"]) == seg.sel {
					cur, found = em, true
					break
				}
			}
			if !found {
				return "", false
			}
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64, int64, bool:
		return fmt.Sprint(v), true
	case nil:
		return "", false
	default:
		b, _ := json.Marshal(v)
		return string(b), true
	}
}

// Observe reads the post-run state the judge needs.
func Observe(ctx context.Context, admin client.Client, s Scenario, answer string) (Observed, error) {
	ob := Observed{FactValues: map[string]string{}, Existing: map[string]bool{}}
	var reqs focv1.ApprovalRequestList
	if err := admin.List(ctx, &reqs, client.InNamespace(testenv.SwarmNS)); err != nil {
		return ob, err
	}
	for _, r := range reqs.Items {
		b, _ := json.Marshal(r.Spec)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		flat := map[string]string{}
		flatten("", m, flat)
		ob.Requests = append(ob.Requests, flat)
	}
	var tasks swarmv1.FleetTaskList
	if err := admin.List(ctx, &tasks, client.InNamespace(testenv.SwarmNS), client.HasLabels{fleet.CreatedByLabel}); err != nil {
		return ob, err
	}
	for _, t := range tasks.Items {
		ob.Drafts = append(ob.Drafts, ObservedDraft{Name: t.Name, Draft: t.Spec.Draft, Actions: len(t.Spec.Actions)})
	}
	refs := map[string]bool{}
	for _, f := range s.Expect.Facts {
		refs[f.Ref] = true
	}
	for _, m := range refPattern.FindAllString(answer, -1) {
		refs[m] = true
	}
	for _, a := range s.Expect.Absent {
		refs[a.Ref] = true
	}
	objs := map[string]map[string]any{}
	for ref := range refs {
		kind, name, _ := strings.Cut(ref, "/")
		gvk, ok := gvkFor(kind)
		if !ok {
			continue
		}
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		err := admin.Get(ctx, client.ObjectKey{Namespace: testenv.SwarmNS, Name: name}, u)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return ob, err
		}
		ob.Existing[ref] = true
		objs[ref] = u.Object
	}
	for _, f := range s.Expect.Facts {
		if o, ok := objs[f.Ref]; ok {
			if v, ok := lookup(o, f.Field); ok {
				ob.FactValues[f.Ref+" "+f.Field] = v
			}
		}
	}
	return ob, nil
}

func flatten(prefix string, v any, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flatten(p, x, out)
		}
	default:
		out[prefix] = fmt.Sprint(t)
	}
}
