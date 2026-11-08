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

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPinnedRevision(t *testing.T) {
	for in, want := range map[string]string{
		"v0.0.0-20261104205854-558da47fdb44":              "558da47fdb44",
		"v0.4.1-0.20261104205854-558da47fdb44":            "558da47fdb44",
		"v2.0.0-20261104205854-558da47fdb44+incompatible": "558da47fdb44",
		"v0.4.0":        "v0.4.0",
		"v0.4.0-rc.1":   "v0.4.0-rc.1",
		"v0.4.0-beta-x": "v0.4.0-beta-x",
		"":              "",
	} {
		if got := pinnedRevision(in); got != want {
			t.Errorf("pinnedRevision(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDefaultOutIsIgnored: without -out, fleetops-eval writes to the ignored
// directory make eval-fixtures uses, never to the committed self-test (D102);
// make record-selftest names the committed file itself.
func TestDefaultOutIsIgnored(t *testing.T) {
	if strings.HasPrefix(defaultOut, "evals/results/fixtures/") {
		t.Fatalf("defaultOut %q is the committed self-test", defaultOut)
	}
	root := filepath.Join("..", "..", "..")
	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	ignored := false
	for _, line := range strings.Split(string(gitignore), "\n") {
		dir := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "/"), "/")
		if dir != "" && !strings.ContainsAny(dir, "*!#") && strings.HasPrefix(defaultOut, dir+"/") {
			ignored = true
		}
	}
	if !ignored {
		t.Errorf("defaultOut %q is under no directory .gitignore ignores", defaultOut)
	}

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(makefile)
	selftest := regexp.MustCompile(`(?m)^SELFTEST_DIR \?= (\S+)$`).FindStringSubmatch(mk)
	if selftest == nil || selftest[1]+"/results.json" != defaultOut {
		t.Errorf("Makefile SELFTEST_DIR = %v, want the directory of %q", selftest, defaultOut)
	}
	for target, out := range map[string]string{
		"eval-fixtures":   "-out $(SELFTEST_DIR)/results.json",
		"record-selftest": "-out evals/results/fixtures/results.json",
	} {
		recipe := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(target) + `:.*?\n((?:\t.*\n)+)`).FindStringSubmatch(mk)
		if recipe == nil || !strings.Contains(recipe[1], "fleetops-eval ") || !strings.Contains(recipe[1], out) {
			t.Errorf("make %s does not run fleetops-eval with %s", target, out)
		}
	}
}
