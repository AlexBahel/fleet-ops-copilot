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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
)

type echoIn struct {
	Text string `json:"text"`
}
type echoOut struct {
	Text     string `json:"text"`
	Operator string `json:"operator"`
}

// The loop passes session identity in _meta, records each call, and reports
// a latency covering the whole request.
func TestLoopRecordsCallsMetaAndLatency(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo"}, func(_ context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		time.Sleep(5 * time.Millisecond)
		op, _ := req.Params.Meta[mcpserver.MetaOperator].(string)
		return nil, echoOut{Text: in.Text, Operator: op}, nil
	})
	ctx := context.Background()
	cs, closeFn, err := Connect(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	args, _ := json.Marshal(map[string]any{"text": "hi"})
	be := &llm.Scripted{Steps: []llm.Step{{Calls: []llm.ToolCall{{ID: "1", Name: "echo", Args: args}}}, {Final: "done"}}}
	tr, err := (&Loop{Backend: be, Session: cs}).Run(ctx, "sam", "s1", "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Final != "done" || len(tr.Calls) != 1 || tr.Calls[0].IsError {
		t.Fatalf("trace %+v", tr)
	}
	var out echoOut
	if err := json.Unmarshal(tr.Calls[0].Result, &out); err != nil || out.Operator != "sam" {
		t.Fatalf("operator not passed in _meta: %s", tr.Calls[0].Result)
	}
	if tr.LatencyMs < 5 || tr.Ended.Before(tr.Started) {
		t.Fatalf("latency %v ms not recorded", tr.LatencyMs)
	}
}

func TestLoopTurnLimit(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Text: in.Text}, nil
	})
	ctx := context.Background()
	cs, closeFn, err := Connect(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	args, _ := json.Marshal(map[string]any{"text": "x"})
	steps := make([]llm.Step, 5)
	for i := range steps {
		steps[i] = llm.Step{Calls: []llm.ToolCall{{ID: "c", Name: "echo", Args: args}}}
	}
	_, err = (&Loop{Backend: &llm.Scripted{Steps: steps}, Session: cs, MaxTurns: 3}).Run(ctx, "sam", "s1", "loop")
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("want ErrMaxTurns, got %v", err)
	}
}

// The system prompt is frozen until the model runs of record (docs/DESIGN.md
// D46). Changing its text must come with a new SystemPromptVersion and a DESIGN
// entry; this test pins the text of the current version.
func TestSystemPromptPinned(t *testing.T) {
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(SystemPrompt)))
	// v3 keeps the v2 text (the tool descriptions changed, D66); v5 and v6
	// keep the v4 text (propose_task's schema changed, D85, D90, D93, D97).
	if SystemPromptVersion != "v6" || got != "70cef1c86bfdd9b885dfe50c3787b2f3ea75c9ee8966fe4c6747ad666cc4b1ff" {
		t.Fatalf("system prompt changed (version %s, sha256 %s): bump SystemPromptVersion, log it in DESIGN, update this test", SystemPromptVersion, got)
	}
}

// P9: the text of every model response of a message reaches the person, in
// order, and the trace records each response's text.
func TestEveryResponseTextReachesThePerson(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Text: in.Text}, nil
	})
	ctx := context.Background()
	cs, closeFn, err := Connect(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	args, _ := json.Marshal(map[string]any{"text": "x"})
	be := &llm.Scripted{Steps: []llm.Step{
		{Final: "Robot/sim-robot-001 status.phase=Idle.", Calls: []llm.ToolCall{{ID: "1", Name: "echo", Args: args}}},
		{Calls: []llm.ToolCall{{ID: "2", Name: "echo", Args: args}}},
		{Final: "It can take the work."},
	}}
	tr, err := (&Loop{Backend: be, Session: cs}).Run(ctx, "sam", "s1", "is sim-robot-001 free?")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Robot/sim-robot-001 status.phase=Idle.\n\nIt can take the work."; tr.Final != want {
		t.Fatalf("final %q, want %q", tr.Final, want)
	}
	if got := fmt.Sprintf("%q", tr.Responses); got != `["Robot/sim-robot-001 status.phase=Idle." "" "It can take the work."]` {
		t.Fatalf("responses %s", got)
	}
}
