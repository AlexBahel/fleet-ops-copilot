// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A made-up key of the documented shape (sk-ant-…, one line, no spaces). It is
// not a real key.
const fakeKey = "sk-ant-api03-" + "Zx9fakeKEYfortests0nly_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-AAAA"

func writeKey(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// hack/check-key-file.sh says whether a file holds a Claude API key alone and
// never prints what the file holds.
func TestKeyFileCheck(t *testing.T) {
	clipboard := "Hi Sam, the build is green. Notes from today are at https://example.com/notes and the next review is Friday."
	for _, c := range []struct {
		name, content string
		ok            bool
		want          string
	}{
		{"good key, no newline", fakeKey, true, "looks like a Claude API key"},
		{"good key, one trailing newline", fakeKey + "\n", true, "looks like a Claude API key"},
		{"empty", "", false, "is empty"},
		{"only a newline", "\n", false, "is empty"},
		{"multi-line: key and another line", fakeKey + "\nsecond line\n", false, "has 2 lines"},
		{"multi-line clipboard text", "line one\nline two\nline three\n", false, "has 3 lines"},
		{"clipboard text", clipboard, false, "spaces"},
		{"key inside clipboard text", "Your new key: " + fakeKey + " (keep it safe)", false, "spaces"},
		{"shell assignment", "ANTHROPIC_API_KEY=" + fakeKey, false, "NAME=value"},
		{"another kind of token", "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB", false, "does not start with sk-ant-"},
		{"web address", "https://console.example.com/settings/keys", false, "web address"},
		{"Windows line ending", fakeKey + "\r\n", false, "carriage return"},
		{"too short", "sk-ant-abc", false, "too short"},
		{"other characters", "sk-ant-api03-abc!def$ghi%jkl^mno&pqr*stu(vwx)yz0123456789", false, "characters"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			p := writeKey(t, c.content)
			out, err := e.runScript(t, "hack/check-key-file.sh", nil, p)
			if (err == nil) != c.ok || !strings.Contains(out, c.want) {
				t.Fatalf("ok=%v want %q; got err=%v\n%s", c.ok, c.want, err, out)
			}
			if !c.ok && !strings.Contains(out, "not a Claude API key") {
				t.Errorf("a refusal without the words 'not a Claude API key':\n%s", out)
			}
			// No part of the file is printed: no 8-character run of it appears in the
			// output, apart from the documented prefix sk-ant- that the rule names.
			body := strings.ReplaceAll(c.content, "sk-ant-", "\x00")
			for i := 0; i+8 <= len(body); i++ {
				if w := body[i : i+8]; !strings.ContainsAny(w, "\x00\n\r") && strings.Contains(out, w) {
					t.Errorf("the output shows part of the file (%q):\n%s", w, out)
				}
			}
		})
	}
	t.Run("no such file", func(t *testing.T) {
		e := newEnv(t)
		out, err := e.runScript(t, "hack/check-key-file.sh", nil, filepath.Join(t.TempDir(), "missing.txt"))
		if err == nil || !strings.Contains(out, "no such file") {
			t.Fatalf("got err=%v\n%s", err, out)
		}
	})
}
