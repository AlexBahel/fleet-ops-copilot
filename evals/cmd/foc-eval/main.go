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

// Command foc-eval runs the evaluation scenarios against fixture states in a
// test API server and writes results.json plus one trace line per run. It
// exits non-zero if any run fails (the CI regression gate).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/AlexBahel/fleet-ops-copilot/evals/harness"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
)

func main() {
	scenarios := flag.String("scenarios", "evals/scenarios", "scenario directory")
	fixtures := flag.String("fixtures", "evals/fixtures", "fixture directory")
	runs := flag.Int("runs", 3, "runs per scenario (N)")
	out := flag.String("out", "evals/results/fixtures/results.json", "results file")
	traces := flag.String("traces", "", "trace file (default: traces.jsonl next to -out)")
	policyPath := flag.String("policy", "policies/default.yaml", "policy file")
	backend := flag.String("backend", "scripted", "backend: scripted (claude and llamacpp arrive in a later track)")
	flag.Parse()
	if *backend != "scripted" {
		log.Fatalf("backend %q is not implemented yet; only scripted is available", *backend)
	}
	if *traces == "" {
		*traces = filepath.Join(filepath.Dir(*out), "traces.jsonl")
	}
	if err := run(*scenarios, *fixtures, *runs, *out, *traces, *policyPath); err != nil {
		log.Fatal(err)
	}
}

func swarmadaVersion(dir string) (string, string) {
	spec := "unknown"
	if f, err := os.Open(filepath.Join(dir, "rfcs", "dist", "RFC-0001-core-spec.md")); err == nil {
		sc := bufio.NewScanner(f)
		for i := 0; sc.Scan() && i < 20; i++ {
			if v, ok := strings.CutPrefix(sc.Text(), "swarmada-version:"); ok {
				spec = strings.TrimSpace(v)
			}
		}
		_ = f.Close()
	}
	rev := "unknown"
	if b, err := runIn(dir, exec.Command("git", "rev-parse", "--short=7", "HEAD")); err == nil {
		rev = b
	} else if b, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/swarmada/swarmada").Output(); err == nil {
		rev = strings.TrimSpace(string(b))
	}
	return spec, rev
}

// runIn runs a command with fixed arguments in a directory.
func runIn(dir string, cmd *exec.Cmd) (string, error) {
	cmd.Dir = dir
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}

// copilotCommit is the commit of this repository the results were produced
// from, and whether tracked files differed from it.
func copilotCommit(repo string) (string, bool) {
	sha, err := runIn(repo, exec.Command("git", "rev-parse", "HEAD"))
	if err != nil {
		return "unknown", true
	}
	st, err := runIn(repo, exec.Command("git", "status", "--porcelain", "--untracked-files=no"))
	return sha, err != nil || st != ""
}

func run(scenarioDir, fixtureDir string, runs int, out, tracePath, policyPath string) error {
	ctx := context.Background()
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	scs, err := harness.LoadScenarios(scenarioDir)
	if err != nil {
		return err
	}
	pol, err := policy.Load(policyPath)
	if err != nil {
		return err
	}
	env, err := testenv.Start(repo, testenv.DefaultPeople)
	if err != nil {
		return err
	}
	defer env.Stop()
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	tf, err := os.Create(tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = tf.Close() }()
	r := &harness.Runner{Env: env, Policy: pol, FixtureDir: fixtureDir, Backend: harness.ScriptedFactory, Runs: runs, Traces: tf}
	srs, err := r.Run(ctx, scs)
	if err != nil {
		return err
	}
	spec, rev := swarmadaVersion(env.SwarmadaDir)
	sha, dirty := copilotCommit(repo)
	envInfo := map[string]string{"swarmadaSpec": spec, "swarmadaCommit": rev, "apiServer": "envtest", "copilotCommit": sha, "copilotTreeDirty": fmt.Sprint(dirty)}
	if cs, err := kubernetes.NewForConfig(env.AdminCfg); err == nil {
		if v, err := cs.Discovery().ServerVersion(); err == nil {
			envInfo["apiServer"] = "envtest kube-apiserver " + v.GitVersion
		}
	}
	res := harness.Summarize("scripted", runs, envInfo, srs,
		"scripted backend: tool execution over in-process MCP against envtest; no model is called, so this is not a model latency")
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Printf("%d scenarios x %d runs: %d/%d runs passed; latency p50 %.3f ms, p95 %.3f ms (%s)\n",
		res.Total.Scenarios, runs, res.Total.Pass, res.Total.Runs, res.Latency.P50Ms, res.Latency.P95Ms, "scripted")
	for _, c := range harness.Categories {
		ct := res.ByCategory[c]
		fmt.Printf("  %-9s %d scenarios, %d/%d runs passed\n", c, ct.Scenarios, ct.Pass, ct.Runs)
	}
	fail := false
	for _, sr := range srs {
		for _, rr := range sr.Runs {
			if !rr.Pass {
				fail = true
				fmt.Printf("FAIL %s run %d: %+v %s\n", sr.ID, rr.Run, rr.Verdict, rr.Error)
			}
		}
	}
	fmt.Printf("results: %s\ntraces:  %s\n", out, tracePath)
	if fail {
		os.Exit(1)
	}
	return nil
}
