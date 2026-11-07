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

// Package dialogue walks a site catalogue's decision trees (docs/DESIGN.md
// §3.10.2, §3.10.3, D71, D72). It is a pure function of the catalogue, the
// dialogue state, what the model read from the person's words, and a fleet
// snapshot: it decides the next question or the request to preview, and it
// renders every question and preview itself. The model never writes them.
package dialogue

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// Interpretations of a "something else" answer (D72).
const (
	Known       = "known"
	Composed    = "composed"
	NotPossible = "notPossible"
)

// AskSomethingElse is the ask the model passes when the person's request
// fits no ask of the catalogue.
const AskSomethingElse = "something-else"

// Waypoint is a named pose.
type Waypoint struct{ Zone, Name, X, Y string }

// Robot is what the choices need of a robot.
type Robot struct {
	Name    string
	Battery *int32
	Phase   string
}

// Draft is a draft FleetTask.
type Draft struct {
	Name    string
	Created time.Time
}

// Snapshot is the fleet state the dialogue reads (through fleetops-mcp's read tools).
type Snapshot struct {
	Zones     []string
	Waypoints []Waypoint
	Robots    []Robot
	Drafts    []Draft
}

// Choice is a rendered, numbered choice.
type Choice struct {
	N             int               `json:"n"`
	Label         string            `json:"label"`
	Kind          string            `json:"kind,omitempty"`
	Set           map[string]string `json:"set,omitempty"`
	SomethingElse bool              `json:"somethingElse,omitempty"`
}

// Question is a rendered question.
type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Choices []Choice `json:"choices,omitempty"`
	// Open is the "something else" question, answered in free text.
	Open bool `json:"open,omitempty"`
}

// Render is the text the person sees.
func (q Question) Render() string {
	var b strings.Builder
	b.WriteString(q.Text)
	for _, c := range q.Choices {
		fmt.Fprintf(&b, "\n%d %s", c.N, c.Label)
	}
	return b.String()
}

// SomethingElse is a "something else" request.
type SomethingElse struct {
	// Text is the person's words, verbatim, kept by fleetops-agent.
	Text           string `json:"text"`
	Interpretation string `json:"interpretation,omitempty"`
	FollowUpAsked  bool   `json:"followUpAsked,omitempty"`
	// Finding is the notYetPossible id of a finding the request asked for
	// (a result, a photo): the preview says it is not reported (R73).
	Finding string `json:"finding,omitempty"`
}

// State is the dialogue state of one conversation. It is stored by
// fleetops-agent between messages and holds no secret.
type State struct {
	Ask    string            `json:"ask,omitempty"`
	Kind   string            `json:"kind,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
	// Asked lists every question shown before the preview, in order, one
	// asked again included; the cap counts it (D87).
	Asked []string `json:"asked,omitempty"`
	// Pending is the question awaiting an answer.
	Pending *Question `json:"pending,omitempty"`
	// Message is the person's first message of this request.
	Message       string         `json:"message,omitempty"`
	SomethingElse *SomethingElse `json:"somethingElse,omitempty"`
	Steps         []StepInput    `json:"steps,omitempty"`
	// Invalid holds values refused for their form, by field (start "now"):
	// never used, kept only to say why the question is asked again (D88).
	Invalid map[string]string `json:"invalid,omitempty"`
}

// set records a field value: one without its field's form is removed from
// the state and remembered as invalid, so the question that fills it is
// asked again (D88); a valid one clears an earlier refusal.
func (st *State) set(f, v string) {
	if st.Fields == nil {
		st.Fields = map[string]string{}
	}
	if form, ok := Forms[f]; ok && !form.Valid(v) {
		delete(st.Fields, f)
		if st.Invalid == nil {
			st.Invalid = map[string]string{}
		}
		st.Invalid[f] = v
		return
	}
	st.Fields[f] = v
	delete(st.Invalid, f)
}

// Active reports whether a request is under way.
func (s *State) Active() bool { return s.Ask != "" || s.SomethingElse != nil }

// StepInput is one basic step of a composed request.
type StepInput struct {
	Kind   string            `json:"kind"`
	Fields map[string]string `json:"fields,omitempty"`
}

// SomethingElseInput is the model's reading of a "something else" text.
type SomethingElseInput struct {
	Interpretation string      `json:"interpretation" jsonschema:"known (a catalogue kind), composed (from basic steps) or notPossible"`
	Kind           string      `json:"kind,omitempty" jsonschema:"the catalogue kind, for known"`
	Steps          []StepInput `json:"steps,omitempty" jsonschema:"basic steps in order, for composed"`
	Need           string      `json:"need,omitempty" jsonschema:"the notYetPossible id it needs, for notPossible"`
	Reason         string      `json:"reason,omitempty" jsonschema:"why it is not possible, in one sentence"`
	LookFor        []string    `json:"lookFor,omitempty" jsonschema:"for an inspection: what the robot looks at (damage, location-state, or both)"`
}

// Input is what the model passes to propose_task.
type Input struct {
	Ask           string              `json:"ask,omitempty" jsonschema:"the ask: one of the catalogue's asks, or something-else"`
	Kind          string              `json:"kind,omitempty" jsonschema:"the kind, when the person's words say it"`
	Fields        map[string]string   `json:"fields,omitempty" jsonschema:"the fields the person's words state, by the catalogue's field names; no other name is accepted"`
	SomethingElse *SomethingElseInput `json:"somethingElse,omitempty" jsonschema:"your reading of a 'something else' description"`
}

// Proposal is a request ready to preview.
type Proposal struct {
	Kind           string
	Title          string
	Tool           string
	Input          any
	Interpretation string
	ResultReported bool
	// Lines are the preview's field lines, "(default)" where not given.
	Lines []string
	// Missing are required items with no value and no default (rule 7).
	Missing []string
	// Notes are shown after the result label: what was asked for that
	// Swarmada does not report (R73).
	Notes []string
}

// Outcome is the engine's answer. Exactly one field is set.
type Outcome struct {
	Question    *Question `json:"question,omitempty"`
	Proposal    *Proposal `json:"-"`
	NotPossible string    `json:"notPossible,omitempty"`
	// Invalid is for the model, not the person: the call named a field the
	// catalogue does not know, and nothing of it was applied (D85).
	Invalid string `json:"invalid,omitempty"`
}

// ReviewEntry is a "something else" answer for the catalogue review log.
type ReviewEntry struct {
	Text           string
	Interpretation string
	Kind           string
	Steps          []string
	Reason         string
	// Finding is set when a notPossible reading named a finding gap and the
	// engine filed it as an inspection instead (R73).
	Finding string
}

// Engine walks one catalogue.
type Engine struct {
	Cat *catalogue.Catalogue
	// Review receives every interpreted "something else" answer (D72).
	Review func(ReviewEntry)
}

// Propose applies what the model read from the person's words and returns
// the next step.
func (e *Engine) Propose(st *State, in Input, snap Snapshot) Outcome {
	if st.Fields == nil {
		st.Fields = map[string]string{}
	}
	if msg := e.unknownFields(st, in); msg != "" {
		return Outcome{Invalid: msg}
	}
	for k, v := range in.Fields {
		if v != "" && slices.Contains(catalogue.Fields, k) {
			st.set(k, v)
		}
	}
	if in.SomethingElse != nil || in.Ask == AskSomethingElse || (in.Ask != "" && !e.knownAsk(in.Ask) && st.Ask == "") {
		if st.SomethingElse == nil {
			st.SomethingElse = &SomethingElse{}
		}
		if st.SomethingElse.Text == "" && in.SomethingElse != nil {
			// The request was "something else" from its first message: the
			// person's words are that message, verbatim.
			st.SomethingElse.Text = st.Message
		}
		if in.SomethingElse == nil {
			// The person's request fits no ask: one open question.
			return e.askOpen(st)
		}
		return e.somethingElse(st, *in.SomethingElse, snap)
	}
	// A "something else" description the model answered with a plain kind
	// is still an interpreted answer: it is logged like one (D72).
	reading := st.SomethingElse != nil && st.SomethingElse.Text != "" && st.SomethingElse.Interpretation == ""
	if reading && in.Ask != "" && in.Ask != st.Ask && e.knownAsk(in.Ask) {
		// A new ask in answer to a "something else" description is the
		// model's reading of it: the request becomes that ask (D87).
		a, _ := e.Cat.AskByName(in.Ask)
		st.Ask, st.Kind, st.Pending = in.Ask, a.Kind, nil
	}
	if st.Ask == "" {
		st.Ask = in.Ask
	}
	if in.Kind != "" {
		if _, ok := e.Cat.KindByName(in.Kind); ok && slices.Contains(e.Cat.AsksFor(in.Kind), st.Ask) {
			st.Kind = in.Kind
		}
	}
	if reading && st.Kind != "" {
		st.SomethingElse.Interpretation = Known
		if e.Review != nil {
			e.Review(ReviewEntry{Text: st.SomethingElse.Text, Interpretation: Known, Kind: st.Kind})
		}
	}
	return e.next(st, snap)
}

func (e *Engine) knownAsk(a string) bool { _, ok := e.Cat.AskByName(a); return ok }

var numbered = regexp.MustCompile(`^\s*(\d+)\b[\s.:)-]*(.*)$`)

// ParseChoice reads a reply that starts with a choice number. rest is the
// text after it, verbatim.
func ParseChoice(msg string) (n int, rest string, ok bool) {
	m := numbered.FindStringSubmatch(msg)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, strings.TrimSpace(m[2]), true
}

// Answer applies a numbered reply to the pending question. needModel is
// true when the reply carries a description the model must read ("4 check
// the extinguisher"); the description is then st.SomethingElse.Text.
func (e *Engine) Answer(st *State, n int, rest string, snap Snapshot) (out Outcome, needModel bool, ok bool) {
	q := st.Pending
	if q == nil || q.Open {
		return Outcome{}, false, false
	}
	var ch *Choice
	for i := range q.Choices {
		if q.Choices[i].N == n {
			ch = &q.Choices[i]
		}
	}
	if ch == nil {
		return Outcome{}, false, false
	}
	st.Pending = nil
	switch {
	case ch.SomethingElse:
		if st.SomethingElse == nil {
			st.SomethingElse = &SomethingElse{}
		}
		if rest == "" {
			return e.askOpen(st), false, true
		}
		st.SomethingElse.Text = rest
		return Outcome{}, true, true
	case ch.Kind != "":
		st.Kind = ch.Kind
	default:
		for k, v := range ch.Set {
			st.set(k, v)
		}
	}
	return e.next(st, snap), false, true
}

// Describe records the person's free-text answer to the open question,
// verbatim; the model reads it next.
func (st *State) Describe(text string) {
	if st.SomethingElse == nil {
		st.SomethingElse = &SomethingElse{}
	}
	st.SomethingElse.Text = text
	st.Pending = nil
}

// damageWords mark a finding about damage when the model gave no lookFor.
var damageWords = regexp.MustCompile(`(?i)\b(damage[sd]?|dent(s|ed)?|crack(s|ed)?|broken|leak(s|ing)?|scratch(es|ed)?|torn|bent)\b`)

// lookFor is what an inspection looks at: the model's lookFor, kept to the
// catalogue's words; else damage when the person's words name damage, and
// the state of the location otherwise.
func (e *Engine) lookFor(given []string, text string) []string {
	words := e.Cat.LookForWords()
	var out []string
	for _, w := range given {
		if w == "both" {
			return words
		}
		if slices.Contains(words, w) {
			out = append(out, w)
		}
	}
	if len(out) > 0 {
		return out
	}
	if damageWords.MatchString(text) {
		return []string{"damage"}
	}
	return []string{"location-state"}
}

func (e *Engine) askOpen(st *State) Outcome {
	if len(st.Asked) >= catalogue.MaxQuestions {
		return e.proposal(st, nil)
	}
	q := Question{ID: e.Cat.SomethingElse.ID, Text: e.Cat.SomethingElse.Text, Open: true}
	st.Asked = append(st.Asked, q.ID)
	st.Pending = &q
	return Outcome{Question: &q}
}

func (e *Engine) somethingElse(st *State, in SomethingElseInput, snap Snapshot) Outcome {
	se := st.SomethingElse
	finding := ""
	if in.Interpretation != Known && in.Interpretation != Composed {
		// R67, R73: a need for a finding (a result, a photo) is an inspection
		// whose result is not reported, not "not possible".
		if g, ok := e.Cat.GapByID(in.Need); ok && g.Finding {
			if k, ok := e.Cat.FindingKind(e.lookFor(in.LookFor, se.Text)); ok {
				finding = g.ID
				in = SomethingElseInput{Interpretation: Known, Kind: k.Name, Reason: in.Reason}
			}
		}
	}
	se.Interpretation = in.Interpretation
	se.Finding = finding
	entry := ReviewEntry{Text: se.Text, Interpretation: in.Interpretation, Kind: in.Kind, Reason: in.Reason, Finding: finding}
	for _, s := range in.Steps {
		entry.Steps = append(entry.Steps, s.Kind)
	}
	if e.Review != nil {
		e.Review(entry)
	}
	switch in.Interpretation {
	case Known:
		k, ok := e.Cat.KindByName(in.Kind)
		if !ok {
			return Outcome{NotPossible: fmt.Sprintf("Not possible here: the site catalogue has no kind %q. Nothing was filed.", in.Kind)}
		}
		st.Kind = k.Name
		if st.Ask == "" {
			if asks := e.Cat.AsksFor(k.Name); len(asks) > 0 {
				st.Ask = asks[0]
			}
		}
		return e.next(st, snap)
	case Composed:
		if len(in.Steps) == 0 {
			return Outcome{NotPossible: "Not possible here: no basic step was named. Nothing was filed."}
		}
		for _, s := range in.Steps {
			if !slices.Contains(e.Cat.BasicSteps, s.Kind) {
				return Outcome{NotPossible: fmt.Sprintf("Not possible here: %q is not one of the basic steps (%s). Nothing was filed.", s.Kind, strings.Join(e.Cat.BasicSteps, ", "))}
			}
		}
		st.Steps = in.Steps
		st.Kind = "composed"
		return e.proposal(st, snap.waypoints())
	default:
		msg := "Not possible here"
		if in.Reason != "" {
			msg += ": " + strings.TrimSuffix(in.Reason, ".")
		}
		msg += "."
		for _, g := range e.Cat.NotYetPossible {
			if g.ID == in.Need {
				msg += " " + g.Say
			}
		}
		return Outcome{NotPossible: msg + " Nothing was filed."}
	}
}

// next walks the tree: the first unanswered question of the ask, within the
// cap; otherwise the proposal.
func (e *Engine) next(st *State, snap Snapshot) Outcome {
	a, ok := e.Cat.AskByName(st.Ask)
	if !ok {
		return e.askOpen(st)
	}
	if st.Kind == "" && a.Kind != "" {
		st.Kind = a.Kind
	}
	for _, q := range a.Questions {
		if e.answered(st, q) {
			continue
		}
		choices := e.choices(st, q, snap)
		if q.Source != nil && len(choices) == 1 {
			// One option: inferred from live state, not asked (rule 2).
			for k, v := range choices[0].Set {
				st.Fields[k] = v
			}
			continue
		}
		if q.Source != nil && len(choices) == 0 {
			return Outcome{NotPossible: fmt.Sprintf("Not possible now: there is nothing to choose for %q in %s. Nothing was filed.", q.Text, e.Cat.Namespace)}
		}
		// Every question shown counts, one asked again included (D87).
		if len(st.Asked) >= catalogue.MaxQuestions {
			break
		}
		rq := Question{ID: q.ID, Text: invalidNote(st, q.Fills) + q.Text}
		for i, c := range choices {
			c.N = i + 1
			rq.Choices = append(rq.Choices, c)
		}
		rq.Choices = append(rq.Choices, Choice{N: len(choices) + 1, Label: catalogue.SomethingElse, SomethingElse: true})
		st.Asked = append(st.Asked, q.ID)
		st.Pending = &rq
		return Outcome{Question: &rq}
	}
	if st.Kind == "" {
		// The kind question was not answered within the cap.
		return Outcome{NotPossible: "Not possible to prepare a request: the kind of work is still open after two questions. Nothing was filed; say what you need in one message."}
	}
	return e.proposal(st, snap.waypoints())
}

// answered reports whether a question need not be asked: every field it
// fills is known, from the person's words or a default of the kind (rule 6:
// defaults are shown in the preview, not asked).
func (e *Engine) answered(st *State, q catalogue.Question) bool {
	k, _ := e.Cat.KindByName(st.Kind)
	for _, f := range q.Fills {
		if _, bad := st.Invalid[f]; bad {
			return false // a refused value is asked again (D88)
		}
		if f == "kind" {
			if st.Kind == "" {
				return false
			}
			continue
		}
		if _, ok := st.Fields[f]; ok {
			continue
		}
		if k != nil {
			if _, ok := e.Cat.Default(k, f); ok {
				continue
			}
		}
		return false
	}
	return true
}

// invalidNote opens a question asked again for a refused value: the value
// and the field's valid forms (D88).
func invalidNote(st *State, fills []string) string {
	var out []string
	for _, f := range fills {
		if v, ok := st.Invalid[f]; ok {
			out = append(out, fmt.Sprintf("The %s %q cannot be used: give %s.", f, v, Forms[f].Forms))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " ") + " "
}

func (s Snapshot) waypoints() map[string]Waypoint {
	m := map[string]Waypoint{}
	for _, w := range s.Waypoints {
		m[w.Name] = w
	}
	return m
}

// mentioned reports whether the person's words name an option, as a whole
// name or a distinctive part of it ("dock" for receiving-dock-1).
func mentioned(msg, name string) bool {
	m := strings.ToLower(msg)
	n := strings.ToLower(name)
	if strings.Contains(m, n) || strings.Contains(m, strings.ReplaceAll(n, "-", " ")) {
		return true
	}
	for _, part := range strings.Split(n, "-") {
		if len(part) >= 4 && regexp.MustCompile(`\b`+regexp.QuoteMeta(part)+`\b`).MatchString(m) {
			return true
		}
	}
	return false
}

func mentionedFirst[T any](msg string, xs []T, name func(T) string) []T {
	var a, b []T
	for _, x := range xs {
		if mentioned(msg, name(x)) {
			a = append(a, x)
		} else {
			b = append(b, x)
		}
	}
	return append(a, b...)
}

// choices builds a question's options, likeliest first, at most MaxChoices
// (rule 4). SomethingElse is appended by the caller.
func (e *Engine) choices(st *State, q catalogue.Question, snap Snapshot) []Choice {
	var out []Choice
	if q.Source == nil {
		for _, c := range q.Choices {
			if c.SomethingElse {
				continue
			}
			out = append(out, Choice{Label: c.Label, Kind: c.Kind, Set: c.Set})
		}
		return limit(out)
	}
	switch q.Source.From {
	case catalogue.SourceWaypoints:
		for _, w := range mentionedFirst(st.Message, snap.Waypoints, func(w Waypoint) string { return w.Name }) {
			out = append(out, Choice{Label: fmt.Sprintf("%s (%s)", w.Name, w.Zone), Set: map[string]string{"location": w.Name}})
		}
	case catalogue.SourceWaypointPairs:
		wps := mentionedFirst(st.Message, snap.Waypoints, func(w Waypoint) string { return w.Name })
		for _, a := range wps {
			for _, b := range wps {
				if a.Name != b.Name {
					out = append(out, Choice{Label: a.Name + " → " + b.Name, Set: map[string]string{"from": a.Name, "to": b.Name}})
				}
			}
		}
	case catalogue.SourceRoutes:
		byZone := map[string][]string{}
		var zones []string
		for _, w := range snap.Waypoints {
			if _, ok := byZone[w.Zone]; !ok {
				zones = append(zones, w.Zone)
			}
			byZone[w.Zone] = append(byZone[w.Zone], w.Name)
		}
		for _, z := range zones {
			if len(byZone[z]) > 1 {
				out = append(out, Choice{Label: fmt.Sprintf("Every waypoint of %s: %s", z, strings.Join(byZone[z], " → ")), Set: map[string]string{"route": strings.Join(byZone[z], ",")}})
			}
		}
		for _, w := range snap.Waypoints {
			out = append(out, Choice{Label: w.Name + " only", Set: map[string]string{"route": w.Name}})
		}
	case catalogue.SourceRobots:
		rs := append([]Robot{}, snap.Robots...)
		sort.SliceStable(rs, func(i, j int) bool {
			if q.Source.Order == "battery" {
				return battery(rs[i]) < battery(rs[j])
			}
			return rs[i].Phase == "Idle" && rs[j].Phase != "Idle"
		})
		for _, r := range mentionedFirst(st.Message, rs, func(r Robot) string { return r.Name }) {
			out = append(out, Choice{Label: fmt.Sprintf("%s (battery %s, %s)", r.Name, batteryText(r), r.Phase), Set: map[string]string{"robot": r.Name}})
		}
	case catalogue.SourceZones:
		zs := append([]string{}, snap.Zones...)
		sort.Strings(zs)
		for _, z := range mentionedFirst(st.Message, zs, func(z string) string { return z }) {
			out = append(out, Choice{Label: z, Set: map[string]string{"zone": z}})
		}
	case catalogue.SourceDrafts:
		ds := append([]Draft{}, snap.Drafts...)
		sort.SliceStable(ds, func(i, j int) bool { return ds[i].Created.After(ds[j].Created) })
		for _, d := range mentionedFirst(st.Message, ds, func(d Draft) string { return d.Name }) {
			out = append(out, Choice{Label: d.Name, Set: map[string]string{"task": d.Name}})
		}
	}
	return limit(out)
}

func limit(cs []Choice) []Choice {
	if len(cs) > catalogue.MaxChoices {
		return cs[:catalogue.MaxChoices]
	}
	return cs
}

func battery(r Robot) int32 {
	if r.Battery == nil {
		return 101
	}
	return *r.Battery
}

func batteryText(r Robot) string {
	if r.Battery == nil {
		return "unreported"
	}
	return fmt.Sprintf("%d%%", *r.Battery)
}

// value returns a field's value, the default, and whether it was defaulted.
func (e *Engine) value(st *State, k *catalogue.Kind, f string) (string, bool, bool) {
	if v, ok := st.Fields[f]; ok {
		return v, true, false
	}
	if f == "reason" && st.Message != "" {
		// The reason defaults to the person's own words.
		return st.Message, true, true
	}
	if v, ok := e.Cat.Default(k, f); ok {
		return v, true, true
	}
	return "", false, false
}

// proposal builds the request to preview, with defaults filled in and the
// required items still missing listed (rule 7).
func (e *Engine) proposal(st *State, wps map[string]Waypoint) Outcome {
	st.Pending = nil
	if wps == nil {
		wps = map[string]Waypoint{}
	}
	if st.Kind == "composed" {
		return Outcome{Proposal: e.composed(st, wps)}
	}
	k, ok := e.Cat.KindByName(st.Kind)
	if !ok {
		return Outcome{NotPossible: "Not possible to prepare a request: the kind of work is still open. Nothing was filed; say what you need in one message."}
	}
	p := &Proposal{Kind: k.Name, Title: k.Title, Interpretation: Known, ResultReported: k.ResultReported}
	if st.SomethingElse != nil && st.SomethingElse.Finding != "" {
		if g, ok := e.Cat.GapByID(st.SomethingElse.Finding); ok {
			p.Notes = append(p.Notes, fmt.Sprintf("Not reported: %s. %s", g.Need, g.Say))
		}
	}
	vals := map[string]string{}
	for _, f := range k.Fields() {
		v, ok, def := e.value(st, k, f)
		if !ok {
			if slices.Contains(k.Required, f) {
				p.Missing = append(p.Missing, f)
				p.Lines = append(p.Lines, fmt.Sprintf("%s: missing", label(f)))
			}
			continue
		}
		vals[f] = v
		if f == "reason" && def {
			p.Lines = append(p.Lines, fmt.Sprintf("%s: %q (your words)", label(f), v))
			continue
		}
		line := fieldLine(f, v, def, k, wps)
		if bad, ok := st.Invalid[f]; ok {
			// Past the cap, or no question fills it: the default is used (D88).
			line += fmt.Sprintf(" — %q could not be used: give %s (choose 3 to change it)", bad, Forms[f].Forms)
		}
		p.Lines = append(p.Lines, line)
	}
	if k.Request == catalogue.RequestDraftTask {
		if _, ok := vals["robot"]; !ok && !hasPin(k) {
			caps := "any"
			if len(k.RequiredCapabilities) > 0 {
				caps = strings.Join(k.RequiredCapabilities, ", ")
			}
			p.Lines = append(p.Lines, fmt.Sprintf("Robot: any eligible robot with %s (default)", caps))
		}
		p.Lines = append(p.Lines, "Start: when a person activates the draft (default)")
	}
	instr := ""
	if st.SomethingElse != nil {
		instr = st.SomethingElse.Text
	}
	pv := &tools.PreviewInput{Kind: k.Name, Interpretation: "Known", Instruction: instr, ResultReported: k.ResultReported,
		FailureHandling: e.Cat.Defaults["failureHandling"], CatalogueVersion: e.Cat.Version, CatalogueSHA256: e.Cat.SHA256}
	switch k.Request {
	case catalogue.RequestDraftTask:
		name := draftName(k.Name, vals, st.Message)
		if n := st.Fields["name"]; n != "" && dnsName.MatchString(n) {
			name = n // the name the person gave
		}
		in := tools.DraftTaskInput{Name: name, Purpose: purpose(k, vals), Preview: pv}
		for _, m := range k.Members {
			in.Actions = append(in.Actions, e.member(m, k, vals, wps, instr))
		}
		if k.Repeat != "" {
			if n, err := strconv.ParseInt(vals[k.Repeat], 10, 32); err == nil && n > 0 {
				in.Repeat = int32(n)
			}
		}
		p.Tool, p.Input = "request_draft_task", in
		p.Lines = append(p.Lines, "Draft name: "+in.Name)
	case catalogue.RequestZoneMaintenance:
		p.Tool, p.Input = "request_zone_maintenance", tools.ZoneMaintenanceInput{ZoneName: vals["zone"], Mode: vals["mode"], Reason: vals["reason"], ScheduledStart: vals["start"], Preview: pv}
	case catalogue.RequestZoneStop:
		p.Tool, p.Input = "request_zone_stop", tools.ZoneStopInput{ZoneName: vals["zone"], Reason: vals["reason"], Preview: pv}
	case catalogue.RequestActivateTask:
		p.Tool, p.Input = "request_activate_task", tools.ActivateTaskInput{TaskName: vals["task"], Preview: pv}
	}
	return Outcome{Proposal: p}
}

func hasPin(k *catalogue.Kind) bool {
	for _, m := range k.Members {
		if m.Pin != "" {
			return true
		}
	}
	return false
}

func (e *Engine) member(m catalogue.Member, k *catalogue.Kind, vals map[string]string, wps map[string]Waypoint, instr string) tools.DraftActionInput {
	a := tools.DraftActionInput{
		Name: m.Name, Type: m.Type, CustomType: m.CustomType, RequiredCapabilities: k.RequiredCapabilities,
		LookFor: k.LookFor, Instruction: instr, DependsOn: m.DependsOn,
		Priority: vals["priority"], OnFailure: vals["onFailure"],
	}
	if m.At != "" {
		a.Location = location(vals[m.At], wps)
	}
	if m.Along != "" {
		for _, n := range strings.Split(vals[m.Along], ",") {
			if n = strings.TrimSpace(n); n != "" {
				a.Route = append(a.Route, *location(n, wps))
			}
		}
	}
	if m.Pin != "" {
		a.RobotSelector = vals[m.Pin]
	} else if r := vals["robot"]; r != "" {
		a.PreferredRobot = r
	}
	if m.Dock != "" {
		a.Dock = vals[m.Dock]
	}
	if m.Bounds != "" {
		a.Bounds = vals[m.Bounds]
	}
	return a
}

func location(name string, wps map[string]Waypoint) *tools.LocationInput {
	if w, ok := wps[name]; ok {
		return &tools.LocationInput{Zone: w.Zone, Waypoint: w.Name, X: w.X, Y: w.Y}
	}
	// An unknown name stays as given; check_task reports WAYPOINT_NOT_FOUND.
	return &tools.LocationInput{Zone: "", Waypoint: name}
}

func (e *Engine) composed(st *State, wps map[string]Waypoint) *Proposal {
	p := &Proposal{Kind: "composed", Title: "Composed from basic steps for human review", Interpretation: Composed, ResultReported: true}
	in := tools.DraftTaskInput{Purpose: "composed from basic steps for human review"}
	var names []string
	prev := ""
	for i, s := range st.Steps {
		k, _ := e.Cat.KindByName(s.Kind)
		vals := map[string]string{}
		for f := range s.Fields {
			vals[f] = s.Fields[f]
		}
		for _, f := range k.Fields() {
			if _, ok := vals[f]; !ok {
				if v, ok := e.Cat.Default(k, f); ok {
					vals[f] = v
				} else if slices.Contains(k.Required, f) {
					p.Missing = append(p.Missing, fmt.Sprintf("step %d %s", i+1, f))
				}
			}
		}
		p.ResultReported = p.ResultReported && k.ResultReported
		m := e.member(k.Members[0], k, vals, wps, st.SomethingElse.Text)
		m.Name = fmt.Sprintf("step%d-%s", i+1, k.Name)
		if prev != "" {
			m.DependsOn = []string{prev}
		}
		prev = m.Name
		in.Actions = append(in.Actions, m)
		names = append(names, k.Name)
		p.Lines = append(p.Lines, fmt.Sprintf("Step %d: %s%s", i+1, k.Title, stepDetail(vals)))
	}
	in.Name = draftName("composed", map[string]string{"steps": strings.Join(names, "-")}, st.Message)
	in.Preview = &tools.PreviewInput{Kind: "composed", Interpretation: "Composed", Instruction: st.SomethingElse.Text, ResultReported: p.ResultReported,
		FailureHandling: e.Cat.Defaults["failureHandling"], CatalogueVersion: e.Cat.Version, CatalogueSHA256: e.Cat.SHA256}
	p.Tool, p.Input = "request_draft_task", in
	p.Lines = append(p.Lines, "Draft name: "+in.Name)
	return p
}

func stepDetail(vals map[string]string) string {
	var ks []string
	for k := range vals {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var parts []string
	for _, k := range ks {
		parts = append(parts, k+" "+vals[k])
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func label(f string) string {
	switch f {
	case "onFailure":
		return "On failure"
	case "from":
		return "From"
	case "to":
		return "To"
	}
	return strings.ToUpper(f[:1]) + f[1:]
}

func fieldLine(f, v string, def bool, k *catalogue.Kind, wps map[string]Waypoint) string {
	shown := v
	switch f {
	case "onFailure":
		shown = "retries up to 3 times, then alerts"
		if v == "Alert" {
			shown = "alerts on the first failure, no retry"
		}
	case "location", "from", "to", "dock":
		if w, ok := wps[v]; ok {
			shown = fmt.Sprintf("%s (FleetZone %s, x %s, y %s)", v, w.Zone, w.X, w.Y)
		}
	case "route":
		shown = strings.ReplaceAll(v, ",", " → ")
	case "times":
		shown = v + " round(s)"
	case "start":
		if v == "" {
			shown = "on approval"
		}
	case "robot":
		for _, m := range k.Members {
			if m.Pin == "robot" {
				shown = v + " (pinned)"
			}
		}
		if shown == v {
			shown = v + " (a preference; another eligible robot runs it if this one cannot)"
		}
	}
	if def {
		shown += " (default)"
	}
	return fmt.Sprintf("%s: %s", label(f), shown)
}

var (
	nonName = regexp.MustCompile(`[^a-z0-9-]+`)
	dnsName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

// draftName is a DNS-1123 name from the kind and its main field, with a
// short hash of the person's words so two requests do not collide.
func draftName(kind string, vals map[string]string, msg string) string {
	main := ""
	for _, f := range []string{"location", "to", "robot", "route", "zone", "steps"} {
		if v := vals[f]; v != "" {
			main = strings.SplitN(v, ",", 2)[0]
			break
		}
	}
	base := nonName.ReplaceAllString(strings.ToLower(kind+"-"+main), "-")
	base = strings.Trim(strings.ReplaceAll(base, "--", "-"), "-")
	if len(base) > 50 {
		base = strings.Trim(base[:50], "-")
	}
	h := sha256.Sum256([]byte(kind + "\x00" + msg + "\x00" + fmt.Sprint(vals)))
	return fmt.Sprintf("%s-%x", base, h[:2])
}

func purpose(k *catalogue.Kind, vals map[string]string) string {
	for _, f := range []string{"location", "route", "robot", "zone"} {
		if v := vals[f]; v != "" {
			return fmt.Sprintf("%s: %s", k.Title, strings.ReplaceAll(v, ",", " → "))
		}
	}
	if vals["from"] != "" {
		return fmt.Sprintf("%s: %s → %s", k.Title, vals["from"], vals["to"])
	}
	return k.Title
}
