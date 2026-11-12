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

package dialogue

import (
	"fmt"
	"strings"

	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// ResultNotReported is shown for a class (b) kind (D70).
const ResultNotReported = "Swarmada records arrival and completion, not what the robot found; a person reviews the result on the robot's side."

// ComposedNote is shown for a composed request (D72).
const ComposedNote = "Composed from basic steps for human review: check that these steps do what was asked."

// Confirmation choices of every preview (DESIGN.md §3.10.5).
const (
	ChoicesOpen    = "1 Confirm · 2 Confirm, and prepare a follow-up request if it fails · 3 Change something — describe it · 4 Cancel"
	ChoicesBlocked = "3 Change something — describe it · 4 Cancel"
)

// Preview is a preview shown to the person, stored in the conversation.
type Preview struct {
	Tool string `json:"tool"`
	// Input is the request the person confirms, as the tool receives it.
	Input any `json:"input"`
	// Blocked previews (a blocker, or a missing required item) cannot be
	// confirmed.
	Blocked bool   `json:"blocked"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
}

// RenderPreview is the preview text for p with check_task's result.
func RenderPreview(p *Proposal, chk tools.CheckTaskResult) Preview {
	var b strings.Builder
	fmt.Fprintf(&b, "Preview — %s (catalogue kind %s)\n", p.Title, p.Kind)
	for _, l := range p.Lines {
		b.WriteString("  " + l + "\n")
	}
	if pv := previewOf(p.Input); pv != nil && pv.Instruction != "" {
		fmt.Fprintf(&b, "  Your words, kept as the instruction: %q\n", pv.Instruction)
	}
	if p.Interpretation == Composed {
		b.WriteString("  " + ComposedNote + "\n")
	}
	if !p.ResultReported {
		b.WriteString("  " + ResultNotReported + "\n")
	}
	for _, n := range p.Notes {
		b.WriteString("  " + n + "\n")
	}
	blocked := len(p.Missing) > 0 || chk.Verdict == feasibility.Blocked
	verdict := chk.Verdict
	if verdict == "" {
		verdict = "not checked"
	}
	fmt.Fprintf(&b, "Checks: %s.", verdict)
	if robots := eligibleText(chk.Eligible); robots != "" {
		b.WriteString(" Eligible now: " + robots + ".")
	}
	b.WriteString("\n")
	for _, f := range chk.Blockers {
		fmt.Fprintf(&b, "  Blocker %s%s: %s\n", f.Code, member(f.Member), f.Reason)
	}
	for _, f := range chk.Warnings {
		fmt.Fprintf(&b, "  Warning %s%s: %s\n", f.Code, member(f.Member), f.Reason)
	}
	for _, a := range chk.Alternatives {
		fmt.Fprintf(&b, "  Alternative: %s\n", a)
	}
	if c := renderChecked(CheckedHeader, CheckedPreview(chk.Facts, namedRobots(p.Input))); c != "" {
		b.WriteString(c + "\n")
	}
	if len(p.Missing) > 0 {
		fmt.Fprintf(&b, "  Missing: %s. Reply 3 with it, or 4 to cancel.\n", strings.Join(p.Missing, ", "))
	}
	b.WriteString(feasibility.Statement + "\n")
	if blocked {
		b.WriteString("Nothing can be filed as it stands. " + ChoicesBlocked)
	} else {
		b.WriteString(ChoicesOpen)
	}
	return Preview{Tool: p.Tool, Input: p.Input, Blocked: blocked, Kind: p.Kind, Text: b.String()}
}

func member(m string) string {
	if m == "" {
		return ""
	}
	return " (" + m + ")"
}

func eligibleText(m map[string][]string) string {
	var parts []string
	for k, v := range m {
		if len(v) > 0 {
			parts = append(parts, fmt.Sprintf("%s: %s", k, strings.Join(v, ", ")))
		}
	}
	return strings.Join(parts, "; ")
}

func previewOf(in any) *tools.PreviewInput {
	switch v := in.(type) {
	case tools.DraftTaskInput:
		return v.Preview
	case tools.ActivateTaskInput:
		return v.Preview
	case tools.ZoneMaintenanceInput:
		return v.Preview
	case tools.ZoneStopInput:
		return v.Preview
	}
	return nil
}

// WithFailureHandling returns the request input with the failure handling
// the person chose when confirming (choice 2: PrepareFollowUp).
func WithFailureHandling(in any, fh string) any {
	set := func(p *tools.PreviewInput) *tools.PreviewInput {
		if p == nil {
			return nil
		}
		c := *p
		c.FailureHandling = fh
		return &c
	}
	switch v := in.(type) {
	case tools.DraftTaskInput:
		v.Preview = set(v.Preview)
		return v
	case tools.ActivateTaskInput:
		v.Preview = set(v.Preview)
		return v
	case tools.ZoneMaintenanceInput:
		v.Preview = set(v.Preview)
		return v
	case tools.ZoneStopInput:
		v.Preview = set(v.Preview)
		return v
	}
	return in
}

// Reply is how the person answered a preview.
type Reply int

// Replies to a preview.
const (
	ReplyOther Reply = iota
	ReplyConfirm
	ReplyConfirmFollowUp
	ReplyChange
	ReplyCancel
)

// ParseReply reads the person's answer to a preview. It is matched here,
// by fleetops-agent, never by the model (D74). Text after "3" is the change.
func ParseReply(msg string) (Reply, string) {
	m := strings.ToLower(strings.TrimSpace(msg))
	switch m {
	case "1", "1.", "confirm", "yes", "confirm it":
		return ReplyConfirm, ""
	case "2", "2.", "confirm with follow-up", "confirm with a follow-up", "confirm, and prepare a follow-up request if it fails":
		return ReplyConfirmFollowUp, ""
	case "4", "4.", "cancel", "no":
		return ReplyCancel, ""
	}
	if n, rest, ok := ParseChoice(msg); ok && n == 3 {
		return ReplyChange, rest
	}
	if strings.HasPrefix(m, "change") {
		return ReplyChange, strings.TrimSpace(msg[len("change"):])
	}
	return ReplyOther, msg
}
