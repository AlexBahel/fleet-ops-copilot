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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// llama.cpp backend defaults (docs/DESIGN.md §3.5, D15).
const (
	// EnvLlamaCppURL names the environment variable that holds the server base
	// URL, for example http://llama-server.fleetops-system.svc:8080. No credential
	// is involved.
	EnvLlamaCppURL = "FLEETOPS_LLAMACPP_URL"
	// EnvLlamaCppTemperature overrides DefaultLlamaCppTemperature.
	EnvLlamaCppTemperature = "FLEETOPS_LLAMACPP_TEMPERATURE"
	// EnvLlamaCppTopP, EnvLlamaCppTopK and EnvLlamaCppRepeatPenalty set
	// top_p, top_k and repeat_penalty. Unset, the field is not sent and
	// llama-server uses its own default.
	EnvLlamaCppTopP          = "FLEETOPS_LLAMACPP_TOP_P"
	EnvLlamaCppTopK          = "FLEETOPS_LLAMACPP_TOP_K"
	EnvLlamaCppRepeatPenalty = "FLEETOPS_LLAMACPP_REPEAT_PENALTY"

	// DefaultLlamaCppModel only labels results: llama-server serves the one model
	// it was started with and ignores the request's model field.
	DefaultLlamaCppModel = "qwen2.5-7b-instruct"
	// DefaultLlamaCppTemperature is sent on every request. Zero is greedy
	// decoding; a measurement that wants sampling sets it and records it.
	DefaultLlamaCppTemperature = 0.0
	// DefaultLlamaCppTimeout bounds one request. A 7B model on CPU spends
	// minutes on a long prompt; the harness context is the outer bound.
	DefaultLlamaCppTimeout = 10 * time.Minute

	llamaMaxBody        = 8 << 20 // reply bytes read at most
	llamaErrExcerpt     = 512     // bytes of an error body kept in messages
	llamaLoadingRetries = 5       // 503 "Loading model" retries
)

// ErrToolCall means the model's tool call could not be turned into a ToolCall:
// the server failed to parse it, it was left as text, or it had no name. The
// agent loop does not retry; the harness records the run as failed with this
// text. Use errors.As to reach the *ToolCallError.
var ErrToolCall = errors.New("llamacpp: model output has an unusable tool call")

// ToolCallError carries the reason and the offending text (truncated).
type ToolCallError struct {
	Reason string
	Body   string
}

func (e *ToolCallError) Error() string {
	return fmt.Sprintf("%v: %s: %s", ErrToolCall, e.Reason, e.Body)
}

// Is makes errors.Is(err, ErrToolCall) true.
func (e *ToolCallError) Is(target error) bool { return target == ErrToolCall }

// ErrTimeout means the request ran longer than LlamaCpp.Timeout.
var ErrTimeout = errors.New("llamacpp: request timed out")

// StatusError is a non-200 answer from the server.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("llamacpp: HTTP %d: %s", e.Code, e.Body) }

// LlamaCpp calls llama-server's OpenAI-compatible POST /v1/chat/completions
// with net/http: one request per Chat, tools passed as OpenAI function tools.
// The server must run with --jinja for tool calling; without it the server
// rejects the request and that error is returned unchanged.
//
// No retry covers a timeout or a bad model answer; the only retry is a 503
// while the server is still loading its model.
type LlamaCpp struct {
	BaseURL     string
	Model       string
	Temperature float64
	// TopP, TopK and RepeatPenalty are sent when not nil.
	TopP          *float64
	TopK          *int
	RepeatPenalty *float64
	MaxTokens     int
	Timeout       time.Duration
	// HTTPClient is used as given; nil means a client with Timeout.
	HTTPClient *http.Client
	// RetryWait is the pause before a 503 retry (default 2s; tests shorten it).
	RetryWait time.Duration

	// Last carries the timings llama-server reported for the latest reply and
	// Total their sum over all replies of this backend (see LlamaTimings). They
	// are not goroutine-safe: use one backend per run, as the harness does.
	Last, Total LlamaTimings
}

// LlamaTimings is llama-server's own timing block for one reply, kept so a
// measurement can record prompt-processing and generation speed on the
// hardware that ran it. Zero values mean the server did not report them.
type LlamaTimings struct {
	PromptN            int     `json:"prompt_n"`
	PromptMs           float64 `json:"prompt_ms"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
	PredictedN         int     `json:"predicted_n"`
	PredictedMs        float64 `json:"predicted_ms"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
	CacheN             int     `json:"cache_n"`
}

// add accumulates t2 and recomputes the rates from the sums (tokens per second
// over all replies, not an average of rates).
func (t *LlamaTimings) add(t2 LlamaTimings) {
	t.PromptN += t2.PromptN
	t.PromptMs += t2.PromptMs
	t.PredictedN += t2.PredictedN
	t.PredictedMs += t2.PredictedMs
	t.CacheN += t2.CacheN
	t.PromptPerSecond, t.PredictedPerSecond = 0, 0
	if t.PromptMs > 0 {
		t.PromptPerSecond = float64(t.PromptN) / t.PromptMs * 1000
	}
	if t.PredictedMs > 0 {
		t.PredictedPerSecond = float64(t.PredictedN) / t.PredictedMs * 1000
	}
}

// NewLlamaCpp builds the backend. An empty baseURL is read from
// FLEETOPS_LLAMACPP_URL; an empty model becomes DefaultLlamaCppModel. The
// temperature comes from FLEETOPS_LLAMACPP_TEMPERATURE when set, and top_p, top_k
// and repeat_penalty from FLEETOPS_LLAMACPP_TOP_P, FLEETOPS_LLAMACPP_TOP_K and
// FLEETOPS_LLAMACPP_REPEAT_PENALTY.
func NewLlamaCpp(baseURL, model string) (*LlamaCpp, error) {
	if baseURL == "" {
		baseURL = os.Getenv(EnvLlamaCppURL)
	}
	if baseURL == "" {
		return nil, fmt.Errorf("%s is not set", EnvLlamaCppURL)
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s=%q is not an http(s) URL", EnvLlamaCppURL, baseURL)
	}
	if model == "" {
		model = DefaultLlamaCppModel
	}
	temp := DefaultLlamaCppTemperature
	if s := os.Getenv(EnvLlamaCppTemperature); s != "" {
		t, err := strconv.ParseFloat(s, 64)
		if err != nil || t < 0 || t > 2 {
			return nil, fmt.Errorf("%s=%q is not a number in [0, 2]", EnvLlamaCppTemperature, s)
		}
		temp = t
	}
	l := &LlamaCpp{
		BaseURL: strings.TrimRight(baseURL, "/"), Model: model, Temperature: temp,
		MaxTokens: 4096, Timeout: DefaultLlamaCppTimeout, RetryWait: 2 * time.Second,
	}
	if s := os.Getenv(EnvLlamaCppTopP); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v <= 0 || v > 1 {
			return nil, fmt.Errorf("%s=%q is not a number in (0, 1]", EnvLlamaCppTopP, s)
		}
		l.TopP = &v
	}
	if s := os.Getenv(EnvLlamaCppTopK); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("%s=%q is not a whole number >= 0", EnvLlamaCppTopK, s)
		}
		l.TopK = &v
	}
	if s := os.Getenv(EnvLlamaCppRepeatPenalty); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("%s=%q is not a number > 0", EnvLlamaCppRepeatPenalty, s)
		}
		l.RepeatPenalty = &v
	}
	return l, nil
}

// Name implements Backend.
func (l *LlamaCpp) Name() string { return "llamacpp:" + l.Model }

// Props returns the server's GET /props document (build, model path, context
// size, default sampling). It is also a readiness check.
func (l *LlamaCpp) Props(ctx context.Context) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.BaseURL+"/props", nil)
	if err != nil {
		return nil, err
	}
	body, err := l.do(req)
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("llamacpp: /props did not return JSON: %s", excerpt(body))
	}
	return body, nil
}

// OpenAI-compatible wire types (only the fields used).
type (
	llamaRequest struct {
		Model         string         `json:"model"`
		Messages      []LlamaMessage `json:"messages"`
		Tools         []llamaTool    `json:"tools,omitempty"`
		ToolChoice    string         `json:"tool_choice,omitempty"`
		Temperature   float64        `json:"temperature"`
		TopP          *float64       `json:"top_p,omitempty"`
		TopK          *int           `json:"top_k,omitempty"`
		RepeatPenalty *float64       `json:"repeat_penalty,omitempty"`
		MaxTokens     int            `json:"max_tokens,omitempty"`
		Stream        bool           `json:"stream"`
	}
	// LlamaMessage is one chat message in the OpenAI wire format. It is the
	// value a LlamaCpp reply stores in Reply.Raw.
	LlamaMessage struct {
		Role       string          `json:"role"`
		Content    *string         `json:"content"`
		ToolCalls  []LlamaToolCall `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	// LlamaToolCall is one tool call; Arguments is the model's text, verbatim.
	LlamaToolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	llamaTool struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	llamaResponse struct {
		Choices []struct {
			Message      LlamaMessage `json:"message"`
			FinishReason string       `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
		Timings *LlamaTimings `json:"timings"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
)

// Chat implements Backend.
func (l *LlamaCpp) Chat(ctx context.Context, system string, history []Message, tools []ToolSpec) (Reply, error) {
	msgs, err := llamaMessages(system, history)
	if err != nil {
		return Reply{}, err
	}
	ts, err := llamaTools(tools)
	if err != nil {
		return Reply{}, err
	}
	rq := l.baseRequest()
	rq.Messages, rq.Tools = msgs, ts
	if len(ts) > 0 {
		rq.ToolChoice = "auto"
	}
	payload, err := json.Marshal(rq)
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	body, err := l.do(req)
	if err != nil {
		return Reply{}, err
	}
	return l.parse(body, tools)
}

// baseRequest holds every request field except the conversation and tools.
// Chat and Settings both start from it.
func (l *LlamaCpp) baseRequest() llamaRequest {
	return llamaRequest{Model: l.Model, Temperature: l.Temperature, TopP: l.TopP, TopK: l.TopK, RepeatPenalty: l.RepeatPenalty, MaxTokens: l.MaxTokens}
}

// llamaSettingKeys are the request fields that control sampling and output
// length in llama-server's chat-completions API.
var llamaSettingKeys = []string{"model", "temperature", "max_tokens", "stream", "top_p", "top_k", "min_p", "repeat_penalty", "repeat_last_n", "seed"}

// Settings implements SettingsReporter from the fields Chat sends.
func (l *LlamaCpp) Settings() map[string]string {
	s, err := settingsFrom(l.baseRequest(), llamaSettingKeys)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	s["tool_choice"] = "auto when tools are offered"
	return s
}

// do sends req (retrying a 503 while the model loads) and returns a 200 body.
func (l *LlamaCpp) do(req *http.Request) ([]byte, error) {
	hc := l.HTTPClient
	if hc == nil {
		t := l.Timeout
		if t == 0 {
			t = DefaultLlamaCppTimeout
		}
		hc = &http.Client{Timeout: t}
	}
	for attempt := 0; ; attempt++ {
		if req.GetBody != nil && attempt > 0 {
			b, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = b
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, l.wrapTransport(req.Context(), err)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, llamaMaxBody))
		_ = resp.Body.Close()
		if rerr != nil {
			return nil, l.wrapTransport(req.Context(), rerr)
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusServiceUnavailable && attempt < llamaLoadingRetries:
			wait := l.RetryWait
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(wait):
			}
		case resp.StatusCode == http.StatusInternalServerError && isParseFailure(body):
			return nil, &ToolCallError{Reason: "server could not parse the model's tool call", Body: errorMessage(body)}
		default:
			return nil, &StatusError{Code: resp.StatusCode, Body: errorMessage(body)}
		}
	}
}

func (l *LlamaCpp) wrapTransport(ctx context.Context, err error) error {
	// A deadline on the caller's context is the caller's own bound and is
	// returned as is; only the client's timeout becomes ErrTimeout.
	var ne net.Error
	if ctx.Err() == nil && (errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())) {
		return fmt.Errorf("%w after %s: %w", ErrTimeout, l.timeout(), err)
	}
	return err
}

func (l *LlamaCpp) timeout() time.Duration {
	if l.HTTPClient != nil && l.HTTPClient.Timeout > 0 {
		return l.HTTPClient.Timeout
	}
	if l.Timeout > 0 {
		return l.Timeout
	}
	return DefaultLlamaCppTimeout
}

func (l *LlamaCpp) parse(body []byte, offered []ToolSpec) (Reply, error) {
	var rs llamaResponse
	if err := json.Unmarshal(body, &rs); err != nil {
		return Reply{}, fmt.Errorf("llamacpp: reply is not JSON: %w: %s", err, excerpt(body))
	}
	if rs.Error != nil {
		return Reply{}, &StatusError{Code: rs.Error.Code, Body: rs.Error.Message}
	}
	if len(rs.Choices) == 0 {
		return Reply{}, fmt.Errorf("llamacpp: reply has no choices: %s", excerpt(body))
	}
	ch := rs.Choices[0]
	msg := ch.Message
	r := Reply{
		StopReason: ch.FinishReason,
		Usage:      Usage{InputTokens: rs.Usage.PromptTokens, OutputTokens: rs.Usage.CompletionTokens},
	}
	l.Last = LlamaTimings{}
	if rs.Timings != nil {
		l.Last = *rs.Timings
		l.Total.add(*rs.Timings)
	}
	if msg.Content != nil {
		r.Text = *msg.Content
	}
	seen := map[string]bool{}
	for i := range msg.ToolCalls {
		tc := &msg.ToolCalls[i]
		if tc.Function.Name == "" {
			return Reply{}, &ToolCallError{Reason: "tool call without a function name", Body: excerpt(body)}
		}
		if tc.ID == "" || seen[tc.ID] {
			// Some chat templates leave the id empty; the result must still name its call.
			tc.ID = fmt.Sprintf("call_%d", i)
		}
		seen[tc.ID] = true
		if tc.Type == "" {
			tc.Type = "function"
		}
		r.Calls = append(r.Calls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: normalizeArgs(tc.Function.Arguments)})
	}
	if len(r.Calls) == 0 && len(offered) > 0 && strings.Contains(r.Text, "<tool_call>") {
		// The template's tool call was not recognised by the server and came back
		// as text. Scoring it as a final answer would hide the failure.
		return Reply{}, &ToolCallError{Reason: "tool call left in the reply text, not parsed", Body: excerpt([]byte(r.Text))}
	}
	if ch.FinishReason == "length" && len(r.Calls) > 0 {
		// Cut off by max_tokens: the arguments may be partial; do not run them.
		r.Calls = nil
		msg.ToolCalls = nil
	}
	r.Raw = msg
	return r, nil
}

// normalizeArgs returns arguments the loop can use: empty text becomes {};
// valid JSON is kept compact; invalid JSON is wrapped as a JSON string, which
// the agent loop reports to the model as invalid tool arguments. The model's
// own text stays in Reply.Raw and goes back through replayArgs.
func normalizeArgs(s string) json.RawMessage {
	t := strings.TrimSpace(s)
	if t == "" || t == "null" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(t)) {
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(t)); err == nil {
			return b.Bytes()
		}
	}
	b, _ := json.Marshal(s)
	return b
}

// replayArgs makes the arguments of an earlier tool call safe to send back.
// llama-server parses the arguments of assistant tool calls in the request
// history as JSON for chat templates that render them with tojson, as Qwen's
// does (common/chat.cpp, func_args_not_string), and rejects the whole request
// when they are not JSON. Text that is not JSON is therefore sent as a JSON
// string holding that text, which the model still reads.
func replayArgs(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	if json.Valid([]byte(s)) {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func llamaMessages(system string, history []Message) ([]LlamaMessage, error) {
	str := func(s string) *string { return &s }
	var out []LlamaMessage
	if system != "" {
		out = append(out, LlamaMessage{Role: "system", Content: str(system)})
	}
	for _, m := range history {
		switch m.Role {
		case "user":
			out = append(out, LlamaMessage{Role: "user", Content: str(m.Text)})
		case "assistant":
			if raw, ok := m.Raw.(LlamaMessage); ok {
				// Copy: the stored turn stays as the model produced it.
				raw.ToolCalls = append([]LlamaToolCall(nil), raw.ToolCalls...)
				for i := range raw.ToolCalls {
					raw.ToolCalls[i].Function.Arguments = replayArgs(raw.ToolCalls[i].Function.Arguments)
				}
				out = append(out, raw)
				continue
			}
			am := LlamaMessage{Role: "assistant", Content: str(m.Text)}
			for _, c := range m.Calls {
				tc := LlamaToolCall{ID: c.ID, Type: "function"}
				tc.Function.Name = c.Name
				tc.Function.Arguments = replayArgs(string(c.Args))
				am.ToolCalls = append(am.ToolCalls, tc)
			}
			out = append(out, am)
		case "tool":
			// The OpenAI format carries one tool message per result.
			for _, r := range m.Results {
				content := r.Content
				if r.IsError {
					content = "[tool error] " + content
				}
				out = append(out, LlamaMessage{Role: "tool", ToolCallID: r.CallID, Content: str(content)})
			}
		default:
			return nil, fmt.Errorf("unknown role %q", m.Role)
		}
	}
	return out, nil
}

func llamaTools(tools []ToolSpec) ([]llamaTool, error) {
	out := make([]llamaTool, 0, len(tools))
	for _, t := range tools {
		lt := llamaTool{Type: "function"}
		lt.Function.Name, lt.Function.Description = t.Name, t.Description
		switch {
		case len(t.InputSchema) == 0 || string(t.InputSchema) == "null":
			lt.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		case json.Valid(t.InputSchema):
			lt.Function.Parameters = t.InputSchema
		default:
			return nil, fmt.Errorf("tool %s schema is not JSON", t.Name)
		}
		out = append(out, lt)
	}
	return out, nil
}

// isParseFailure recognises an error status from llama-server about parsing
// the model's tool call. Builds with a lenient output parser (the pinned one)
// return the text instead, which Chat reports as a tool call left in the
// reply; this covers builds that raise an error.
func isParseFailure(body []byte) bool {
	m := strings.ToLower(errorMessage(body))
	return strings.Contains(m, "failed to parse") || strings.Contains(m, "parse tool call") || strings.Contains(m, "parsing tool call")
}

// errorMessage pulls error.message out of an OpenAI-style error body, or
// falls back to the body text.
func errorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return truncate(e.Error.Message)
	}
	return excerpt(body)
}

func excerpt(b []byte) string { return truncate(strings.TrimSpace(string(b))) }

func truncate(s string) string {
	if len(s) > llamaErrExcerpt {
		return s[:llamaErrExcerpt] + "…"
	}
	return s
}
