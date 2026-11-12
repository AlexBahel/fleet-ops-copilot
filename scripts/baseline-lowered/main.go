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

// Command baseline-lowered compares evals/baseline.json in the working tree
// with the same file at a base revision and prints one GitHub Actions
// "::warning" annotation for every category the change lowers.
//
//	go run ./scripts/baseline-lowered -base <revision>
//
// The CI regression gate (docs/DESIGN.md D58) compares a run with the baseline
// at the same commit, so a commit that lowers the baseline and the results
// together passes. This is the signal for that case: the numbers show up as
// an annotation on the run instead of only in the diff. A category is lowered
// when its pass, runs or scenarios count is below the base's, when it is gone,
// or when runsPerScenario fell. The command never fails the build for a lowered
// baseline; it exits non-zero only when it cannot read the current file.
//
// -base is a revision (a pull request's base SHA, a push's "before" SHA). When
// it is empty, all zeros (a new branch) or not a commit in this clone, the
// merge base with -fallback (default origin/main) is used. With no usable base
// the command prints a notice and exits 0.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/AlexBahel/fleet-ops-copilot/evals/harness"
)

// baselineFile is the gate's baseline, relative to the repository root.
const baselineFile = "evals/baseline.json"

func main() {
	base := flag.String("base", "", "base revision to compare with")
	fallback := flag.String("fallback", "origin/main", "revision whose merge base with HEAD is used when -base is unusable")
	flag.Parse()

	cur, err := os.ReadFile(baselineFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "baseline-lowered:", err)
		os.Exit(1)
	}
	rev := resolveBase(*base, *fallback)
	if rev == "" {
		fmt.Println("::notice title=eval baseline::no base revision to compare " + baselineFile + " with (new branch without " + *fallback + "); nothing checked")
		return
	}
	old, err := gitShow(rev, baselineFile)
	if err != nil {
		fmt.Printf("::notice title=eval baseline::%s does not exist at %s; nothing to compare\n", baselineFile, short(rev))
		return
	}
	lowered, err := compare(old, cur)
	if err != nil {
		fmt.Printf("::notice title=eval baseline::%v; nothing compared\n", err)
		return
	}
	report(os.Stdout, baselineFile, rev, lowered)
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600); err == nil { //nolint:gosec // G304: the runner names this file
			summary(f, baselineFile, rev, lowered)
			_ = f.Close()
		}
	}
}

// Lowered is one thing the current baseline holds below the base.
type Lowered struct {
	Category string // empty for runsPerScenario
	What     string
	From, To int
}

func (l Lowered) String() string {
	if l.Category == "" {
		return fmt.Sprintf("%s: %d -> %d", l.What, l.From, l.To)
	}
	return fmt.Sprintf("category %s: %s %d -> %d", l.Category, l.What, l.From, l.To)
}

// compare returns what the current baseline lowers relative to old, ordered by
// category. Both inputs are baseline JSON. A category present only in the
// current file is an addition, not a lowering.
func compare(old, cur []byte) ([]Lowered, error) {
	var o, c harness.Baseline
	if err := json.Unmarshal(old, &o); err != nil {
		return nil, fmt.Errorf("base baseline is not readable: %w", err)
	}
	if err := json.Unmarshal(cur, &c); err != nil {
		return nil, fmt.Errorf("current baseline is not readable: %w", err)
	}
	var out []Lowered
	if c.RunsPerScenario < o.RunsPerScenario {
		out = append(out, Lowered{What: "runsPerScenario", From: o.RunsPerScenario, To: c.RunsPerScenario})
	}
	names := make([]string, 0, len(o.ByCategory))
	for n := range o.ByCategory {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ot := o.ByCategory[n]
		ct, ok := c.ByCategory[n]
		if !ok {
			out = append(out, Lowered{Category: n, What: "removed (pass was)", From: ot.Pass, To: 0})
			continue
		}
		for _, f := range []struct {
			what     string
			from, to int
		}{{"pass", ot.Pass, ct.Pass}, {"runs", ot.Runs, ct.Runs}, {"scenarios", ot.Scenarios, ct.Scenarios}} {
			if f.to < f.from {
				out = append(out, Lowered{Category: n, What: f.what, From: f.from, To: f.to})
			}
		}
	}
	return out, nil
}

func report(w io.Writer, file, rev string, lowered []Lowered) {
	if len(lowered) == 0 {
		_, _ = fmt.Fprintf(w, "baseline-lowered: %s does not lower any category compared with %s\n", file, short(rev))
		return
	}
	for _, l := range lowered {
		_, _ = fmt.Fprintf(w, "::warning file=%s,title=eval baseline lowered::%s (base %s)\n", file, l, short(rev))
	}
	_, _ = fmt.Fprintf(w, "baseline-lowered: %d lowering(s) in %s compared with %s\n", len(lowered), file, short(rev))
}

func summary(w io.Writer, file, rev string, lowered []Lowered) {
	if len(lowered) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "### Eval baseline lowered\n\n`%s` holds less than at `%s`:\n\n", file, short(rev))
	for _, l := range lowered {
		_, _ = fmt.Fprintf(w, "- %s\n", l)
	}
	_, _ = fmt.Fprintln(w)
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// resolveBase returns a commit to compare with, or "".
func resolveBase(base, fallback string) string {
	if base != "" && strings.Trim(base, "0") != "" {
		if out, err := git("rev-parse", "--verify", "--quiet", base+"^{commit}"); err == nil {
			return strings.TrimSpace(out)
		}
	}
	if fallback != "" {
		if out, err := git("merge-base", "HEAD", fallback); err == nil {
			return strings.TrimSpace(out)
		}
	}
	return ""
}

func gitShow(rev, file string) ([]byte, error) {
	out, err := git("show", rev+":"+file)
	return []byte(out), err
}

func git(args ...string) (string, error) {
	cmd := exec.Command("git", args...) //nolint:gosec // G204: fixed subcommands; revisions come from the CI event
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
