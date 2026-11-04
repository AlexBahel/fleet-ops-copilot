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
// test API server and writes results.json plus one trace line per run.
//
// -backend scripted (the CI regression gate) exits non-zero if any run fails.
// -backend claude calls the Claude API (ANTHROPIC_API_KEY from the
// environment) with -model and -effort; it is a measurement, so failed runs
// are recorded, not fatal. With the -price-* flags it adds the cost.
// -backend llamacpp calls llama-server at -llama-url (default
// $FOC_LLAMACPP_URL); also a measurement, with no dollar cost.
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
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/AlexBahel/fleet-ops-copilot/evals/harness"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
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
	backend := flag.String("backend", "scripted", "backend: scripted, claude or llamacpp")
	model := flag.String("model", "", "claude: model id (default "+llm.DefaultClaudeModel+"); llamacpp: label for the served model (default "+llm.DefaultLlamaCppModel+")")
	llamaURL := flag.String("llama-url", "", "llamacpp: base URL (default: $"+llm.EnvLlamaCppURL+")")
	effort := flag.String("effort", llm.DefaultClaudeEffort, "claude: output_config.effort")
	only := flag.String("only", "", "comma-separated scenario ids to run (default: all)")
	liveCtx := flag.String("live-context", "", "run against this kubeconfig context instead of envtest (state is not loaded; see harness.LiveTarget)")
	liveMCP := flag.String("live-mcp-url", "", "with -live-context: foc-mcp streamable HTTP endpoint")
	var pr harness.Pricing
	flag.Float64Var(&pr.InputPerMTok, "price-input", 0, "claude: USD per million input tokens")
	flag.Float64Var(&pr.OutputPerMTok, "price-output", 0, "claude: USD per million output tokens")
	flag.Float64Var(&pr.CacheWritePerMTok, "price-cache-write", 0, "claude: USD per million cache-write input tokens")
	flag.Float64Var(&pr.CacheReadPerMTok, "price-cache-read", 0, "claude: USD per million cache-read input tokens")
	flag.StringVar(&pr.Source, "pricing-source", "", "claude: pricing page URL")
	flag.StringVar(&pr.ReadOn, "pricing-read-on", "", "claude: date the pricing page was read (YYYY-MM-DD)")
	flag.Parse()
	if *traces == "" {
		*traces = filepath.Join(filepath.Dir(*out), "traces.jsonl")
	}
	cfg := runConfig{scenarioDir: *scenarios, fixtureDir: *fixtures, runs: *runs, out: *out, tracePath: *traces, policyPath: *policyPath, only: *only, liveCtx: *liveCtx, liveMCP: *liveMCP}
	switch *backend {
	case "scripted":
		cfg.name, cfg.factory = "scripted", harness.ScriptedFactory
		cfg.latencyScope = "scripted backend: tool execution over in-process MCP against envtest; no model is called, so this is not a model latency"
		cfg.gate = true
	case "claude":
		if *model == "" {
			*model = llm.DefaultClaudeModel
		}
		c, err := llm.NewClaude(*model, *effort)
		if err != nil {
			log.Fatal(err)
		}
		cfg.settings = c.Settings()
		cfg.name = "claude:" + *model + " effort=" + *effort
		cfg.factory = func(harness.Scenario) (llm.Backend, error) { return llm.NewClaude(*model, *effort) }
		cfg.latencyScope = "Claude API (" + *model + ", effort " + *effort + ") from this machine, plus tool execution over in-process MCP against envtest; request sent until final answer"
		if pr.Source != "" {
			pr.Model = *model
			cfg.pricing = &pr
		}
	case "llamacpp":
		l, err := llm.NewLlamaCpp(*llamaURL, *model)
		if err != nil {
			log.Fatal(err)
		}
		*model = l.Model
		cfg.settings = l.Settings()
		cfg.name = fmt.Sprintf("llamacpp:%s temperature=%g", l.Model, l.Temperature)
		if l.TopP != nil {
			cfg.name += fmt.Sprintf(" top_p=%g", *l.TopP)
		}
		if l.TopK != nil {
			cfg.name += fmt.Sprintf(" top_k=%d", *l.TopK)
		}
		if l.RepeatPenalty != nil {
			cfg.name += fmt.Sprintf(" repeat_penalty=%g", *l.RepeatPenalty)
		}
		cfg.factory = func(harness.Scenario) (llm.Backend, error) { return llm.NewLlamaCpp(*llamaURL, *model) }
		cfg.latencyScope = "llama-server at " + l.BaseURL + " from this machine, plus tool execution over in-process MCP against envtest; request sent until final answer"
	default:
		log.Fatalf("backend %q is not implemented; use scripted, claude or llamacpp", *backend)
	}
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

type runConfig struct {
	scenarioDir, fixtureDir, out, tracePath, policyPath, only string
	liveCtx, liveMCP                                          string
	runs                                                      int
	name, latencyScope                                        string
	factory                                                   harness.BackendFactory
	pricing                                                   *harness.Pricing
	settings                                                  map[string]string
	gate                                                      bool
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
	if swarmadaResolution() == resolutionPin {
		if b, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/swarmada/swarmada").Output(); err == nil {
			rev = pinnedRevision(strings.TrimSpace(string(b)))
		}
	} else if b, err := runIn(dir, exec.Command("git", "rev-parse", "--short=7", "HEAD")); err == nil {
		rev = b
	}
	return spec, rev
}

// pinnedRevision returns the commit a go.mod version names: the revision
// field of a pseudo-version (v0.0.0-20261104205854-558da47fdb44 gives
// 558da47fdb44), or the version itself when it is a tag.
func pinnedRevision(version string) string {
	i := strings.LastIndexByte(version, '-')
	if i < 0 {
		return version
	}
	rev := strings.TrimSuffix(version[i+1:], "+incompatible")
	if len(rev) != 12 || strings.Trim(rev, "0123456789abcdef") != "" {
		return version
	}
	return rev
}

const resolutionPin = "go.mod pin"

// swarmadaResolution says how the Swarmada module was resolved: through the
// go.mod pin, or through a local go.work that overrides it.
func swarmadaResolution() string {
	if b, err := exec.Command("go", "env", "GOWORK").Output(); err == nil && strings.TrimSpace(string(b)) != "" {
		return "local go.work (overrides the go.mod pin)"
	}
	return resolutionPin
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

func run(c runConfig) error {
	scenarioDir, fixtureDir, runs, out, tracePath, policyPath := c.scenarioDir, c.fixtureDir, c.runs, c.out, c.tracePath, c.policyPath
	ctx := context.Background()
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	// Read before any output file is written: the outputs are tracked files,
	// and rewriting them must not count as a dirty tree.
	sha, dirty := copilotCommit(repo)
	scs, err := harness.LoadScenarios(scenarioDir)
	if err != nil {
		return err
	}
	if c.only != "" {
		keep := map[string]bool{}
		for _, id := range strings.Split(c.only, ",") {
			keep[strings.TrimSpace(id)] = true
		}
		var sel []harness.Scenario
		for _, s := range scs {
			if keep[s.ID] {
				sel = append(sel, s)
			}
		}
		scs = sel
	}
	pol, err := policy.Load(policyPath)
	if err != nil {
		return err
	}
	var env *testenv.Env
	var live *harness.LiveTarget
	var adminCfg *rest.Config
	if c.liveCtx != "" {
		if c.liveMCP == "" {
			return fmt.Errorf("-live-context needs -live-mcp-url")
		}
		if adminCfg, err = config.GetConfigWithContext(c.liveCtx); err != nil {
			return err
		}
		admin, err := client.New(adminCfg, client.Options{Scheme: fleet.NewScheme()})
		if err != nil {
			return err
		}
		live = &harness.LiveTarget{Admin: admin, MCPURL: c.liveMCP}
	} else {
		if env, err = testenv.Start(repo, testenv.DefaultPeople); err != nil {
			return err
		}
		defer env.Stop()
		adminCfg = env.AdminCfg
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	tf, err := os.Create(tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = tf.Close() }()
	r := &harness.Runner{Env: env, Policy: pol, FixtureDir: fixtureDir, Backend: c.factory, Runs: runs, Live: live, Traces: tf}
	srs, err := r.Run(ctx, scs)
	if err != nil {
		return err
	}
	swDir, _ := testenv.SwarmadaDir(repo)
	spec, rev := swarmadaVersion(swDir)
	envInfo := map[string]string{"swarmadaSpec": spec, "swarmadaCommit": rev, "swarmadaResolution": swarmadaResolution(), "apiServer": "envtest", "copilotCommit": sha, "copilotTreeDirty": fmt.Sprint(dirty)}
	kind := "envtest kube-apiserver "
	if live != nil {
		kind = "live cluster (Swarmada controllers and the deployed foc-mcp; state from the scenario's quickstart run, not loaded from the fixture) kube-apiserver "
		envInfo["fixtures"] = "not loaded: the namespace's live state is used"
	}
	if cs, err := kubernetes.NewForConfig(adminCfg); err == nil {
		if v, err := cs.Discovery().ServerVersion(); err == nil {
			envInfo["apiServer"] = kind + v.GitVersion
		}
	}
	res := harness.Summarize(c.name, runs, envInfo, srs, c.latencyScope)
	res.BackendSettings = c.settings
	if c.pricing != nil {
		res.SetCost(*c.pricing)
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Printf("%d scenarios x %d runs: %d/%d runs passed; latency p50 %.3f ms, p95 %.3f ms (%s)\n",
		res.Total.Scenarios, runs, res.Total.Pass, res.Total.Runs, res.Latency.P50Ms, res.Latency.P95Ms, c.name)
	fmt.Printf("tokens: input %d, output %d, cache write %d, cache read %d\n", res.Tokens.InputTokens, res.Tokens.OutputTokens, res.Tokens.CacheCreationInputTokens, res.Tokens.CacheReadInputTokens)
	if res.Cost != nil {
		fmt.Printf("cost: %.4f USD total, %.4f USD per 100 requests (%s, read %s)\n", res.Cost.TotalUSD, res.Cost.Per100RequestsUSD, res.Cost.Pricing.Source, res.Cost.Pricing.ReadOn)
	}
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
	if fail && c.gate {
		os.Exit(1)
	}
	return nil
}
