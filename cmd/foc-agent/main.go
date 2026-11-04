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

// Command foc-agent is the process that talks to the model (docs/DESIGN.md
// §3, §3.5). It runs the agent loop against foc-mcp over MCP (streamable
// HTTP), serves the web chat and the approval-requests panel, and exports
// traces over OTLP when an endpoint is set.
//
// It runs as ServiceAccount foc-agent, which may only get and list
// ApprovalRequests: it cannot write to Swarmada or to the copilot's own
// resources, and it cannot decide a request. The page shows the command a
// person runs to decide one.
//
//	foc-agent -backend claude   (ANTHROPIC_API_KEY; model from -model or FOC_CLAUDE_MODEL)
//	foc-agent -backend llamacpp (-llama-url or FOC_LLAMACPP_URL)
//	foc-agent -backend scripted -script steps.json   (fixed turns, for demos and tests)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tracing"
	"github.com/AlexBahel/fleet-ops-copilot/internal/webui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

type config struct {
	listen, mcpURL, backend, model, effort, llamaURL, script, namespaces, otlp string
	maxTurns, maxConcurrent                                                    int
	chatTimeout                                                                time.Duration
}

func parseFlags(args []string) (config, error) {
	var c config
	fs := flag.NewFlagSet("foc-agent", flag.ContinueOnError)
	fs.StringVar(&c.listen, "listen", ":8090", "HTTP address for the web UI, /healthz and /readyz")
	fs.StringVar(&c.mcpURL, "mcp-url", "http://foc-mcp:8080/mcp", "foc-mcp streamable HTTP endpoint")
	fs.StringVar(&c.backend, "backend", "claude", "model backend: claude, llamacpp or scripted")
	fs.StringVar(&c.model, "model", "", "claude: model id (default $FOC_CLAUDE_MODEL, else "+llm.DefaultClaudeModel+"); llamacpp: label of the served model (default "+llm.DefaultLlamaCppModel+")")
	fs.StringVar(&c.effort, "effort", llm.DefaultClaudeEffort, "claude: output_config.effort")
	fs.StringVar(&c.llamaURL, "llama-url", "", "llamacpp: llama-server base URL (default $"+llm.EnvLlamaCppURL+")")
	fs.StringVar(&c.script, "script", "", "scripted: JSON file {\"steps\":[{\"calls\":[{\"name\":..,\"args\":{..}}]},{\"final\":..}]}, replayed from the start for every request")
	fs.StringVar(&c.namespaces, "namespaces", "warehouse-a", "comma-separated namespaces whose ApprovalRequests the panel shows")
	fs.IntVar(&c.maxTurns, "max-turns", 12, "model turns per request")
	fs.IntVar(&c.maxConcurrent, "max-concurrent", 4, "chat requests in flight; more get HTTP 429")
	fs.DurationVar(&c.chatTimeout, "chat-timeout", 10*time.Minute, "limit for one chat request, end to end")
	fs.StringVar(&c.otlp, "otlp-endpoint", tracing.EndpointFromEnv(), "OTLP/gRPC collector (host:port, http:// or https://); empty disables tracing. Default $OTEL_EXPORTER_OTLP_TRACES_ENDPOINT or $OTEL_EXPORTER_OTLP_ENDPOINT")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if len(splitList(c.namespaces)) == 0 {
		return c, errors.New("-namespaces must name at least one namespace")
	}
	return c, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// backendFactory checks the backend configuration once at start-up and
// returns a constructor called for every request, so no backend state is
// shared between requests.
func backendFactory(c config) (func() (llm.Backend, error), string, error) {
	switch c.backend {
	case "claude":
		model := c.model
		if model == "" {
			model = os.Getenv("FOC_CLAUDE_MODEL")
		}
		b, err := llm.NewClaude(model, c.effort)
		if err != nil {
			return nil, "", err
		}
		return func() (llm.Backend, error) { return llm.NewClaude(model, c.effort) }, b.Name(), nil
	case "llamacpp":
		b, err := llm.NewLlamaCpp(c.llamaURL, c.model)
		if err != nil {
			return nil, "", err
		}
		return func() (llm.Backend, error) { return llm.NewLlamaCpp(c.llamaURL, c.model) }, b.Name(), nil
	case "scripted":
		steps, err := loadScript(c.script)
		if err != nil {
			return nil, "", err
		}
		return func() (llm.Backend, error) { return &llm.Scripted{Steps: steps}, nil }, "scripted", nil
	}
	return nil, "", fmt.Errorf("-backend %q: use claude, llamacpp or scripted", c.backend)
}

type scriptFile struct {
	Steps []struct {
		Calls []struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"calls"`
		Final string `json:"final"`
	} `json:"steps"`
}

// loadScript reads the turns the scripted backend replays. The last step
// must be a final answer, so a request always ends.
func loadScript(path string) ([]llm.Step, error) {
	if path == "" {
		return nil, errors.New("-backend scripted needs -script")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f scriptFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(f.Steps) == 0 || f.Steps[len(f.Steps)-1].Final == "" || len(f.Steps[len(f.Steps)-1].Calls) > 0 {
		return nil, fmt.Errorf("%s: needs at least one step, and the last step must be a final answer with no calls", path)
	}
	steps := make([]llm.Step, 0, len(f.Steps))
	n := 0
	for i, s := range f.Steps {
		if (len(s.Calls) == 0) == (s.Final == "") {
			return nil, fmt.Errorf("%s: step %d must have either calls or a final answer", path, i)
		}
		st := llm.Step{Final: s.Final}
		for _, c := range s.Calls {
			if c.Name == "" {
				return nil, fmt.Errorf("%s: step %d has a call with no name", path, i)
			}
			n++
			args := c.Args
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			st.Calls = append(st.Calls, llm.ToolCall{ID: fmt.Sprintf("call-%d", n), Name: c.Name, Args: args})
		}
		steps = append(steps, st)
	}
	return steps, nil
}

func run(args []string) error {
	c, err := parseFlags(args)
	if err != nil {
		return err
	}
	newBackend, backendName, err := backendFactory(c)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	red := redact.FromEnv()
	tp, shutdownTracing, err := tracing.Setup(ctx, tracing.Config{Endpoint: c.otlp, ServiceName: "foc-agent", Redactor: red})
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(sctx)
	}()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	k, err := client.New(cfg, client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "foc-agent ", log.LstdFlags|log.LUTC)
	srv := &webui.Server{
		Runner: &webui.AgentRunner{
			Transport: webui.MCPURL(c.mcpURL), NewBackend: newBackend, BackendName: backendName,
			MaxTurns: c.maxTurns, Tracer: tracing.Tracer(tp),
		},
		Requests: k, Namespaces: splitList(c.namespaces), MaxConcurrent: c.maxConcurrent,
		ChatTimeout: c.chatTimeout, Redactor: red, Log: logger,
	}
	hs := &http.Server{Addr: c.listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	tracingState := "off"
	if c.otlp != "" {
		tracingState = "OTLP " + c.otlp
	}
	logger.Printf("listening on %s; backend %s; foc-mcp %s; namespaces %s; tracing %s", c.listen, backendName, c.mcpURL, c.namespaces, red.String(tracingState))
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
