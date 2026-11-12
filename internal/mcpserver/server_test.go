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

package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// The tool surface is exactly the designed list: no clear, release, delete,
// approve or edit tool exists.
func TestToolSurface(t *testing.T) {
	p, err := policy.Load("../../policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&tools.Service{Engine: policy.New(p)})
	if err := Check(context.Background(), srv); err != nil {
		t.Fatal(err)
	}
	for _, n := range append(append([]string{}, ReadTools...), RequestTools...) {
		for _, bad := range []string{"clear", "release", "resume", "delete", "approve", "cancel", "patch", "update"} {
			if strings.Contains(n, bad) {
				t.Errorf("tool %q contains forbidden verb %q", n, bad)
			}
		}
	}
}

// D66: every namespace parameter names the namespace the tools use and says
// that others are refused, in the deployment's own terms.
func TestNamespaceParameterDescribed(t *testing.T) {
	p, err := policy.Load("../../policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	svc := &tools.Service{Engine: policy.New(p), DefaultNamespace: "warehouse-a"}
	want := NamespaceDescription(svc)
	if !strings.Contains(want, `"warehouse-a"`) || !strings.Contains(want, "refused") {
		t.Fatalf("description %q does not name the namespace and the refusal", want)
	}
	tl, err := ListTools(context.Background(), New(svc))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tool := range tl {
		b, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		ns, ok := s.Properties["namespace"]
		if !ok {
			continue
		}
		n++
		if ns.Description != want {
			t.Errorf("%s: namespace description %q, want %q", tool.Name, ns.Description, want)
		}
	}
	if n == 0 {
		t.Fatal("no tool has a namespace parameter")
	}
	t.Logf("%d tools describe namespace as %q", n, want)

	d1, err := ToolsDigest(context.Background(), New(svc))
	if err != nil {
		t.Fatal(err)
	}
	other := &tools.Service{Engine: policy.New(p), DefaultNamespace: "warehouse-b"}
	d2, _ := ToolsDigest(context.Background(), New(other))
	if d1 == d2 || len(d1) != 64 {
		t.Errorf("digest does not follow the served descriptions: %s %s", d1, d2)
	}
}
