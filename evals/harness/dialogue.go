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
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/dialogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/draft"
	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
)

// MaxQuestions is the cap of questions before the preview (D71).
const MaxQuestions = 2

// questionsBeforePreview counts the questions the copilot asked before its
// first preview, and lists their ids in order.
func questionsBeforePreview(tr agent.Trace) (int, []string, bool) {
	var ids []string
	for _, t := range tr.Conversation {
		d := t.Dialogue
		if d == nil {
			continue
		}
		if d.Outcome == agent.OutcomePreview {
			return len(ids), ids, true
		}
		if d.Outcome == agent.OutcomeQuestion && d.QuestionID != "preview.change" {
			ids = append(ids, d.QuestionID)
		}
	}
	return len(ids), ids, false
}

func dialogueStats(tr agent.Trace) DialogueStats {
	n, _, preview := questionsBeforePreview(tr)
	st := DialogueStats{Messages: len(tr.Conversation), Questions: n, ReachedPreview: preview, CapViolation: n > MaxQuestions}
	for _, t := range tr.Conversation {
		st.Stalled = st.Stalled || t.Stalled
	}
	return st
}

// stallCodes are blockers under which drafted work would sit Pending after
// activation.
var stallCodes = []string{
	feasibility.CodeNoEligibleRobot, feasibility.CodeNoRobotWithCapability, feasibility.CodeNoCarryingRobot,
	feasibility.CodeChargeBelowFloor, feasibility.CodeRobotNotFound, feasibility.CodeZoneStopped, feasibility.CodeNamespaceStopped,
}

// stallProxy re-runs check_task's evaluation, on the cluster at the end of
// the run, for every DraftTask request the run filed.
func stallProxy(ctx context.Context, admin client.Client, now time.Time) (int, error) {
	var reqs fleetopsv1.ApprovalRequestList
	if err := admin.List(ctx, &reqs, client.InNamespace(testenv.SwarmNS)); err != nil {
		return 0, err
	}
	n := 0
	var snap *feasibility.Snapshot
	for _, r := range reqs.Items {
		if r.Spec.Type != fleetopsv1.RequestDraftTask || r.Spec.DraftTask == nil {
			continue
		}
		switch r.Status.State {
		case fleetopsv1.StateFailed, fleetopsv1.StateRejected, fleetopsv1.StateExpired:
			continue // never created: nothing to sit Pending
		}
		t, err := draft.Task(r.Namespace, r.Spec.DraftTask)
		if err != nil {
			return 0, err
		}
		if snap == nil {
			s := feasibility.Load(ctx, admin, testenv.SwarmNS, now)
			snap = &s
		}
		rep := feasibility.Evaluate(feasibility.Proposal{Task: t}, *snap)
		for _, b := range rep.Blockers {
			if slices.Contains(stallCodes, b.Code) {
				n++
				break
			}
		}
	}
	return n, nil
}

// judgeDialogue is criterion 5 (docs/DESIGN.md §4.4). Every scenario: at most
// two questions before the preview; every request filed matches a preview
// the person confirmed, and nothing is filed without one. With
// expect.dialogue: the expected questions, preview, interpretation,
// blockers and alternative; with execute: the request's final state.
func judgeDialogue(s Scenario, tr agent.Trace, ob Observed) Criterion {
	c := Criterion{Pass: true}
	fail := func(f string, a ...any) { c.Pass = false; c.Detail = append(c.Detail, fmt.Sprintf(f, a...)) }
	n, ids, _ := questionsBeforePreview(tr)
	if n > MaxQuestions {
		fail("%d questions before the preview; the cap is %d", n, MaxQuestions)
	}
	previews := map[string]bool{}
	filed := map[string]bool{}
	for _, t := range tr.Conversation {
		d := t.Dialogue
		if d == nil {
			continue
		}
		switch d.Outcome {
		case agent.OutcomePreview:
			previews[d.Kind] = true
		case agent.OutcomeFiled:
			reply, _ := dialogue.ParseReply(t.Person)
			if reply != dialogue.ReplyConfirm && reply != dialogue.ReplyConfirmFollowUp {
				fail("a request was filed on %q, which is not a confirmation", t.Person)
			}
			if !previews[d.Kind] {
				fail("a request was filed without a preview of %s shown first", d.Kind)
			}
			filed[d.PreviewID] = true
		}
	}
	for _, r := range ob.Requests {
		if !filed[r["preview.id"]] {
			fail("ApprovalRequest with preview %q was not filed after the person confirmed that preview", r["preview.id"])
		}
	}
	if s.Execute != nil {
		judgeExecuted(s.Execute, ob, fail)
	}
	want := s.Expect.Dialogue
	if want == nil {
		return c
	}
	if want.MaxQuestions != nil && n > *want.MaxQuestions {
		fail("%d questions before the preview, want at most %d", n, *want.MaxQuestions)
	}
	if want.Questions != nil && !slices.Equal(ids, want.Questions) {
		fail("questions %v, want %v in this order", ids, want.Questions)
	}
	if want.Preview && len(previews) == 0 {
		fail("no preview was shown")
	}
	if want.Kind != "" && !previews[want.Kind] {
		fail("no preview of kind %s", want.Kind)
	}
	var interps, blockers []string
	notPossible, alternative := false, false
	instr := ""
	for _, t := range tr.Conversation {
		if strings.Contains(t.Reply, "Alternative:") {
			alternative = true
		}
		d := t.Dialogue
		if d == nil {
			continue
		}
		if d.Outcome == agent.OutcomeNotPossible {
			notPossible = true
			interps = append(interps, dialogue.NotPossible)
		}
		if d.Outcome == agent.OutcomePreview && d.Interpretation != "" {
			interps = append(interps, d.Interpretation)
			blockers = append(blockers, d.Blockers...)
		}
		if d.Instruction != "" {
			instr = d.Instruction
		}
	}
	if want.Interpretation != "" && !slices.Contains(interps, want.Interpretation) {
		fail("interpretation %v, want %s", interps, want.Interpretation)
	}
	if want.NotPossible {
		if !notPossible {
			fail("the copilot did not say the request is not possible")
		}
		if len(ob.Requests) > 0 {
			fail("not possible, but %d request(s) filed", len(ob.Requests))
		}
	}
	for _, b := range want.Blockers {
		if !slices.Contains(blockers, b) {
			fail("blocker %s not in the preview (had %v)", b, blockers)
		}
	}
	if want.Alternative && !alternative {
		fail("no alternative stated")
	}
	if want.Instruction != "" && instr != want.Instruction {
		fail("instruction kept %q, want the person's words %q", instr, want.Instruction)
	}
	for _, w := range want.PreviewSays {
		found := false
		for _, t := range tr.Conversation {
			if t.Dialogue != nil && t.Dialogue.Outcome == agent.OutcomePreview && strings.Contains(t.Reply, w) {
				found = true
			}
		}
		if !found {
			fail("no preview says %q", w)
		}
	}
	return c
}

func judgeExecuted(e *Execute, ob Observed, fail func(string, ...any)) {
	if len(ob.RequestStates) != 1 {
		fail("execute: %d requests, want 1", len(ob.RequestStates))
		return
	}
	for name, st := range ob.RequestStates {
		if st != e.State {
			fail("execute: %s ended %q, want %q (%s)", name, st, e.State, ob.RequestMessages[name])
		}
		if e.Code != "" && !strings.Contains(ob.RequestMessages[name], e.Code) {
			fail("execute: %s message %q lacks %s", name, ob.RequestMessages[name], e.Code)
		}
	}
}

// DialogueSummary aggregates DialogueStats (results.json, §4.4).
type DialogueSummary struct {
	// QuestionsPerTask is over the runs that reached a preview.
	QuestionsPerTask             float64     `json:"questionsPerTask"`
	QuestionsDistribution        map[int]int `json:"questionsDistribution"`
	RunsReachingPreview          int         `json:"runsReachingPreview"`
	CapViolations                int         `json:"capViolations"`
	Stalls                       int         `json:"stalledConversations"`
	StallsAfterActivationProxy   int         `json:"stallsAfterActivationProxy"`
	StallsAfterActivationMeaning string      `json:"stallsAfterActivationProxyMeaning"`
}

func summarizeDialogue(srs []ScenarioResult) *DialogueSummary {
	d := &DialogueSummary{QuestionsDistribution: map[int]int{},
		StallsAfterActivationMeaning: "drafts filed in a run that check_task, re-run on the cluster at the end of the run, finds no eligible robot for (or a stop in force): work that would sit Pending after activation; a proxy, not an observation of Swarmada"}
	total := 0
	for _, sr := range srs {
		for _, r := range sr.Runs {
			if r.Dialogue.ReachedPreview {
				d.RunsReachingPreview++
				total += r.Dialogue.Questions
				d.QuestionsDistribution[r.Dialogue.Questions]++
			}
			if r.Dialogue.CapViolation {
				d.CapViolations++
			}
			if r.Dialogue.Stalled {
				d.Stalls++
			}
			d.StallsAfterActivationProxy += r.Dialogue.StallProxy
		}
	}
	if d.RunsReachingPreview > 0 {
		d.QuestionsPerTask = math.Round(float64(total)/float64(d.RunsReachingPreview)*1000) / 1000
	}
	return d
}
