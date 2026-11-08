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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AlexBahel/fleet-ops-copilot/internal/catalogue"
)

// FieldForm is what a request field holds: the description propose_task's
// schema gives it, its allowed values when they are a closed list, and, for
// a field with a fixed form, the check a value must pass (D85, D88).
type FieldForm struct {
	Desc string
	Enum []string
	// Forms says the valid forms to the person when a value is refused.
	Forms string
	valid func(string) bool
}

// Valid reports whether v has the field's form; a field without a fixed
// form takes any value (check_task checks names against the fleet).
func (f FieldForm) Valid(v string) bool {
	if len(f.Enum) > 0 {
		return slices.Contains(f.Enum, v)
	}
	return f.valid == nil || f.valid(v)
}

// Forms are the forms of catalogue.Fields, by name.
var Forms = map[string]FieldForm{
	"location": {Desc: "a waypoint name (list_zones lists each zone's waypoints)"},
	"from":     {Desc: "the waypoint name to pick up from"},
	"to":       {Desc: "the waypoint name to drop off at"},
	"route":    {Desc: "waypoint names in order, separated by commas"},
	"robot":    {Desc: "a Robot name"},
	"zone":     {Desc: "a FleetZone name"},
	"times": {Desc: "the number of rounds, a whole number of 1 or more", Forms: "a whole number of rounds, 1 or more",
		valid: func(v string) bool { n, err := strconv.Atoi(v); return err == nil && n > 0 }},
	"mode": {Desc: "Graceful (robots finish their current action) or Immediate (current actions are requeued)", Enum: []string{"Graceful", "Immediate"},
		Forms: "Graceful or Immediate"},
	"start": {Desc: "an RFC 3339 time, for example 2026-11-04T08:00:00Z; leave it out to start on approval",
		Forms: "on approval, or a time such as 2026-11-04T08:00:00Z",
		valid: func(v string) bool { _, err := time.Parse(time.RFC3339, v); return v == "" || err == nil }},
	"reason":   {Desc: "why, in the person's words"},
	"task":     {Desc: "the name of a draft FleetTask"},
	"priority": {Desc: "the action priority", Enum: []string{"Low", "Normal", "High", "Critical"}, Forms: "Low, Normal, High or Critical"},
	"onFailure": {Desc: "Requeue (retry up to 3 times, then alert) or Alert (alert on the first failure)", Enum: []string{"Requeue", "Alert"},
		Forms: "Requeue or Alert"},
	"dock": {Desc: "the charging dock's waypoint name"},
	"name": {Desc: "the draft's name, only when the person gives one (lower-case letters, digits and '-')",
		Forms: "lower-case letters, digits and '-'", valid: func(v string) bool { return dnsName.MatchString(v) }},
}

// rightAway reports whether a start value means "as soon as possible":
// now, immediately or asap, in any case (D91). Such a window starts on
// approval, before the start's form is checked; its mode is not changed.
func rightAway(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "now", "immediately", "asap":
		return true
	}
	return false
}

// unknownFields refuses field names the catalogue does not know, before
// anything of the call is applied (D85): the model is told which names
// are allowed, for the kind when it is known, and asked which one it meant.
func (e *Engine) unknownFields(st *State, in Input) string {
	var bad []string
	check := func(fs map[string]string) {
		for k := range fs {
			if !slices.Contains(catalogue.Fields, k) && !slices.Contains(bad, k) {
				bad = append(bad, k)
			}
		}
	}
	check(in.Fields)
	if in.SomethingElse != nil {
		for _, s := range in.SomethingElse.Steps {
			check(s.Fields)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	slices.Sort(bad)
	quoted := make([]string, len(bad))
	for i, b := range bad {
		quoted[i] = strconv.Quote(b)
	}
	msg := fmt.Sprintf("propose_task was not applied: the site catalogue has no field %s.", strings.Join(quoted, ", "))
	kind := in.Kind
	if kind == "" {
		kind = st.Kind
	}
	if kind == "" {
		if a, ok := e.Cat.AskByName(in.Ask); ok {
			kind = a.Kind
		}
	}
	if k, ok := e.Cat.KindByName(kind); ok {
		msg += fmt.Sprintf(" Which of the fields of %s did the person mean: %s?", k.Name, strings.Join(k.Fields(), ", "))
	} else {
		msg += " Which of the allowed fields did the person mean?"
	}
	return msg + " Allowed fields: " + strings.Join(catalogue.Fields, ", ") + ". Call propose_task again with the allowed names."
}
