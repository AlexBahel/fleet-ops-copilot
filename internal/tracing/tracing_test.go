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

package tracing

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
)

const plantedValue = "sk-ant-api03-testsecretvalue-0123456789"

func TestRedactingExporterMasksEverySecretString(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	tp := NewProvider("t", NewRedactingExporter(mem, redact.New("custom-secret-value")), true)
	_, span := Tracer(tp).Start(context.Background(), "span custom-secret-value")
	span.SetAttributes(
		attribute.String("a", "key="+plantedValue),
		attribute.StringSlice("b", []string{"ok", "custom-secret-value"}),
		attribute.Int("n", 7),
	)
	span.RecordError(errors.New("upstream said Bearer abc.def.ghi"))
	span.SetStatus(codes.Error, "failed with custom-secret-value")
	span.End()
	_ = tp.ForceFlush(context.Background()) // Shutdown would clear the in-memory exporter

	spans := mem.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
	s := spans[0]
	all := s.Name + " " + s.Status.Description
	for _, kv := range s.Attributes {
		all += " " + kv.Value.Emit()
	}
	for _, ev := range s.Events {
		for _, kv := range ev.Attributes {
			all += " " + kv.Value.Emit()
		}
	}
	for _, leak := range []string{plantedValue, "custom-secret-value", "abc.def.ghi"} {
		if strings.Contains(all, leak) {
			t.Fatalf("exported span contains %q: %s", leak, all)
		}
	}
	if !strings.Contains(all, redact.Mask) {
		t.Fatalf("no mask in %s", all)
	}
	for _, kv := range s.Attributes {
		if kv.Key == "n" && kv.Value.AsInt64() != 7 {
			t.Fatalf("non-string attribute changed: %v", kv)
		}
	}
}

func TestMetaPropagation(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	tp := NewProvider("t", mem, true)
	meta := map[string]any{"other": "x"}
	InjectMeta(context.Background(), meta)
	if len(meta) != 1 {
		t.Fatalf("injected without a span: %v", meta)
	}
	ctx, span := Tracer(tp).Start(context.Background(), "parent")
	InjectMeta(ctx, meta)
	if _, ok := meta["traceparent"].(string); !ok {
		t.Fatalf("no traceparent in %v", meta)
	}
	got := trace.SpanContextFromContext(ExtractMeta(context.Background(), meta))
	if got.TraceID() != span.SpanContext().TraceID() || got.SpanID() != span.SpanContext().SpanID() || !got.IsRemote() {
		t.Fatalf("extracted %v, want %v", got, span.SpanContext())
	}
	if ExtractMeta(context.Background(), nil) == nil {
		t.Fatal("nil context")
	}
	span.End()
}

func TestSetupDisabledWithoutEndpoint(t *testing.T) {
	tp, shutdown, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, span := Tracer(tp).Start(context.Background(), "x")
	if span.SpanContext().IsValid() || span.IsRecording() {
		t.Fatal("span recorded with tracing disabled")
	}
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestParseEndpoint(t *testing.T) {
	for _, c := range []struct {
		in, host string
		insecure bool
		bad      bool
	}{
		{"otel-collector:4317", "otel-collector:4317", true, false},
		{"http://otel:4317", "otel:4317", true, false},
		{"https://otel.example:4317", "otel.example:4317", false, false},
		{"ftp://otel:4317", "", false, true},
		{"http://otel:4318/v1/traces", "", false, true},
	} {
		h, ins, err := parseEndpoint(c.in)
		if (err != nil) != c.bad || h != c.host || ins != c.insecure {
			t.Errorf("%s: got %q %v %v", c.in, h, ins, err)
		}
	}
}

type collector struct {
	coltrace.UnimplementedTraceServiceServer
	mu   sync.Mutex
	reqs []*coltrace.ExportTraceServiceRequest
}

func (c *collector) Export(_ context.Context, r *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, r)
	return &coltrace.ExportTraceServiceResponse{}, nil
}

// The configured OTLP/gRPC exporter delivers spans to a collector, with the
// service name set and secrets masked.
func TestSetupExportsOTLPToCollector(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	col := &collector{}
	coltrace.RegisterTraceServiceServer(gs, col)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	ctx := context.Background()
	tp, shutdown, err := Setup(ctx, Config{Endpoint: "http://" + lis.Addr().String(), ServiceName: "fleetops-test", Redactor: redact.New()})
	if err != nil {
		t.Fatal(err)
	}
	_, span := Tracer(tp).Start(ctx, SpanRequest)
	span.SetAttributes(AttrOperator.String("sam"), attribute.String("leak", plantedValue))
	span.End()
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := shutdown(sctx); err != nil {
		t.Fatal(err)
	}

	col.mu.Lock()
	defer col.mu.Unlock()
	var names []string
	var service string
	for _, r := range col.reqs {
		for _, rs := range r.ResourceSpans {
			for _, kv := range rs.Resource.Attributes {
				if kv.Key == "service.name" {
					service = kv.Value.GetStringValue()
				}
			}
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					names = append(names, s.Name)
					for _, kv := range s.Attributes {
						if strings.Contains(kv.Value.GetStringValue(), plantedValue) {
							t.Fatalf("planted value exported in %s", kv.Key)
						}
					}
				}
			}
		}
	}
	if len(names) != 1 || names[0] != SpanRequest || service != "fleetops-test" {
		t.Fatalf("collector got spans %v, service %q", names, service)
	}
}
