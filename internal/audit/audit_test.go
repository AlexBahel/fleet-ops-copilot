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

package audit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
)

func TestChainVerifies(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, redact.New())
	for i := 0; i < 5; i++ {
		if _, err := w.Append(Entry{Kind: KindToolCall, Tool: "list_robots"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := Verify(bytes.NewReader(buf.Bytes()))
	if err != nil || n != 5 {
		t.Fatalf("Verify = %d, %v", n, err)
	}
}

func TestTamperDetected(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, redact.New())
	for _, tool := range []string{"a", "b", "c"} {
		if _, err := w.Append(Entry{Kind: KindToolCall, Tool: tool}); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	cases := map[string]string{
		"edited":  strings.Join([]string{lines[0], strings.Replace(lines[1], `"tool":"b"`, `"tool":"x"`, 1), lines[2]}, "\n"),
		"removed": strings.Join([]string{lines[0], lines[2]}, "\n"),
		"swapped": strings.Join([]string{lines[1], lines[0], lines[2]}, "\n"),
	}
	for name, doc := range cases {
		if _, err := Verify(strings.NewReader(doc)); !errors.Is(err, ErrChain) {
			t.Errorf("%s: expected ErrChain, got %v", name, err)
		}
	}
}

func TestRedactedBeforeWrite(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, redact.New("very-secret-api-key"))
	if _, err := w.Append(Entry{Kind: KindToolCall, Message: "used very-secret-api-key", Detail: map[string]any{"k": "very-secret-api-key"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "very-secret-api-key") {
		t.Fatalf("secret written: %s", buf.String())
	}
	if _, err := Verify(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("redacted entry must still verify: %v", err)
	}
}

func TestOpenContinuesChainAndRefusesBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w, err := Open(path, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Entry{Kind: KindRequestCreated}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	w, err = Open(path, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	e, err := w.Append(Entry{Kind: KindApproved})
	if err != nil || e.Seq != 2 {
		t.Fatalf("continued seq = %d, %v", e.Seq, err)
	}
	w.Close()
	f, _ := os.Open(path)
	n, err := Verify(f)
	f.Close()
	if err != nil || n != 2 {
		t.Fatalf("Verify after reopen = %d, %v", n, err)
	}
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.Replace(b, []byte("approved"), []byte("rejected"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, redact.New()); err == nil {
		t.Fatal("Open accepted a tampered file")
	}
}
