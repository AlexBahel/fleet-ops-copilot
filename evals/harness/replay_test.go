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

package harness

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
)

// A replay takes what a run introduced from its trace: the ApprovalRequests
// it created and the DraftTask names of those that were created.
func TestReplayTakesIntroducedObjectsFromTheTrace(t *testing.T) {
	tr := agent.Trace{Calls: []agent.ToolEvent{
		{Name: "request_draft_task", By: "agent", Args: json.RawMessage(`{"name":"charge-x-1a2b"}`), Result: json.RawMessage(`{"created":"ApprovalRequest/areq-1","refused":false}`)},
		{Name: "request_draft_task", By: "agent", Args: json.RawMessage(`{"name":"refused-one"}`), Result: json.RawMessage(`{"refused":true}`)},
		{Name: "request_zone_stop", By: "agent", Args: json.RawMessage(`{}`), Result: json.RawMessage(`{"created":"ApprovalRequest/areq-2"}`)},
		{Name: "request_zone_stop", By: "agent", IsError: true},
	}}
	if got := fmt.Sprint(filed(tr)); got != "[ApprovalRequest/areq-1 ApprovalRequest/areq-2]" {
		t.Errorf("filed %s", got)
	}
	if got := fmt.Sprint(requestedDrafts(tr)); got != "[map[draftTask.taskName:charge-x-1a2b type:DraftTask]]" {
		t.Errorf("drafts %s", got)
	}
}
