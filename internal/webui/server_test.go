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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

const plantedValue = "sk-ant-api03-webui-planted-value-0123456789"

// stubRunner records what it was asked and returns a fixed trace or error.
type stubRunner struct {
	mu       sync.Mutex
	got      []string
	trace    agent.Trace
	err      error
	readyErr error
	block    chan struct{}
}

func (s *stubRunner) Run(ctx context.Context, op, sess, prompt string) (agent.Trace, error) {
	s.mu.Lock()
	s.got = append(s.got, op+"|"+sess+"|"+prompt)
	s.mu.Unlock()
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
		}
	}
	return s.trace, s.err
}
func (s *stubRunner) Ready(context.Context) error { return s.readyErr }
func (s *stubRunner) Backend() string             { return "scripted" }

func newServer(t *testing.T, r Runner, c client.Reader) *httptest.Server {
	t.Helper()
	if c == nil {
		c = fake.NewClientBuilder().WithScheme(fleet.NewScheme()).Build()
	}
	s := &Server{Runner: r, Requests: c, Namespaces: []string{"warehouse-a"}, MaxConcurrent: 1, Redactor: redact.New(plantedValue)}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postChat(t *testing.T, ts *httptest.Server, body string, hdr map[string]string) (int, chatResponse) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out chatResponse
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestChatValidatesInput(t *testing.T) {
	r := &stubRunner{}
	ts := newServer(t, r, nil)
	for _, c := range []struct {
		name, body string
		hdr        map[string]string
		code       int
	}{
		{"no operator", `{"message":"hi"}`, nil, 400},
		{"operator with space", `{"operator":"a b","message":"hi"}`, nil, 400},
		{"empty message", `{"operator":"sam","message":"  "}`, nil, 400},
		{"long message", `{"operator":"sam","message":"` + strings.Repeat("x", 8001) + `"}`, nil, 400},
		{"bad session", `{"operator":"sam","message":"hi","sessionId":"Bad_Session"}`, nil, 400},
		{"unknown field", `{"operator":"sam","message":"hi","approve":true}`, nil, 400},
		{"not JSON", `operator=sam`, nil, 400},
		{"form content type", `{"operator":"sam","message":"hi"}`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415},
		{"cross-site", `{"operator":"sam","message":"hi"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
	} {
		if code, _ := postChat(t, ts, c.body, c.hdr); code != c.code {
			t.Errorf("%s: status %d, want %d", c.name, code, c.code)
		}
	}
	if len(r.got) != 0 {
		t.Fatalf("runner called for invalid input: %v", r.got)
	}
}

func TestChatAnswersAndReportsPolicy(t *testing.T) {
	r := &stubRunner{trace: agent.Trace{Final: "Requested.", Turns: 2, LatencyMs: 12, Calls: []agent.ToolEvent{
		{Name: "get_zone", Result: json.RawMessage(`{"name":"aisle-b"}`)},
		{Name: "request_zone_stop", Result: json.RawMessage(`{"refused":false,"policy.effect":"RequireApproval","policy.code":"REQUIRE_APPROVAL","created":"ApprovalRequest/areq-1"}`)},
		{Name: "request_zone_maintenance", Result: json.RawMessage(`{"refused":true,"policy.effect":"Deny","policy.code":"DENY_RELEASE"}`)},
	}}}
	ts := newServer(t, r, nil)
	code, out := postChat(t, ts, `{"operator":"jane@example.com","message":" stop aisle-b "}`, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if code != 200 || out.Answer != "Requested." || out.Turns != 2 || !strings.HasPrefix(out.SessionID, "web-") {
		t.Fatalf("%d %+v", code, out)
	}
	if r.got[0] != "jane@example.com|"+out.SessionID+"|stop aisle-b" {
		t.Fatalf("runner got %q", r.got[0])
	}
	want := []ToolCall{{Name: "get_zone"}, {Name: "request_zone_stop", PolicyCode: "REQUIRE_APPROVAL", Created: "ApprovalRequest/areq-1"}, {Name: "request_zone_maintenance", Refused: true, PolicyCode: "DENY_RELEASE"}}
	if len(out.ToolCalls) != 3 || out.ToolCalls[0] != want[0] || out.ToolCalls[1] != want[1] || out.ToolCalls[2] != want[2] {
		t.Fatalf("tool calls %+v", out.ToolCalls)
	}
	// The session id the page sends back is kept.
	if _, out2 := postChat(t, ts, `{"operator":"jane@example.com","message":"again","sessionId":"`+out.SessionID+`"}`, nil); out2.SessionID != out.SessionID {
		t.Fatalf("session %s, want %s", out2.SessionID, out.SessionID)
	}
}

func TestChatErrorIsRedacted(t *testing.T) {
	ts := newServer(t, &stubRunner{err: errors.New("401 from model API, key " + plantedValue)}, nil)
	code, out := postChat(t, ts, `{"operator":"sam","message":"hi"}`, nil)
	if code != http.StatusBadGateway || strings.Contains(out.Error, plantedValue) || !strings.Contains(out.Error, redact.Mask) {
		t.Fatalf("%d %q", code, out.Error)
	}
}

func TestChatConcurrencyLimit(t *testing.T) {
	r := &stubRunner{block: make(chan struct{})}
	ts := newServer(t, r, nil)
	done := make(chan int)
	go func() { c, _ := postChat(t, ts, `{"operator":"sam","message":"one"}`, nil); done <- c }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.got)
		r.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first chat never reached the runner")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, _ := postChat(t, ts, `{"operator":"sam","message":"two"}`, nil); code != http.StatusTooManyRequests {
		t.Fatalf("second chat: %d, want 429", code)
	}
	close(r.block)
	if c := <-done; c != 200 {
		t.Fatalf("first chat: %d", c)
	}
}

type got struct {
	StatusCode int
	Header     http.Header
}

func get(t *testing.T, url string) (got, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return got{StatusCode: res.StatusCode, Header: res.Header}, string(b)
}

func TestStaticPageAndHeaders(t *testing.T) {
	ts := newServer(t, &stubRunner{}, nil)
	res, body := get(t, ts.URL+"/")
	if res.StatusCode != 200 || !strings.Contains(body, "This page cannot approve or reject") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", res.Header)
	}
	if res, body := get(t, ts.URL+"/app.js"); res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "javascript") || strings.Contains(body, "innerHTML") {
		t.Fatalf("app.js: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if res, body := get(t, ts.URL+"/api/info"); res.StatusCode != 200 || !strings.Contains(body, `"backend":"scripted"`) {
		t.Fatalf("info %d %s", res.StatusCode, body)
	}
	// Nothing on the page's API decides a request.
	for _, p := range []string{"/api/approve", "/api/reject", "/api/requests"} {
		res, err := http.Post(ts.URL+p, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed && res.StatusCode != http.StatusNotFound {
			t.Fatalf("POST %s: %d", p, res.StatusCode)
		}
	}
}

func TestHealthAndReadiness(t *testing.T) {
	r := &stubRunner{}
	ts := newServer(t, r, nil)
	if res, _ := get(t, ts.URL+"/healthz"); res.StatusCode != 200 {
		t.Fatalf("healthz %d", res.StatusCode)
	}
	if res, body := get(t, ts.URL+"/readyz"); res.StatusCode != 200 {
		t.Fatalf("readyz %d %s", res.StatusCode, body)
	}
	r.readyErr = errors.New("connecting to foc-mcp: refused")
	if res, body := get(t, ts.URL+"/readyz"); res.StatusCode != 503 || !strings.Contains(body, "foc-mcp") {
		t.Fatalf("readyz with MCP down: %d %s", res.StatusCode, body)
	}
	r.readyErr = nil
	denied := fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("forbidden")
		},
	}).Build()
	ts2 := newServer(t, r, denied)
	if res, body := get(t, ts2.URL+"/readyz"); res.StatusCode != 503 || !strings.Contains(body, "approvalrequests") {
		t.Fatalf("readyz with reads refused: %d %s", res.StatusCode, body)
	}
	if res, body := get(t, ts2.URL+"/api/requests"); res.StatusCode != http.StatusBadGateway || !strings.Contains(body, "forbidden") {
		t.Fatalf("requests with reads refused: %d %s", res.StatusCode, body)
	}
}

// inMemoryMCP returns a Transport that connects each new session to srv.
func inMemoryMCP(t *testing.T, srv *mcp.Server) func() mcp.Transport {
	return func() mcp.Transport {
		ct, st := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(context.Background(), st, nil)
		if err != nil {
			t.Error(err)
		}
		t.Cleanup(func() { _ = ss.Close() })
		return ct
	}
}

// End to end without a cluster: a chat runs the agent loop against the real
// tool server and policy engine, which files an ApprovalRequest; the panel
// then lists it with the exact patch and the commands to decide it. The UI
// creates no ApprovalDecision and changes no Swarmada object.
func TestChatFilesRequestThatPanelShows(t *testing.T) {
	p, err := policy.Load("../../policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	zone := &swarmv1.FleetZone{ObjectMeta: metav1.ObjectMeta{Name: "aisle-b", Namespace: "warehouse-a"}}
	c := fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithObjects(zone).Build()
	svc := &tools.Service{C: c, Engine: policy.New(p), Identity: "system:serviceaccount:foc-system:foc-mcp", DefaultNamespace: "warehouse-a"}
	args, _ := json.Marshal(map[string]any{"zoneName": "aisle-b", "reason": "spill in aisle B"})
	runner := &AgentRunner{
		Transport:   inMemoryMCP(t, mcpserver.New(svc)),
		BackendName: "scripted",
		NewBackend: func() (llm.Backend, error) {
			return &llm.Scripted{Steps: []llm.Step{{Calls: []llm.ToolCall{{ID: "1", Name: "request_zone_stop", Args: args}}}, {Final: "Requested a stop of FleetZone/aisle-b."}}}, nil
		},
	}
	ts := newServer(t, runner, c)
	if res, body := get(t, ts.URL+"/readyz"); res.StatusCode != 200 {
		t.Fatalf("readyz %d %s", res.StatusCode, body)
	}
	code, out := postChat(t, ts, `{"operator":"jane@example.com","message":"stop aisle-b"}`, nil)
	if code != 200 || len(out.ToolCalls) != 1 || out.ToolCalls[0].PolicyCode != "REQUIRE_APPROVAL" || !strings.HasPrefix(out.ToolCalls[0].Created, "ApprovalRequest/areq-") {
		t.Fatalf("%d %+v", code, out)
	}
	_, body := get(t, ts.URL+"/api/requests")
	var rr requestsResponse
	if err := json.Unmarshal([]byte(body), &rr); err != nil {
		t.Fatal(err)
	}
	if len(rr.Items) != 1 {
		t.Fatalf("items %s", body)
	}
	v := rr.Items[0]
	name := strings.TrimPrefix(out.ToolCalls[0].Created, "ApprovalRequest/")
	if v.Name != name || v.State != "Pending" || v.RequestedFor != "jane@example.com" || v.SessionID != out.SessionID ||
		v.Approve != "foc approve "+name+" -n warehouse-a" || v.Reject != "foc reject "+name+" -n warehouse-a" {
		t.Fatalf("view %+v", v)
	}
	if v.Change.Body != `{"metadata":{"annotations":{"swarmada.io/estop-triggered":"spill in aisle B"}}}` || v.Change.Target != "FleetZone warehouse-a/aisle-b" {
		t.Fatalf("change %+v", v.Change)
	}
	var decisions focv1.ApprovalDecisionList
	if err := c.List(context.Background(), &decisions); err != nil || len(decisions.Items) != 0 {
		t.Fatalf("decisions %v %v", decisions.Items, err)
	}
	var z swarmv1.FleetZone
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(zone), &z); err != nil || len(z.Annotations) != 0 {
		t.Fatalf("zone changed: %v %v", z.Annotations, err)
	}
}
