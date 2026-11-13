// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

package scripts

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const staleNote = "could not be continued"

// fakeAgent is fleetops-agent's /api/chat as far as a kept conversation goes:
// it continues only an id it issued, and a new demo (a new fleetops-agent)
// has issued none.
type fakeAgent struct {
	mu     sync.Mutex
	issued map[string]bool
	n      int
	sent   []string // the conversationId of each request
}

func (a *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConversationID string `json:"conversationId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, in.ConversationID)
	id, notice := in.ConversationID, ""
	if id != "" && !a.issued[id] {
		id, notice = "", "That conversation could not be continued (not yours, or expired); this is a new one."
	}
	if id == "" {
		a.n++
		id = fmt.Sprintf("conv%022d", a.n)
		a.issued[id] = true
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"sessionId": "web-1234567890abcdef", "conversationId": id, "notice": notice, "answer": "ok"})
}

func (a *fakeAgent) newDemo() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.issued = map[string]bool{}
}

func (a *fakeAgent) lastSent() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sent[len(a.sent)-1]
}

// stateFiles lists every file under the CLI's state directory, relative to it.
func stateFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

// make demo-down forgets the conversations fleetops chat kept for the demo's
// contexts (sam, dana and eve @<cluster>, and kind-<cluster>), so the first
// question on the next demo starts a new conversation, without the "could not
// be continued" note. Nothing else under the CLI's state directory changes.
func TestDemoDownForgetsTheDemoConversations(t *testing.T) {
	e := newEnv(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "fleetops")
	build := exec.Command("go", "build", "-o", bin, "./cmd/fleetops") //nolint:gosec // G204: fixed package, output in t.TempDir()
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	const cluster = "fleetops-test-1"
	demoContexts := []string{"sam@" + cluster, "dana@" + cluster, "eve@" + cluster, "kind-" + cluster}
	other := "sam@fleetops-other"
	var ctxs strings.Builder
	for _, c := range append(slices.Clone(demoContexts), other) {
		fmt.Fprintf(&ctxs, "  - {name: %q, context: {cluster: c, user: u}}\n", c)
	}
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kc, []byte("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: \"https://127.0.0.1:6443\"}}]\nusers: [{name: u, user: {token: person-token-1}}]\ncontexts:\n"+ctxs.String()+"current-context: "+other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	agent := &fakeAgent{issued: map[string]bool{}}
	ts := httptest.NewServer(agent)
	defer ts.Close()
	chat := func(kctx string) string {
		t.Helper()
		cmd := exec.Command(bin, "chat", "-agent", ts.URL, "--context", kctx, "Show me all robots") //nolint:gosec // G204: the binary built above
		cmd.Env = []string{"HOME=" + e.home, "KUBECONFIG=" + kc, "PATH=/usr/bin:/bin"}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("fleetops chat --context %s: %v\n%s", kctx, err, stderr.String())
		}
		return stderr.String()
	}
	state := filepath.Join(e.home, ".local", "state", "fleetops")

	// Kept and not the demo's: another context's conversation and other files.
	chat(other)
	for _, f := range []string{"other-file", filepath.Join("conversations", "0123456789abcdef0123456789abcdef")} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(state, f)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, f), []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := stateFiles(t, state)

	for _, c := range demoContexts {
		chat(c)
	}
	// Control: on the same demo the conversation continues.
	if note := chat(demoContexts[0]); strings.Contains(note, staleNote) || agent.lastSent() == "" {
		t.Fatalf("the conversation did not continue on the same demo: sent %q, stderr %q", agent.lastSent(), note)
	}

	out, err := e.runScript(t, "hack/demo-down.sh", map[string]string{
		"CLUSTER": cluster, "KUBECONFIG": e.dedicated(t), "DEMO_DIR": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("demo-down: %v\n%s", err, out)
	}
	if got := stateFiles(t, state); !slices.Equal(got, keep) {
		t.Errorf("after demo-down the CLI's state is\n  %q\nwant only what is not the demo's\n  %q", got, keep)
	}

	agent.newDemo()
	for _, c := range demoContexts {
		if note := chat(c); strings.Contains(note, staleNote) || agent.lastSent() != "" {
			t.Errorf("--context %s on the next demo sent conversation %q; stderr %q", c, agent.lastSent(), note)
		}
	}
}
