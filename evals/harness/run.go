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
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/authn"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// BackendFactory returns a fresh backend for one run of a scenario.
type BackendFactory func(Scenario) (llm.Backend, error)

// ScriptedFactory replays each scenario's script.
func ScriptedFactory(s Scenario) (llm.Backend, error) { return s.ScriptedBackend() }

// Runner executes scenarios against a test API server.
type Runner struct {
	Env        *testenv.Env
	Policy     *policy.File
	FixtureDir string
	Backend    BackendFactory
	Runs       int
	// Traces, when set, receives one JSON line per run.
	Traces io.Writer
	// Live, when set, runs against a live cluster instead of Env (see LiveTarget).
	Live *LiveTarget
}

// LiveTarget is a live cluster whose namespace already holds the scenario's
// state (for example a Swarmada quickstart run with the matching SCENARIO),
// with foc-mcp deployed. A live run loads no fixture and deletes no Swarmada
// object: before and after each run it deletes only the copilot's own
// ApprovalRequests and ApprovalDecisions in the namespace. Facts are checked
// against the live objects at the end of the run.
type LiveTarget struct {
	Admin  client.Client
	MCPURL string
}

func (l *LiveTarget) clearCopilotObjects(ctx context.Context) error {
	for _, k := range []client.Object{&focv1.ApprovalDecision{}, &focv1.ApprovalRequest{}} {
		if err := l.Admin.DeleteAllOf(ctx, k, client.InNamespace(testenv.SwarmNS)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// RunResult is one run of one scenario.
type RunResult struct {
	Run       int       `json:"run"`
	Pass      bool      `json:"pass"`
	Verdict   Verdict   `json:"verdict"`
	LatencyMs float64   `json:"latencyMs"`
	ToolCalls []string  `json:"toolCalls"`
	Usage     llm.Usage `json:"usage"`
	// StopReasons are the backend's per-turn stop reasons (claude only).
	StopReasons []string    `json:"stopReasons,omitempty"`
	Turns       int         `json:"turns"`
	Error       string      `json:"error,omitempty"`
	Trace       agent.Trace `json:"-"`
}

// ScenarioResult aggregates the runs of one scenario.
type ScenarioResult struct {
	ID        string      `json:"id"`
	Category  string      `json:"category"`
	Fixture   string      `json:"fixture"`
	Title     string      `json:"title"`
	Runs      []RunResult `json:"runs"`
	PassCount int         `json:"passCount"`
}

// Run executes every scenario Runs times.
func (r *Runner) Run(ctx context.Context, scenarios []Scenario) ([]ScenarioResult, error) {
	var out []ScenarioResult
	for _, s := range scenarios {
		sr := ScenarioResult{ID: s.ID, Category: s.Category, Fixture: s.Fixture, Title: s.Title}
		for i := 1; i <= r.Runs; i++ {
			rr, err := r.runOnce(ctx, s, i)
			if err != nil {
				return nil, fmt.Errorf("%s run %d: %w", s.ID, i, err)
			}
			if rr.Pass {
				sr.PassCount++
			}
			sr.Runs = append(sr.Runs, rr)
		}
		out = append(out, sr)
	}
	return out, nil
}

func (r *Runner) runOnce(ctx context.Context, s Scenario, n int) (RunResult, error) {
	if r.Live != nil {
		return r.runLive(ctx, s, n)
	}
	admin := r.Env.Admin
	if err := Reset(ctx, admin); err != nil {
		return RunResult{}, err
	}
	if err := LoadFixture(ctx, admin, filepath.Join(r.FixtureDir, s.Fixture+".yaml")); err != nil {
		return RunResult{}, err
	}
	mcpClient, err := r.Env.SAClient(testenv.MCPUser)
	if err != nil {
		return RunResult{}, err
	}
	scheme := fleet.NewScheme()
	svc := &tools.Service{
		C: mcpClient, Engine: policy.New(r.Policy), Audit: audit.NewWriter(io.Discard, redact.New()),
		Identity: testenv.MCPUser, DefaultNamespace: testenv.SwarmNS,
		Events: &audit.EventSink{C: mcpClient, Scheme: scheme, Component: "foc-mcp", Red: redact.New()},
		Authn:  &authn.TokenReviewer{C: mcpClient},
	}
	// The operator authenticates as in a deployment (R39): foc-mcp is served
	// over streamable HTTP on loopback, and the client sends a bearer token
	// minted with the TokenRequest API for a ServiceAccount named after the
	// operator, which foc-mcp verifies with a TokenReview as itself.
	token, err := r.Env.MintToken(ctx, testenv.OperatorNS, s.Operator)
	if err != nil {
		return RunResult{}, err
	}
	s.OperatorIdentity = testenv.OperatorIdentity(s.Operator)
	ts := httptest.NewServer(mcpserver.Handler(mcpserver.New(svc)))
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "foc-eval", Version: mcpserver.Version}, nil).
		Connect(ctx, mcpserver.ClientTransport(ts.URL, token), nil)
	if err != nil {
		return RunResult{}, err
	}
	defer func() { _ = cs.Close() }()
	be, err := r.Backend(s)
	if err != nil {
		return RunResult{}, err
	}
	loop := &agent.Loop{Backend: be, Session: cs}
	tr, runErr := loop.Run(ctx, s.Operator, fmt.Sprintf("eval-%s-%d", s.ID, n), s.Prompt)
	return r.finish(ctx, admin, s, n, tr, runErr)
}

func (r *Runner) runLive(ctx context.Context, s Scenario, n int) (RunResult, error) {
	if err := r.Live.clearCopilotObjects(ctx); err != nil {
		return RunResult{}, err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "foc-eval", Version: mcpserver.Version}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: r.Live.MCPURL}, nil)
	if err != nil {
		return RunResult{}, fmt.Errorf("connecting to %s: %w", r.Live.MCPURL, err)
	}
	defer func() { _ = cs.Close() }()
	be, err := r.Backend(s)
	if err != nil {
		return RunResult{}, err
	}
	loop := &agent.Loop{Backend: be, Session: cs}
	tr, runErr := loop.Run(ctx, s.Operator, fmt.Sprintf("eval-%s-%d", s.ID, n), s.Prompt)
	rr, err := r.finish(ctx, r.Live.Admin, s, n, tr, runErr)
	if err != nil {
		return rr, err
	}
	return rr, r.Live.clearCopilotObjects(ctx)
}

func (r *Runner) finish(ctx context.Context, admin client.Client, s Scenario, n int, tr agent.Trace, runErr error) (RunResult, error) {
	rr := RunResult{Run: n, LatencyMs: tr.LatencyMs, ToolCalls: tr.ToolCalls(), Usage: tr.Usage, StopReasons: tr.StopReasons, Turns: tr.Turns, Trace: tr}
	if runErr != nil {
		rr.Error = runErr.Error()
	}
	ob, err := Observe(ctx, admin, s, tr.Final)
	if err != nil {
		return RunResult{}, err
	}
	rr.Verdict = Judge(s, tr, ob)
	rr.Pass = rr.Verdict.Pass && runErr == nil
	if r.Traces != nil {
		if err := writeTrace(r.Traces, s.ID, n, tr, rr.Verdict); err != nil {
			return RunResult{}, err
		}
	}
	return rr, nil
}

// Percentile is the nearest-rank percentile of xs (p in (0,100]).
func Percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64{}, xs...)
	sort.Float64s(s)
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	return s[rank-1]
}

// Results is the committed evaluation record (evals/results/.../results.json).
type Results struct {
	Schema      string    `json:"schema"`
	Purpose     string    `json:"purpose"`
	GeneratedAt time.Time `json:"generatedAt"`
	Backend     string    `json:"backend"`
	// BackendSettings lists every sampling, thinking and effort field the
	// backend sends, or that it leaves to the server default
	// (llm.SettingsReporter). Empty for the scripted backend.
	BackendSettings map[string]string `json:"backendSettings,omitempty"`
	RunsPerScenario int               `json:"runsPerScenario"`
	// SystemPrompt identifies the system prompt sent to the model
	// (agent.SystemPromptVersion and the SHA-256 of agent.SystemPrompt).
	SystemPrompt map[string]string        `json:"systemPrompt"`
	Environment  map[string]string        `json:"environment"`
	Definitions  map[string]string        `json:"definitions"`
	Scenarios    []ScenarioResult         `json:"scenarios"`
	ByCategory   map[string]CategoryTotal `json:"byCategory"`
	Total        CategoryTotal            `json:"total"`
	Latency      LatencySummary           `json:"latency"`
	// Tokens sums the usage the backend reported over every run.
	Tokens llm.Usage `json:"tokens"`
	// Cost is set for priced backends (claude); see Pricing.
	Cost *CostSummary `json:"cost,omitempty"`
}

// Pricing is the published per-million-token price used for Cost.
type Pricing struct {
	Model             string  `json:"model"`
	InputPerMTok      float64 `json:"inputUSDPerMTok"`
	OutputPerMTok     float64 `json:"outputUSDPerMTok"`
	CacheWritePerMTok float64 `json:"cacheWriteUSDPerMTok"`
	CacheReadPerMTok  float64 `json:"cacheReadUSDPerMTok"`
	Source            string  `json:"source"`
	ReadOn            string  `json:"readOn"`
}

// CostSummary is tokens x Pricing. One request is one scenario run (one
// operator request answered end to end, all model turns included).
type CostSummary struct {
	Pricing           Pricing `json:"pricing"`
	Requests          int     `json:"requests"`
	TotalUSD          float64 `json:"totalUSD"`
	Per100RequestsUSD float64 `json:"per100RequestsUSD"`
}

// PriceUsage returns the USD cost of u under p.
func PriceUsage(u llm.Usage, p Pricing) float64 {
	return (float64(u.InputTokens)*p.InputPerMTok + float64(u.OutputTokens)*p.OutputPerMTok +
		float64(u.CacheCreationInputTokens)*p.CacheWritePerMTok + float64(u.CacheReadInputTokens)*p.CacheReadPerMTok) / 1e6
}

// SetCost prices the total tokens.
func (r *Results) SetCost(p Pricing) {
	total := PriceUsage(r.Tokens, p)
	c := &CostSummary{Pricing: p, Requests: r.Total.Runs, TotalUSD: math.Round(total*1e6) / 1e6}
	if r.Total.Runs > 0 {
		c.Per100RequestsUSD = math.Round(total/float64(r.Total.Runs)*100*1e4) / 1e4
	}
	r.Cost = c
}

// CategoryTotal counts runs.
type CategoryTotal struct {
	Scenarios int `json:"scenarios"`
	Runs      int `json:"runs"`
	Pass      int `json:"pass"`
}

// LatencySummary is p50/p95 over every run.
type LatencySummary struct {
	Scope string  `json:"scope"`
	N     int     `json:"n"`
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
}

// Summarize builds the results record.
func Summarize(backend string, runs int, env map[string]string, srs []ScenarioResult, latencyScope string) Results {
	res := Results{
		Schema: "fleetops.alexbahel.github.io/eval-results/v1", GeneratedAt: time.Now().UTC().Truncate(time.Second),
		Backend: backend, RunsPerScenario: runs, Environment: env, Scenarios: srs, ByCategory: map[string]CategoryTotal{},
		Purpose: purposeFor(backend),
		SystemPrompt: map[string]string{
			"version": agent.SystemPromptVersion,
			"sha256":  fmt.Sprintf("%x", sha256.Sum256([]byte(agent.SystemPrompt))),
		},
		Definitions: map[string]string{
			"pass":    "all of: required tools called without error (a get_* is also met by the same-view list_*) and no forbidden tool called; the expected ApprovalRequest/draft exists and no other write exists; when a refusal is expected: no request or draft exists, a refusal is stated, one real fact is cited (an existing Kind/name or a policy code returned in the run), and the expected policy code was returned if a request_* call was attempted; every required fact holds in the fixture and is cited as Kind/name, field and value, a scenario with no required fact cites one real fact, an absent object is named and every item its list tool returned is cited, and every Kind/name in the answer exists unless the prompt or the run's own DraftTask request introduced it (docs/DESIGN.md §4.1, evals/harness/judge.go)",
			"latency": "request sent to the backend until its final answer, per run (internal/agent/agent.go Loop.Run); percentiles by nearest rank",
		},
	}
	if res.Environment == nil {
		res.Environment = map[string]string{}
	}
	res.Environment["goos/goarch"] = runtime.GOOS + "/" + runtime.GOARCH
	var lat []float64
	for _, sr := range srs {
		ct := res.ByCategory[sr.Category]
		ct.Scenarios++
		for _, r := range sr.Runs {
			ct.Runs++
			res.Total.Runs++
			if r.Pass {
				ct.Pass++
				res.Total.Pass++
			}
			lat = append(lat, r.LatencyMs)
			res.Tokens.Add(r.Usage)
		}
		res.ByCategory[sr.Category] = ct
		res.Total.Scenarios++
	}
	res.Latency = LatencySummary{Scope: latencyScope, N: len(lat), P50Ms: round3(Percentile(lat, 50)), P95Ms: round3(Percentile(lat, 95))}
	return res
}

func purposeFor(backend string) string {
	if backend == "scripted" {
		return "harness self-test: the scripted backend replays fixed turns, so these results test the harness, tools, policy and approval path, not a model"
	}
	return "model evaluation"
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
