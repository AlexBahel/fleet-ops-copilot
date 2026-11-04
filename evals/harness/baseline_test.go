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
	"path/filepath"
	"strings"
	"testing"
)

func scriptedResults() Results {
	return Results{
		Backend: "scripted", RunsPerScenario: 3,
		ByCategory: map[string]CategoryTotal{
			"diagnose": {Scenarios: 7, Runs: 21, Pass: 21},
			"safety":   {Scenarios: 6, Runs: 18, Pass: 18},
		},
		Total: CategoryTotal{Scenarios: 13, Runs: 39, Pass: 39},
	}
}

func TestBaselineCheck(t *testing.T) {
	base, err := BaselineFrom(scriptedResults())
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(*Results)) Results {
		r := scriptedResults()
		f(&r)
		return r
	}
	cases := []struct {
		name string
		res  Results
		want string // substring of the one expected violation; "" means the gate passes
	}{
		{"identical", scriptedResults(), ""},
		{"one run lost in one category", mutate(func(r *Results) {
			c := r.ByCategory["safety"]
			c.Pass--
			r.ByCategory["safety"] = c
			r.Total.Pass--
		}), "REGRESSION: category safety passed 17 of 18 runs, baseline is 18"},
		{"scenario added to a category", mutate(func(r *Results) {
			r.ByCategory["safety"] = CategoryTotal{Scenarios: 7, Runs: 21, Pass: 21}
			r.Total = CategoryTotal{Scenarios: 14, Runs: 42, Pass: 42}
		}), "COUNTS CHANGED: 14 scenarios, baseline has 13"},
		{"scenario moved between categories", mutate(func(r *Results) {
			r.ByCategory["diagnose"] = CategoryTotal{Scenarios: 6, Runs: 18, Pass: 18}
			r.ByCategory["safety"] = CategoryTotal{Scenarios: 7, Runs: 21, Pass: 21}
		}), "category diagnose ran 6 scenarios / 18 runs, baseline has 7 / 21"},
		{"category dropped", mutate(func(r *Results) {
			delete(r.ByCategory, "safety")
			r.Total = CategoryTotal{Scenarios: 7, Runs: 21, Pass: 21}
		}), "category safety is in the baseline"},
		{"category added", mutate(func(r *Results) { r.ByCategory["zone-ops"] = CategoryTotal{Scenarios: 0} }), "category zone-ops has 0 scenarios but is not in the baseline"},
		{"runs per scenario changed", mutate(func(r *Results) { r.RunsPerScenario = 5 }), "5 runs per scenario, baseline has 3"},
		{"other backend", mutate(func(r *Results) { r.Backend = "claude:x" }), "backend is"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := base.Check(tc.res)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected violations: %v", got)
				}
				return
			}
			if !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Fatalf("violations %q do not contain %q", got, tc.want)
			}
		})
	}
}

func TestBaselineFromRefusesFailures(t *testing.T) {
	r := scriptedResults()
	r.Total.Pass--
	if _, err := BaselineFrom(r); err == nil {
		t.Fatal("a baseline was recorded from results with a failed run")
	}
}

func TestBaselineRoundTrip(t *testing.T) {
	base, err := BaselineFrom(scriptedResults())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "baseline.json")
	if err := base.Write(p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBaseline(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Check(scriptedResults()); len(v) != 0 {
		t.Fatalf("round trip changed the baseline: %v", v)
	}
}

// The committed baseline must describe the committed scenarios: this fails in
// `make test` (before the envtest run) when a scenario is added, removed or
// moved without evals/baseline.json being updated.
func TestCommittedBaselineMatchesScenarios(t *testing.T) {
	base, err := LoadBaseline("../baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	scs, err := LoadScenarios("../scenarios")
	if err != nil {
		t.Fatal(err)
	}
	per := map[string]int{}
	for _, s := range scs {
		per[s.Category]++
	}
	if len(per) != len(base.ByCategory) {
		t.Errorf("scenarios span %d categories, baseline has %d", len(per), len(base.ByCategory))
	}
	for c, n := range per {
		if b, ok := base.ByCategory[c]; !ok || b.Scenarios != n {
			t.Errorf("category %s has %d scenarios, baseline has %+v", c, n, b)
		}
	}
	if base.Total.Scenarios != len(scs) {
		t.Errorf("%d scenarios, baseline has %d", len(scs), base.Total.Scenarios)
	}
	if base.Backend != "scripted" {
		t.Errorf("committed baseline is for backend %q, want scripted", base.Backend)
	}
}
