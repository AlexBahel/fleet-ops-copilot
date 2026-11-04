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

package webui

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"

	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
)

// AgentRunner answers each request with the agent loop (internal/agent)
// over a fresh MCP session to foc-mcp and a fresh backend, so a foc-mcp
// restart affects only requests in flight and no backend state is shared
// between requests.
type AgentRunner struct {
	// Transport returns a new MCP client transport (streamable HTTP to
	// foc-mcp in the cluster).
	Transport  func() mcp.Transport
	NewBackend func() (llm.Backend, error)
	// BackendName is shown in the page header.
	BackendName string
	MaxTurns    int
	Tracer      trace.Tracer
}

// MCPURL returns a Transport for foc-mcp's streamable HTTP endpoint.
func MCPURL(url string) func() mcp.Transport {
	return func() mcp.Transport { return &mcp.StreamableClientTransport{Endpoint: url} }
}

func (a *AgentRunner) connect(ctx context.Context) (*mcp.ClientSession, error) {
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "foc-agent", Version: mcpserver.Version}, nil).Connect(ctx, a.Transport(), nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to foc-mcp: %w", err)
	}
	return cs, nil
}

// Run implements Runner.
func (a *AgentRunner) Run(ctx context.Context, operator, sessionID, prompt string) (agent.Trace, error) {
	be, err := a.NewBackend()
	if err != nil {
		return agent.Trace{Prompt: prompt}, fmt.Errorf("model backend: %w", err)
	}
	cs, err := a.connect(ctx)
	if err != nil {
		return agent.Trace{Prompt: prompt}, err
	}
	defer func() { _ = cs.Close() }()
	return (&agent.Loop{Backend: be, Session: cs, MaxTurns: a.MaxTurns, Tracer: a.Tracer}).Run(ctx, operator, sessionID, prompt)
}

// Ready implements Runner: foc-mcp accepts a session and answers a ping.
func (a *AgentRunner) Ready(ctx context.Context) error {
	cs, err := a.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cs.Close() }()
	return cs.Ping(ctx, nil)
}

// Backend implements Runner.
func (a *AgentRunner) Backend() string { return a.BackendName }
