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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "steps.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseFlagsDefaults(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel:4317")
	c, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.backend != "claude" || c.mcpURL != "http://fleetops-mcp:8080/mcp" || c.listen != ":8090" || c.otlp != "otel:4317" || c.maxTurns != 12 {
		t.Fatalf("%+v", c)
	}
	if _, err := parseFlags([]string{"-namespaces", " , "}); err == nil {
		t.Fatal("empty namespace list accepted")
	}
	if _, err := parseFlags([]string{"extra"}); err == nil {
		t.Fatal("stray argument accepted")
	}
	if got := splitList(" warehouse-a, warehouse-b ,"); strings.Join(got, "|") != "warehouse-a|warehouse-b" {
		t.Fatalf("%v", got)
	}
}

func TestBackendFactory(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(llm.EnvLlamaCppURL, "")
	if _, _, err := backendFactory(config{backend: "claude"}); err == nil {
		t.Fatal("claude without ANTHROPIC_API_KEY accepted")
	}
	if _, _, err := backendFactory(config{backend: "llamacpp"}); err == nil {
		t.Fatal("llamacpp without a URL accepted")
	}
	if _, _, err := backendFactory(config{backend: "gpt"}); err == nil {
		t.Fatal("unknown backend accepted")
	}

	t.Setenv("ANTHROPIC_API_KEY", "test-value-not-a-real-key-1234")
	t.Setenv("FLEETOPS_CLAUDE_MODEL", "claude-opus-5-5")
	_, name, err := backendFactory(config{backend: "claude", effort: "medium"})
	if err != nil || name != "claude:claude-opus-5-5" {
		t.Fatalf("%q %v", name, err)
	}
	if _, name, _ := backendFactory(config{backend: "claude", model: "claude-sonnet-5-5"}); name != "claude:claude-sonnet-5-5" {
		t.Fatalf("-model does not override FLEETOPS_CLAUDE_MODEL: %q", name)
	}
	_, name, err = backendFactory(config{backend: "llamacpp", llamaURL: "http://llama-server:8080"})
	if err != nil || name != "llamacpp:"+llm.DefaultLlamaCppModel {
		t.Fatalf("%q %v", name, err)
	}
}

// Each request gets a fresh scripted backend that replays from the start.
func TestScriptedBackendReplaysPerRequest(t *testing.T) {
	p := writeScript(t, `{"steps":[{"calls":[{"name":"list_zones"},{"name":"get_zone","args":{"name":"aisle-b"}}]},{"final":"FleetZone/aisle-b exists."}]}`)
	newBackend, name, err := backendFactory(config{backend: "scripted", script: p})
	if err != nil || name != "scripted" {
		t.Fatal(name, err)
	}
	for i := 0; i < 2; i++ {
		b, _ := newBackend()
		r, err := b.Chat(context.Background(), "", nil, nil)
		if err != nil || len(r.Calls) != 2 || r.Calls[0].ID != "call-1" || string(r.Calls[0].Args) != "{}" || r.Calls[1].Name != "get_zone" {
			t.Fatalf("request %d: %+v %v", i, r, err)
		}
		if r, _ := b.Chat(context.Background(), "", nil, nil); r.Text != "FleetZone/aisle-b exists." {
			t.Fatalf("final %+v", r)
		}
	}
}

func TestLoadScriptRefusesBadFiles(t *testing.T) {
	for name, body := range map[string]string{
		"no final":      `{"steps":[{"calls":[{"name":"list_zones"}]}]}`,
		"empty":         `{"steps":[]}`,
		"both":          `{"steps":[{"calls":[{"name":"x"}],"final":"y"},{"final":"z"}]}`,
		"neither":       `{"steps":[{},{"final":"z"}]}`,
		"no name":       `{"steps":[{"calls":[{"args":{}}]},{"final":"z"}]}`,
		"unknown field": `{"steps":[{"final":"z","approve":true}]}`,
		"not JSON":      `steps: []`,
	} {
		if _, err := loadScript(writeScript(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := loadScript(""); err == nil {
		t.Error("no -script accepted")
	}
}
