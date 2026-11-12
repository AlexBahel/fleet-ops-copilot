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

// Command fleetops-eval runs the evaluation scenarios against fixture states in a
// test API server and writes results.json plus one trace line per run.
//
// -backend scripted (the CI regression gate) exits non-zero if any run fails.
// With -baseline it also compares the per-category pass counts with the
// committed baseline (evals/baseline.json) and exits non-zero when a category
// passes fewer runs than recorded or when the scenario or category counts
// differ; -update-baseline rewrites that file from a fully passing run.
// -backend claude calls the Claude API (ANTHROPIC_API_KEY from the
// environment) with -model and -effort; it is a measurement, so failed runs
// are recorded, not fatal. With the -price-* flags it adds the cost.
// -backend llamacpp calls llama-server at -llama-url (default
// $FLEETOPS_LLAMACPP_URL); also a measurement, with no dollar cost. It records
// llama-server's GET /props (context size, slots, build) in backendSettings,
// and with -llama-context the Deployment's arguments and resources, the
// threads the server logged, and the node's capacity (D64).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/AlexBahel/fleet-ops-copilot/evals/harness"
	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/dialogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tracing"
)

// defaultOut is the results file when -out is not given: the ignored directory
// make eval-fixtures writes to (D102), so a run without -out changes no tracked
// file. The committed self-test is written only by make record-selftest, which
// passes -out evals/results/fixtures/results.json.
const defaultOut = "evals/results/tmp/selftest/results.json"

// fixturesDir holds the fixtures the scenarios name.
const fixturesDir = "evals/fixtures"

// The llama-server Deployment (and its container) whose settings a llamacpp
// measurement records with -llama-context, as config/airgap deploys it.
const (
	llamaServerNamespace  = "fleetops-system"
	llamaServerDeployment = "llama-server"
)

func main() {
	scenarios := flag.String("scenarios", "evals/scenarios", "scenario directory")
	runs := flag.Int("runs", 3, "runs per scenario (N)")
	out := flag.String("out", defaultOut, "results file; traces.jsonl is written next to it")
	policyPath := flag.String("policy", "policies/default.yaml", "policy file")
	cataloguePath := flag.String("catalogue", "catalogue/warehouse-a.yaml", "site catalogue fleetops-agent reads (D70); its SHA-256 is recorded")
	backend := flag.String("backend", "scripted", "backend: scripted, claude or llamacpp")
	model := flag.String("model", "", "claude: model id (default "+llm.DefaultClaudeModel+"); llamacpp: label for the served model (default "+llm.DefaultLlamaCppModel+")")
	llamaURL := flag.String("llama-url", "", "llamacpp: base URL (default: $"+llm.EnvLlamaCppURL+")")
	effort := flag.String("effort", llm.DefaultClaudeEffort, "claude: output_config.effort")
	baselinePath := flag.String("baseline", "", "scripted: committed per-category baseline to hold (evals/baseline.json); the run fails below it or when the counts differ")
	updateBaseline := flag.Bool("update-baseline", false, "scripted: rewrite the -baseline file from this run (every run must pass)")
	only := flag.String("only", "", "comma-separated scenario ids to run (default: all)")
	liveCtx := flag.String("live-context", "", "run against this kubeconfig context instead of envtest (state is not loaded; see harness.LiveTarget)")
	liveMCP := flag.String("live-mcp-url", "", "with -live-context: fleetops-mcp streamable HTTP endpoint")
	otlp := flag.String("otlp-endpoint", "", "OTLP/gRPC collector (host:port, http:// or https://): trace each run as services fleetops-eval-agent and fleetops-eval-mcp; the trace ID goes into results.json and traces.jsonl. Empty: no tracing")
	llamaCtx := flag.String("llama-context", "", "llamacpp: kubeconfig context of the cluster running llama-server; its Deployment, pod log and node are recorded")
	rejudge := flag.String("rejudge", "", "judge the answers of a recorded traces.jsonl again with this judge, no backend called (criteria 1, 3 and 4 from the trace, harness.Rejudge); -scenarios must be the files the runs were made with. Writes one JSON line per run to -rejudge-out")
	rejudgeOut := flag.String("rejudge-out", "", "with -rejudge: output file")
	var pr harness.Pricing
	flag.Float64Var(&pr.InputPerMTok, "price-input", 0, "claude: USD per million input tokens")
	flag.Float64Var(&pr.OutputPerMTok, "price-output", 0, "claude: USD per million output tokens")
	flag.Float64Var(&pr.CacheWritePerMTok, "price-cache-write", 0, "claude: USD per million cache-write input tokens")
	flag.Float64Var(&pr.CacheReadPerMTok, "price-cache-read", 0, "claude: USD per million cache-read input tokens")
	flag.StringVar(&pr.Source, "pricing-source", "", "claude: pricing page URL")
	flag.StringVar(&pr.ReadOn, "pricing-read-on", "", "claude: date the pricing page was read (YYYY-MM-DD)")
	flag.Parse()
	traces := filepath.Join(filepath.Dir(*out), "traces.jsonl")
	if *rejudge != "" {
		if err := runRejudge(*scenarios, fixturesDir, *rejudge, *rejudgeOut); err != nil {
			log.Fatal(err)
		}
		return
	}
	cfg := runConfig{scenarioDir: *scenarios, fixtureDir: fixturesDir, runs: *runs, out: *out, tracePath: traces, policyPath: *policyPath, cataloguePath: *cataloguePath, baseline: *baselinePath, updateBaseline: *updateBaseline, only: *only, liveCtx: *liveCtx, liveMCP: *liveMCP, otlp: *otlp}
	switch *backend {
	case "scripted":
		cfg.name, cfg.factory = "scripted", harness.ScriptedFactory
		cfg.latencyScope = "scripted backend: tool execution over MCP (streamable HTTP on loopback, bearer token verified by TokenReview) against envtest; no model is called, so this is not a model latency"
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
		cfg.envExtra = hostHardware()
		cfg.name = "claude:" + *model + " effort=" + *effort
		cfg.factory = func(harness.Scenario) (llm.Backend, error) { return llm.NewClaude(*model, *effort) }
		cfg.latencyScope = "Claude API (" + *model + ", effort " + *effort + ") from this machine, plus tool execution over MCP (streamable HTTP on loopback) against envtest; request sent until final answer"
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
		srv, envExtra, err := llamaServerSettings(l.BaseURL, *llamaCtx, llamaServerNamespace, llamaServerDeployment)
		if err != nil {
			log.Fatal(err)
		}
		mergeInto(cfg.settings, srv)
		cfg.envExtra = envExtra
		cfg.factory = func(harness.Scenario) (llm.Backend, error) { return llm.NewLlamaCpp(*llamaURL, *model) }
		cfg.latencyScope = "llama-server at " + l.BaseURL + " from this machine, plus tool execution over MCP (streamable HTTP on loopback) against envtest; request sent until final answer"
	default:
		log.Fatalf("backend %q is not implemented; use scripted, claude or llamacpp", *backend)
	}
	if cfg.baseline != "" && (!cfg.gate || cfg.only != "" || cfg.liveCtx != "") {
		log.Fatal("-baseline applies to the whole scripted run: not with another backend, -only or -live-context")
	}
	if cfg.updateBaseline && cfg.baseline == "" {
		log.Fatal("-update-baseline needs -baseline")
	}
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

// runRejudge judges recorded answers again (harness.Rejudge) and prints
// how many verdicts changed.
func runRejudge(scenarioDir, fixtureDir, tracesPath, outPath string) error {
	if outPath == "" {
		return fmt.Errorf("-rejudge needs -rejudge-out")
	}
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	scs, err := harness.LoadScenarios(scenarioDir)
	if err != nil {
		return err
	}
	env, err := testenv.Start(repo, testenv.DefaultPeople)
	if err != nil {
		return err
	}
	defer env.Stop()
	in, err := os.Open(tracesPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	n, err := (&harness.Runner{Env: env, FixtureDir: fixtureDir}).Rejudge(context.Background(), scs, in, out)
	fmt.Printf("rejudged %d runs of %s with judge %s into %s\n", n, tracesPath, harness.JudgeVersion, outPath)
	return err
}

// llamaServerSettings returns the server-side settings for backendSettings
// and the host and node descriptions for environment. /props must answer:
// a measurement without the server's context size is not recorded.
func llamaServerSettings(base, kubeCtx, ns, deploy string) (map[string]string, map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv, err := llamaProps(ctx, &http.Client{Timeout: 20 * time.Second}, base)
	if err != nil {
		return nil, nil, err
	}
	env := hostHardware()
	if kubeCtx == "" {
		srv["server.deployment"] = "not read (no -llama-context)"
		return srv, env, nil
	}
	cfg, err := config.GetConfigWithContext(kubeCtx)
	if err != nil {
		return nil, nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	d, node, err := llamaDeployment(ctx, cs, ns, deploy)
	if err != nil {
		return nil, nil, fmt.Errorf("-llama-context %s: %w", kubeCtx, err)
	}
	mergeInto(srv, d)
	mergeInto(env, node)
	return srv, env, nil
}

type runConfig struct {
	scenarioDir, fixtureDir, out, tracePath, policyPath, only string
	cataloguePath                                             string
	baseline                                                  string
	updateBaseline                                            bool
	liveCtx, liveMCP                                          string
	otlp                                                      string
	runs                                                      int
	name, latencyScope                                        string
	factory                                                   harness.BackendFactory
	pricing                                                   *harness.Pricing
	settings                                                  map[string]string
	envExtra                                                  map[string]string // host and model node, model backends only
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
	cat, err := catalogue.Load(c.cataloguePath)
	if err != nil {
		return err
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
	r := &harness.Runner{Env: env, Policy: pol, FixtureDir: fixtureDir, Backend: c.factory, Runs: runs, Live: live, Traces: tf, Catalogue: cat}
	if live == nil {
		// Fixture state is fixed in time; so is the clock (D81).
		r.Now = func() time.Time { return harness.FixtureClock }
	}
	if c.otlp != "" {
		red := redact.FromEnv()
		for _, t := range []struct {
			service string
			dst     *trace.Tracer
		}{{"fleetops-eval-agent", &r.AgentTracer}, {"fleetops-eval-mcp", &r.MCPTracer}} {
			tp, shutdown, err := tracing.Setup(ctx, tracing.Config{Endpoint: c.otlp, ServiceName: t.service, Redactor: red})
			if err != nil {
				return err
			}
			defer func() {
				sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := shutdown(sctx); err != nil {
					log.Printf("tracing: flushing %s: %v", t.service, err)
				}
			}()
			*t.dst = tracing.Tracer(tp)
		}
		fmt.Printf("tracing: OTLP %s (services fleetops-eval-agent, fleetops-eval-mcp)\n", red.String(c.otlp))
	}
	srs, err := r.Run(ctx, scs)
	if err != nil {
		return err
	}
	swDir, _ := testenv.SwarmadaDir(repo)
	spec, rev := swarmadaVersion(swDir)
	envInfo := map[string]string{"swarmadaSpec": spec, "swarmadaCommit": rev, "swarmadaResolution": swarmadaResolution(), "apiServer": "envtest", "copilotCommit": sha, "copilotTreeDirty": fmt.Sprint(dirty)}
	kind := "envtest kube-apiserver "
	if live != nil {
		kind = "live cluster (Swarmada controllers and the deployed fleetops-mcp; state from the scenario's quickstart run, not loaded from the fixture) kube-apiserver "
		envInfo["fixtures"] = "not loaded: the namespace's live state is used"
	}
	if cs, err := kubernetes.NewForConfig(adminCfg); err == nil {
		if v, err := cs.Discovery().ServerVersion(); err == nil {
			envInfo["apiServer"] = kind + v.GitVersion
		}
	}
	mergeInto(envInfo, c.envExtra)
	if c.otlp != "" {
		envInfo["tracing"] = "OTLP/gRPC export on (services fleetops-eval-agent, fleetops-eval-mcp)"
	}
	res := harness.Summarize(c.name, runs, envInfo, srs, c.latencyScope)
	res.BackendSettings = c.settings
	// The tool list as the harness's fleetops-mcp serves it (D66): with
	// -live-context the deployed fleetops-mcp serves its own, from its flags.
	digest, err := mcpserver.ToolsDigest(ctx, mcpserver.New(&tools.Service{Engine: policy.New(pol), DefaultNamespace: testenv.SwarmNS}), agent.ProposeMCPTool(cat, dialogue.AllowedBy(pol)))
	if err != nil {
		return err
	}
	res.SystemPrompt["toolsSha256"] = digest
	res.SystemPrompt["judge"] = harness.JudgeVersion
	res.SystemPrompt["catalogue"] = c.cataloguePath
	res.SystemPrompt["catalogueVersion"] = fmt.Sprint(cat.Version)
	res.SystemPrompt["catalogueSha256"] = cat.SHA256
	if live == nil {
		res.Environment["clock"] = harness.FixtureClock.Format(time.RFC3339) + " (fixed for fixture runs)"
	}
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
	if c.baseline != "" {
		if c.updateBaseline {
			nb, err := harness.BaselineFrom(res)
			if err != nil {
				return err
			}
			if err := nb.Write(c.baseline); err != nil {
				return err
			}
			fmt.Printf("baseline: %s rewritten (%d scenarios, %d runs)\n", c.baseline, nb.Total.Scenarios, nb.Total.Runs)
		} else {
			base, err := harness.LoadBaseline(c.baseline)
			if err != nil {
				return err
			}
			if v := base.Check(res); len(v) > 0 {
				for _, line := range v {
					fmt.Printf("BASELINE %s\n", line)
				}
				fmt.Printf("baseline: %s NOT held\n", c.baseline)
				fail = true
			} else {
				fmt.Printf("baseline: %s held (%d scenarios, %d/%d runs)\n", c.baseline, base.Total.Scenarios, res.Total.Pass, res.Total.Runs)
			}
		}
	}
	if fail && c.gate {
		os.Exit(1)
	}
	return nil
}
