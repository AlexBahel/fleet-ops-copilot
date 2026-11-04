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

package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
)

type echoIn struct {
	Name string `json:"name"`
}
type echoOut struct {
	Name string `json:"name"`
}

func completion(content, toolCalls, finish string) string {
	c := "null"
	if content != "" {
		b, _ := json.Marshal(content)
		c = string(b)
	}
	tc := ""
	if toolCalls != "" {
		tc = `,"tool_calls":` + toolCalls
	}
	return `{"choices":[{"finish_reason":"` + finish + `","message":{"role":"assistant","content":` + c + tc + `}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
}

// The unchanged agent loop drives the llamacpp backend: a malformed tool call
// comes back to the model as an error, the model's own text is replayed to the
// server, and the corrected call runs. The trace marshals.
func TestLoopWithLlamaCppRecoversFromMalformedArguments(t *testing.T) {
	const bad = `{"name": sim-robot-002`
	badJSON, _ := json.Marshal(bad)
	replies := []string{
		completion("", `[{"id":"c1","type":"function","function":{"name":"get_robot","arguments":`+string(badJSON)+`}}]`, "tool_calls"),
		completion("", `[{"id":"c2","type":"function","function":{"name":"get_robot","arguments":"{\"name\":\"sim-robot-002\"}"}}]`, "tool_calls"),
		completion("Robot/sim-robot-002 exists", "", "stop"),
	}
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		i := len(bodies) - 1
		mu.Unlock()
		_, _ = w.Write([]byte(replies[i]))
	}))
	defer srv.Close()

	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	mcp.AddTool(mcpSrv, &mcp.Tool{Name: "get_robot"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut(in), nil
	})
	ctx := context.Background()
	cs, closeFn, err := agent.Connect(ctx, mcpSrv)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	be, err := llm.NewLlamaCpp(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := (&agent.Loop{Backend: be, Session: cs}).Run(ctx, "sam", "s1", "does sim-robot-002 exist?")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Final != "Robot/sim-robot-002 exists" || tr.Turns != 3 || len(tr.Calls) != 2 {
		t.Fatalf("trace %+v", tr)
	}
	if !tr.Calls[0].IsError || !strings.Contains(tr.Calls[0].ErrorText, "invalid tool arguments") || tr.Calls[1].IsError {
		t.Fatalf("calls %+v", tr.Calls)
	}
	if tr.Usage.InputTokens != 30 || tr.Usage.OutputTokens != 15 || len(tr.StopReasons) != 3 || tr.StopReasons[2] != "stop" {
		t.Errorf("usage/stop reasons %+v %v", tr.Usage, tr.StopReasons)
	}
	if _, err := json.Marshal(tr); err != nil {
		t.Fatalf("trace does not marshal: %v", err)
	}
	// Second request: the model sees its own text and the error as a tool message.
	var rq struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				Function struct{ Arguments string } `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &rq); err != nil {
		t.Fatal(err)
	}
	m := rq.Messages
	if len(m) != 4 || m[0].Role != "system" || m[2].ToolCalls[0].Function.Arguments != bad ||
		m[3].Role != "tool" || m[3].ToolCallID != "c1" || !strings.HasPrefix(m[3].Content, "[tool error] invalid tool arguments") {
		t.Fatalf("second request: %s", bodies[1])
	}
}
