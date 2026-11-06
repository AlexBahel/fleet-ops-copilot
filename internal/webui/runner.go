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
	"errors"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"

	"sigs.k8s.io/controller-runtime/pkg/client"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/conversation"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
)

// ChatInput is one person message.
type ChatInput struct {
	// Token is the caller's bearer token, forwarded to foc-mcp ("" for the page).
	Token     string
	Operator  string
	SessionID string
	// ConversationID continues a conversation of the same person (D75).
	ConversationID string
	// FollowUp names an ApprovalRequest whose prepared follow-up starts this
	// conversation at its preview (D78).
	FollowUp string
	Message  string
}

// ChatOutput is the answer to one message.
type ChatOutput struct {
	Trace          agent.Trace
	ConversationID string
	// Notice says, when it applies, that the given conversation could not be
	// continued and a new one was started.
	Notice string
}

// AgentRunner answers each message with the agent (internal/agent) over a
// fresh MCP session to foc-mcp, so a foc-mcp restart affects only messages
// in flight. Conversations, with their dialogue state, are kept in Store,
// bound to the requester foc-mcp verified (D75).
type AgentRunner struct {
	// Transport returns a new MCP client transport (streamable HTTP to
	// foc-mcp in the cluster) that sends token, if any, as the caller's
	// bearer token; foc-mcp verifies it (R39).
	Transport  func(token string) mcp.Transport
	NewBackend func() (llm.Backend, error)
	// BackendName is shown in the page header.
	BackendName string
	MaxTurns    int
	Tracer      trace.Tracer
	// Catalogue enables guided requests (D70); nil leaves plain answers.
	Catalogue *catalogue.Catalogue
	// Store holds conversations; nil makes one.
	Store *conversation.Store
	// Audit receives catalogue_review entries.
	Audit *audit.Writer
	// Requests reads an ApprovalRequest for a follow-up.
	Requests client.Reader
	// Redact masks secrets in the stored conversation text.
	Redact func(string) string

	once sync.Once
}

// MCPURL returns a Transport for foc-mcp's streamable HTTP endpoint.
func MCPURL(url string) func(token string) mcp.Transport {
	return func(token string) mcp.Transport { return mcpserver.ClientTransport(url, token) }
}

func (a *AgentRunner) connect(ctx context.Context, token string) (*mcp.ClientSession, error) {
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "foc-agent", Version: mcpserver.Version}, nil).Connect(ctx, a.Transport(token), nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to foc-mcp: %w", err)
	}
	return cs, nil
}

// ErrBusy means another message of the same conversation is in flight.
var ErrBusy = errors.New("another message of this conversation is still being answered")

// Run implements Runner.
func (a *AgentRunner) Run(ctx context.Context, in ChatInput) (ChatOutput, error) {
	a.once.Do(func() {
		if a.Store == nil {
			a.Store = conversation.New()
		}
	})
	cs, err := a.connect(ctx, in.Token)
	if err != nil {
		return ChatOutput{Trace: agent.Trace{Prompt: in.Message}}, err
	}
	defer func() { _ = cs.Close() }()
	who, err := agent.Whoami(ctx, cs, in.Operator)
	if err != nil {
		return ChatOutput{Trace: agent.Trace{Prompt: in.Message}}, fmt.Errorf("whoami: %w", err)
	}
	out := ChatOutput{ConversationID: in.ConversationID}
	var conv *conversation.Conversation
	if in.ConversationID != "" {
		conv, _ = a.Store.Get(in.ConversationID, who.Requester)
	}
	if conv == nil || in.FollowUp != "" {
		if in.ConversationID != "" && conv == nil {
			out.Notice = "That conversation could not be continued (not yours, or expired); this is a new one."
		}
		be, err := a.NewBackend()
		if err != nil {
			return ChatOutput{Trace: agent.Trace{Prompt: in.Message}}, fmt.Errorf("model backend: %w", err)
		}
		out.ConversationID, conv = a.Store.Start(who.Requester, be)
	}
	if !conv.TryLock() {
		return out, ErrBusy
	}
	defer conv.Unlock()
	chat := &agent.Chat{Catalogue: a.Catalogue, MaxTurns: a.MaxTurns, Tracer: a.Tracer, Audit: a.Audit, Redact: a.Redact}
	if in.FollowUp != "" {
		var req focv1.ApprovalRequest
		if a.Requests == nil {
			return out, errors.New("follow-ups are not available: no request reader")
		}
		ns := ""
		if a.Catalogue != nil {
			ns = a.Catalogue.Namespace
		}
		if err := a.Requests.Get(ctx, client.ObjectKey{Namespace: ns, Name: in.FollowUp}, &req); err != nil {
			return out, fmt.Errorf("reading %s: %w", in.FollowUp, err)
		}
		out.Trace, err = chat.FollowUp(ctx, cs, conv, who.Requester, in.SessionID, &req)
		return out, err
	}
	out.Trace, err = chat.Turn(ctx, cs, conv, in.Operator, in.SessionID, in.Message)
	return out, err
}

// Ready implements Runner: foc-mcp accepts a session and answers a ping.
func (a *AgentRunner) Ready(ctx context.Context) error {
	cs, err := a.connect(ctx, "")
	if err != nil {
		return err
	}
	defer func() { _ = cs.Close() }()
	return cs.Ping(ctx, nil)
}

// Backend implements Runner.
func (a *AgentRunner) Backend() string { return a.BackendName }
