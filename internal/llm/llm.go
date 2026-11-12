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

// Package llm defines the model-backend interface the agent loop uses, and
// three backends: claude (Claude API, claude.go), llamacpp (llama-server's
// chat-completions API, llamacpp.go) and scripted, which replays fixed turns
// for tests and the CI evaluation gate.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sort"
)

// ToolSpec describes one tool offered to the model.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolCall is the model asking for one tool.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult is a tool's answer to one call.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Message is one turn of history.
type Message struct {
	Role    string // "user", "assistant" or "tool"
	Text    string
	Calls   []ToolCall
	Results []ToolResult
	// Raw is the backend's own record of an assistant turn (for claude, the
	// message with its thinking blocks), passed back unchanged on the next
	// request. Other backends ignore it.
	Raw any
}

// Usage is the token count a backend reports for one reply.
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	// Prompt-cache tokens, billed at their own rates; not included in InputTokens.
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens,omitempty"`
}

// Add accumulates u2 into u.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.OutputTokens += u2.OutputTokens
	u.CacheCreationInputTokens += u2.CacheCreationInputTokens
	u.CacheReadInputTokens += u2.CacheReadInputTokens
}

// Reply is the model's turn: tool calls, or final text when Calls is empty.
type Reply struct {
	Text  string
	Calls []ToolCall
	Usage Usage
	// StopReason as reported by the backend (claude: end_turn, tool_use, max_tokens, refusal, ...).
	StopReason string
	Raw        any
}

// Backend is a chat model with tool use.
type Backend interface {
	Name() string
	Chat(ctx context.Context, system string, history []Message, tools []ToolSpec) (Reply, error)
}

// Step is one scripted model turn: tool calls, or a final answer.
type Step struct {
	Calls []ToolCall
	Final string
}

// Scripted replays fixed steps, ignoring its input. It measures the harness,
// tools and policy path, not a model.
type Scripted struct {
	Steps []Step
	next  int
}

// Name implements Backend.
func (s *Scripted) Name() string { return "scripted" }

// Chat implements Backend.
func (s *Scripted) Chat(_ context.Context, _ string, _ []Message, _ []ToolSpec) (Reply, error) {
	if s.next >= len(s.Steps) {
		return Reply{}, fmt.Errorf("script exhausted after %d steps", len(s.Steps))
	}
	st := s.Steps[s.next]
	s.next++
	return Reply{Text: st.Final, Calls: st.Calls}, nil
}

// NotSent marks a setting the backend leaves out of its requests.
const NotSent = "not sent (server default)"

// settingsFrom reads the named top-level fields from the JSON encoding of a
// request, so the record is taken from what is sent rather than restated.
func settingsFrom(req any, keys []string) (map[string]string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	out := map[string]string{}
	sort.Strings(keys)
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			out[k] = NotSent
			continue
		}
		var str string
		if json.Unmarshal(v, &str) == nil {
			out[k] = str
		} else {
			out[k] = string(v)
		}
	}
	return out, nil
}

// moduleVersion is the version of a dependency compiled into the binary.
func moduleVersion(path string) string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == path {
				return d.Version
			}
		}
	}
	return "unknown"
}
