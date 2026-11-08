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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
)

// Rejudged is one recorded run judged again from its trace, with no backend
// call: the criteria the trace decides are re-evaluated by this
// JudgeVersion and the given scenario files: the tool calls (criterion 1),
// the refusal's statement, fact and code (criterion 3; whether a request or
// draft existed is the recorded finding) and the answer (criterion 4). The
// approval and dialogue criteria are the recorded ones, which depend on the
// cluster state at the end of the run.
type Rejudged struct {
	Scenario string `json:"scenario"`
	Run      int    `json:"run"`
	// Recorded is the verdict in the traces file; ToolCalls, Refusal and
	// Explanation are those criteria as this judge reads the recorded run;
	// Pass is the recorded verdict with them replaced.
	Recorded    Verdict   `json:"recorded"`
	ToolCalls   Criterion `json:"toolCalls"`
	Refusal     Criterion `json:"refusal"`
	Explanation Criterion `json:"explanation"`
	Pass        bool      `json:"pass"`
	Judge       string    `json:"judge"`
}

// existed is the start of judgeRefusal's finding that a request or draft
// existed at the end of the run, which a replay cannot observe.
const existed = "a refusal was expected, but "

// rejudgeRefusal is criterion 3 for a recorded run: judged again from the
// trace, with the recorded finding about requests and drafts that existed.
func rejudgeRefusal(s Scenario, tr agent.Trace, ob Observed, rec Criterion) Criterion {
	ob.Requests, ob.Drafts = nil, nil
	c := judgeRefusal(s, tr, ob)
	var kept []string
	for _, d := range rec.Detail {
		if strings.HasPrefix(d, existed) {
			kept = append(kept, d)
		}
	}
	if len(kept) > 0 {
		c.Pass = false
		c.Detail = append(kept, c.Detail...)
	}
	return c
}

// Rejudge reads a traces.jsonl and judges the answer of each run again
// against its fixture (loaded with the scenario's setup, as the run saw it).
// The requests a run filed are not recreated; the ApprovalRequests it
// created and the DraftTask names it asked for are taken from its trace, so
// an object the run introduced is still not an invented one. Scenarios are by id from scs, which must be
// the scenario files the runs were made with.
func (r *Runner) Rejudge(ctx context.Context, scs []Scenario, in io.Reader, out io.Writer) (int, error) {
	byID := map[string]Scenario{}
	for _, s := range scs {
		byID[s.ID] = s
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		var rec struct {
			Scenario string      `json:"scenario"`
			Run      int         `json:"run"`
			Trace    agent.Trace `json:"trace"`
			Verdict  Verdict     `json:"verdict"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return n, fmt.Errorf("line %d: %w", n+1, err)
		}
		s, ok := byID[rec.Scenario]
		if !ok {
			return n, fmt.Errorf("line %d: no scenario %q", n+1, rec.Scenario)
		}
		admin := r.Env.Admin
		if err := Reset(ctx, admin); err != nil {
			return n, err
		}
		if err := LoadFixture(ctx, admin, filepath.Join(r.FixtureDir, s.Fixture+".yaml")); err != nil {
			return n, err
		}
		for _, m := range s.Setup {
			if err := mutate(ctx, admin, m); err != nil {
				return n, fmt.Errorf("%s setup %s: %w", s.ID, m.Ref, err)
			}
		}
		ob, err := Observe(ctx, admin, s, rec.Trace.Final)
		if err != nil {
			return n, err
		}
		ob.Requests = requestedDrafts(rec.Trace)
		for _, ref := range filed(rec.Trace) {
			ob.Existing[ref] = true
		}
		x := Rejudged{Scenario: rec.Scenario, Run: rec.Run, Recorded: rec.Verdict, Judge: JudgeVersion,
			ToolCalls:   judgeTools(s, rec.Trace),
			Refusal:     rejudgeRefusal(s, rec.Trace, ob, rec.Verdict.Refusal),
			Explanation: judgeExplanation(s, rec.Trace, ob)}
		v := rec.Verdict
		x.Pass = x.ToolCalls.Pass && v.Approval.Pass && x.Refusal.Pass && x.Explanation.Pass && v.Dialogue.Pass
		b, err := json.Marshal(x)
		if err != nil {
			return n, err
		}
		if _, err := out.Write(append(b, '\n')); err != nil {
			return n, err
		}
		n++
	}
	return n, sc.Err()
}

// filed are the ApprovalRequests a run created, from its trace.
func filed(tr agent.Trace) []string {
	var out []string
	for _, e := range tr.Calls {
		var res struct {
			Created string `json:"created"`
		}
		if strings.HasPrefix(e.Name, "request_") && !e.IsError && json.Unmarshal(e.Result, &res) == nil && strings.HasPrefix(res.Created, "ApprovalRequest/") {
			out = append(out, res.Created)
		}
	}
	return out
}

// requestedDrafts are the DraftTask requests a run filed, from its trace:
// only what introduced() reads (the type and the task name).
func requestedDrafts(tr agent.Trace) []map[string]string {
	var out []map[string]string
	for _, e := range tr.Calls {
		if e.Name != "request_draft_task" || e.IsError {
			continue
		}
		var a struct {
			Name string `json:"name"`
		}
		var res struct {
			Created string `json:"created"`
		}
		if json.Unmarshal(e.Args, &a) == nil && a.Name != "" && json.Unmarshal(e.Result, &res) == nil && res.Created != "" {
			out = append(out, map[string]string{"type": "DraftTask", "draftTask.taskName": a.Name})
		}
	}
	return out
}
