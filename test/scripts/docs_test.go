// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

package scripts

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	fence = regexp.MustCompile("^\\s*```\\s*([A-Za-z]*)")
	// quoted removes quoted strings, where # is not a comment.
	quoted = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	// comment is a # that starts a word: a shell comment.
	comment = regexp.MustCompile(`(^|\s)#`)
)

// An interactive zsh, the macOS default shell, does not treat # as a comment
// (its interactivecomments option is off), so a pasted line "make demo  # ..."
// passes "#" and the words after it as arguments. No shell block in a tracked
// Markdown file carries a comment; explanations go in the prose.
func TestDocsShellBlocksHaveNoComments(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "ls-files", "--", "*.md")
	git.Dir = root
	out, err := git.Output()
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatal("no tracked Markdown files")
	}
	for _, f := range files {
		fh, err := os.Open(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		in, shell := false, false
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if m := fence.FindStringSubmatch(line); m != nil {
				if !in {
					switch m[1] {
					case "", "bash", "sh", "zsh", "shell", "console":
						shell = true
					default:
						shell = false
					}
				}
				in = !in
				continue
			}
			if in && shell && comment.MatchString(quoted.ReplaceAllString(line, "")) {
				t.Errorf("%s:%d: a comment in a shell block: %s", f, n, line)
			}
		}
		_ = fh.Close()
		if err := sc.Err(); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
}
