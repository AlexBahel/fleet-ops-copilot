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

package redact

import (
	"strings"
	"testing"
)

func TestRedactsEnvValuesAndPatterns(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-value-not-a-real-key-1234")
	t.Setenv("FOC_EXTRA_SECRET", "another-secret-value")
	r := FromEnv("FOC_EXTRA_SECRET")
	in := "key=test-value-not-a-real-key-1234 extra=another-secret-value " +
		"auth=Bearer abc.def-ghi jwt=eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4eXoifQ.c2lnbmF0dXJl " +
		"anth=sk-ant-api03-ABCDEFGHIJKLMNOP"
	out := r.String(in)
	for _, leak := range []string{"test-value-not-a-real-key-1234", "another-secret-value", "abc.def-ghi", "eyJhbGci", "sk-ant-api03"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in %q", leak, out)
		}
	}
	if strings.Count(out, Mask) != 5 {
		t.Errorf("expected 5 masks, got %q", out)
	}
}

func TestShortValuesNotUsedAsLiterals(t *testing.T) {
	r := New("abc")
	if got := r.String("abc is fine"); got != "abc is fine" {
		t.Fatalf("short value masked: %q", got)
	}
}

func TestValueWalksStructures(t *testing.T) {
	r := New("super-secret-value")
	v := map[string]any{"a": []any{"x super-secret-value"}, "b": map[string]any{"c": "super-secret-value"}, "n": 3}
	out := r.Value(v).(map[string]any)
	if out["a"].([]any)[0] != "x "+Mask || out["b"].(map[string]any)["c"] != Mask || out["n"] != 3 {
		t.Fatalf("unexpected %v", out)
	}
}
