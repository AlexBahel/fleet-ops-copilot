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

// Package tracing sets up OpenTelemetry tracing for the copilot's processes
// (docs/DESIGN.md §3.8): one trace per operator request, with spans for each
// model call, tool call and policy decision.
//
// Every exported span passes through a redacting exporter that masks the
// values of configured secrets and known token patterns (internal/redact) in
// span names, attributes, event attributes and status text. The spans
// themselves carry no prompt, answer, tool argument or tool result text, so
// redaction is a second line, not the only one.
//
// The trace context crosses the MCP hop from fleetops-agent to fleetops-mcp as W3C
// traceparent/tracestate keys in the tools/call request _meta.
package tracing

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
)

// ScopeName is the instrumentation scope of every copilot span.
const ScopeName = "github.com/AlexBahel/fleet-ops-copilot"

// Span names.
const (
	SpanRequest = "fleetops.request" // one operator request (fleetops-agent)
	SpanChat    = "llm.chat"         // one model call (fleetops-agent)
	SpanTool    = "tool.call"        // one tool call, client side (fleetops-agent)
	SpanMCPTool = "mcp.tool"         // one tool call, server side (fleetops-mcp)
	SpanPolicy  = "policy.decide"    // one policy engine decision (fleetops-mcp)
)

// Attribute keys. gen_ai.* follow the OpenTelemetry GenAI semantic
// conventions; fleetops.* are the copilot's own.
const (
	AttrOperation      = attribute.Key("gen_ai.operation.name")
	AttrProvider       = attribute.Key("gen_ai.provider.name")
	AttrModel          = attribute.Key("gen_ai.request.model")
	AttrInputTokens    = attribute.Key("gen_ai.usage.input_tokens")
	AttrOutputTokens   = attribute.Key("gen_ai.usage.output_tokens")
	AttrCacheCreation  = attribute.Key("gen_ai.usage.cache_creation.input_tokens")
	AttrCacheRead      = attribute.Key("gen_ai.usage.cache_read.input_tokens")
	AttrFinishReasons  = attribute.Key("gen_ai.response.finish_reasons")
	AttrToolName       = attribute.Key("gen_ai.tool.name")
	AttrToolCallID     = attribute.Key("gen_ai.tool.call.id")
	AttrBackend        = attribute.Key("fleetops.backend")
	AttrOperator       = attribute.Key("fleetops.operator")
	AttrSession        = attribute.Key("fleetops.session.id")
	AttrPromptChars    = attribute.Key("fleetops.prompt.chars")
	AttrAnswerChars    = attribute.Key("fleetops.answer.chars")
	AttrTurn           = attribute.Key("fleetops.turn")
	AttrTurns          = attribute.Key("fleetops.turns")
	AttrToolCalls      = attribute.Key("fleetops.tool_calls")
	AttrLatencyMs      = attribute.Key("fleetops.latency_ms")
	AttrToolIsError    = attribute.Key("fleetops.tool.is_error")
	AttrPolicyOp       = attribute.Key("fleetops.policy.op")
	AttrPolicyEffect   = attribute.Key("fleetops.policy.effect")
	AttrPolicyCode     = attribute.Key("fleetops.policy.code")
	AttrPolicyRefused  = attribute.Key("fleetops.policy.refused")
	AttrRequestCreated = attribute.Key("fleetops.request.created")
	AttrNamespace      = attribute.Key("fleetops.namespace")
)

// Config selects the exporter.
type Config struct {
	// Endpoint of an OTLP/gRPC collector: host:port, http://host:port
	// (plaintext) or https://host:port (TLS). Empty disables tracing.
	Endpoint string
	// ServiceName is recorded as the resource attribute service.name.
	ServiceName string
	// Redactor masks secrets in exported spans. nil uses redact.FromEnv().
	Redactor *redact.Redactor
}

// EndpointFromEnv returns OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, else
// OTEL_EXPORTER_OTLP_ENDPOINT, else "".
func EndpointFromEnv() string {
	if v := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"); v != "" {
		return v
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
}

// Setup returns the TracerProvider for a process and a shutdown function
// that flushes it. With no endpoint it returns a no-op provider: nothing is
// recorded or sent. Exporter headers (for example an authorization token)
// are read by the exporter from OTEL_EXPORTER_OTLP_HEADERS and never become
// span attributes; that variable is also in redact.DefaultSecretEnv.
func Setup(ctx context.Context, cfg Config) (trace.TracerProvider, func(context.Context) error, error) {
	if cfg.Endpoint == "" {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}
	hostport, insecure, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, nil, err
	}
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(hostport), otlptracegrpc.WithTimeout(10 * time.Second)}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("OTLP exporter: %w", err)
	}
	red := cfg.Redactor
	if red == nil {
		red = redact.FromEnv()
	}
	tp := NewProvider(cfg.ServiceName, NewRedactingExporter(exp, red), false)
	return tp, tp.Shutdown, nil
}

// parseEndpoint accepts host:port (plaintext, as the in-cluster default),
// http://host:port or https://host:port.
func parseEndpoint(ep string) (hostport string, insecure bool, err error) {
	if !strings.Contains(ep, "://") {
		return ep, true, nil
	}
	u, err := url.Parse(ep)
	if err != nil {
		return "", false, fmt.Errorf("OTLP endpoint %q: %w", ep, err)
	}
	switch u.Scheme {
	case "http":
		insecure = true
	case "https":
	default:
		return "", false, fmt.Errorf("OTLP endpoint %q: scheme must be http or https", ep)
	}
	if u.Host == "" || (u.Path != "" && u.Path != "/") {
		return "", false, fmt.Errorf("OTLP endpoint %q: want scheme://host:port with no path (OTLP/gRPC)", ep)
	}
	return u.Host, insecure, nil
}

// NewProvider builds a TracerProvider that sends every span to exp. sync
// exports each span as it ends (tests); otherwise spans are batched.
func NewProvider(service string, exp sdktrace.SpanExporter, sync bool) *sdktrace.TracerProvider {
	opt := sdktrace.WithBatcher(exp)
	if sync {
		opt = sdktrace.WithSyncer(exp)
	}
	res := resource.NewSchemaless(attribute.String("service.name", service))
	return sdktrace.NewTracerProvider(opt, sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.AlwaysSample()))
}

// Tracer returns the copilot's tracer from tp, or a no-op tracer if tp is nil.
func Tracer(tp trace.TracerProvider) trace.Tracer {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	return tp.Tracer(ScopeName)
}

// OrNoop returns t, or a no-op tracer if t is nil.
func OrNoop(t trace.Tracer) trace.Tracer {
	if t == nil {
		return noop.NewTracerProvider().Tracer(ScopeName)
	}
	return t
}

// RedactingExporter masks secrets in every span before passing it on.
type RedactingExporter struct {
	next sdktrace.SpanExporter
	red  *redact.Redactor
}

// NewRedactingExporter wraps next.
func NewRedactingExporter(next sdktrace.SpanExporter, red *redact.Redactor) *RedactingExporter {
	return &RedactingExporter{next: next, red: red}
}

// ExportSpans implements sdktrace.SpanExporter.
func (e *RedactingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		st := tracetest.SpanStubFromReadOnlySpan(s)
		st.Name = e.red.String(st.Name)
		st.Attributes = e.attrs(st.Attributes)
		st.Status.Description = e.red.String(st.Status.Description)
		for j := range st.Events {
			st.Events[j].Name = e.red.String(st.Events[j].Name)
			st.Events[j].Attributes = e.attrs(st.Events[j].Attributes)
		}
		for j := range st.Links {
			st.Links[j].Attributes = e.attrs(st.Links[j].Attributes)
		}
		out[i] = st.Snapshot()
	}
	return e.next.ExportSpans(ctx, out)
}

// Shutdown implements sdktrace.SpanExporter.
func (e *RedactingExporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }

func (e *RedactingExporter) attrs(in []attribute.KeyValue) []attribute.KeyValue {
	if len(in) == 0 {
		return in
	}
	out := make([]attribute.KeyValue, len(in))
	for i, kv := range in {
		switch kv.Value.Type() {
		case attribute.STRING:
			out[i] = attribute.String(string(kv.Key), e.red.String(kv.Value.AsString()))
		case attribute.STRINGSLICE:
			ss := kv.Value.AsStringSlice()
			for j := range ss {
				ss[j] = e.red.String(ss[j])
			}
			out[i] = attribute.StringSlice(string(kv.Key), ss)
		default:
			out[i] = kv
		}
	}
	return out
}

// metaCarrier adapts MCP request _meta (a map[string]any) to a
// propagation.TextMapCarrier.
type metaCarrier map[string]any

func (m metaCarrier) Get(k string) string { s, _ := m[k].(string); return s }
func (m metaCarrier) Set(k, v string)     { m[k] = v }
func (m metaCarrier) Keys() []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

var propagator = propagation.TraceContext{}

// InjectMeta writes the W3C trace context of ctx into MCP _meta. It writes
// nothing when ctx carries no recording span, so _meta is unchanged with
// tracing off.
func InjectMeta(ctx context.Context, meta map[string]any) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return
	}
	propagator.Inject(ctx, metaCarrier(meta))
}

// ExtractMeta returns ctx with the remote span context found in MCP _meta,
// if any.
func ExtractMeta(ctx context.Context, meta map[string]any) context.Context {
	if meta == nil {
		return ctx
	}
	return propagator.Extract(ctx, metaCarrier(meta))
}
