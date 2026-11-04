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
