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
	"strings"
	"testing"
)

func TestActivationBanner(t *testing.T) {
	const instr = `kubectl patch fleettask t1 -n warehouse-a --type merge -p '{"spec":{"draft":false}}'`
	t.Run("current context", func(t *testing.T) {
		got := activationBanner("https://127.0.0.1:6443", "warehouse-a", "", instr)
		for _, want := range []string{"API server: https://127.0.0.1:6443\n", "namespace:  warehouse-a\n", "  " + instr + "\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("banner lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "context:") {
			t.Errorf("no context given, banner names one:\n%s", got)
		}
		if strings.Index(got, "API server:") > strings.Index(got, "kubectl ") {
			t.Errorf("API server must come before the command:\n%s", got)
		}
	})
	t.Run("explicit context", func(t *testing.T) {
		got := activationBanner("https://127.0.0.1:6443", "warehouse-a", "kind-foc-x", instr)
		want := `  kubectl --context kind-foc-x patch fleettask t1 -n warehouse-a --type merge -p '{"spec":{"draft":false}}'` + "\n"
		if !strings.Contains(got, want) || !strings.Contains(got, "context:    kind-foc-x\n") {
			t.Errorf("banner:\n%s", got)
		}
	})
}
