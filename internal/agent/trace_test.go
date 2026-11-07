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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tracing"
)

const plantedValue = "sk-ant-api03-loop-trace-secret-0123456789"

// usageBackend replays a script and reports fixed token counts per reply.
type usageBackend struct {
	llm.Scripted
	fail error
}

func (b *usageBackend) Chat(ctx context.Context, s string, h []llm.Message, t []llm.ToolSpec) (llm.Reply, error) {
	if b.fail != nil {
		return llm.Reply{}, b.fail
	}
	r, err := b.Scripted.Chat(ctx, s, h, t)
	r.Usage = llm.Usage{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 5}
	r.StopReason = "tool_use"
	if len(r.Calls) == 0 {
		r.StopReason = "end_turn"
	}
	return r, err
}

func call(id, name string, args map[string]any) llm.ToolCall {
	b, _ := json.Marshal(args)
	return llm.ToolCall{ID: id, Name: name, Args: b}
}

func attrs(kvs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}
	return m
}

// One operator request through the real tool server and policy engine gives
// one trace: the request span, a span per model call with tokens and
// latency, a span per tool call on each side of MCP, and a span per policy
// decision under the tool call that made it. No span holds the prompt, a
// tool argument, or the API key.
func TestLoopTraceAcrossMCPAndPolicy(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", plantedValue)
	mem := tracetest.NewInMemoryExporter()
	tp := tracing.NewProvider("test", tracing.NewRedactingExporter(mem, redact.FromEnv()), true)
	tr := tracing.Tracer(tp)

	p, err := policy.Load("../../policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	zone := &swarmv1.FleetZone{ObjectMeta: metav1.ObjectMeta{Name: "aisle-b", Namespace: "warehouse-a"}}
	c := fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithObjects(zone).Build()
	svc := &tools.Service{C: c, Engine: policy.New(p), Identity: "system:serviceaccount:fleetops-system:fleetops-mcp", DefaultNamespace: "warehouse-a", Tracer: tr}
	ctx := context.Background()
	cs, closeFn, err := Connect(ctx, mcpserver.New(svc))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	secretReason := "spill, key " + plantedValue
	be := &usageBackend{Scripted: llm.Scripted{Steps: []llm.Step{
		{Calls: []llm.ToolCall{call("c1", "get_zone", map[string]any{"name": "aisle-b"})}},
		{Calls: []llm.ToolCall{call("c2", "request_zone_stop", map[string]any{"zoneName": "aisle-b", "reason": secretReason})}},
		{Calls: []llm.ToolCall{call("c3", "request_zone_maintenance", map[string]any{"zoneName": "aisle-b", "reason": "x", "autoResumeAfterMinutes": 5})}},
		{Final: "Requested a stop of FleetZone/aisle-b; maintenance with auto-resume refused (DENY_RELEASE)."},
	}}}
	prompt := "stop aisle-b now, my key is " + plantedValue
	res, err := (&Loop{Backend: be, Session: cs, Tracer: tr}).Run(ctx, "sam", "web-0123456789abcdef", prompt)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Calls) != 3 || res.Calls[1].IsError {
		t.Fatalf("calls %+v", res.Calls)
	}

	spans := mem.GetSpans()
	if res.TraceID == "" || res.TraceID != spans[0].SpanContext.TraceID().String() {
		t.Fatalf("Trace.TraceID %q, spans in trace %s", res.TraceID, spans[0].SpanContext.TraceID())
	}
	byName := map[string][]tracetest.SpanStub{}
	for _, s := range spans {
		byName[s.Name] = append(byName[s.Name], s)
		if s.SpanContext.TraceID() != spans[0].SpanContext.TraceID() {
			t.Fatalf("span %s in another trace", s.Name)
		}
		for _, kv := range s.Attributes {
			v := kv.Value.Emit()
			if strings.Contains(v, plantedValue) || strings.Contains(v, "stop aisle-b now") || strings.Contains(v, "spill") {
				t.Fatalf("span %s attribute %s holds request text or a secret: %q", s.Name, kv.Key, v)
			}
		}
	}
	want := map[string]int{tracing.SpanRequest: 1, tracing.SpanChat: 4, tracing.SpanTool: 3, tracing.SpanMCPTool: 3, tracing.SpanPolicy: 2}
	for n, k := range want {
		if len(byName[n]) != k {
			t.Fatalf("%d %s spans, want %d (all: %d)", len(byName[n]), n, k, len(spans))
		}
	}
	root := byName[tracing.SpanRequest][0]
	if root.Parent.IsValid() {
		t.Fatal("request span has a parent")
	}
	ra := attrs(root.Attributes)
	if ra[tracing.AttrOperator].AsString() != "sam" || ra[tracing.AttrInputTokens].AsInt64() != 400 || ra[tracing.AttrOutputTokens].AsInt64() != 40 ||
		ra[tracing.AttrTurns].AsInt64() != 4 || ra[tracing.AttrToolCalls].AsInt64() != 3 || ra[tracing.AttrBackend].AsString() != "scripted" {
		t.Fatalf("request attributes %v", root.Attributes)
	}
	for _, s := range byName[tracing.SpanChat] {
		a := attrs(s.Attributes)
		if s.Parent.SpanID() != root.SpanContext.SpanID() || a[tracing.AttrInputTokens].AsInt64() != 100 || a[tracing.AttrOutputTokens].AsInt64() != 10 ||
			a[tracing.AttrCacheRead].AsInt64() != 5 || !s.EndTime.After(s.StartTime) {
			t.Fatalf("chat span %v parent %v", s.Attributes, s.Parent)
		}
		if _, ok := a[tracing.AttrLatencyMs]; !ok {
			t.Fatal("chat span has no latency")
		}
	}
	server := map[string]tracetest.SpanStub{} // tool.call span id -> mcp.tool span
	for _, s := range byName[tracing.SpanMCPTool] {
		server[s.Parent.SpanID().String()] = s
	}
	tools := map[string]tracetest.SpanStub{} // tool name -> tool.call span
	for _, s := range byName[tracing.SpanTool] {
		if s.Parent.SpanID() != root.SpanContext.SpanID() {
			t.Fatal("tool span not under the request span")
		}
		if _, ok := server[s.SpanContext.SpanID().String()]; !ok {
			t.Fatalf("no fleetops-mcp span under tool span %v", s.Attributes)
		}
		tools[attrs(s.Attributes)[tracing.AttrToolName].AsString()] = s
	}
	// A model's own request call carries no confirmed preview, so it is
	// refused (D74); the span still records the policy decision.
	stop := attrs(tools["request_zone_stop"].Attributes)
	if stop[tracing.AttrPolicyCode].AsString() != "DENY_NOT_CONFIRMED" || !stop[tracing.AttrPolicyRefused].AsBool() {
		t.Fatalf("zone stop tool span %v", tools["request_zone_stop"].Attributes)
	}
	if attrs(tools["request_zone_maintenance"].Attributes)[tracing.AttrPolicyCode].AsString() != "DENY_NOT_CONFIRMED" {
		t.Fatalf("maintenance tool span %v", tools["request_zone_maintenance"].Attributes)
	}
	if _, ok := attrs(tools["get_zone"].Attributes)[tracing.AttrPolicyCode]; ok {
		t.Fatal("read tool span carries a policy code")
	}
	codesSeen := map[string]string{}
	for _, s := range byName[tracing.SpanPolicy] {
		parent := ""
		for _, m := range byName[tracing.SpanMCPTool] {
			if m.SpanContext.SpanID() == s.Parent.SpanID() {
				parent = attrs(m.Attributes)[tracing.AttrToolName].AsString()
			}
		}
		a := attrs(s.Attributes)
		codesSeen[parent] = a[tracing.AttrPolicyEffect].AsString() + "/" + a[tracing.AttrPolicyCode].AsString()
	}
	if codesSeen["request_zone_stop"] != "Deny/DENY_NOT_CONFIRMED" || codesSeen["request_zone_maintenance"] != "Deny/DENY_NOT_CONFIRMED" {
		t.Fatalf("policy spans by tool: %v", codesSeen)
	}
}

// A failed model call marks the chat and request spans as errors, with the
// API key masked in the status text.
func TestLoopTraceRecordsBackendError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", plantedValue)
	mem := tracetest.NewInMemoryExporter()
	tp := tracing.NewProvider("test", tracing.NewRedactingExporter(mem, redact.FromEnv()), true)
	srv := mcpserver.New(&tools.Service{})
	ctx := context.Background()
	cs, closeFn, err := Connect(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	be := &usageBackend{fail: errors.New("401 invalid x-api-key " + plantedValue)}
	if _, err := (&Loop{Backend: be, Session: cs, Tracer: tracing.Tracer(tp)}).Run(ctx, "sam", "s1", "hi"); err == nil {
		t.Fatal("no error")
	}
	n := 0
	for _, s := range mem.GetSpans() {
		if s.Status.Code != codes.Error {
			t.Fatalf("span %s status %v", s.Name, s.Status)
		}
		if strings.Contains(s.Status.Description, plantedValue) || !strings.Contains(s.Status.Description, redact.Mask) {
			t.Fatalf("status %q", s.Status.Description)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("%d spans, want request and chat", n)
	}
}

// metaSpy is an MCP server with one tool that records the _meta it receives.
type metaSpy struct{ meta map[string]any }

func (m *metaSpy) server() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "spy"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo"}, func(_ context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		m.meta = map[string]any(req.Params.Meta)
		return nil, echoOut{Text: in.Text}, nil
	})
	return srv
}

// With no tracer the loop adds no trace context to _meta (the SDK's own
// io.modelcontextprotocol/* keys are not the loop's).
func TestLoopWithoutTracerAddsNoMeta(t *testing.T) {
	sp := &metaSpy{}
	ctx := context.Background()
	cs, closeFn, err := Connect(ctx, sp.server())
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	be := &llm.Scripted{Steps: []llm.Step{{Calls: []llm.ToolCall{call("1", "echo", map[string]any{"text": "x"})}}, {Final: "ok"}}}
	res, err := (&Loop{Backend: be, Session: cs}).Run(ctx, "sam", "s1", "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.TraceID != "" {
		t.Fatalf("TraceID %q without a Tracer", res.TraceID)
	}
	seen := sp.meta
	_, tp := seen["traceparent"]
	_, ts := seen["tracestate"]
	if tp || ts || seen[mcpserver.MetaOperator] != "sam" || seen[mcpserver.MetaSession] != "s1" {
		t.Fatalf("_meta %v", seen)
	}
}
