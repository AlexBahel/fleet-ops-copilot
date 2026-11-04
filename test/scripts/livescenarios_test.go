// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

// Package scripts tests the shell scripts under hack/ without a cluster: every
// external command the script could run (kubectl, curl, make, go, swarmctl, kind)
// is replaced by a stub that only records its arguments.
package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var stubbed = []string{"kubectl", "curl", "make", "go", "swarmctl", "kind"}

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
		stub := "#!/bin/sh\necho \"" + name + " $*\" >> '" + e.calls + "'\nexit 0\n"
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

// run executes hack/live-scenarios.sh with only the given variables set (plus
// HOME and a PATH whose first entry is the stub directory).
func (e env) run(t *testing.T, vars map[string]string, unset ...string) (string, []string, error) {
	t.Helper()
	script, err := filepath.Abs("../../hack/live-scenarios.sh")
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]string{
		"HOME":         e.home,
		"PATH":         e.stubs + ":/usr/bin:/bin",
		"SWARMADA_DIR": t.TempDir(),
		"SWARMCTL":     filepath.Join(e.stubs, "swarmctl"),
		"OUT":          t.TempDir(),
		// No scenario uses live/comms-flaky or live/estop-drill, so a passing
		// run starts the port-forward and then has nothing to run.
		"FIXTURES": "comms-flaky estop-drill",
	}
	for k, v := range vars {
		all[k] = v
	}
	for _, k := range unset {
		delete(all, k)
	}
	cmd := exec.Command("bash", script) //nolint:gosec // G204: the script path is fixed (hack/live-scenarios.sh)
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

func TestLiveScenariosRefuses(t *testing.T) {
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
		{"cluster underscore", map[string]string{"CLUSTER": "foc-a_b", "KUBECONFIG": dedicated}, nil, "CLUSTER must match"},
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
			out, calls, err := e.run(t, c.vars, c.unset...)
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

func TestLiveScenariosRefusesDefaultKubeconfigNotYetCreated(t *testing.T) {
	e := newEnv(t)
	if err := os.Remove(filepath.Join(e.home, ".kube", "config")); err != nil {
		t.Fatal(err)
	}
	out, calls, err := e.run(t, map[string]string{"CLUSTER": "foc-test", "KUBECONFIG": filepath.Join(e.home, ".kube", "config")})
	if err == nil || !strings.Contains(out, "resolves to ~/.kube/config") {
		t.Fatalf("want refusal, got err=%v output:\n%s", err, out)
	}
	if len(calls) > 0 {
		t.Errorf("external commands ran: %q", calls)
	}
}

func TestLiveScenariosAcceptsDedicatedKubeconfig(t *testing.T) {
	e := newEnv(t)
	out, calls, err := e.run(t, map[string]string{"CLUSTER": "foc-test-1", "KUBECONFIG": e.dedicated(t)})
	if err != nil {
		t.Fatalf("script refused a valid setup: %v\n%s", err, out)
	}
	var kubectl []string
	for _, c := range calls {
		if strings.HasPrefix(c, "kubectl ") {
			kubectl = append(kubectl, c)
		}
		if strings.HasPrefix(c, "make ") || strings.HasPrefix(c, "go ") {
			t.Errorf("no scenario uses these fixtures, but %q ran", c)
		}
	}
	if len(kubectl) == 0 {
		t.Fatalf("kubectl port-forward never started; calls: %q", calls)
	}
	for _, c := range kubectl {
		if !strings.Contains(c, "--context kind-foc-test-1 ") {
			t.Errorf("kubectl call without the explicit context: %q", c)
		}
	}
}
