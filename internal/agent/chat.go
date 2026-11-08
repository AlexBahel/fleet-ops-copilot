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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/conversation"
	"github.com/AlexBahel/fleet-ops-copilot/internal/dialogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tracing"
)

// Dialogue outcomes of one message (Trace.Dialogue.Outcome).
const (
	OutcomeQuestion    = "question"
	OutcomePreview     = "preview"
	OutcomeFiled       = "filed"
	OutcomeRefused     = "refused"
	OutcomeNotPossible = "notPossible"
	OutcomeCancelled   = "cancelled"
	OutcomeBlocked     = "blockedConfirm"
)

// CodeFilingError is the dialogue code of a filing that failed as a call
// rather than with a policy decision (D86).
const CodeFilingError = "FILING_ERROR"

// DialogueEvent is what one message did in a guided request.
type DialogueEvent struct {
	Outcome        string   `json:"outcome"`
	QuestionID     string   `json:"questionID,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	Interpretation string   `json:"interpretation,omitempty"`
	PreviewID      string   `json:"previewID,omitempty"`
	Blocked        bool     `json:"blocked,omitempty"`
	Blockers       []string `json:"blockers,omitempty"`
	Missing        []string `json:"missing,omitempty"`
	Filed          string   `json:"filed,omitempty"`
	Code           string   `json:"code,omitempty"`
	// Asked is the number of questions asked before the preview so far.
	Asked int `json:"asked"`
	// Instruction is the person's verbatim "something else" text, when kept.
	Instruction string `json:"instruction,omitempty"`
}

// ProposeTool is the local tool the model reads a request through.
const ProposeTool = "propose_task"

// Chat runs one message of a conversation (DESIGN.md §3.10, D71–D75).
type Chat struct {
	// Catalogue enables guided requests; nil leaves only plain answers.
	Catalogue *catalogue.Catalogue
	MaxTurns  int
	Tracer    trace.Tracer
	// Audit receives catalogue_review entries; nil records none.
	Audit *audit.Writer
	// Redact masks secrets in the conversation text kept between messages.
	Redact func(string) string
}

// ProposeSpec is the propose_task tool as the model sees it.
func ProposeSpec(cat *catalogue.Catalogue) llm.ToolSpec {
	schema, err := jsonschema.For[dialogue.Input](nil)
	if err != nil {
		panic(err)
	}
	enumerate(schema, cat)
	b, _ := json.Marshal(schema)
	var asks, kinds, steps, gaps []string
	for _, a := range cat.Asks {
		asks = append(asks, a.Name)
	}
	for _, k := range cat.Kinds {
		kinds = append(kinds, fmt.Sprintf("%s (%s)", k.Name, k.Title))
	}
	steps = cat.BasicSteps
	var findings []string
	for _, g := range cat.NotYetPossible {
		if g.Finding {
			findings = append(findings, g.Need)
			continue
		}
		gaps = append(gaps, fmt.Sprintf("%s: %s", g.ID, g.Need))
	}
	desc := "Start or continue a request for work. Pass the ask and every field the person's words state; " +
		"the copilot then asks its own questions, shows a preview with checks, and files only after the person confirms. " +
		"Asks: " + strings.Join(asks, ", ") + ", or " + dialogue.AskSomethingElse + " when none fits. " +
		"Kinds: " + strings.Join(kinds, "; ") + ". " +
		"For a 'something else' description set somethingElse: known (with kind), composed (with steps from: " + strings.Join(steps, ", ") +
		"), or notPossible (with need, one of: " + strings.Join(gaps, "; ") + ", and the reason). " +
		"notPossible is only for work no robot can do at all. A need for a finding (" + strings.Join(findings, "; ") + "; damage, what is at a place, whether something is in place) " +
		"is an inspection: use known with the inspect kind for what it looks for (lookFor: " + strings.Join(cat.LookForWords(), ", ") + ", or both); " +
		"the preview says what is not reported. " +
		"Fields are strings; location, from, to and dock are waypoint names, route is waypoint names separated by commas."
	return llm.ToolSpec{Name: ProposeTool, Description: desc, InputSchema: b}
}

// enumerate narrows propose_task's schema to the site catalogue (D85): the
// asks, kinds, basic steps, gaps and lookFor words as enums, and fields as
// an object with exactly catalogue.Fields, each described, closed values
// as enums, the kinds that use it named.
func enumerate(s *jsonschema.Schema, cat *catalogue.Catalogue) {
	var asks, kinds, gaps []string
	for _, a := range cat.Asks {
		asks = append(asks, a.Name)
	}
	for _, k := range cat.Kinds {
		kinds = append(kinds, k.Name)
	}
	for _, g := range cat.NotYetPossible {
		gaps = append(gaps, g.ID)
	}
	enum := func(xs ...string) []any {
		out := make([]any, len(xs))
		for i, x := range xs {
			out[i] = x
		}
		return out
	}
	fields := func(desc string) *jsonschema.Schema {
		f := &jsonschema.Schema{Type: "object", Description: desc, Properties: map[string]*jsonschema.Schema{}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
		for _, name := range catalogue.Fields {
			form := dialogue.Forms[name]
			var used []string
			for _, k := range cat.Kinds {
				if slices.Contains(k.Fields(), name) {
					used = append(used, k.Name)
				}
			}
			d := form.Desc
			if len(used) > 0 {
				d += " (kinds: " + strings.Join(used, ", ") + ")"
			}
			p := &jsonschema.Schema{Type: "string", Description: d}
			if len(form.Enum) > 0 {
				p.Enum = enum(form.Enum...)
			}
			f.Properties[name] = p
		}
		return f
	}
	s.Properties["ask"].Enum = enum(append(asks, dialogue.AskSomethingElse)...)
	s.Properties["kind"].Enum = enum(kinds...)
	s.Properties["fields"] = fields(s.Properties["fields"].Description)
	se := s.Properties["somethingElse"]
	se.Properties["interpretation"].Enum = enum(dialogue.Known, dialogue.Composed, dialogue.NotPossible)
	se.Properties["kind"].Enum = enum(kinds...)
	se.Properties["need"].Enum = enum(gaps...)
	se.Properties["lookFor"].Items.Enum = enum(append(cat.LookForWords(), "both")...)
	step := se.Properties["steps"].Items
	step.Properties["kind"].Enum = enum(cat.BasicSteps...)
	step.Properties["fields"] = fields(step.Properties["fields"].Description)
}

// ProposeTool as an mcp.Tool, for the tools digest (mcpserver.ToolsDigest).
func ProposeMCPTool(cat *catalogue.Catalogue) *mcp.Tool {
	sp := ProposeSpec(cat)
	var schema any
	_ = json.Unmarshal(sp.InputSchema, &schema)
	return &mcp.Tool{Name: sp.Name, Description: sp.Description, InputSchema: schema}
}

// Turn answers one person message in conv. Numbered answers to a question,
// and replies to a preview, are handled here without the model; the request
// is filed by this code, with the confirmed preview's parameters, never by
// the model (D74).
func (c *Chat) Turn(ctx context.Context, cs *mcp.ClientSession, conv *conversation.Conversation, operator, sessionID, message string) (Trace, error) {
	start := time.Now()
	tr, err := c.turn(ctx, cs, conv, operator, sessionID, message)
	tr.Prompt = message
	if tr.Started.IsZero() {
		tr.Started = start
	}
	tr.Ended = time.Now()
	tr.LatencyMs = float64(tr.Ended.Sub(tr.Started).Microseconds()) / 1000
	conv.Append("user", message, c.Redact)
	if tr.Final != "" {
		conv.Append("assistant", tr.Final, c.Redact)
	}
	return tr, err
}

// FollowUp starts a conversation at the preview of a follow-up fleetops-approvals
// prepared after a failure (D78). Only the original requester may use it,
// and it is filed only after a new confirmation, like any request.
func (c *Chat) FollowUp(ctx context.Context, cs *mcp.ClientSession, conv *conversation.Conversation, requester, sessionID string, req *fleetopsv1.ApprovalRequest) (Trace, error) {
	tr := Trace{Prompt: "follow-up of " + req.Name, Started: time.Now()}
	fu := req.Status.FollowUp
	switch {
	case c.Catalogue == nil:
		return tr, fmt.Errorf("follow-ups need the site catalogue")
	case fu == nil || fu.DraftTask == nil:
		tr.Final = fmt.Sprintf("%s has no prepared follow-up.", req.Name)
		return tr, nil
	case req.Spec.RequestedFor != requester:
		tr.Final = fmt.Sprintf("%s was requested by someone else; only they can start its follow-up.", req.Name)
		return tr, nil
	}
	in := tools.DraftInputFrom(fu.DraftTask)
	in.Name = followUpName(fu.DraftTask.TaskName)
	p := &dialogue.Proposal{Kind: fu.Kind, Title: "Follow-up of " + req.Name + ": " + fu.Summary, Tool: "request_draft_task", Interpretation: dialogue.Known, ResultReported: true}
	if k, ok := c.Catalogue.KindByName(fu.Kind); ok {
		p.ResultReported = k.ResultReported
	}
	in.Preview = &tools.PreviewInput{Kind: fu.Kind, Interpretation: "Known", ResultReported: p.ResultReported,
		FailureHandling: "ReportOnly", CatalogueVersion: c.Catalogue.Version, CatalogueSHA256: c.Catalogue.SHA256}
	if req.Spec.Preview != nil {
		in.Preview.Instruction = req.Spec.Preview.Instruction
	}
	p.Input = in
	p.Lines = []string{"Same kind and fields as " + req.Name + ", as a new draft", "Draft name: " + in.Name}
	conv.Reset()
	conv.State.Kind, conv.State.Message = fu.Kind, "follow-up of "+req.Name
	text, ev, err := c.render(ctx, cs, conv, requester, sessionID, dialogue.Outcome{Proposal: p}, &tr)
	tr.Final, tr.Dialogue = text, ev
	tr.Ended = time.Now()
	conv.Append("assistant", text, c.Redact)
	return tr, err
}

func followUpName(orig string) string {
	if len(orig) > 56 {
		orig = strings.TrimRight(orig[:56], "-")
	}
	return orig + "-retry"
}

func (c *Chat) engine(operator string) *dialogue.Engine {
	e := &dialogue.Engine{Cat: c.Catalogue}
	if c.Audit != nil {
		e.Review = func(r dialogue.ReviewEntry) {
			_, _ = c.Audit.Append(audit.Entry{Kind: audit.KindCatalogueReview, Actor: operator, Message: r.Text,
				Detail: map[string]any{"interpretation": r.Interpretation, "kind": r.Kind, "steps": r.Steps, "reason": r.Reason, "finding": r.Finding}})
		}
	}
	return e
}

func (c *Chat) turn(ctx context.Context, cs *mcp.ClientSession, conv *conversation.Conversation, operator, sessionID, message string) (Trace, error) {
	if c.Catalogue != nil && conv.Preview != nil {
		reply, rest := dialogue.ParseReply(message)
		switch reply {
		case dialogue.ReplyConfirm, dialogue.ReplyConfirmFollowUp:
			return c.file(ctx, cs, conv, operator, sessionID, reply == dialogue.ReplyConfirmFollowUp)
		case dialogue.ReplyCancel:
			ev := &DialogueEvent{Outcome: OutcomeCancelled, Kind: conv.Preview.Kind, Asked: len(conv.State.Asked)}
			conv.Reset()
			return Trace{Final: "Cancelled; nothing was filed.", Dialogue: ev}, nil
		case dialogue.ReplyChange:
			if strings.TrimSpace(rest) == "" {
				return Trace{Final: "What should change? Describe it in one message.", Dialogue: &DialogueEvent{Outcome: OutcomeQuestion, QuestionID: "preview.change", Kind: conv.Preview.Kind, Asked: len(conv.State.Asked)}}, nil
			}
			conv.Preview = nil
			message = rest
		default:
			// A change in other words: the model reads it against the same
			// request; the questions already asked still count, and the
			// preview is redone.
			conv.Preview = nil
		}
	}
	if c.Catalogue != nil && conv.State.Pending != nil {
		if conv.State.Pending.Open {
			conv.State.Describe(message)
		} else if n, rest, ok := dialogue.ParseChoice(message); ok {
			snap, calls, err := c.snapshot(ctx, cs, operator, sessionID)
			if err != nil {
				return Trace{Calls: calls}, err
			}
			out, needModel, ok := c.engine(operator).Answer(&conv.State, n, rest, snap)
			if ok && !needModel {
				tr := Trace{Calls: calls}
				text, ev, err := c.render(ctx, cs, conv, operator, sessionID, out, &tr)
				tr.Final, tr.Dialogue = text, ev
				return tr, err
			}
			if ok && needModel {
				message = rest
			}
		}
	}
	loop := &Loop{Backend: conv.Backend, Session: cs, MaxTurns: c.MaxTurns, Tracer: c.Tracer, Offer: mcpserver.ForModel}
	var ev *DialogueEvent
	var extra []ToolEvent
	if c.Catalogue != nil {
		loop.Local = []LocalTool{{Spec: ProposeSpec(c.Catalogue), Call: func(ctx context.Context, args json.RawMessage) (string, string, error) {
			var in dialogue.Input
			if err := json.Unmarshal(args, &in); err != nil {
				return "", "", fmt.Errorf("propose_task arguments: %w", err)
			}
			if !conv.State.Active() {
				conv.State = dialogue.State{Message: message, Fields: map[string]string{}}
			}
			snap, calls, err := c.snapshot(ctx, cs, operator, sessionID)
			extra = append(extra, calls...)
			if err != nil {
				return "", "", err
			}
			out := c.engine(operator).Propose(&conv.State, in, snap)
			if out.Invalid != "" {
				// Back to the model, which reads the person's words again (D85).
				return "", "", errors.New(out.Invalid)
			}
			tr := Trace{}
			text, e, err := c.render(ctx, cs, conv, operator, sessionID, out, &tr)
			extra = append(extra, tr.Calls...)
			ev = e
			if err != nil {
				return "", "", err
			}
			return "", text, nil
		}}}
	}
	tr, err := loop.RunWith(ctx, operator, sessionID, conv.LLMHistory(), message)
	tr.Calls = append(tr.Calls, extra...)
	tr.Dialogue = ev
	return tr, err
}

// render turns an engine outcome into the text the person sees. A proposal
// is checked with check_task first (an agent call) and stored as the
// conversation's preview.
func (c *Chat) render(ctx context.Context, cs *mcp.ClientSession, conv *conversation.Conversation, operator, sessionID string, out dialogue.Outcome, tr *Trace) (string, *DialogueEvent, error) {
	asked := len(conv.State.Asked)
	switch {
	case out.Question != nil:
		return out.Question.Render(), &DialogueEvent{Outcome: OutcomeQuestion, QuestionID: out.Question.ID, Kind: conv.State.Kind, Asked: asked}, nil
	case out.NotPossible != "":
		ev := &DialogueEvent{Outcome: OutcomeNotPossible, Asked: asked}
		if se := conv.State.SomethingElse; se != nil {
			ev.Interpretation, ev.Instruction = se.Interpretation, se.Text
		}
		conv.Reset()
		return out.NotPossible, ev, nil
	case out.Proposal != nil:
		p := out.Proposal
		args := map[string]any{}
		switch p.Tool {
		case "request_draft_task":
			args["draftTask"] = p.Input
		case "request_activate_task":
			args["activateTask"] = p.Input
		case "request_zone_maintenance":
			args["zoneMaintenance"] = p.Input
		case "request_zone_stop":
			args["zoneStop"] = p.Input
		}
		var chk tools.CheckTaskResult
		evc, raw, err := c.agentCall(ctx, cs, operator, sessionID, "check_task", args, nil)
		tr.Calls = append(tr.Calls, evc)
		if err != nil {
			return "", nil, err
		}
		if err := json.Unmarshal(raw, &chk); err != nil {
			return "", nil, fmt.Errorf("check_task result: %w", err)
		}
		pv := dialogue.RenderPreview(p, chk)
		conv.Preview = &pv
		id, _ := tools.PreviewIDFor(p.Input)
		ev := &DialogueEvent{Outcome: OutcomePreview, Kind: p.Kind, Interpretation: p.Interpretation, PreviewID: id, Blocked: pv.Blocked, Missing: p.Missing, Asked: asked}
		for _, b := range chk.Blockers {
			ev.Blockers = append(ev.Blockers, b.Code)
		}
		if se := conv.State.SomethingElse; se != nil {
			ev.Instruction = se.Text
		}
		return pv.Text, ev, nil
	}
	return "", nil, fmt.Errorf("dialogue produced no outcome")
}

// file sends the confirmed preview's request, with exactly its parameters
// and the confirmation in _meta.
func (c *Chat) file(ctx context.Context, cs *mcp.ClientSession, conv *conversation.Conversation, operator, sessionID string, followUp bool) (Trace, error) {
	pv := conv.Preview
	ev := &DialogueEvent{Kind: pv.Kind, Asked: len(conv.State.Asked)}
	if pv.Blocked {
		ev.Outcome = OutcomeBlocked
		return Trace{Final: "This preview cannot be filed as it stands. " + dialogue.ChoicesBlocked, Dialogue: ev}, nil
	}
	in := pv.Input
	if followUp {
		in = dialogue.WithFailureHandling(in, "PrepareFollowUp")
	}
	id, err := tools.PreviewIDFor(in)
	if err != nil {
		return Trace{}, err
	}
	ev.PreviewID = id
	tr := Trace{}
	evc, raw, err := c.agentCall(ctx, cs, operator, sessionID, pv.Tool, in, mcp.Meta{mcpserver.MetaConfirmedPreview: id})
	tr.Calls = append(tr.Calls, evc)
	var res tools.RequestResult
	if err == nil {
		_ = json.Unmarshal(raw, &res)
	}
	ns := c.Catalogue.Namespace
	switch {
	case err != nil:
		// The filing failed as a call (the API server refused the
		// ApprovalRequest, fleetops-mcp was unreachable): the person is told
		// what was refused and why, never left with an empty reply (D86).
		ev.Outcome, ev.Code = OutcomeRefused, CodeFilingError
		title := pv.Kind
		if k, ok := c.Catalogue.KindByName(pv.Kind); ok {
			title = k.Title
		}
		tr.Final = fmt.Sprintf("Not filed: the request (%s) was refused when it was filed: %s. Nothing was filed; start the request again to change it.", title, err.Error())
	case res.Refused || res.Created == "":
		ev.Outcome, ev.Code = OutcomeRefused, res.Code
		tr.Final = fmt.Sprintf("Not filed: %s — %s", res.Code, res.Reason)
	default:
		name := strings.TrimPrefix(res.Created, "ApprovalRequest/")
		ev.Outcome, ev.Filed, ev.Code = OutcomeFiled, name, res.Code
		tr.Final = fmt.Sprintf("Filed %s (%s). It waits for a named person: fleetops approve %s -n %s, or fleetops reject %s -n %s. Next step: %s",
			res.Created, pv.Kind, name, ns, name, ns, res.NextStep)
		if followUp {
			tr.Final += " If the task fails, a follow-up request is prepared for you to review; it is never filed without your confirmation."
		}
	}
	tr.Dialogue = ev
	conv.Reset()
	return tr, nil
}

// agentCall is a call fleetops-agent makes itself (By "agent").
func (c *Chat) agentCall(ctx context.Context, cs *mcp.ClientSession, operator, sessionID, name string, args any, extra mcp.Meta) (ToolEvent, json.RawMessage, error) {
	b, _ := json.Marshal(args)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	meta := mcp.Meta{mcpserver.MetaSession: sessionID, mcpserver.MetaOperator: operator}
	for k, v := range extra {
		meta[k] = v
	}
	tracing.InjectMeta(ctx, meta)
	ev := ToolEvent{Name: name, By: "agent", Args: b}
	start := time.Now()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Meta: meta, Name: name, Arguments: m})
	ev.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		ev.IsError, ev.ErrorText = true, err.Error()
		return ev, nil, err
	}
	raw, _ := json.Marshal(res.StructuredContent)
	ev.Result = raw
	if res.IsError {
		var text []string
		for _, ct := range res.Content {
			if t, ok := ct.(*mcp.TextContent); ok {
				text = append(text, t.Text)
			}
		}
		ev.IsError, ev.ErrorText = true, strings.Join(text, "\n")
		return ev, raw, fmt.Errorf("%s: %s", name, ev.ErrorText)
	}
	return ev, raw, nil
}

// snapshot reads what the dialogue needs through fleetops-mcp's read tools.
func (c *Chat) snapshot(ctx context.Context, cs *mcp.ClientSession, operator, sessionID string) (dialogue.Snapshot, []ToolEvent, error) {
	var snap dialogue.Snapshot
	var calls []ToolEvent
	var zones tools.ZoneList
	ev, raw, err := c.agentCall(ctx, cs, operator, sessionID, "list_zones", map[string]any{}, nil)
	calls = append(calls, ev)
	if err != nil {
		return snap, calls, err
	}
	_ = json.Unmarshal(raw, &zones)
	for _, z := range zones.Items {
		snap.Zones = append(snap.Zones, z.Name)
		snap.ZoneStates = append(snap.ZoneStates, dialogue.ZoneState{Name: z.Name, EstopStatus: z.EstopStatus, RobotCount: z.RobotCount})
		for _, w := range z.Waypoints {
			snap.Waypoints = append(snap.Waypoints, dialogue.Waypoint{Zone: z.Name, Name: w.Name, X: w.X, Y: w.Y})
		}
	}
	var robots tools.RobotList
	ev, raw, err = c.agentCall(ctx, cs, operator, sessionID, "list_robots", map[string]any{}, nil)
	calls = append(calls, ev)
	if err != nil {
		return snap, calls, err
	}
	_ = json.Unmarshal(raw, &robots)
	for _, r := range robots.Items {
		snap.Robots = append(snap.Robots, dialogue.Robot{Name: r.Name, Battery: r.Status.BatteryPercent, Phase: r.Status.Phase,
			CurrentZone: r.Status.CurrentZone, EstopState: r.Status.EstopState})
	}
	var tasks tools.FleetTaskList
	ev, raw, err = c.agentCall(ctx, cs, operator, sessionID, "list_fleet_tasks", map[string]any{}, nil)
	calls = append(calls, ev)
	if err != nil {
		return snap, calls, err
	}
	_ = json.Unmarshal(raw, &tasks)
	for _, t := range tasks.Items {
		if t.Draft {
			created, _ := time.Parse(time.RFC3339, t.CreatedAt)
			snap.Drafts = append(snap.Drafts, dialogue.Draft{Name: t.Name, Created: created, Phase: t.Phase})
		}
	}
	return snap, calls, nil
}

// Whoami asks fleetops-mcp who the caller is (D75): the verified user of the
// bearer token, or unverified:<asserted operator>.
func Whoami(ctx context.Context, cs *mcp.ClientSession, operator string) (tools.WhoamiResult, error) {
	var w tools.WhoamiResult
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami", Arguments: map[string]any{}, Meta: mcp.Meta{mcpserver.MetaOperator: operator}})
	if err != nil {
		return w, err
	}
	raw, _ := json.Marshal(res.StructuredContent)
	err = json.Unmarshal(raw, &w)
	return w, err
}
