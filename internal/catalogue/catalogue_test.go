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

package catalogue

import (
	"os"
	"strings"
	"testing"
)

const sitePath = "../../catalogue/warehouse-a.yaml"

func site(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSiteCatalogueLoads(t *testing.T) {
	c, err := Parse(site(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SHA256) != 64 || c.Version != 1 || c.Namespace != "warehouse-a" {
		t.Fatalf("got version %d namespace %q sha %q", c.Version, c.Namespace, c.SHA256)
	}
	for _, k := range c.Kinds {
		if len(c.AsksFor(k.Name)) == 0 {
			t.Errorf("kind %q is reached by no ask", k.Name)
		}
	}
}

// TestInspectFirstQuestion pins R64/R69: inspect's first question is exactly
// 1 Damage · 2 Location · 3 Both · 4 Something else — describe it.
func TestInspectFirstQuestion(t *testing.T) {
	c, err := Parse(site(t))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := c.AskByName("inspect")
	if !ok {
		t.Fatal("no inspect ask")
	}
	q := a.Questions[0]
	var labels []string
	for _, ch := range q.Choices {
		labels = append(labels, ch.Label)
	}
	if got, want := strings.Join(labels, " · "), "Damage · Location · Both · Something else — describe it"; got != want {
		t.Fatalf("inspect Q1 choices %q, want %q", got, want)
	}
	if q.Text != "What should the robot inspect?" {
		t.Fatalf("inspect Q1 text %q", q.Text)
	}
}

// TestClassBKindsDoNotReportResults pins R67.
func TestClassBKindsDoNotReportResults(t *testing.T) {
	c, err := Parse(site(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range c.Kinds {
		if strings.HasPrefix(k.Name, "inspect-") && (k.Class != "b" || k.ResultReported) {
			t.Errorf("%s: class %s resultReported %v, want b/false", k.Name, k.Class, k.ResultReported)
		}
	}
}

func TestValidationRefuses(t *testing.T) {
	base := string(site(t))
	cases := []struct{ name, from, to, want string }{
		{"unknown key", "version: 1\n", "version: 1\nextra: x\n", "unknown field"},
		{"class c", "class: b\n    resultReported: false\n    request: DraftTask\n    members:\n      - {name: survey", "class: c\n    resultReported: false\n    request: DraftTask\n    members:\n      - {name: survey", "only a and b"},
		{"result on class b", "name: survey\n    title: Survey (map) an area\n    class: b\n    resultReported: false", "name: survey\n    title: Survey (map) an area\n    class: b\n    resultReported: true", "resultReported: true on a class (b)"},
		{"no something else", "          - {label: 5 rounds, set: {times: \"5\"}}\n          - {label: \"Something else — describe it\", somethingElse: true}", "          - {label: 5 rounds, set: {times: \"5\"}}", "last choice must be"},
		{"type outside api/v1", "{name: move, type: Navigate, at: location}", "{name: move, type: Teleport, at: location}", "not a Swarmada api/v1 robot action type"},
		{"undefined kind", "{label: Both, kind: inspect-both}", "{label: Both, kind: inspect-all}", "undefined kind"},
		{"default for missing field", "defaults: {dock: charging-bay}", "defaults: {dock: charging-bay, zone: x}", "which the kind does not have"},
		{"three questions", "      - id: patrol.times\n", "      - id: patrol.extra\n        text: x?\n        fills: [robot]\n        source: {from: robots}\n      - id: patrol.times\n", "at most 2 before the preview"},
		{"unknown field", "fills: [route]", "fills: [path]", "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(base, tc.from) {
				t.Fatalf("test fixture text %q not in the site catalogue", tc.from)
			}
			_, err := Parse([]byte(strings.Replace(base, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
