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

// Package catalogue loads and validates a site catalogue: the kinds of work a
// site offers, how each maps to Swarmada, and the decision tree of questions
// that leads to each (docs/DESIGN.md §3.10.1, D70).
package catalogue

import (
	"crypto/sha256"
	"fmt"
	"os"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// SomethingElse is the label every choice list ends with (R64, R69).
const SomethingElse = "Something else — describe it"

// MaxQuestions is the cap on questions before the preview (R64).
const MaxQuestions = 2

// MaxChoices is the number of choices a question may offer besides SomethingElse.
const MaxChoices = 4

const (
	apiVersion = "fleetops.alexbahel.github.io/v1alpha1"
	kindName   = "SiteCatalogue"
)

// Request types a kind can lead to (api/v1alpha1 RequestType values).
const (
	RequestDraftTask       = "DraftTask"
	RequestActivateTask    = "ActivateTask"
	RequestZoneMaintenance = "ZoneMaintenance"
	RequestZoneStop        = "ZoneStop"
)

// Fields is the closed vocabulary of request fields a kind, a question or a
// default may name.
// "name" is the draft's name when the person gives one; every DraftTask kind
// accepts it without listing it.
var Fields = []string{"location", "from", "to", "route", "robot", "zone", "times", "mode", "start", "reason", "task", "priority", "onFailure", "dock", "name"}

// fieldsFor lists the fields each request type can carry.
var fieldsFor = map[string][]string{
	RequestDraftTask:       {"location", "from", "to", "route", "robot", "zone", "times", "priority", "onFailure", "dock"},
	RequestActivateTask:    {"task"},
	RequestZoneMaintenance: {"zone", "mode", "start", "reason"},
	RequestZoneStop:        {"zone", "reason"},
}

// actionTypes are Swarmada api/v1 FleetAction spec.type values a robot
// member may use (External is not a robot action, inventory §1.4).
var actionTypes = []string{"Navigate", "PickUp", "DropOff", "Patrol", "Charge", "Clean", "Custom"}

// Sources a question's choices can come from.
const (
	SourceWaypoints     = "waypoints"
	SourceWaypointPairs = "waypointPairs"
	SourceRoutes        = "routes"
	SourceRobots        = "robots"
	SourceZones         = "zones"
	SourceDrafts        = "drafts"
)

var sourceFills = map[string][]string{
	SourceWaypoints:     {"location"},
	SourceWaypointPairs: {"from", "to"},
	SourceRoutes:        {"route"},
	SourceRobots:        {"robot"},
	SourceZones:         {"zone"},
	SourceDrafts:        {"task"},
}

// Catalogue is one site's catalogue.
type Catalogue struct {
	APIVersion     string            `json:"apiVersion"`
	Kind           string            `json:"kind"`
	Version        int32             `json:"version"`
	Site           string            `json:"site"`
	Namespace      string            `json:"namespace"`
	Location       LocationKeys      `json:"location"`
	Defaults       map[string]string `json:"defaults"`
	Kinds          []Kind            `json:"kinds"`
	Asks           []Ask             `json:"asks"`
	SomethingElse  OpenQuestion      `json:"somethingElse"`
	BasicSteps     []string          `json:"basicSteps"`
	NotYetPossible []Gap             `json:"notYetPossible"`

	// SHA256 is the file's SHA-256, set by Parse.
	SHA256 string `json:"-"`
}

// LocationKeys names the payload keys a location is written with at this site.
type LocationKeys struct {
	Waypoint string `json:"waypoint"`
	X        string `json:"x"`
	Y        string `json:"y"`
}

// Kind is one kind of work.
type Kind struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Class is a or b (docs/DESIGN.md §3.10.1).
	Class string `json:"class"`
	// ResultReported is false for class (b): Swarmada records arrival and
	// completion, not what the robot found (R67).
	ResultReported       bool              `json:"resultReported"`
	Request              string            `json:"request"`
	RequiredCapabilities []string          `json:"requiredCapabilities,omitempty"`
	Members              []Member          `json:"members,omitempty"`
	LookFor              []string          `json:"lookFor,omitempty"`
	Repeat               string            `json:"repeat,omitempty"`
	Required             []string          `json:"required"`
	Optional             []string          `json:"optional,omitempty"`
	Defaults             map[string]string `json:"defaults,omitempty"`
	Results              []string          `json:"results"`
}

// Member maps to one FleetTask member. At names the location field it runs
// at; Along names a route field (one member per stop); Pin names the field
// whose robot it is pinned to; Dock names the dock field; Bounds the zone
// field of a survey.
type Member struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	CustomType string   `json:"customType,omitempty"`
	At         string   `json:"at,omitempty"`
	Along      string   `json:"along,omitempty"`
	Pin        string   `json:"pin,omitempty"`
	Dock       string   `json:"dock,omitempty"`
	Bounds     string   `json:"bounds,omitempty"`
	DependsOn  []string `json:"dependsOn,omitempty"`
}

// Ask is a decision tree.
type Ask struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Kind is the kind this ask leads to when no question selects one.
	Kind      string     `json:"kind,omitempty"`
	Questions []Question `json:"questions"`
}

// Question is one node.
type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Fills   []string `json:"fills"`
	Choices []Choice `json:"choices,omitempty"`
	Source  *Source  `json:"source,omitempty"`
}

// Choice is one numbered choice. Exactly one of Kind, Set or SomethingElse.
type Choice struct {
	Label         string            `json:"label"`
	Kind          string            `json:"kind,omitempty"`
	Set           map[string]string `json:"set,omitempty"`
	SomethingElse bool              `json:"somethingElse,omitempty"`
}

// Source declares choices read from the cluster.
type Source struct {
	From  string `json:"from"`
	Order string `json:"order,omitempty"`
}

// OpenQuestion is the "something else" question.
type OpenQuestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Gap is a class (c) item: something no request can do until Swarmada changes.
type Gap struct {
	ID   string `json:"id"`
	Need string `json:"need"`
	Say  string `json:"say"`
	// Finding marks a gap in what is reported, not in what a robot can do
	// (a result, a photo): a request that needs it is still an inspection,
	// filed as the class (b) inspect kind for what it looks for, with the
	// "not reported" label and Say shown in the preview. It is never a
	// reason for "not possible" (R67, R73).
	Finding bool `json:"finding,omitempty"`
}

// classB is a kind Swarmada runs but whose result it does not report (R67).
const classB = "b"

// GapByID returns a notYetPossible entry.
func (c *Catalogue) GapByID(id string) (*Gap, bool) {
	for i := range c.NotYetPossible {
		if c.NotYetPossible[i].ID == id {
			return &c.NotYetPossible[i], true
		}
	}
	return nil, false
}

// LookForWords are the values of lookFor across the class (b) kinds.
func (c *Catalogue) LookForWords() []string {
	var out []string
	for _, k := range c.Kinds {
		if k.Class != classB {
			continue
		}
		for _, w := range k.LookFor {
			if !slices.Contains(out, w) {
				out = append(out, w)
			}
		}
	}
	return out
}

// FindingKind returns the class (b) kind whose lookFor is exactly the given
// set, in any order (R73).
func (c *Catalogue) FindingKind(lookFor []string) (*Kind, bool) {
	want := slices.Clone(lookFor)
	slices.Sort(want)
	want = slices.Compact(want)
	for i := range c.Kinds {
		k := &c.Kinds[i]
		if k.Class != classB || len(k.LookFor) == 0 {
			continue
		}
		have := slices.Clone(k.LookFor)
		slices.Sort(have)
		if slices.Equal(have, want) {
			return k, true
		}
	}
	return nil, false
}

// Load reads and validates a catalogue file.
func Load(path string) (*Catalogue, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads and validates a catalogue. Unknown keys are errors.
func Parse(b []byte) (*Catalogue, error) {
	var c Catalogue
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("catalogue: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("catalogue: %w", err)
	}
	c.SHA256 = fmt.Sprintf("%x", sha256.Sum256(b))
	return &c, nil
}

// KindByName returns a kind.
func (c *Catalogue) KindByName(name string) (*Kind, bool) {
	for i := range c.Kinds {
		if c.Kinds[i].Name == name {
			return &c.Kinds[i], true
		}
	}
	return nil, false
}

// AskByName returns an ask.
func (c *Catalogue) AskByName(name string) (*Ask, bool) {
	for i := range c.Asks {
		if c.Asks[i].Name == name {
			return &c.Asks[i], true
		}
	}
	return nil, false
}

// AsksFor lists the asks whose tree can lead to kind.
func (c *Catalogue) AsksFor(kind string) []string {
	var out []string
	for _, a := range c.Asks {
		if a.Kind == kind {
			out = append(out, a.Name)
			continue
		}
		for _, q := range a.Questions {
			for _, ch := range q.Choices {
				if ch.Kind == kind {
					out = append(out, a.Name)
				}
			}
		}
	}
	return out
}

// Fields returns the required and optional fields of k.
func (k *Kind) Fields() []string { return append(append([]string{}, k.Required...), k.Optional...) }

// Default returns the value of a field when the person gave none: the kind's
// default, else the site's.
func (c *Catalogue) Default(k *Kind, field string) (string, bool) {
	if v, ok := k.Defaults[field]; ok {
		return v, true
	}
	if !slices.Contains(k.Fields(), field) {
		return "", false
	}
	v, ok := c.Defaults[field]
	return v, ok
}

// Validate applies the rules of DESIGN.md §3.10.1.
func (c *Catalogue) Validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if c.APIVersion != apiVersion || c.Kind != kindName {
		add("apiVersion/kind must be %s %s", apiVersion, kindName)
	}
	if c.Version < 1 {
		add("version must be at least 1")
	}
	if c.Namespace == "" {
		add("namespace is required")
	}
	if c.Location.Waypoint == "" || c.Location.X == "" || c.Location.Y == "" {
		add("location.waypoint, location.x and location.y (payload keys) are required")
	}
	for f := range c.Defaults {
		if !slices.Contains([]string{"priority", "onFailure", "failureHandling"}, f) {
			add("site default %q: only priority, onFailure and failureHandling have site defaults", f)
		}
	}
	if v := c.Defaults["onFailure"]; v != "" && v != "Requeue" && v != "Alert" {
		add("site default onFailure %q: Requeue or Alert", v)
	}
	if v := c.Defaults["failureHandling"]; v != "" && v != "ReportOnly" && v != "PrepareFollowUp" {
		add("site default failureHandling %q: ReportOnly or PrepareFollowUp", v)
	}
	kinds := map[string]bool{}
	for _, k := range c.Kinds {
		if kinds[k.Name] {
			add("kind %q defined twice", k.Name)
		}
		kinds[k.Name] = true
		for _, e := range validateKind(k) {
			add("kind %q: %s", k.Name, e)
		}
	}
	asks := map[string]bool{}
	qids := map[string]bool{c.SomethingElse.ID: true}
	for _, a := range c.Asks {
		if asks[a.Name] {
			add("ask %q defined twice", a.Name)
		}
		asks[a.Name] = true
		for _, e := range c.validateAsk(a, kinds, qids) {
			add("ask %q: %s", a.Name, e)
		}
	}
	if c.SomethingElse.ID == "" || c.SomethingElse.Text == "" {
		add("somethingElse needs an id and a text")
	}
	for _, s := range c.BasicSteps {
		k, ok := c.KindByName(s)
		if !ok {
			add("basic step %q is not a kind", s)
			continue
		}
		if k.Request != RequestDraftTask || len(k.Members) != 1 {
			add("basic step %q must be a DraftTask kind with one member", s)
		}
	}
	for _, g := range c.NotYetPossible {
		if g.ID == "" || g.Need == "" || g.Say == "" {
			add("notYetPossible entries need id, need and say")
		}
		if g.Finding && len(c.LookForWords()) == 0 {
			add("notYetPossible %s is a finding, but no class b kind has lookFor", g.ID)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func validateKind(k Kind) []string {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if k.Name == "" || k.Title == "" {
		add("name and title are required")
	}
	switch k.Class {
	case "a":
	case "b":
		if k.ResultReported {
			add("resultReported: true on a class (b) kind")
		}
	default:
		add("class %q: only a and b can be offered (class c is notYetPossible)", k.Class)
	}
	allowed, ok := fieldsFor[k.Request]
	if !ok {
		add("request %q is not a request type", k.Request)
	}
	if len(k.Required) == 0 {
		add("at least one required field")
	}
	if len(k.Results) == 0 {
		add("results: name the fields a person reads afterwards")
	}
	for _, f := range k.Fields() {
		if !slices.Contains(allowed, f) {
			add("field %q is not a field of a %s request", f, k.Request)
		}
	}
	for f := range k.Defaults {
		if !slices.Contains(k.Fields(), f) {
			add("default for %q, which the kind does not have", f)
		}
	}
	if k.Request == RequestDraftTask {
		if len(k.Members) == 0 {
			add("a DraftTask kind needs members (its Swarmada mapping)")
		}
		names := map[string]bool{}
		for _, m := range k.Members {
			names[m.Name] = true
		}
		for _, m := range k.Members {
			if !slices.Contains(actionTypes, m.Type) {
				add("member %q type %q is not a Swarmada api/v1 robot action type", m.Name, m.Type)
			}
			if m.CustomType != "" && m.Type != "Custom" {
				add("member %q: customType needs type Custom", m.Name)
			}
			for _, ref := range []string{m.At, m.Along, m.Pin, m.Dock, m.Bounds} {
				if ref != "" && !slices.Contains(k.Fields(), ref) {
					add("member %q refers to field %q, which the kind does not have", m.Name, ref)
				}
			}
			for _, d := range m.DependsOn {
				if !names[d] {
					add("member %q depends on %q, which is not a member", m.Name, d)
				}
			}
		}
		if k.Repeat != "" && !slices.Contains(k.Fields(), k.Repeat) {
			add("repeat refers to field %q, which the kind does not have", k.Repeat)
		}
	} else if len(k.Members) > 0 || len(k.LookFor) > 0 || k.Repeat != "" {
		add("members, lookFor and repeat apply to DraftTask kinds only")
	}
	return errs
}

func (c *Catalogue) validateAsk(a Ask, kinds, qids map[string]bool) []string {
	var errs []string
	add := func(f string, x ...any) { errs = append(errs, fmt.Sprintf(f, x...)) }
	if a.Kind != "" && !kinds[a.Kind] {
		add("kind %q is not defined", a.Kind)
	}
	if len(a.Questions) > MaxQuestions {
		add("%d questions; at most %d before the preview", len(a.Questions), MaxQuestions)
	}
	selects := false
	for _, q := range a.Questions {
		if q.ID == "" || q.Text == "" {
			add("a question needs an id and a text")
		}
		if qids[q.ID] {
			add("question id %q used twice", q.ID)
		}
		qids[q.ID] = true
		if (q.Source == nil) == (len(q.Choices) == 0) {
			add("question %q needs exactly one of choices and source", q.ID)
		}
		if q.Source != nil {
			want, ok := sourceFills[q.Source.From]
			if !ok {
				add("question %q: unknown source %q", q.ID, q.Source.From)
			} else if !slices.Equal(want, q.Fills) {
				add("question %q: source %s fills %v", q.ID, q.Source.From, want)
			}
		}
		if len(q.Choices) > 0 {
			last := q.Choices[len(q.Choices)-1]
			if !last.SomethingElse || last.Label != SomethingElse {
				add("question %q: the last choice must be %q with somethingElse: true", q.ID, SomethingElse)
			}
			if len(q.Choices)-1 > MaxChoices {
				add("question %q: %d choices besides %q; at most %d", q.ID, len(q.Choices)-1, SomethingElse, MaxChoices)
			}
			for i, ch := range q.Choices {
				n := 0
				if ch.Kind != "" {
					n++
					selects = true
					if !kinds[ch.Kind] {
						add("question %q: choice %q selects undefined kind %q", q.ID, ch.Label, ch.Kind)
					}
				}
				if len(ch.Set) > 0 {
					n++
					for f := range ch.Set {
						if !slices.Contains(q.Fills, f) {
							add("question %q: choice %q sets %q, which the question does not fill", q.ID, ch.Label, f)
						}
					}
				}
				if ch.SomethingElse {
					n++
					if i != len(q.Choices)-1 {
						add("question %q: %q must be last", q.ID, SomethingElse)
					}
				}
				if n != 1 || ch.Label == "" {
					add("question %q: choice %d needs a label and exactly one of kind, set, somethingElse", q.ID, i+1)
				}
			}
		}
		for _, f := range q.Fills {
			if f != "kind" && !slices.Contains(Fields, f) {
				add("question %q fills unknown field %q", q.ID, f)
			}
		}
	}
	if a.Kind == "" && !selects {
		add("an ask without a kind needs a question whose choices select one")
	}
	// Every field a question fills must belong to a kind the ask can lead to.
	var reach []*Kind
	if a.Kind != "" {
		if k, ok := c.KindByName(a.Kind); ok {
			reach = append(reach, k)
		}
	}
	for _, q := range a.Questions {
		for _, ch := range q.Choices {
			if k, ok := c.KindByName(ch.Kind); ok {
				reach = append(reach, k)
			}
		}
	}
	for _, q := range a.Questions {
		for _, f := range q.Fills {
			if f == "kind" {
				continue
			}
			found := false
			for _, k := range reach {
				found = found || slices.Contains(k.Fields(), f)
			}
			if !found {
				add("question %q fills %q, which no kind of this ask has", q.ID, f)
			}
		}
	}
	return errs
}
