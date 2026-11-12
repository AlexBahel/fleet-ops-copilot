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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Claude defaults (docs/DESIGN.md §2, §3.5). The model and effort are set
// explicitly on every request.
const (
	DefaultClaudeModel  = "claude-sonnet-5-5"
	DefaultClaudeEffort = "medium"
)

// Claude calls the Claude Messages API with a manual tool-use loop step: one
// request per Chat. The API key comes from ANTHROPIC_API_KEY only.
//
// Thinking is adaptive (the default on the models used here); each assistant
// turn is kept as the API returned it (Reply.Raw) and sent back unchanged, so
// thinking blocks survive between tool calls. Tools and the system prompt are
// prompt-cached. No server-side refusal fallback is configured: a refusal is
// reported as StopReason "refusal" rather than answered by another model, so
// a measurement always names the model that answered.
type Claude struct {
	Model     string
	Effort    string
	MaxTokens int64
	client    anthropic.Client
}

// NewClaude builds the backend. model and effort default to
// DefaultClaudeModel and DefaultClaudeEffort.
func NewClaude(model, effort string) (*Claude, error) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}
	if model == "" {
		model = DefaultClaudeModel
	}
	if effort == "" {
		effort = DefaultClaudeEffort
	}
	return &Claude{
		Model: model, Effort: effort, MaxTokens: 16000,
		client: anthropic.NewClient(option.WithRequestTimeout(5*time.Minute), option.WithMaxRetries(4)),
	}, nil
}

// Name implements Backend.
func (c *Claude) Name() string { return "claude:" + c.Model }

// Chat implements Backend.
func (c *Claude) Chat(ctx context.Context, system string, history []Message, tools []ToolSpec) (Reply, error) {
	msgs, err := claudeMessages(history)
	if err != nil {
		return Reply{}, err
	}
	ts, err := claudeTools(tools)
	if err != nil {
		return Reply{}, err
	}
	p := c.baseParams()
	p.System = []anthropic.TextBlockParam{{Text: system}}
	p.Messages = msgs
	p.Tools = ts
	resp, err := c.client.Messages.New(ctx, p)
	if err != nil {
		return Reply{}, err
	}
	r := Reply{
		StopReason: string(resp.StopReason),
		Raw:        resp.ToParam(),
		Usage: Usage{
			InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens, CacheReadInputTokens: resp.Usage.CacheReadInputTokens,
		},
	}
	var text []string
	for _, b := range resp.Content {
		switch v := b.AsAny().(type) {
		case anthropic.TextBlock:
			text = append(text, v.Text)
		case anthropic.ToolUseBlock:
			r.Calls = append(r.Calls, ToolCall{ID: v.ID, Name: v.Name, Args: json.RawMessage(v.JSON.Input.Raw())})
		}
	}
	r.Text = strings.Join(text, "\n")
	if resp.StopReason == anthropic.StopReasonRefusal {
		r.Calls = nil
		r.Text = fmt.Sprintf("[refusal: category=%s] %s", resp.StopDetails.Category, r.Text)
	}
	if resp.StopReason == anthropic.StopReasonMaxTokens && len(r.Calls) > 0 {
		// A tool call cut off by max_tokens may carry partial input; do not run it.
		r.Calls = nil
	}
	return r, nil
}

// baseParams holds every request field except the conversation and tools.
// Chat and Settings both start from it.
func (c *Claude) baseParams() anthropic.MessageNewParams {
	return anthropic.MessageNewParams{
		Model:        c.Model,
		MaxTokens:    c.MaxTokens,
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(c.Effort)},
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}
}

// claudeSettingKeys are the request fields that control sampling, thinking,
// effort and output length.
var claudeSettingKeys = []string{"model", "max_tokens", "output_config", "thinking", "temperature", "top_p", "top_k", "cache_control"}

// Settings returns every request field that controls sampling, thinking or
// effort, with the value Chat sends, or NotSent for a field it leaves out
// (the server's default applies). Evaluation results record it.
func (c *Claude) Settings() map[string]string {
	s, err := settingsFrom(c.baseParams(), claudeSettingKeys)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	s["anthropic-version"] = "set by github.com/anthropics/anthropic-sdk-go/v2 " + moduleVersion("github.com/anthropics/anthropic-sdk-go/v2")
	return s
}

func claudeMessages(history []Message) ([]anthropic.MessageParam, error) {
	var out []anthropic.MessageParam
	for _, m := range history {
		switch m.Role {
		case "user":
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Text)))
		case "assistant":
			if raw, ok := m.Raw.(anthropic.MessageParam); ok {
				out = append(out, raw)
				continue
			}
			var blocks []anthropic.ContentBlockParamUnion
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Text))
			}
			for _, c := range m.Calls {
				var in any = map[string]any{}
				if len(c.Args) > 0 {
					if err := json.Unmarshal(c.Args, &in); err != nil {
						return nil, err
					}
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(c.ID, in, c.Name))
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		case "tool":
			var blocks []anthropic.ContentBlockParamUnion
			for _, r := range m.Results {
				blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
			}
			// All results of one turn go back in a single user message.
			out = append(out, anthropic.NewUserMessage(blocks...))
		default:
			return nil, fmt.Errorf("unknown role %q", m.Role)
		}
	}
	return out, nil
}

func claudeTools(tools []ToolSpec) ([]anthropic.ToolUnionParam, error) {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		var schema map[string]any
		if len(t.InputSchema) > 0 && string(t.InputSchema) != "null" {
			if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("tool %s schema: %w", t.Name, err)
			}
		}
		is := anthropic.ToolInputSchemaParam{Properties: schema["properties"], ExtraFields: map[string]any{}}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					is.Required = append(is.Required, s)
				}
			}
		}
		for k, v := range schema {
			if k != "properties" && k != "required" && k != "type" && k != "$schema" {
				is.ExtraFields[k] = v
			}
		}
		tp := anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description), InputSchema: is}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out, nil
}
