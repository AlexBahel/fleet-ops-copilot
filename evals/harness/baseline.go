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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// BaselineSchema identifies the baseline file format.
const BaselineSchema = "fleetops.alexbahel.github.io/eval-baseline/v1"

// Baseline is the committed per-category result the scripted fixture evals
// must keep reaching (evals/baseline.json). It is the CI regression gate's
// reference: Check fails when a category passes fewer runs than recorded, and
// when the scenario count, a category's scenario or run count, or the number
// of runs per scenario differs from it. A change to any of those counts
// therefore needs a baseline update in the same commit.
type Baseline struct {
	Schema          string                   `json:"schema"`
	Backend         string                   `json:"backend"`
	RunsPerScenario int                      `json:"runsPerScenario"`
	ByCategory      map[string]CategoryTotal `json:"byCategory"`
	Total           CategoryTotal            `json:"total"`
}

// BaselineFrom records res as a baseline. It refuses results with a run that
// failed: a baseline is a floor to hold, not a place to record a regression.
func BaselineFrom(res Results) (Baseline, error) {
	if res.Total.Pass != res.Total.Runs {
		return Baseline{}, fmt.Errorf("%d of %d runs failed; fix them before recording a baseline", res.Total.Runs-res.Total.Pass, res.Total.Runs)
	}
	b := Baseline{Schema: BaselineSchema, Backend: res.Backend, RunsPerScenario: res.RunsPerScenario, ByCategory: map[string]CategoryTotal{}, Total: res.Total}
	for c, t := range res.ByCategory {
		b.ByCategory[c] = t
	}
	return b, nil
}

// LoadBaseline reads and validates a baseline file.
func LoadBaseline(path string) (Baseline, error) {
	var b Baseline
	raw, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, fmt.Errorf("%s: %w", path, err)
	}
	if b.Schema != BaselineSchema {
		return b, fmt.Errorf("%s: schema %q, want %q", path, b.Schema, BaselineSchema)
	}
	sumS, sumR, sumP := 0, 0, 0
	for c, t := range b.ByCategory {
		if t.Pass < 0 || t.Pass > t.Runs || t.Runs != t.Scenarios*b.RunsPerScenario {
			return b, fmt.Errorf("%s: category %s is inconsistent: %+v with %d runs per scenario", path, c, t, b.RunsPerScenario)
		}
		sumS, sumR, sumP = sumS+t.Scenarios, sumR+t.Runs, sumP+t.Pass
	}
	if b.Total != (CategoryTotal{Scenarios: sumS, Runs: sumR, Pass: sumP}) {
		return b, fmt.Errorf("%s: total %+v is not the sum of the categories (%d scenarios, %d runs, %d pass)", path, b.Total, sumS, sumR, sumP)
	}
	return b, nil
}

// Write stores the baseline as indented JSON.
func (b Baseline) Write(path string) error {
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// Check compares res with the baseline and returns one line per violation;
// none means the gate passes. Regressions (fewer passing runs) and changed
// counts are reported separately so the log says which rule failed.
func (b Baseline) Check(res Results) []string {
	var v []string
	if res.Backend != b.Backend {
		v = append(v, fmt.Sprintf("COUNTS CHANGED: backend is %q, baseline is for %q", res.Backend, b.Backend))
	}
	if res.RunsPerScenario != b.RunsPerScenario {
		v = append(v, fmt.Sprintf("COUNTS CHANGED: %d runs per scenario, baseline has %d; update evals/baseline.json in the same commit", res.RunsPerScenario, b.RunsPerScenario))
	}
	if res.Total.Scenarios != b.Total.Scenarios {
		v = append(v, fmt.Sprintf("COUNTS CHANGED: %d scenarios, baseline has %d; update evals/baseline.json in the same commit", res.Total.Scenarios, b.Total.Scenarios))
	}
	names := map[string]bool{}
	for c := range b.ByCategory {
		names[c] = true
	}
	for c := range res.ByCategory {
		names[c] = true
	}
	sorted := make([]string, 0, len(names))
	for c := range names {
		sorted = append(sorted, c)
	}
	sort.Strings(sorted)
	for _, c := range sorted {
		want, inBase := b.ByCategory[c]
		got, inRes := res.ByCategory[c]
		switch {
		case !inBase:
			v = append(v, fmt.Sprintf("COUNTS CHANGED: category %s has %d scenarios but is not in the baseline; update evals/baseline.json in the same commit", c, got.Scenarios))
		case !inRes:
			v = append(v, fmt.Sprintf("COUNTS CHANGED: category %s is in the baseline (%d scenarios) but ran none; update evals/baseline.json in the same commit", c, want.Scenarios))
		default:
			if got.Scenarios != want.Scenarios || got.Runs != want.Runs {
				v = append(v, fmt.Sprintf("COUNTS CHANGED: category %s ran %d scenarios / %d runs, baseline has %d / %d; update evals/baseline.json in the same commit", c, got.Scenarios, got.Runs, want.Scenarios, want.Runs))
			}
			if got.Pass < want.Pass {
				v = append(v, fmt.Sprintf("REGRESSION: category %s passed %d of %d runs, baseline is %d", c, got.Pass, got.Runs, want.Pass))
			}
		}
	}
	return v
}
