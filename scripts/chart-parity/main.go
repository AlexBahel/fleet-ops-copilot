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

// Command chart-parity checks that the Helm chart, rendered with its default
// values, produces the same objects as the plain manifests in config/ that
// envtest, the evals and make deploy-kind use.
//
//	helm template foc charts/fleet-ops-copilot -n foc-system --include-crds > rendered.yaml
//	go run ./scripts/chart-parity -rendered rendered.yaml
//
// Objects are compared after parsing, so formatting differences do not count.
// Ignored, and only these: the chart's own labels (helm.sh/chart,
// app.kubernetes.io/managed-by, app.kubernetes.io/instance,
// app.kubernetes.io/version), the pod-template annotation checksum/policy, and
// the Namespace object in config/rbac (the chart relies on
// helm install --create-namespace). The ConfigMap fleetops-policy, which
// make deploy-kind creates from policies/default.yaml, is compared with that
// file byte for byte, as are the chart's copies of the CRDs and the policy.
//
// It also refuses any rendered Ingress, Route or Gateway API object and any
// Service that is not ClusterIP (R38: foc-agent's web UI has no login).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

var chartLabels = []string{
	"helm.sh/chart",
	"app.kubernetes.io/managed-by",
	"app.kubernetes.io/instance",
	"app.kubernetes.io/version",
}

const chartAnnotation = "checksum/policy"

type obj = map[string]any

func main() {
	rendered := flag.String("rendered", "", "helm template output (with --include-crds)")
	repo := flag.String("repo", ".", "repository root")
	flag.Parse()
	if *rendered == "" {
		fmt.Fprintln(os.Stderr, "chart-parity: -rendered is required")
		os.Exit(2)
	}
	problems, err := check(*repo, *rendered)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chart-parity:", err)
		os.Exit(2)
	}
	for _, p := range problems {
		fmt.Println("DIFF", p)
	}
	if len(problems) > 0 {
		fmt.Printf("chart-parity: %d difference(s)\n", len(problems))
		os.Exit(1)
	}
}

func check(repo, rendered string) ([]string, error) {
	var problems []string
	chart := filepath.Join(repo, "charts", "fleet-ops-copilot")

	// Byte-for-byte copies.
	crds, err := filepath.Glob(filepath.Join(repo, "config", "crd", "*.yaml"))
	if err != nil {
		return nil, err
	}
	pairs := [][2]string{{filepath.Join(repo, "policies", "default.yaml"), filepath.Join(chart, "files", "default-policy.yaml")}}
	for _, c := range crds {
		pairs = append(pairs, [2]string{c, filepath.Join(chart, "crds", filepath.Base(c))})
	}
	chartCRDs, err := filepath.Glob(filepath.Join(chart, "crds", "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(chartCRDs) != len(crds) {
		problems = append(problems, fmt.Sprintf("chart has %d CRD files, config/crd has %d", len(chartCRDs), len(crds)))
	}
	for _, p := range pairs {
		a, err := os.ReadFile(p[0])
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(p[1])
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", p[1], err))
			continue
		}
		if !bytes.Equal(a, b) {
			problems = append(problems, fmt.Sprintf("%s differs from %s", p[1], p[0]))
		}
	}

	// Parsed objects.
	want := map[string]obj{}
	for _, dir := range []string{"crd", "rbac", "admission", "deploy"} {
		files, err := filepath.Glob(filepath.Join(repo, "config", dir, "*.yaml"))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if err := load(f, want); err != nil {
				return nil, err
			}
		}
	}
	got := map[string]obj{}
	if err := load(rendered, got); err != nil {
		return nil, err
	}

	delete(want, "v1/Namespace//foc-system") // created by helm install --create-namespace

	const policyKey = "v1/ConfigMap/foc-system/fleetops-policy"
	if cm, ok := got[policyKey]; ok {
		policy, err := os.ReadFile(filepath.Join(repo, "policies", "default.yaml"))
		if err != nil {
			return nil, err
		}
		data, _ := cm["data"].(obj)
		if s, _ := data["default.yaml"].(string); s != string(policy) {
			problems = append(problems, "ConfigMap fleetops-policy data[default.yaml] differs from policies/default.yaml")
		}
		delete(got, policyKey)
	} else {
		problems = append(problems, "chart renders no ConfigMap fleetops-policy")
	}

	for _, k := range sortedKeys(want) {
		g, ok := got[k]
		if !ok {
			problems = append(problems, "missing from chart: "+k)
			continue
		}
		w := want[k]
		normalize(g)
		normalize(w)
		if !reflect.DeepEqual(g, w) {
			problems = append(problems, fmt.Sprintf("%s:\n  config: %s\n  chart:  %s", k, js(w), js(g)))
		}
	}
	for _, k := range sortedKeys(got) {
		if _, ok := want[k]; !ok {
			problems = append(problems, "only in chart: "+k)
		}
	}
	problems = append(problems, exposure(got)...)
	fmt.Printf("chart-parity: %d objects from config/ compared, %d byte-compared files\n", len(want), len(pairs))
	return problems, nil
}

// exposureKinds would publish a Service outside the cluster.
var exposureKinds = map[string]bool{"Ingress": true, "Route": true, "Gateway": true, "HTTPRoute": true, "GRPCRoute": true, "TLSRoute": true, "TCPRoute": true}

// exposure enforces R38 on the rendered chart: foc-agent's web UI has no
// login, so nothing may expose a Service outside the cluster. Every Service
// is ClusterIP and no Ingress, Route or Gateway API object is rendered.
func exposure(got map[string]obj) []string {
	var problems []string
	for _, k := range sortedKeys(got) {
		o := got[k]
		kind, _ := o["kind"].(string)
		if exposureKinds[kind] {
			problems = append(problems, "R38: chart renders "+k+"; foc-agent's web UI has no login and must not be exposed")
		}
		if kind == "Service" {
			spec, _ := o["spec"].(obj)
			if t, _ := spec["type"].(string); t != "" && t != "ClusterIP" {
				problems = append(problems, fmt.Sprintf("R38: %s has type %s; only ClusterIP is allowed", k, t))
			}
		}
	}
	return problems
}

// load reads every YAML document in path into m, keyed by
// apiVersion/kind/namespace/name.
func load(path string, m map[string]obj) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, doc := range splitDocs(raw) {
		var o obj
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if len(o) == 0 {
			continue
		}
		md, _ := o["metadata"].(obj)
		ns, _ := md["namespace"].(string)
		name, _ := md["name"].(string)
		k := fmt.Sprintf("%v/%v/%s/%s", o["apiVersion"], o["kind"], ns, name)
		if _, dup := m[k]; dup {
			return fmt.Errorf("%s: duplicate object %s", path, k)
		}
		m[k] = o
	}
	return nil
}

func splitDocs(raw []byte) [][]byte {
	var docs [][]byte
	var cur bytes.Buffer
	for line := range strings.SplitAfterSeq(string(raw), "\n") {
		if strings.TrimRight(line, " \r\n") == "---" {
			docs = append(docs, bytes.Clone(cur.Bytes()))
			cur.Reset()
			continue
		}
		cur.WriteString(line)
	}
	return append(docs, cur.Bytes())
}

func normalize(o obj) {
	strip(o)
	if spec, ok := o["spec"].(obj); ok {
		if tmpl, ok := spec["template"].(obj); ok {
			strip(tmpl)
		}
	}
}

func strip(o obj) {
	md, ok := o["metadata"].(obj)
	if !ok {
		return
	}
	if l, ok := md["labels"].(obj); ok {
		for _, k := range chartLabels {
			delete(l, k)
		}
		if len(l) == 0 {
			delete(md, "labels")
		}
	}
	if a, ok := md["annotations"].(obj); ok {
		delete(a, chartAnnotation)
		if len(a) == 0 {
			delete(md, "annotations")
		}
	}
}

func sortedKeys(m map[string]obj) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func js(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(b.String())
}
