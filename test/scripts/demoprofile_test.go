// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dryRun runs make demo's script in dry-run mode with the given extra variables.
func dryRun(t *testing.T, e env, vars map[string]string) (string, error) {
	t.Helper()
	all := map[string]string{"CLUSTER": "fleetops-test-1", "KUBECONFIG": e.dedicated(t), "DEMO_DIR": t.TempDir(), "DEMO_DRY_RUN": "1"}
	for k, v := range vars {
		all[k] = v
	}
	return e.runScript(t, "hack/demo-up.sh", all)
}

// D103: make demo deploys the air-gapped profile unless DEMO_MODEL=cloud is
// given: llama-server and its model files, the egress policy, the chart's
// llamacpp backend, and no API key Secret.
func TestDemoProfileDefaultIsAirgap(t *testing.T) {
	e := newEnv(t)
	out, err := dryRun(t, e, nil)
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	order := []string{
		"model profile: airgap",
		"hack/fetch-model.sh",
		"people tokens",
		"kind create cluster",
		"upgrade --install fleetops", "llm.profile=airgap",
		"load-llama-image KIND_CLUSTER=fleetops-test-1",
		"load-llama-model KIND_CLUSTER=fleetops-test-1",
		"deploy-airgap KIND_CLUSTER=fleetops-test-1",
		"dry run finished",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("airgap plan lacks %q after offset %d:\n%s", want, at, out)
		}
		at += i + len(want)
	}
	for _, not := range []string{"fleet-ops-copilot-claude --from-file", "llm.profile=cloud", "ANTHROPIC_API_KEY="} {
		if strings.Contains(out, not) {
			t.Errorf("the default (air-gapped) plan contains %q:\n%s", not, out)
		}
	}
}

// The cloud profile only with DEMO_MODEL=cloud and a key file; a key file
// alone, cloud without a key file, or an unknown profile is refused before
// anything is created.
func TestDemoProfileCloudOnlyOnRequest(t *testing.T) {
	e := newEnv(t)
	good := writeKey(t, fakeKey+"\n")

	out, err := dryRun(t, e, map[string]string{"DEMO_MODEL": "cloud", "DEMO_ANTHROPIC_KEY_FILE": good})
	if err != nil {
		t.Fatalf("cloud dry run failed: %v\n%s", err, out)
	}
	for _, want := range []string{"model profile: cloud", "fleet-ops-copilot-claude", "llm.profile=cloud"} {
		if !strings.Contains(out, want) {
			t.Errorf("cloud plan lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"fetch-model.sh", "load-llama-model", "deploy-airgap", "llm.profile=airgap"} {
		if strings.Contains(out, not) {
			t.Errorf("cloud plan contains %q:\n%s", not, out)
		}
	}
	if strings.Contains(out, fakeKey) {
		t.Error("the key was printed")
	}

	// zsh leaves the ~ of make demo DEMO_ANTHROPIC_KEY_FILE=~/key.txt unexpanded.
	if err := os.WriteFile(filepath.Join(e.home, "key.txt"), []byte(fakeKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := dryRun(t, e, map[string]string{"DEMO_MODEL": "cloud", "DEMO_ANTHROPIC_KEY_FILE": "~/key.txt"}); err != nil || !strings.Contains(out, "looks like a Claude API key") {
		t.Errorf("a key file named ~/key.txt: err=%v\n%s", err, out)
	}

	for _, c := range []struct {
		name string
		vars map[string]string
		want string
	}{
		{"key file without DEMO_MODEL=cloud", map[string]string{"DEMO_ANTHROPIC_KEY_FILE": good}, "DEMO_MODEL=cloud"},
		{"key file with DEMO_MODEL=airgap", map[string]string{"DEMO_MODEL": "airgap", "DEMO_ANTHROPIC_KEY_FILE": good}, "DEMO_MODEL=cloud"},
		{"cloud without a key file", map[string]string{"DEMO_MODEL": "cloud"}, "DEMO_ANTHROPIC_KEY_FILE"},
		{"unknown profile", map[string]string{"DEMO_MODEL": "gpt"}, "airgap or cloud"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "demo")
			vars := map[string]string{"DEMO_DIR": dir}
			for k, v := range c.vars {
				vars[k] = v
			}
			out, err := dryRun(t, e, vars)
			if err == nil || !strings.Contains(out, c.want) {
				t.Fatalf("want a refusal naming %q, got err=%v\n%s", c.want, err, out)
			}
			if _, err := os.Stat(dir); err == nil {
				t.Error("a refused run created DEMO_DIR")
			}
		})
	}
}

// demoStubs adds the tools a real (not dry) make demo checks for, as stubs
// that record their calls; docker reports memTotal bytes for the Docker VM.
func demoStubs(t *testing.T, e env, memTotal string) {
	t.Helper()
	stubs := map[string]string{
		"docker":  "case \"$*\" in *MemTotal*) echo " + memTotal + ";; esac\nexit 0\n",
		"helm":    "exit 0\n",
		"jq":      "exit 0\n",
		"openssl": "exit 0\n",
		"python3": "exit 0\n",
	}
	for name, body := range stubs {
		s := "#!/bin/sh\necho \"" + name + " $*\" >> '" + e.calls + "'\n" + body
		if err := os.WriteFile(filepath.Join(e.stubs, name), []byte(s), 0o700); err != nil { //nolint:gosec // G306: executable stub in t.TempDir()
			t.Fatal(err)
		}
	}
}

// Alex's 401: a key file holding clipboard text. make demo refuses it before
// any command runs and before DEMO_DIR exists, saying what the file seems to
// hold, never the content.
func TestDemoUpRefusesABadKeyFileBeforeAnything(t *testing.T) {
	for _, c := range []struct{ name, content, want string }{
		{"clipboard text", "Meeting moved to 3pm, see the calendar invite for the room.", "spaces"},
		{"empty", "", "is empty"},
		{"multi-line", fakeKey + "\nmore\n", "has 2 lines"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			demoStubs(t, e, "23066771456")
			dir := filepath.Join(t.TempDir(), "demo")
			out, err := e.runScript(t, "hack/demo-up.sh", map[string]string{
				"CLUSTER": "fleetops-test-1", "KUBECONFIG": e.dedicated(t), "DEMO_DIR": dir,
				"DEMO_MODEL": "cloud", "DEMO_ANTHROPIC_KEY_FILE": writeKey(t, c.content),
			})
			if err == nil || !strings.Contains(out, "not a Claude API key") || !strings.Contains(out, c.want) || !strings.Contains(out, "Nothing was started") {
				t.Fatalf("want a refusal (%q) before anything, got err=%v\n%s", c.want, err, out)
			}
			if c.content != "" && strings.Contains(out, c.content[:10]) {
				t.Errorf("the output shows the file's content:\n%s", out)
			}
			if b, _ := os.ReadFile(e.calls); len(b) > 0 {
				t.Errorf("commands ran before the refusal:\n%s", b)
			}
			if _, err := os.Stat(dir); err == nil {
				t.Error("DEMO_DIR was created")
			}
		})
	}
}

// The air-gapped profile needs a Docker VM of at least 12 GiB; a smaller one
// is refused at the tools stage, before the model download and the cluster.
func TestDemoUpRefusesTooLittleDockerMemory(t *testing.T) {
	e := newEnv(t)
	demoStubs(t, e, "8589934592")
	out, err := e.runScript(t, "hack/demo-up.sh", map[string]string{
		"CLUSTER": "fleetops-test-1", "KUBECONFIG": e.dedicated(t), "DEMO_DIR": t.TempDir(),
	})
	if err == nil || !strings.Contains(out, "12 GiB") || !strings.Contains(out, "8.0 GiB") {
		t.Fatalf("want a refusal naming 12 GiB and the VM's 8.0 GiB, got err=%v\n%s", err, out)
	}
	calls := readFile(t, e.calls)
	for _, l := range strings.Split(strings.TrimSpace(calls), "\n") {
		if strings.HasPrefix(l, "kind ") || strings.HasPrefix(l, "curl ") {
			t.Errorf("ran %q before refusing", l)
		}
	}
}

// The summary names the model the stack runs: the air-gapped one with its
// record and no key, or Claude with the key Secret.
func TestDemoUpSummaryPerProfile(t *testing.T) {
	e := newEnv(t)
	summaryOf := func(vars map[string]string) string {
		out, err := dryRun(t, e, vars)
		if err != nil {
			t.Fatalf("dry run failed: %v\n%s", err, out)
		}
		i := strings.Index(out, "dry run finished")
		if i < 0 {
			t.Fatalf("no summary:\n%s", out)
		}
		return out[i:]
	}
	air := summaryOf(nil)
	for _, want := range []string{"Qwen2.5-7B-Instruct", "llama-server", "no API key", "26/150", "120/150", "DEMO_MODEL=cloud"} {
		if !strings.Contains(air, want) {
			t.Errorf("air-gapped summary lacks %q:\n%s", want, air)
		}
	}
	for _, not := range []string{"no API key Secret", "Claude API key"} {
		if strings.Contains(air, not) {
			t.Errorf("air-gapped summary mentions %q:\n%s", not, air)
		}
	}
	cloud := summaryOf(map[string]string{"DEMO_MODEL": "cloud", "DEMO_ANTHROPIC_KEY_FILE": writeKey(t, fakeKey)})
	for _, want := range []string{"Claude Sonnet 5.5", "Claude API", "fleet-ops-copilot-claude"} {
		if !strings.Contains(cloud, want) {
			t.Errorf("cloud summary lacks %q:\n%s", want, cloud)
		}
	}
	for _, not := range []string{"Qwen", "llama-server"} {
		if strings.Contains(cloud, not) {
			t.Errorf("cloud summary mentions %q:\n%s", not, cloud)
		}
	}
	for _, s := range []string{air, cloud} {
		for _, l := range strings.Split(s, "\n") {
			if strings.Contains(l, " #") || strings.HasPrefix(strings.TrimSpace(l), "#") {
				t.Errorf("summary line with a # comment: %s", l)
			}
		}
	}
}
