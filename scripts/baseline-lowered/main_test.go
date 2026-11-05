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

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func baselineJSON(rps int, cats map[string][3]int) []byte {
	var b strings.Builder
	b.WriteString(`{"schema":"fleetops.alexbahel.github.io/eval-baseline/v1","backend":"scripted","runsPerScenario":`)
	b.WriteString(strconv.Itoa(rps))
	b.WriteString(`,"byCategory":{`)
	first := true
	for _, n := range []string{"diagnose", "safety", "zone-ops"} {
		c, ok := cats[n]
		if !ok {
			continue
		}
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"` + n + `":{"scenarios":` + strconv.Itoa(c[0]) + `,"runs":` + strconv.Itoa(c[1]) + `,"pass":` + strconv.Itoa(c[2]) + `}`)
	}
	b.WriteString(`},"total":{"scenarios":0,"runs":0,"pass":0}}`)
	return []byte(b.String())
}

func TestCompare(t *testing.T) {
	base := baselineJSON(3, map[string][3]int{"diagnose": {7, 21, 21}, "safety": {6, 18, 18}, "zone-ops": {6, 18, 18}})
	cases := []struct {
		name string
		cur  []byte
		want []string
	}{
		{"unchanged", base, nil},
		{"raised pass is not a lowering", baselineJSON(3, map[string][3]int{"diagnose": {8, 24, 24}, "safety": {6, 18, 18}, "zone-ops": {6, 18, 18}}), nil},
		{"pass lowered", baselineJSON(3, map[string][3]int{"diagnose": {7, 21, 21}, "safety": {6, 18, 15}, "zone-ops": {6, 18, 18}}),
			[]string{"category safety: pass 18 -> 15"}},
		{"scenario removed lowers scenarios and runs", baselineJSON(3, map[string][3]int{"diagnose": {7, 21, 21}, "safety": {5, 15, 15}, "zone-ops": {6, 18, 18}}),
			[]string{"category safety: pass 18 -> 15", "category safety: runs 18 -> 15", "category safety: scenarios 6 -> 5"}},
		{"category removed", baselineJSON(3, map[string][3]int{"diagnose": {7, 21, 21}, "safety": {6, 18, 18}}),
			[]string{"category zone-ops: removed (pass was) 18 -> 0"}},
		{"runs per scenario lowered", baselineJSON(1, map[string][3]int{"diagnose": {7, 7, 7}, "safety": {6, 6, 6}, "zone-ops": {6, 6, 6}}),
			[]string{"runsPerScenario: 3 -> 1", "category diagnose: pass 21 -> 7", "category diagnose: runs 21 -> 7",
				"category safety: pass 18 -> 6", "category safety: runs 18 -> 6", "category zone-ops: pass 18 -> 6", "category zone-ops: runs 18 -> 6"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := compare(base, c.cur)
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for _, l := range got {
				lines = append(lines, l.String())
			}
			if strings.Join(lines, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

func TestCompareUnreadable(t *testing.T) {
	if _, err := compare([]byte("not json"), []byte("{}")); err == nil {
		t.Error("want an error for an unreadable base")
	}
	if _, err := compare([]byte("{}"), []byte("not json")); err == nil {
		t.Error("want an error for an unreadable current file")
	}
}

func TestReportFormat(t *testing.T) {
	var b bytes.Buffer
	report(&b, "evals/baseline.json", "0123456789abcdef", []Lowered{{Category: "safety", What: "pass", From: 18, To: 15}})
	want := "::warning file=evals/baseline.json,title=eval baseline lowered::category safety: pass 18 -> 15 (base 0123456789ab)\n"
	if !strings.HasPrefix(b.String(), want) {
		t.Errorf("got %q, want prefix %q", b.String(), want)
	}
	b.Reset()
	report(&b, "evals/baseline.json", "0123456789abcdef", nil)
	if strings.Contains(b.String(), "::warning") {
		t.Errorf("a warning was printed for an unchanged baseline: %q", b.String())
	}
}

// TestCommandInGitRepository runs the command in a scratch repository: a
// commit lowering a category, a commit raising it, and the new-branch case.
func TestCommandInGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := filepath.Join(t.TempDir(), "baseline-lowered")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil { //nolint:gosec // G204: fixed arguments
		t.Fatalf("build: %v\n%s", err, out)
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:gosec // G204: fixed arguments in a test
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, "evals"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "evals", "baseline.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...) //nolint:gosec // G204: the binary built above
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("baseline-lowered %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	write(baselineJSON(3, map[string][3]int{"safety": {6, 18, 18}}))
	run("add", ".")
	run("commit", "-q", "-m", "base")
	baseSHA := run("rev-parse", "HEAD")

	write(baselineJSON(3, map[string][3]int{"safety": {6, 18, 15}}))
	if out := command("-base", baseSHA); !strings.Contains(out, "::warning file=evals/baseline.json,title=eval baseline lowered::category safety: pass 18 -> 15") {
		t.Errorf("no warning for a lowered category:\n%s", out)
	}
	// A new branch: the push event's "before" is all zeros; main is the fallback.
	run("checkout", "-q", "-b", "feature")
	run("commit", "-q", "-am", "lower")
	zero := strings.Repeat("0", 40)
	if out := command("-base", zero, "-fallback", "main"); !strings.Contains(out, "::warning") {
		t.Errorf("no warning on a new branch compared with main:\n%s", out)
	}
	// Nothing to compare with: a notice, exit 0.
	if out := command("-base", zero, "-fallback", "does-not-exist"); !strings.Contains(out, "::notice") || strings.Contains(out, "::warning") {
		t.Errorf("want only a notice without a base:\n%s", out)
	}
	write(baselineJSON(3, map[string][3]int{"safety": {6, 18, 18}}))
	if out := command("-base", baseSHA); strings.Contains(out, "::warning") {
		t.Errorf("warning for an unchanged baseline:\n%s", out)
	}
}
