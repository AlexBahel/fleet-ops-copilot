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

// Command fleetops-capture writes a fixture file from the live state of one
// Swarmada namespace, for the eval harness (evals/harness.LoadFixture).
//
//	fleetops-capture --context kind-fleetops-quickstart -n warehouse-a -scenario healthy-fleet \
//	    -source "Swarmada v0.4 quickstart, SCENARIO=healthy-fleet" -out evals/fixtures/live/healthy-fleet.yaml
//
// It reads with the caller's kubeconfig and writes nothing to the cluster.
// Server-set metadata (uid, resourceVersion, managedFields, timestamps, owner
// references) and finalizers are removed so the objects can be created in a
// fresh API server; spec and status are kept as read.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

// kinds are captured in this order, so that a zone exists before the objects
// that name it when the fixture is loaded.
var kinds = []string{"FleetZone", "Robot", "FleetAdapter", "DiscoveredRobot", "TaskTrigger", "FleetTask", "FleetAction", "ZoneMaintenance"}

// dropAnnotations are client-side bookkeeping, not fleet state.
var dropAnnotations = []string{"kubectl.kubernetes.io/last-applied-configuration"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fleetops-capture:", err)
		os.Exit(1)
	}
}

func run() error {
	kctx := flag.String("context", "", "kubeconfig context (required)")
	ns := flag.String("n", "warehouse-a", "Swarmada namespace")
	scenario := flag.String("scenario", "", "fixture name (required)")
	source := flag.String("source", "", "how the state was produced (required), e.g. the quickstart command")
	out := flag.String("out", "", "output file (required)")
	flag.Parse()
	if *kctx == "" || *scenario == "" || *source == "" || *out == "" {
		flag.Usage()
		return fmt.Errorf("-context, -scenario, -source and -out are required")
	}
	cfg, err := config.GetConfigWithContext(*kctx)
	if err != nil {
		return err
	}
	c, err := client.New(cfg, client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Fixture: %s.\n", *scenario)
	fmt.Fprintf(&buf, "# Captured from a live cluster by evals/cmd/fleetops-capture at %s.\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&buf, "# Source: %s.\n", *source)
	buf.WriteString("# Spec and status as read from the API server; server-set metadata, owner\n")
	buf.WriteString("# references and finalizers removed. Status written by hand by the quickstart\n")
	buf.WriteString("# runner (its \"Honest notes\") is captured as it was. Fictional site.\n")
	n := 0
	for _, k := range kinds {
		l := &unstructured.UnstructuredList{}
		l.SetGroupVersionKind(schema.GroupVersionKind{Group: "swarmada.io", Version: "v1", Kind: k + "List"})
		if err := c.List(ctx, l, client.InNamespace(*ns)); err != nil {
			return fmt.Errorf("listing %s: %w", k, err)
		}
		sort.Slice(l.Items, func(i, j int) bool { return l.Items[i].GetName() < l.Items[j].GetName() })
		for i := range l.Items {
			b, err := yaml.Marshal(clean(&l.Items[i]))
			if err != nil {
				return err
			}
			buf.WriteString("---\n")
			buf.Write(b)
			n++
		}
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o600); err != nil {
		return err
	}
	fmt.Printf("%s: %d objects from namespace %s\n", *out, n, *ns)
	return nil
}

// clean keeps apiVersion, kind, name, labels, annotations, spec and status.
func clean(u *unstructured.Unstructured) map[string]any {
	md := map[string]any{"name": u.GetName()}
	if l := u.GetLabels(); len(l) > 0 {
		md["labels"] = l
	}
	if a := u.GetAnnotations(); len(a) > 0 {
		for _, k := range dropAnnotations {
			delete(a, k)
		}
		if len(a) > 0 {
			md["annotations"] = a
		}
	}
	o := map[string]any{"apiVersion": u.GetAPIVersion(), "kind": u.GetKind(), "metadata": md}
	for _, f := range []string{"spec", "status"} {
		if v, ok := u.Object[f]; ok {
			o[f] = v
		}
	}
	return o
}
