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

import "testing"

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
