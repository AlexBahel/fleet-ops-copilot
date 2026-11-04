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

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeReply is one canned answer of the fake llama-server.
type fakeReply struct {
	status int // 0 means 200
	body   string
	delay  time.Duration
}

// fakeLlama replays replies in order and records each request body.
type fakeLlama struct {
	t       *testing.T
	mu      sync.Mutex
	replies []fakeReply
	reqs    []string
	paths   []string
	srv     *httptest.Server
}

func newFakeLlama(t *testing.T, replies ...fakeReply) *fakeLlama {
	f := &fakeLlama{t: t, replies: replies}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		f.reqs = append(f.reqs, b.String())
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		var rp fakeReply
		if len(f.replies) > 0 {
			rp, f.replies = f.replies[0], f.replies[1:]
		} else {
			rp = fakeReply{status: 599, body: `{"error":{"message":"fake: no reply left"}}`}
		}
		f.mu.Unlock()
		if rp.delay > 0 {
			select {
			case <-time.After(rp.delay):
			case <-r.Context().Done():
				return
			}
		}
		if rp.status == 0 {
			rp.status = 200
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rp.status)
		_, _ = w.Write([]byte(rp.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLlama) backend() *LlamaCpp {
	l, err := NewLlamaCpp(f.srv.URL+"/", "")
	if err != nil {
		f.t.Fatal(err)
	}
	l.RetryWait = time.Millisecond
	return l
}

func (f *fakeLlama) request(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.reqs) {
		f.t.Fatalf("only %d requests, want index %d", len(f.reqs), i)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(f.reqs[i]), &m); err != nil {
		f.t.Fatalf("request %d is not JSON: %v: %s", i, err, f.reqs[i])
	}
	return m
}

func (f *fakeLlama) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

const getRobotSchema = `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`

var oneTool = []ToolSpec{{Name: "get_robot", Description: "Read one robot", InputSchema: json.RawMessage(getRobotSchema)}}

func chatReply(content, calls, finish string) string {
	c := "null"
	if content != "" {
		b, _ := json.Marshal(content)
		c = string(b)
	}
	tc := ""
	if calls != "" {
		tc = `,"tool_calls":` + calls
	}
	return `{"choices":[{"index":0,"finish_reason":"` + finish + `","message":{"role":"assistant","content":` + c + tc + `}}],` +
		`"usage":{"prompt_tokens":321,"completion_tokens":45,"total_tokens":366},` +
		`"timings":{"prompt_n":321,"prompt_ms":1500.5,"prompt_per_second":213.9,"predicted_n":45,"predicted_ms":4500,"predicted_per_second":10,"cache_n":0}}`
}

func TestNewLlamaCppFromEnv(t *testing.T) {
	t.Setenv(EnvLlamaCppURL, "")
	t.Setenv(EnvLlamaCppTemperature, "")
	if _, err := NewLlamaCpp("", ""); err == nil || !strings.Contains(err.Error(), EnvLlamaCppURL) {
		t.Fatalf("empty URL accepted: %v", err)
	}
	for _, bad := range []string{"llama-server:8080", "ftp://x", "http://", "://"} {
		if _, err := NewLlamaCpp(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	t.Setenv(EnvLlamaCppURL, "http://llama-server.foc-system.svc:8080/")
	l, err := NewLlamaCpp("", "")
	if err != nil {
		t.Fatal(err)
	}
	if l.BaseURL != "http://llama-server.foc-system.svc:8080" || l.Model != DefaultLlamaCppModel ||
		l.Temperature != DefaultLlamaCppTemperature || l.Name() != "llamacpp:"+DefaultLlamaCppModel {
		t.Fatalf("%+v", l)
	}
	// An explicit URL wins over the environment.
	l, err = NewLlamaCpp("http://other:1", "my-model")
	if err != nil || l.BaseURL != "http://other:1" || l.Name() != "llamacpp:my-model" {
		t.Fatalf("%+v %v", l, err)
	}
	t.Setenv(EnvLlamaCppTemperature, "0.7")
	if l, err = NewLlamaCpp("", ""); err != nil || l.Temperature != 0.7 {
		t.Fatalf("%+v %v", l, err)
	}
	for _, bad := range []string{"hot", "-1", "3"} {
		t.Setenv(EnvLlamaCppTemperature, bad)
		if _, err := NewLlamaCpp("", ""); err == nil {
			t.Errorf("temperature %q accepted", bad)
		}
	}
}

func TestChatRequestShape(t *testing.T) {
	f := newFakeLlama(t, fakeReply{body: chatReply("done", "", "stop")})
	l := f.backend()
	hist := []Message{
		{Role: "user", Text: "why is sim-robot-002 idle?"},
		{Role: "assistant", Calls: []ToolCall{{ID: "c1", Name: "get_robot", Args: json.RawMessage(`{"name":"sim-robot-002"}`)}, {ID: "c2", Name: "get_robot", Args: nil}}},
		{Role: "tool", Results: []ToolResult{{CallID: "c1", Content: `{"status":{}}`}, {CallID: "c2", Content: "no such robot", IsError: true}}},
	}
	if _, err := l.Chat(context.Background(), "be brief", hist, oneTool); err != nil {
		t.Fatal(err)
	}
	if f.paths[0] != "POST /v1/chat/completions" {
		t.Fatalf("path %v", f.paths)
	}
	rq := f.request(0)
	if rq["model"] != DefaultLlamaCppModel || rq["stream"] != false || rq["tool_choice"] != "auto" || rq["temperature"] != float64(0) {
		t.Errorf("fields %v", rq)
	}
	msgs := rq["messages"].([]any)
	roles := ""
	for _, m := range msgs {
		roles += m.(map[string]any)["role"].(string) + ","
	}
	// system, user, assistant (two calls), then one tool message per result.
	if roles != "system,user,assistant,tool,tool," {
		t.Fatalf("roles %s", roles)
	}
	if msgs[0].(map[string]any)["content"] != "be brief" {
		t.Errorf("system message %v", msgs[0])
	}
	calls := msgs[2].(map[string]any)["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if calls[0].(map[string]any)["id"] != "c1" || calls[0].(map[string]any)["type"] != "function" ||
		fn["name"] != "get_robot" || fn["arguments"] != `{"name":"sim-robot-002"}` {
		t.Errorf("call 1 %v", calls[0])
	}
	if calls[1].(map[string]any)["function"].(map[string]any)["arguments"] != "{}" {
		t.Errorf("call without args must send {}: %v", calls[1])
	}
	t1, t2 := msgs[3].(map[string]any), msgs[4].(map[string]any)
	if t1["tool_call_id"] != "c1" || t1["content"] != `{"status":{}}` || t2["tool_call_id"] != "c2" || t2["content"] != "[tool error] no such robot" {
		t.Errorf("tool messages %v %v", t1, t2)
	}
	tools := rq["tools"].([]any)
	tf := tools[0].(map[string]any)["function"].(map[string]any)
	if tools[0].(map[string]any)["type"] != "function" || tf["name"] != "get_robot" || tf["description"] != "Read one robot" {
		t.Errorf("tools %v", tools)
	}
	if p := tf["parameters"].(map[string]any); p["type"] != "object" || p["additionalProperties"] != false {
		t.Errorf("parameters %v", p)
	}
}

func TestChatNoToolsOmitsToolFields(t *testing.T) {
	f := newFakeLlama(t, fakeReply{body: chatReply("hello", "", "stop")})
	if _, err := f.backend().Chat(context.Background(), "", []Message{{Role: "user", Text: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	rq := f.request(0)
	if _, ok := rq["tools"]; ok {
		t.Error("tools sent without any tool")
	}
	if _, ok := rq["tool_choice"]; ok {
		t.Error("tool_choice sent without any tool")
	}
	if n := len(rq["messages"].([]any)); n != 1 {
		t.Errorf("empty system prompt still sent: %d messages", n)
	}
}

func TestChatToolWithoutSchemaGetsEmptyObject(t *testing.T) {
	ts, err := llamaTools([]ToolSpec{{Name: "list_zones"}, {Name: "n", InputSchema: json.RawMessage("null")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range ts {
		if string(x.Function.Parameters) != `{"type":"object","properties":{}}` {
			t.Errorf("%s: %s", x.Function.Name, x.Function.Parameters)
		}
	}
	if _, err := llamaTools([]ToolSpec{{Name: "bad", InputSchema: json.RawMessage("{")}}); err == nil {
		t.Error("invalid schema accepted")
	}
}

func TestChatFinalAnswer(t *testing.T) {
	f := newFakeLlama(t, fakeReply{body: chatReply("Robot/sim-robot-002 status.estopState=Stopped", "", "stop")})
	l := f.backend()
	r, err := l.Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, oneTool)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Robot/sim-robot-002 status.estopState=Stopped" || len(r.Calls) != 0 || r.StopReason != "stop" {
		t.Fatalf("%+v", r)
	}
	if r.Usage != (Usage{InputTokens: 321, OutputTokens: 45}) {
		t.Errorf("usage %+v", r.Usage)
	}
	if l.Last.PromptN != 321 || l.Last.PredictedPerSecond != 10 || l.Last.PromptMs != 1500.5 {
		t.Errorf("timings %+v", l.Last)
	}
}

func TestChatToolCalls(t *testing.T) {
	calls := `[` +
		`{"id":"abc123","type":"function","function":{"name":"get_robot","arguments":"{ \"name\": \"sim-robot-002\" }"}},` +
		`{"id":"","type":"function","function":{"name":"list_zones","arguments":""}},` +
		`{"id":"abc123","type":"function","function":{"name":"get_zone","arguments":"null"}}]`
	f := newFakeLlama(t, fakeReply{body: chatReply("", calls, "tool_calls")})
	r, err := f.backend().Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, oneTool)
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != "tool_calls" || r.Text != "" || len(r.Calls) != 3 {
		t.Fatalf("%+v", r)
	}
	want := []struct{ id, name, args string }{
		{"abc123", "get_robot", `{"name":"sim-robot-002"}`}, // compacted
		{"call_1", "list_zones", `{}`},                      // empty id and empty arguments
		{"call_2", "get_zone", `{}`},                        // duplicate id
	}
	for i, w := range want {
		c := r.Calls[i]
		if c.ID != w.id || c.Name != w.name || string(c.Args) != w.args {
			t.Errorf("call %d: %+v want %+v", i, c, w)
		}
	}
	// Raw is what goes back on the next request, with the repaired ids.
	raw, ok := r.Raw.(LlamaMessage)
	if !ok || len(raw.ToolCalls) != 3 || raw.ToolCalls[1].ID != "call_1" {
		t.Fatalf("raw %#v", r.Raw)
	}
}

func TestChatLengthCutsOffToolCalls(t *testing.T) {
	calls := `[{"id":"a","type":"function","function":{"name":"get_robot","arguments":"{\"name\":\"sim-ro"}}]`
	f := newFakeLlama(t, fakeReply{body: chatReply("", calls, "length")})
	r, err := f.backend().Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, oneTool)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 0 || r.StopReason != "length" {
		t.Fatalf("a call cut off by max_tokens must not run: %+v", r)
	}
	if raw := r.Raw.(LlamaMessage); len(raw.ToolCalls) != 0 {
		t.Errorf("history must not keep the partial call: %+v", raw)
	}
}

// Arguments that are not JSON must not reach json.RawMessage unchecked (a
// trace holding them would not marshal). They arrive as a JSON string, which
// the agent loop reports to the model as invalid arguments; the original text
// stays in Raw.
func TestChatMalformedArguments(t *testing.T) {
	const bad = `{"name": sim-robot-002`
	badJSON, _ := json.Marshal(bad)
	calls := `[{"id":"c1","type":"function","function":{"name":"get_robot","arguments":` + string(badJSON) + `}}]`
	f := newFakeLlama(t, fakeReply{body: chatReply("", calls, "tool_calls")}, fakeReply{body: chatReply("ok", "", "stop")})
	l := f.backend()
	r, err := l.Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, oneTool)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 1 || !json.Valid(r.Calls[0].Args) {
		t.Fatalf("args must be valid JSON: %+v", r.Calls)
	}
	var s string
	if err := json.Unmarshal(r.Calls[0].Args, &s); err != nil || s != bad {
		t.Fatalf("wrapped args %s", r.Calls[0].Args)
	}
	var asMap map[string]any
	if json.Unmarshal(r.Calls[0].Args, &asMap) == nil {
		t.Fatal("wrapped args must fail the loop's object decode so the model is told")
	}
	if _, err := json.Marshal(struct{ Args json.RawMessage }{r.Calls[0].Args}); err != nil {
		t.Fatalf("trace would not marshal: %v", err)
	}
	// Next turn: the assistant message is replayed, with the model's text as a
	// JSON string, because llama-server rejects history arguments that are not JSON.
	hist := []Message{
		{Role: "user", Text: "q"},
		{Role: "assistant", Calls: r.Calls, Raw: r.Raw},
		{Role: "tool", Results: []ToolResult{{CallID: "c1", Content: "invalid tool arguments", IsError: true}}},
	}
	if _, err := l.Chat(context.Background(), "s", hist, oneTool); err != nil {
		t.Fatal(err)
	}
	m := f.request(1)["messages"].([]any)
	fn := m[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	sent, _ := fn["arguments"].(string)
	var back string
	if !json.Valid([]byte(sent)) || json.Unmarshal([]byte(sent), &back) != nil || back != bad {
		t.Errorf("history arguments %q: want a JSON string holding the model's text %q", sent, bad)
	}
	// The stored turn is not changed by replaying it.
	if raw := r.Raw.(LlamaMessage); raw.ToolCalls[0].Function.Arguments != bad {
		t.Errorf("Raw was modified: %q", raw.ToolCalls[0].Function.Arguments)
	}
}

func TestNormalizeArgs(t *testing.T) {
	for in, want := range map[string]string{
		"":                     `{}`,
		"  ":                   `{}`,
		"null":                 `{}`,
		`{"a": 1}`:             `{"a":1}`,
		"[1, 2]":               `[1,2]`, // valid JSON, not an object: the loop's decode rejects it
		`"x"`:                  `"x"`,
		`{"a":`:                `"{\"a\":"`,
		"<tool_call>x":         "\"\\u003ctool_call\\u003ex\"", // json.Marshal escapes <; the decoded text is unchanged
		"{\"name\": 'single'}": `"{\"name\": 'single'}"`,
	} {
		if got := string(normalizeArgs(in)); got != want {
			t.Errorf("normalizeArgs(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestChatToolCallFailures(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply fakeReply
		want  string
	}{
		{"server parse failure", fakeReply{status: 500, body: `{"error":{"code":500,"message":"Failed to parse input at pos 17: <tool_call>{\"name\":","type":"server_error"}}`}, "server could not parse"},
		{"call without name", fakeReply{body: chatReply("", `[{"id":"a","type":"function","function":{"name":"","arguments":"{}"}}]`, "tool_calls")}, "without a function name"},
		{"call left in text", fakeReply{body: chatReply("Let me look.\n<tool_call>\n{\"name\": \"get_robot\", \"arguments\": {\"name\": \"x\"}}\n</tool_call>", "", "stop")}, "left in the reply text"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeLlama(t, c.reply)
			_, err := f.backend().Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, oneTool)
			var tce *ToolCallError
			if !errors.Is(err, ErrToolCall) || !errors.As(err, &tce) || !strings.Contains(tce.Reason, c.want) {
				t.Fatalf("got %v", err)
			}
			if f.count() != 1 {
				t.Errorf("a bad model answer must not be retried: %d requests", f.count())
			}
		})
	}
	// Without tools offered, the same text is just an answer.
	f := newFakeLlama(t, fakeReply{body: chatReply("write <tool_call> literally", "", "stop")})
	if r, err := f.backend().Chat(context.Background(), "", []Message{{Role: "user", Text: "q"}}, nil); err != nil || !strings.Contains(r.Text, "<tool_call>") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestChatHTTPErrors(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply fakeReply
		want  string
	}{
		{"tools need --jinja", fakeReply{status: 500, body: `{"error":{"code":500,"message":"tools param requires --jinja flag","type":"server_error"}}`}, "HTTP 500: tools param requires --jinja flag"},
		{"bad request", fakeReply{status: 400, body: `{"error":{"code":400,"message":"prompt is too long","type":"invalid_request_error"}}`}, "HTTP 400: prompt is too long"},
		{"non-JSON error body", fakeReply{status: 502, body: "<html>bad gateway</html>"}, "HTTP 502: <html>bad gateway</html>"},
		{"error inside a 200", fakeReply{body: `{"error":{"code":429,"message":"slot busy"}}`}, "HTTP 429: slot busy"},
		{"not JSON", fakeReply{body: "OK"}, "reply is not JSON"},
		{"no choices", fakeReply{body: `{"choices":[]}`}, "no choices"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeLlama(t, c.reply)
			_, err := f.backend().Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, oneTool)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if errors.Is(err, ErrToolCall) || errors.Is(err, ErrTimeout) {
				t.Errorf("wrong class: %v", err)
			}
		})
	}
	// Long error bodies are cut.
	f := newFakeLlama(t, fakeReply{status: 500, body: strings.Repeat("x", 5000)})
	_, err := f.backend().Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, nil)
	var se *StatusError
	if !errors.As(err, &se) || len(se.Body) > llamaErrExcerpt+4 {
		t.Fatalf("%v", err)
	}
}

func TestChatRetriesWhileModelLoads(t *testing.T) {
	loading := fakeReply{status: 503, body: `{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`}
	f := newFakeLlama(t, loading, loading, fakeReply{body: chatReply("up", "", "stop")})
	r, err := f.backend().Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, nil)
	if err != nil || r.Text != "up" || f.count() != 3 {
		t.Fatalf("%+v %v after %d requests", r, err, f.count())
	}
	// The retried request carries the full body.
	if f.request(2)["model"] != DefaultLlamaCppModel {
		t.Error("retry lost the body")
	}
	// A server that never comes up gives up with the 503.
	g := newFakeLlama(t, loading, loading, loading, loading, loading, loading, loading)
	_, err = g.backend().Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 503 || g.count() != llamaLoadingRetries+1 {
		t.Fatalf("%v after %d requests", err, g.count())
	}
	// Cancelling during the wait returns at once.
	h := newFakeLlama(t, loading, loading)
	l := h.backend()
	l.RetryWait = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = l.Chat(ctx, "s", []Message{{Role: "user", Text: "q"}}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

func TestChatTimeout(t *testing.T) {
	f := newFakeLlama(t, fakeReply{body: chatReply("late", "", "stop"), delay: 2 * time.Second})
	l := f.backend()
	l.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := l.Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, nil)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("returned after %v", time.Since(start))
	}
	if f.count() != 1 {
		t.Errorf("a timeout must not be retried: %d requests", f.count())
	}
	if !strings.Contains(err.Error(), "100ms") {
		t.Errorf("message should name the limit: %v", err)
	}
}

// The caller's own context deadline is not reported as the backend's timeout.
func TestChatContextDeadlineIsNotBackendTimeout(t *testing.T) {
	f := newFakeLlama(t, fakeReply{body: chatReply("late", "", "stop"), delay: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := f.backend().Chat(ctx, "s", []Message{{Role: "user", Text: "q"}}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := f.backend().Chat(ctx2, "s", []Message{{Role: "user", Text: "q"}}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestChatServerDown(t *testing.T) {
	f := newFakeLlama(t)
	l := f.backend()
	f.srv.Close()
	_, err := l.Chat(context.Background(), "s", []Message{{Role: "user", Text: "q"}}, nil)
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
}

func TestChatUnknownRole(t *testing.T) {
	f := newFakeLlama(t)
	if _, err := f.backend().Chat(context.Background(), "", []Message{{Role: "system"}}, nil); err == nil {
		t.Fatal("unknown role accepted")
	}
	if f.count() != 0 {
		t.Error("request sent for an invalid history")
	}
}

func TestProps(t *testing.T) {
	f := newFakeLlama(t, fakeReply{body: `{"build_info":"b12004-abc","model_path":"/models/q.gguf","default_generation_settings":{"n_ctx":8192}}`}, fakeReply{status: 503, body: `{"error":{"message":"x"}}`})
	l := f.backend()
	l.RetryWait = time.Millisecond
	p, err := l.Props(context.Background())
	if err != nil || !strings.Contains(string(p), "b12004-abc") || f.paths[0] != "GET /props" {
		t.Fatalf("%s %v %v", p, err, f.paths)
	}
	h := newFakeLlama(t, fakeReply{body: "not json"})
	if _, err := h.backend().Props(context.Background()); err == nil {
		t.Fatal("non-JSON /props accepted")
	}
}

func TestReplayArgs(t *testing.T) {
	for in, want := range map[string]string{
		"":          `{}`,
		"   ":       `{}`,
		`{"a": 1}`:  `{"a": 1}`,
		`null`:      `null`,
		`{"a":`:     `"{\"a\":"`,
		`"{\"a\":"`: `"{\"a\":"`,
	} {
		if got := replayArgs(in); got != want {
			t.Errorf("replayArgs(%q) = %s, want %s", in, got, want)
		}
	}
}
