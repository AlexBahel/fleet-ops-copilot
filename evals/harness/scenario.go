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

// Package harness runs evaluation scenarios against fixture cluster states and
// judges each run by the Pass definition in docs/DESIGN.md §4.1.
package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
)

// Categories are the four scenario categories of docs/DESIGN.md §4.2.
var Categories = []string{"diagnose", "dispatch", "zone-ops", "safety"}

// Scenario is one evaluation case.
type Scenario struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	// Fixture names evals/fixtures/<fixture>.yaml.
	Fixture  string `json:"fixture"`
	Operator string `json:"operator"`
	// OperatorIdentity is the username foc-mcp authenticated for Operator in
	// this run (set by the runner, never by a scenario file). The fixture
	// harness sends a TokenRequest token for a ServiceAccount named after the
	// operator, so an expected requestedFor of Operator is met by this
	// identity (R39).
	OperatorIdentity string `json:"-"`
	Prompt           string `json:"prompt"`
	// Script is the scripted backend's turns. Model backends ignore it.
	Script []ScriptStep `json:"script"`
	Expect Expect       `json:"expect"`
}

// ScriptStep is one scripted turn: one tool call, or the final answer.
type ScriptStep struct {
	Tool  string         `json:"tool,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
	Final string         `json:"final,omitempty"`
}

// Expect is what a passing run must show.
type Expect struct {
	// RequiredTools must each be called at least once without a tool error.
	RequiredTools []string `json:"requiredTools"`
	// ForbiddenTools must not be called.
	ForbiddenTools []string `json:"forbiddenTools,omitempty"`
	// Approval, when set, requires exactly one ApprovalRequest with these
	// fields; when nil, no ApprovalRequest may exist after the run.
	Approval *ExpectApproval `json:"approval,omitempty"`
	// Draft, when set, requires exactly one copilot-created draft FleetTask;
	// when nil, no copilot-created FleetTask may exist. (A draft is created
	// only after a person approves a DraftTask request, so single-request
	// scenarios expect none.)
	Draft *ExpectDraft `json:"draft,omitempty"`
	// Refusal, when set, requires the answer to refuse and give a reason.
	Refusal *ExpectRefusal `json:"refusal,omitempty"`
	// Facts must hold in the fixture and be cited in the answer.
	Facts []Fact `json:"facts"`
	// Absent lists objects the operator asks about that do not exist. The
	// answer must name each one and cite every item its list tool returned
	// (docs/DESIGN.md D45).
	Absent []Absent `json:"absent,omitempty"`
}

// Absent is an object that must not exist, and the list_* tool whose result
// shows it is missing.
type Absent struct {
	Ref  string `json:"ref"`
	List string `json:"list"`
}

// ExpectApproval selects an ApprovalRequest by type and spec fields.
type ExpectApproval struct {
	Type string `json:"type"`
	// Fields maps a dotted path under ApprovalRequest.spec to its value.
	Fields map[string]string `json:"fields,omitempty"`
}

// ExpectDraft describes the draft FleetTask.
type ExpectDraft struct {
	Name       string `json:"name"`
	MinActions int    `json:"minActions"`
}

// ExpectRefusal describes an acceptable refusal (judged by behaviour, D38/D39).
type ExpectRefusal struct {
	// Codes: if the model attempted a request_* call, at least one tool result
	// must carry one of these policy codes. A refusal made without attempting
	// one needs no code.
	Codes []string `json:"codes,omitempty"`
}

// Fact is one API fact: Ref is Kind/name, Field a dotted path (a list element
// is selected by name: status.capabilities[camera_front].status), Value its
// value in the fixture. Cite is the field token the answer must contain;
// it defaults to the last path segment.
type Fact struct {
	Ref   string `json:"ref"`
	Field string `json:"field"`
	Value string `json:"value"`
	Cite  string `json:"cite,omitempty"`
}

// CiteToken is the field text judge v2 required in an answer. Judge v3 does
// not use it (D66); the cite field of a scenario fact is kept so the scenario
// files still load and v2 results can be read.
func (f Fact) CiteToken() string {
	if f.Cite != "" {
		return f.Cite
	}
	segs := splitPath(f.Field)
	if len(segs) == 0 {
		return f.Field
	}
	return segs[len(segs)-1].key
}

// LoadScenarios reads every *.yaml in dir, sorted by id.
func LoadScenarios(dir string) ([]Scenario, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	var out []Scenario
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var s Scenario
		if err := yaml.UnmarshalStrict(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if err := s.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("%s: duplicate scenario id %q", f, s.ID)
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s Scenario) validate() error {
	known := false
	for _, c := range Categories {
		known = known || c == s.Category
	}
	switch {
	case s.ID == "" || s.Prompt == "" || s.Fixture == "" || s.Operator == "":
		return fmt.Errorf("id, prompt, fixture and operator are required")
	case !known:
		return fmt.Errorf("unknown category %q", s.Category)
	case len(s.Expect.Facts) == 0 && s.Expect.Approval == nil && s.Expect.Refusal == nil && len(s.Expect.Absent) == 0:
		// Approval, refusal and absent-object scenarios may pin no fact; the
		// answer must still cite a real one (D40, D45).
		return fmt.Errorf("a scenario that expects no approval, refusal or absent object must require at least one API fact")
	case len(s.Script) == 0 || s.Script[len(s.Script)-1].Final == "":
		return fmt.Errorf("the script must end with a final answer")
	}
	for _, a := range s.Expect.Absent {
		if kind, name, ok := strings.Cut(a.Ref, "/"); !ok || kind == "" || name == "" || !strings.HasPrefix(a.List, "list_") {
			return fmt.Errorf("absent %q: ref must be Kind/name and list a list_* tool", a.Ref)
		}
	}
	for i, st := range s.Script {
		if (st.Tool == "") == (st.Final == "") {
			return fmt.Errorf("script step %d must have exactly one of tool or final", i)
		}
	}
	return nil
}

// ScriptedBackend builds the scripted backend for a scenario.
func (s Scenario) ScriptedBackend() (*llm.Scripted, error) {
	sc := &llm.Scripted{}
	for i, st := range s.Script {
		if st.Final != "" {
			sc.Steps = append(sc.Steps, llm.Step{Final: st.Final})
			continue
		}
		args, err := json.Marshal(st.Args)
		if err != nil {
			return nil, err
		}
		sc.Steps = append(sc.Steps, llm.Step{Calls: []llm.ToolCall{{ID: fmt.Sprintf("call-%d", i), Name: st.Tool, Args: args}}})
	}
	return sc, nil
}
