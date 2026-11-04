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

// Package redact removes secret values from text before it is logged,
// traced, audited, or written to a Kubernetes Event.
package redact

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

// Mask replaces every redacted value.
const Mask = "[REDACTED]"

// DefaultSecretEnv names environment variables whose values are always masked.
var DefaultSecretEnv = []string{"ANTHROPIC_API_KEY", "FOC_LLM_API_KEY", "OTEL_EXPORTER_OTLP_HEADERS"}

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{8,}`),                                  // Anthropic API keys
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/\-]+=*`),                         // HTTP bearer tokens
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]+`), // JWTs (ServiceAccount, OIDC)
}

// Redactor masks known secret values and token patterns.
type Redactor struct {
	values []string
}

// FromEnv builds a Redactor from the values of the named environment
// variables plus DefaultSecretEnv. Values shorter than 8 characters are not
// used as literals, to avoid masking ordinary words.
func FromEnv(extra ...string) *Redactor {
	names := append(append([]string{}, DefaultSecretEnv...), extra...)
	var vals []string
	for _, n := range names {
		if v := os.Getenv(n); len(v) >= 8 {
			vals = append(vals, v)
		}
	}
	return New(vals...)
}

// New builds a Redactor for explicit secret values.
func New(values ...string) *Redactor {
	vs := make([]string, 0, len(values))
	for _, v := range values {
		if len(v) >= 8 {
			vs = append(vs, v)
		}
	}
	// Longest first, so a secret containing another is masked whole.
	sort.Slice(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
	return &Redactor{values: vs}
}

// String masks secrets in s.
func (r *Redactor) String(s string) string {
	if r != nil {
		for _, v := range r.values {
			s = strings.ReplaceAll(s, v, Mask)
		}
	}
	for _, p := range patterns {
		s = p.ReplaceAllString(s, Mask)
	}
	return s
}

// Value masks secrets in every string reachable in v (maps, slices, strings).
// Other values are returned unchanged.
func (r *Redactor) Value(v any) any {
	switch t := v.(type) {
	case string:
		return r.String(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = r.Value(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = r.Value(x)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, x := range t {
			out[i] = r.String(x)
		}
		return out
	default:
		return v
	}
}
