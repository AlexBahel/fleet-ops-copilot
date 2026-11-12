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
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

func TestClaudeMessagesRoundTrip(t *testing.T) {
	raw := anthropic.NewAssistantMessage(anthropic.NewTextBlock("kept as returned"))
	h := []Message{
		{Role: "user", Text: "why is sim-robot-002 idle?"},
		{Role: "assistant", Calls: []ToolCall{{ID: "t1", Name: "get_robot", Args: json.RawMessage(`{"name":"sim-robot-002"}`)}}},
		{Role: "tool", Results: []ToolResult{{CallID: "t1", Content: "{}"}, {CallID: "t2", Content: "boom", IsError: true}}},
		{Role: "assistant", Text: "ignored when Raw is set", Raw: raw},
	}
	msgs, err := claudeMessages(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages", len(msgs))
	}
	b, _ := json.Marshal(msgs)
	s := string(b)
	for _, want := range []string{`"tool_use"`, `"tool_use_id":"t1"`, `"tool_use_id":"t2"`, `"is_error":true`, `kept as returned`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "ignored when Raw is set") {
		t.Error("Raw must replace the rebuilt assistant turn")
	}
	// Both tool results travel in one user message.
	m2, _ := json.Marshal(msgs[2])
	if !strings.Contains(string(m2), `"role":"user"`) || strings.Count(string(m2), `"tool_result"`) != 2 {
		t.Errorf("tool results: %s", m2)
	}
	if _, err := claudeMessages([]Message{{Role: "system"}}); err == nil {
		t.Error("unknown role accepted")
	}
}

func TestClaudeTools(t *testing.T) {
	ts, err := claudeTools([]ToolSpec{{Name: "get_robot", Description: "d", InputSchema: json.RawMessage(
		`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(ts)
	for _, want := range []string{`"name":"get_robot"`, `"required":["name"]`, `"additionalProperties":false`, `"type":"object"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}

func TestNewClaudeNeedsKeyFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := NewClaude("", ""); err == nil {
		t.Fatal("NewClaude without ANTHROPIC_API_KEY")
	}
	t.Setenv("ANTHROPIC_API_KEY", "test-not-a-key")
	c, err := NewClaude("", "")
	if err != nil || c.Model != DefaultClaudeModel || c.Effort != DefaultClaudeEffort {
		t.Fatalf("%+v %v", c, err)
	}
}

// D43: the recorded settings come from the request fields Chat sends.
func TestClaudeSettingsRecordWhatIsSent(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	c, err := NewClaude("claude-sonnet-5-5", "medium")
	if err != nil {
		t.Fatal(err)
	}
	s := c.Settings()
	want := map[string]string{
		"model": "claude-sonnet-5-5", "max_tokens": "16000", "output_config": `{"effort":"medium"}`,
		"thinking": NotSent, "temperature": NotSent, "top_p": NotSent, "top_k": NotSent,
		"cache_control": `{"type":"ephemeral"}`,
	}
	for k, v := range want {
		if s[k] != v {
			t.Errorf("%s = %q, want %q", k, s[k], v)
		}
	}
	if !strings.HasPrefix(s["anthropic-version"], "set by github.com/anthropics/anthropic-sdk-go/v2 ") {
		t.Errorf("anthropic-version = %q", s["anthropic-version"])
	}
}
