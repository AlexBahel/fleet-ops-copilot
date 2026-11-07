// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

// Package scripts tests the shell scripts under hack/ that act on a live kind
// cluster, without a cluster: every external command a script could run
// (kubectl, curl, make, go, swarmctl, kind) is replaced by a stub that only
// records its arguments. Each script must refuse a non-foc cluster name and the
// default kubeconfig (hack/lib/foc-guard.sh) before running anything.
package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var stubbed = []string{"kubectl", "curl", "make", "go", "swarmctl", "kind"}

// script is one guarded script: the variables it needs besides CLUSTER and
// KUBECONFIG, set so that an accepted run touches nothing outside the test, and
// a check of what an accepted run called.
type script struct {
	path   string
	vars   func(t *testing.T, e env) map[string]string
	accept func(t *testing.T, calls []string)
}

var scripts = []script{
	{
		path: "hack/live-scenarios.sh",
		vars: func(t *testing.T, e env) map[string]string {
			return map[string]string{
				"SWARMADA_DIR": t.TempDir(), "SWARMCTL": filepath.Join(e.stubs, "swarmctl"), "OUT": t.TempDir(),
				// No scenario uses live/comms-flaky or live/estop-drill, so a passing
				// run starts the port-forward and then has nothing to run.
				"FIXTURES": "comms-flaky estop-drill",
			}
		},
		accept: func(t *testing.T, calls []string) {
			requireContext(t, calls, true)
			forbid(t, calls, "make ", "go ")
		},
	},
	{
		path: "hack/capture-live-fixtures.sh",
		vars: func(t *testing.T, e env) map[string]string {
			// SCENARIOS of one space: no scenario, so only the capture tool is built.
			return map[string]string{"SWARMADA_DIR": t.TempDir(), "SWARMCTL": filepath.Join(e.stubs, "swarmctl"), "SCENARIOS": " "}
		},
		accept: func(t *testing.T, calls []string) {
			if len(calls) == 0 || !strings.HasPrefix(calls[0], "go build") {
				t.Errorf("want the capture tool built first; calls: %q", calls)
			}
			forbid(t, calls, "make ", "kubectl ", "swarmctl ")
		},
	},
	{
		path: "hack/live-approval-path.sh",
		vars: func(t *testing.T, e env) map[string]string {
			work := t.TempDir()
			// An existing approver kubeconfig skips hack/kind-people.sh kubeconfig.
			if err := os.WriteFile(filepath.Join(work, "dana.kubeconfig"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tokens := filepath.Join(work, "tokens.csv")
			if err := os.WriteFile(tokens, []byte("tok,dana,dana,\"fleet-ops\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return map[string]string{"PHASE": "no-webhooks", "WORK": work, "TOKENS_FILE": tokens}
		},
		accept: func(t *testing.T, calls []string) {
			requireContext(t, calls, true)
			if !slices.Contains(calls, "make test-live") {
				t.Errorf("make test-live did not run; calls: %q", calls)
			}
		},
	},
	{
		// A dry run: it prints the plan and runs no external command at all.
		path: "hack/foc-up.sh",
		vars: func(t *testing.T, e env) map[string]string {
			return map[string]string{"DEMO_DRY_RUN": "1", "DEMO_DIR": t.TempDir()}
		},
		accept: func(t *testing.T, calls []string) {
			if len(calls) != 0 {
				t.Errorf("a dry run ran external commands: %q", calls)
			}
		},
	},
	{
		path: "hack/foc-down.sh",
		vars: func(t *testing.T, e env) map[string]string {
			return map[string]string{"DEMO_DIR": t.TempDir()}
		},
		accept: func(t *testing.T, calls []string) {
			// No cluster of that name and no Swarmada copy: nothing to remove.
			forbid(t, calls, "kubectl ", "make ", "go ", "kind delete")
		},
	},
}

func requireContext(t *testing.T, calls []string, needKubectl bool) {
	t.Helper()
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "kubectl ") {
			n++
			if !strings.Contains(c, "--context kind-foc-test-1 ") {
				t.Errorf("kubectl call without the explicit context: %q", c)
			}
		}
	}
	if needKubectl && n == 0 {
		t.Errorf("no kubectl call; calls: %q", calls)
	}
}

func forbid(t *testing.T, calls []string, prefixes ...string) {
	t.Helper()
	for _, c := range calls {
		for _, p := range prefixes {
			if strings.HasPrefix(c, p) {
				t.Errorf("unexpected call %q", c)
			}
		}
	}
}

type env struct {
	home, stubs, calls string
}

func newEnv(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	e := env{home: filepath.Join(root, "home"), stubs: filepath.Join(root, "stubs"), calls: filepath.Join(root, "calls.log")}
	for _, d := range []string{filepath.Join(e.home, ".kube"), e.stubs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.home, ".kube", "config"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range stubbed {
		stub := "#!/bin/sh\necho \"" + name + " $*\" >> '" + e.calls + "'\n"
		if name == "curl" {
			// The scripts start `kubectl port-forward` in the background and then
			// poll it with curl. Waiting here until the kubectl call is recorded
			// keeps the script's exit trap from killing it unrecorded.
			stub += "for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do grep -q '^kubectl ' '" + e.calls + "' && break; sleep 0.1; done\n"
		}
		stub += "exit 0\n"
		// Stubs must be executable; they live in the test's temp dir.
		if err := os.WriteFile(filepath.Join(e.stubs, name), []byte(stub), 0o700); err != nil { //nolint:gosec // G306: executable stub in t.TempDir()
			t.Fatal(err)
		}
	}
	return e
}

func (e env) dedicated(t *testing.T) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(e.home), "foc-kubeconfig")
	if err := os.WriteFile(p, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// run executes the script with only the given variables set (plus HOME, a
// PATH whose first entry is the stub directory, and the script's own variables).
func (e env) run(t *testing.T, sc script, vars map[string]string, unset ...string) (string, []string, error) {
	t.Helper()
	path, err := filepath.Abs("../../" + sc.path)
	if err != nil {
		t.Fatal(err)
	}
	all := sc.vars(t, e)
	all["HOME"] = e.home
	all["PATH"] = e.stubs + ":/usr/bin:/bin"
	for k, v := range vars {
		all[k] = v
	}
	for _, k := range unset {
		delete(all, k)
	}
	cmd := exec.Command("bash", path) //nolint:gosec // G204: the script paths are fixed (the scripts table)
	cmd.Dir = filepath.Dir(e.home)
	for k, v := range all {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, runErr := cmd.CombinedOutput()
	var calls []string
	if b, err := os.ReadFile(e.calls); err == nil {
		calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return string(out), calls, runErr
}

func TestGuardRefuses(t *testing.T) {
	for _, sc := range scripts {
		t.Run(filepath.Base(sc.path), func(t *testing.T) { testRefuses(t, sc) })
	}
}

func testRefuses(t *testing.T, sc script) {
	e := newEnv(t)
	dedicated := e.dedicated(t)
	defaultCfg := filepath.Join(e.home, ".kube", "config")

	link := filepath.Join(filepath.Dir(e.home), "link-to-default")
	if err := os.Symlink(defaultCfg, link); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(filepath.Dir(e.home), "hardlink-to-default")
	if err := os.Link(defaultCfg, hard); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		vars  map[string]string
		unset []string
		want  string
	}{
		{"cluster unset", map[string]string{"KUBECONFIG": dedicated}, []string{"CLUSTER"}, "CLUSTER must match"},
		{"cluster empty", map[string]string{"CLUSTER": "", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
		{"cluster foc- alone", map[string]string{"CLUSTER": "foc-", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
		{"cluster without prefix", map[string]string{"CLUSTER": "quickstart", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
		{"cluster prefix later", map[string]string{"CLUSTER": "x-foc-a", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
		{"cluster upper case", map[string]string{"CLUSTER": "foc-A", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
		{"cluster underscore", map[string]string{"CLUSTER": "fleetops-a_b", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
		{"cluster space", map[string]string{"CLUSTER": "foc-a b", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
		{"cluster newline", map[string]string{"CLUSTER": "foc-a\nother", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
		{"kubeconfig unset", map[string]string{"CLUSTER": "foc-test"}, []string{"KUBECONFIG"}, "KUBECONFIG must be set"},
		{"kubeconfig empty", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": ""}, nil, "KUBECONFIG must be set"},
		{"kubeconfig only separators", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": "::"}, nil, "names no file"},
		{"kubeconfig default path", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": defaultCfg}, nil, "resolves to ~/.kube/config"},
		{"kubeconfig tilde", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": "~/.kube/config"}, nil, "resolves to ~/.kube/config"},
		{"kubeconfig relative", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": "home/.kube/config"}, nil, "resolves to ~/.kube/config"},
		{"kubeconfig dot segments", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": e.home + "/.kube/../.kube/config"}, nil, "resolves to ~/.kube/config"},
		{"kubeconfig symlink", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": link}, nil, "resolves to ~/.kube/config"},
		{"kubeconfig hard link", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": hard}, nil, "resolves to ~/.kube/config"},
		{"kubeconfig list with default", map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": dedicated + ":" + defaultCfg}, nil, "resolves to ~/.kube/config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_ = os.Remove(e.calls)
			out, calls, err := e.run(t, sc, c.vars, c.unset...)
			if err == nil {
				t.Fatalf("script accepted the input; output:\n%s", out)
			}
			if !strings.Contains(out, "refused") || !strings.Contains(out, c.want) {
				t.Errorf("output does not name the refusal %q:\n%s", c.want, out)
			}
			if len(calls) > 0 {
				t.Errorf("refused, but external commands ran first: %q", calls)
			}
		})
	}
}

func TestGuardRefusesDefaultKubeconfigNotYetCreated(t *testing.T) {
	for _, sc := range scripts {
		t.Run(filepath.Base(sc.path), func(t *testing.T) {
			e := newEnv(t)
			if err := os.Remove(filepath.Join(e.home, ".kube", "config")); err != nil {
				t.Fatal(err)
			}
			out, calls, err := e.run(t, sc, map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": filepath.Join(e.home, ".kube", "config")})
			if err == nil || !strings.Contains(out, "resolves to ~/.kube/config") {
				t.Fatalf("want refusal, got err=%v output:\n%s", err, out)
			}
			if len(calls) > 0 {
				t.Errorf("external commands ran: %q", calls)
			}
		})
	}
}

func TestGuardAcceptsDedicatedKubeconfig(t *testing.T) {
	for _, sc := range scripts {
		t.Run(filepath.Base(sc.path), func(t *testing.T) {
			e := newEnv(t)
			out, calls, err := e.run(t, sc, map[string]string{"CLUSTER": "foc-test-1", "KUBECONFIG": e.dedicated(t)})
			if err != nil {
				t.Fatalf("script refused a valid setup: %v\n%s", err, out)
			}
			sc.accept(t, calls)
		})
	}
}
