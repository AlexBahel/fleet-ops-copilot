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

// Package agent runs the tool-use loop: it offers the MCP server's tools to a
// model backend, executes the calls the model makes over MCP, and records a
// trace. The operator identity and session id travel in MCP _meta, set here
// and never by the model.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tracing"
)

// SystemPromptVersion names what the model is told: the SystemPrompt text
// and the tool descriptions it is offered. Evaluation results record it with
// the prompt's SHA-256 and the tool list's (docs/DESIGN.md D46, D66, D81).
// v2 (2026-11-04) added three sentences (P1-P3). v3 (2026-11-05, R46)
// described every namespace parameter. v4 (2026-11-06, D81, R62-R69): the
// model reads a request through propose_task and is offered no write tool;
// the copilot asks, previews and files after the person's confirmation.
// v5 (2026-11-07, D90) keeps the v4 text; propose_task's schema is
// narrowed to the site catalogue (D85).
const SystemPromptVersion = "v5"

// SystemPrompt is the instruction every backend receives.
const SystemPrompt = `You are an operations assistant for a Swarmada robot fleet.
Answer from tool results only. Cite every fact as Kind/name field=value, using the field paths the tools return (for example Robot/sim-robot-002 status.estopState=Stopped).
You cannot clear an emergency stop, release or resume a robot, delete anything, approve a request, or change work that already exists. When asked to, say that you cannot, give the reason, and name who can.
When the person asks for work (an inspection, moving or delivering, charging, a patrol, a survey, zone maintenance, a zone stop, activating a draft), call propose_task with the ask and every field their words state. Do not ask your own questions and do not write a preview: the copilot asks at most two questions, shows a preview with its checks, and files a request only after the person confirms it. When the person describes something the copilot's choices did not cover, call propose_task with somethingElse: known (a catalogue kind), composed (basic steps), or notPossible (the need and the reason).
Before you propose work or answer about a zone, read its current state (get_zone, and the robots in it) and cite it.
Cite each fact with the full Kind/name and field path, e.g. Robot/sim-robot-002 status.phase=Idle, also when several objects share a value.
Never substitute a parameter the operator asked for; if a value is not allowed, say so.`

// ToolEvent is one executed tool call.
type ToolEvent struct {
	Name string `json:"name"`
	// By is "agent" for a call fleetops-agent made itself (a fleet snapshot for
	// the dialogue, check_task for a preview, the request filed after a
	// confirmation, whoami), "local" for a tool fleetops-agent handles in process
	// (propose_task), and empty for a model's call to fleetops-mcp.
	By         string          `json:"by,omitempty"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result,omitempty"`
	ErrorText  string          `json:"error,omitempty"`
	IsError    bool            `json:"isError"`
	DurationMs float64         `json:"durationMs"`
}

// Trace is the record of one request.
type Trace struct {
	Prompt string      `json:"prompt"`
	Calls  []ToolEvent `json:"calls"`
	// Final is what the person sees: the text of every model response of the
	// message, in order, then the engine's question or preview (D84).
	Final string `json:"final"`
	// Responses is each model response's text, one entry per model turn, in
	// order ("" for a response with tool calls only), D84.
	Responses []string `json:"responses,omitempty"`
	Turns     int      `json:"turns"`
	Usage  llm.Usage   `json:"usage"`
	// StopReasons lists the backend stop reason of each model turn (claude only).
	StopReasons []string  `json:"stopReasons,omitempty"`
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended"`
	LatencyMs   float64   `json:"latencyMs"`
	// TraceID is the OpenTelemetry trace of this request (hex), empty when
	// the Loop has no Tracer or a no-op one.
	TraceID string `json:"traceID,omitempty"`
	// Dialogue is what this message did in a guided request (D71): a
	// question asked, a preview shown, a request filed, ...; nil for a plain
	// answer.
	Dialogue *DialogueEvent `json:"dialogue,omitempty"`
	// Conversation is set on a trace that combines several messages (the
	// evaluation harness): one record per person message, in order.
	Conversation []TurnRecord `json:"conversation,omitempty"`
}

// TurnRecord is one person message and the copilot's reply.
type TurnRecord struct {
	Person   string         `json:"person"`
	Reply    string         `json:"reply"`
	Dialogue *DialogueEvent `json:"dialogue,omitempty"`
	// Stalled: the scripted person had no reply for the question asked.
	Stalled bool `json:"stalled,omitempty"`
}

// LocalTool is a tool fleetops-agent handles itself. Call returns the content
// given to the model, or, when stop is non-empty, the text that ends the
// turn (a question or a preview the engine rendered).
type LocalTool struct {
	Spec llm.ToolSpec
	Call func(ctx context.Context, args json.RawMessage) (content string, stop string, err error)
}

// Loop runs one backend against one MCP session.
type Loop struct {
	Backend  llm.Backend
	Session  *mcp.ClientSession
	MaxTurns int
	// Offer says whether a served tool is offered to the model; nil offers
	// all. fleetops-agent offers no request_* tool and not whoami (D74).
	Offer func(name string) bool
	// Local tools are handled by the caller, not fleetops-mcp.
	Local []LocalTool
	// Tracer records one span per request, model call and tool call
	// (internal/tracing). nil records nothing.
	Tracer trace.Tracer
}

// ErrMaxTurns means the model did not finish within the turn limit.
var ErrMaxTurns = errors.New("turn limit reached without a final answer")

// Run answers one operator request. Latency is measured from the request
// being sent to the backend until the final answer.
func (l *Loop) Run(ctx context.Context, operator, sessionID, prompt string) (tr Trace, err error) {
	return l.RunWith(ctx, operator, sessionID, nil, prompt)
}

// RunWith answers prompt after earlier messages of the same conversation
// (person and assistant text only).
func (l *Loop) RunWith(ctx context.Context, operator, sessionID string, earlier []llm.Message, prompt string) (tr Trace, err error) {
	tr = Trace{Prompt: prompt, Started: time.Now()}
	ctx, span := tracing.OrNoop(l.Tracer).Start(ctx, tracing.SpanRequest, trace.WithAttributes(
		tracing.AttrOperator.String(operator), tracing.AttrSession.String(sessionID),
		tracing.AttrBackend.String(l.Backend.Name()), tracing.AttrPromptChars.Int(len(prompt)),
	))
	if sc := span.SpanContext(); sc.HasTraceID() {
		tr.TraceID = sc.TraceID().String()
	}
	defer func() {
		tr.Ended = time.Now()
		tr.LatencyMs = float64(tr.Ended.Sub(tr.Started).Microseconds()) / 1000
		span.SetAttributes(
			tracing.AttrTurns.Int(tr.Turns), tracing.AttrToolCalls.Int(len(tr.Calls)),
			tracing.AttrInputTokens.Int64(tr.Usage.InputTokens), tracing.AttrOutputTokens.Int64(tr.Usage.OutputTokens),
			tracing.AttrCacheCreation.Int64(tr.Usage.CacheCreationInputTokens), tracing.AttrCacheRead.Int64(tr.Usage.CacheReadInputTokens),
			tracing.AttrLatencyMs.Float64(tr.LatencyMs), tracing.AttrAnswerChars.Int(len(tr.Final)),
		)
		endSpan(span, err)
	}()
	tl, err := l.Session.ListTools(ctx, nil)
	if err != nil {
		return tr, err
	}
	specs := make([]llm.ToolSpec, 0, len(tl.Tools)+len(l.Local))
	for _, t := range tl.Tools {
		if l.Offer != nil && !l.Offer(t.Name) {
			continue
		}
		schema, _ := json.Marshal(t.InputSchema)
		specs = append(specs, llm.ToolSpec{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	local := map[string]LocalTool{}
	for _, lt := range l.Local {
		specs = append(specs, lt.Spec)
		local[lt.Spec.Name] = lt
	}
	max := l.MaxTurns
	if max == 0 {
		max = 12
	}
	history := append(append([]llm.Message{}, earlier...), llm.Message{Role: "user", Text: prompt})
	for tr.Turns < max {
		tr.Turns++
		reply, err := l.chat(ctx, tr.Turns, history, specs)
		if err != nil {
			return tr, err
		}
		tr.Usage.Add(reply.Usage)
		if reply.StopReason != "" {
			tr.StopReasons = append(tr.StopReasons, reply.StopReason)
		}
		history = append(history, llm.Message{Role: "assistant", Text: reply.Text, Calls: reply.Calls, Raw: reply.Raw})
		tr.Responses = append(tr.Responses, reply.Text)
		if len(reply.Calls) == 0 {
			tr.Final = shown(tr.Responses, "")
			return tr, nil
		}
		var results []llm.ToolResult
		for _, c := range reply.Calls {
			if lt, ok := local[c.Name]; ok {
				ev := ToolEvent{Name: c.Name, By: "local", Args: c.Args}
				start := time.Now()
				content, stop, cerr := lt.Call(ctx, c.Args)
				ev.DurationMs = float64(time.Since(start).Microseconds()) / 1000
				if cerr != nil {
					ev.IsError, ev.ErrorText, content = true, cerr.Error(), cerr.Error()
				} else if content != "" && json.Valid([]byte(content)) {
					ev.Result = json.RawMessage(content)
				}
				tr.Calls = append(tr.Calls, ev)
				if stop != "" {
					tr.Final = shown(tr.Responses, stop)
					return tr, nil
				}
				results = append(results, llm.ToolResult{CallID: c.ID, Content: content, IsError: cerr != nil})
				continue
			}
			if l.Offer != nil && !l.Offer(c.Name) {
				// A tool the model was not offered is not called (D74).
				ev := ToolEvent{Name: c.Name, Args: c.Args, IsError: true, ErrorText: "tool not offered"}
				tr.Calls = append(tr.Calls, ev)
				results = append(results, llm.ToolResult{CallID: c.ID, Content: "tool " + c.Name + " is not offered: requests are filed by the copilot after the person confirms a preview; call propose_task", IsError: true})
				continue
			}
			ev, res := l.call(ctx, operator, sessionID, c)
			tr.Calls = append(tr.Calls, ev)
			results = append(results, res)
		}
		history = append(history, llm.Message{Role: "tool", Results: results})
	}
	tr.Final = shown(tr.Responses, "")
	return tr, ErrMaxTurns
}

// shown is the reply the person sees (D84): the text of every model
// response of the message, in order, then the engine's text
// (a question or a preview), separated by blank lines.
func shown(responses []string, engine string) string {
	var parts []string
	for _, r := range responses {
		if r = strings.TrimSpace(r); r != "" {
			parts = append(parts, r)
		}
	}
	if engine = strings.TrimSpace(engine); engine != "" {
		parts = append(parts, engine)
	}
	return strings.Join(parts, "\n\n")
}

// chat is one model call, traced with its token counts and latency. No
// prompt or answer text goes into the span.
func (l *Loop) chat(ctx context.Context, turn int, history []llm.Message, specs []llm.ToolSpec) (reply llm.Reply, err error) {
	ctx, span := tracing.OrNoop(l.Tracer).Start(ctx, tracing.SpanChat, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		tracing.AttrOperation.String("chat"), tracing.AttrBackend.String(l.Backend.Name()), tracing.AttrTurn.Int(turn),
	))
	start := time.Now()
	defer func() {
		span.SetAttributes(
			tracing.AttrLatencyMs.Float64(float64(time.Since(start).Microseconds())/1000),
			tracing.AttrInputTokens.Int64(reply.Usage.InputTokens), tracing.AttrOutputTokens.Int64(reply.Usage.OutputTokens),
			tracing.AttrCacheCreation.Int64(reply.Usage.CacheCreationInputTokens), tracing.AttrCacheRead.Int64(reply.Usage.CacheReadInputTokens),
			tracing.AttrToolCalls.Int(len(reply.Calls)),
		)
		if reply.StopReason != "" {
			span.SetAttributes(tracing.AttrFinishReasons.StringSlice([]string{reply.StopReason}))
		}
		endSpan(span, err)
	}()
	return l.Backend.Chat(ctx, SystemPrompt, history, specs)
}

// endSpan records err (masked again by the redacting exporter) and ends span.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func (l *Loop) call(ctx context.Context, operator, sessionID string, c llm.ToolCall) (ev ToolEvent, tr llm.ToolResult) {
	ev = ToolEvent{Name: c.Name, Args: c.Args}
	ctx, span := tracing.OrNoop(l.Tracer).Start(ctx, tracing.SpanTool, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		tracing.AttrOperation.String("execute_tool"), tracing.AttrToolName.String(c.Name), tracing.AttrToolCallID.String(c.ID),
	))
	defer func() {
		span.SetAttributes(tracing.AttrToolIsError.Bool(ev.IsError), tracing.AttrLatencyMs.Float64(ev.DurationMs))
		span.SetAttributes(policyAttrs(ev.Result)...)
		if ev.IsError {
			span.SetStatus(codes.Error, "tool call failed")
		}
		span.End()
	}()
	var args map[string]any
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			ev.IsError, ev.ErrorText = true, "invalid tool arguments: "+err.Error()
			return ev, llm.ToolResult{CallID: c.ID, Content: ev.ErrorText, IsError: true}
		}
	}
	meta := mcp.Meta{mcpserver.MetaSession: sessionID, mcpserver.MetaOperator: operator}
	tracing.InjectMeta(ctx, meta)
	start := time.Now()
	res, err := l.Session.CallTool(ctx, &mcp.CallToolParams{
		Meta:      meta,
		Name:      c.Name,
		Arguments: args,
	})
	ev.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		ev.IsError, ev.ErrorText = true, err.Error()
		return ev, llm.ToolResult{CallID: c.ID, Content: err.Error(), IsError: true}
	}
	var text []string
	for _, ct := range res.Content {
		if t, ok := ct.(*mcp.TextContent); ok {
			text = append(text, t.Text)
		}
	}
	content := strings.Join(text, "\n")
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			ev.Result = b
			content = string(b)
		}
	}
	if res.IsError {
		ev.IsError, ev.ErrorText = true, content
	}
	return ev, llm.ToolResult{CallID: c.ID, Content: content, IsError: res.IsError}
}

// policyAttrs reads the policy fields a request tool returns
// (tools.RequestResult) into span attributes. Read tools return none.
func policyAttrs(result json.RawMessage) []attribute.KeyValue {
	var r struct {
		Refused *bool  `json:"refused"`
		Effect  string `json:"policy.effect"`
		Code    string `json:"policy.code"`
		Created string `json:"created"`
	}
	if len(result) == 0 || json.Unmarshal(result, &r) != nil || r.Refused == nil {
		return nil
	}
	out := []attribute.KeyValue{tracing.AttrPolicyRefused.Bool(*r.Refused), tracing.AttrPolicyEffect.String(r.Effect), tracing.AttrPolicyCode.String(r.Code)}
	if r.Created != "" {
		out = append(out, tracing.AttrRequestCreated.String(r.Created))
	}
	return out
}

// Connect wires an in-process MCP server and client together.
func Connect(ctx context.Context, srv *mcp.Server) (*mcp.ClientSession, func(), error) {
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return nil, nil, err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "fleetops-agent", Version: mcpserver.Version}, nil).Connect(ctx, ct, nil)
	if err != nil {
		_ = ss.Close()
		return nil, nil, err
	}
	return cs, func() { _ = cs.Close(); _ = ss.Close() }, nil
}

// ToolCalls returns the names of the calls in a trace.
func (t Trace) ToolCalls() []string {
	out := make([]string, 0, len(t.Calls))
	for _, c := range t.Calls {
		out = append(out, c.Name)
	}
	return out
}

// String is a short description for logs.
func (e ToolEvent) String() string { return fmt.Sprintf("%s(%s) error=%v", e.Name, e.Args, e.IsError) }
