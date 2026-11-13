// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hack/fetch-model.sh downloads each model file once into the cache, checks
// its sha256 before keeping it, and reuses a cached file that still matches.
func TestFetchModelOnceWithSha256(t *testing.T) {
	e := newEnv(t)
	// curl stub: writes "content of <file name>" to the -o path.
	curl := `#!/bin/sh
echo "curl $*" >> '` + e.calls + `'
out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in -o) out="$2"; shift ;; http*) url="$1" ;; esac
  shift
done
printf 'content of %s' "${url##*/}" > "$out"
`
	if err := os.WriteFile(filepath.Join(e.stubs, "curl"), []byte(curl), 0o700); err != nil { //nolint:gosec // G306: executable stub in t.TempDir()
		t.Fatal(err)
	}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	sums := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(sums, []byte(sum("content of a.gguf")+"  a.gguf\n"+sum("content of b.gguf")+"  b.gguf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	vars := map[string]string{"MODEL_SUMS": sums, "MODEL_BASE_URL": "https://models.example/rev"}
	countCurl := func() int { b, _ := os.ReadFile(e.calls); return strings.Count(string(b), "curl ") }

	out, err := e.runScript(t, "hack/fetch-model.sh", vars, cache)
	if err != nil || countCurl() != 2 {
		t.Fatalf("first run: err=%v, %d downloads\n%s", err, countCurl(), out)
	}
	for _, f := range []string{"a.gguf", "b.gguf"} {
		if got := readFile(t, filepath.Join(cache, f)); got != "content of "+f {
			t.Errorf("%s holds %q", f, got)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(cache, "*.part")); len(m) > 0 {
		t.Errorf("partial files left: %v", m)
	}

	out, err = e.runScript(t, "hack/fetch-model.sh", vars, cache)
	if err != nil || countCurl() != 2 || !strings.Contains(out, "sha256 OK") {
		t.Fatalf("second run downloaded again or did not check: err=%v, %d downloads\n%s", err, countCurl(), out)
	}

	// A cached file that no longer matches is downloaded again.
	if err := os.WriteFile(filepath.Join(cache, "b.gguf"), []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = e.runScript(t, "hack/fetch-model.sh", vars, cache)
	if err != nil || countCurl() != 3 || readFile(t, filepath.Join(cache, "b.gguf")) != "content of b.gguf" {
		t.Fatalf("a corrupted cached file was not replaced: err=%v, %d downloads\n%s", err, countCurl(), out)
	}

	// A download that does not match is refused and not kept.
	bad := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(bad, []byte(sum("something else")+"  c.gguf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = e.runScript(t, "hack/fetch-model.sh", map[string]string{"MODEL_SUMS": bad, "MODEL_BASE_URL": "https://models.example/rev"}, cache)
	if err == nil || !strings.Contains(out, "sha256") {
		t.Fatalf("a mismatching download was accepted: err=%v\n%s", err, out)
	}
	for _, f := range []string{"c.gguf", "c.gguf.part"} {
		if _, err := os.Stat(filepath.Join(cache, f)); err == nil {
			t.Errorf("%s was kept after a sha256 mismatch", f)
		}
	}
}
