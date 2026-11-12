// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

package scripts

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
)

// runScript runs a hack script with the stub directory first on PATH and only
// the given variables set.
func (e env) runScript(t *testing.T, script string, vars map[string]string, args ...string) (string, error) {
	t.Helper()
	path, err := filepath.Abs("../../" + script)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{path}, args...)...) //nolint:gosec // G204: fixed script paths
	cmd.Dir = filepath.Dir(e.home)
	cmd.Env = []string{"HOME=" + e.home, "PATH=" + e.stubs + ":/usr/bin:/bin"}
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var tokenLine = regexp.MustCompile(`^([0-9a-f]{48}),([a-z]+),([a-z]+),"([a-z-]+)"$`)

// D60: gen writes the kube-apiserver static token file, mode 600, one random
// token per person, and keeps an existing file.
func TestKindPeopleGen(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(t.TempDir(), "people")
	if out, err := e.runScript(t, "hack/kind-people.sh", nil, "gen", dir); err != nil {
		t.Fatalf("gen: %v\n%s", err, out)
	}
	file := filepath.Join(dir, "tokens.csv")
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("tokens.csv mode %v, want 0600", fi.Mode().Perm())
	}
	first := readFile(t, file)
	seen := map[string]bool{}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(first), "\n") {
		m := tokenLine.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("line %q is not token,user,uid,\"group\"", redactToken(l))
		}
		if m[2] != m[3] || seen[m[1]] {
			t.Errorf("uid must equal the user, and tokens must differ: %q", redactToken(l))
		}
		seen[m[1]] = true
		names = append(names, m[2])
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"dana", "eve", "sam"}) {
		t.Errorf("people %v, want dana eve sam", names)
	}
	if out, err := e.runScript(t, "hack/kind-people.sh", nil, "gen", dir); err != nil || !strings.Contains(out, "keeping it") {
		t.Fatalf("second gen: %v\n%s", err, out)
	}
	if readFile(t, file) != first {
		t.Error("a second gen replaced the token file")
	}
}

func redactToken(l string) string {
	if i := strings.Index(l, ","); i > 0 {
		return "<token>" + l[i:]
	}
	return l
}

// The people the script makes are the people envtest uses (same names, groups
// and roles), so a test written against one holds against the other.
func TestKindPeopleMatchEnvtest(t *testing.T) {
	src := readFile(t, "../../hack/kind-people.sh")
	block := src[strings.Index(src, "PEOPLE=(")+len("PEOPLE=("):]
	block = block[:strings.Index(block, "\n)")]
	got := map[string][2]string{}
	for _, l := range strings.Split(block, "\n") {
		l = strings.Trim(strings.TrimSpace(l), `"`)
		if l == "" {
			continue
		}
		f := strings.Split(l, "|")
		if len(f) != 3 {
			t.Fatalf("PEOPLE entry %q is not name|group|roles", l)
		}
		got[f[0]] = [2]string{f[1], f[2]}
	}
	if len(got) != len(testenv.DefaultPeople) {
		t.Fatalf("script has %d people, envtest has %d", len(got), len(testenv.DefaultPeople))
	}
	for _, p := range testenv.DefaultPeople {
		g, ok := got[p.Name]
		if !ok {
			t.Errorf("%s is not in hack/kind-people.sh", p.Name)
			continue
		}
		if g[0] != strings.Join(p.Groups, ",") || g[1] != strings.Join(p.Roles, ",") {
			t.Errorf("%s: script has group %q roles %q; envtest has %v %v", p.Name, g[0], g[1], p.Groups, p.Roles)
		}
	}
}

// kubectlStub answers the two reads kind-people.sh kubeconfig makes and
// records every call.
func (e env) kubectlStub(t *testing.T, server string) {
	t.Helper()
	body := "#!/bin/sh\necho \"kubectl $*\" >> '" + e.calls + "'\n" +
		"case \"$*\" in\n" +
		"  *certificate-authority-data*) printf '%s' '" + base64.StdEncoding.EncodeToString([]byte("fake-ca")) + "' ;;\n" +
		"  *.server*) printf '%s' '" + server + "' ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(e.stubs, "kubectl"), []byte(body), 0o700); err != nil { //nolint:gosec // G306: executable stub in t.TempDir()
		t.Fatal(err)
	}
}

// The kubeconfig for a person holds their token and nothing else secret; the
// token appears in no command line and no output.
func TestKindPeopleKubeconfig(t *testing.T) {
	e := newEnv(t)
	e.kubectlStub(t, "https://127.0.0.1:40123")
	dir := filepath.Join(t.TempDir(), "people")
	if out, err := e.runScript(t, "hack/kind-people.sh", nil, "gen", dir); err != nil {
		t.Fatalf("gen: %v\n%s", err, out)
	}
	tokens := filepath.Join(dir, "tokens.csv")
	var samToken string
	for _, l := range strings.Split(strings.TrimSpace(readFile(t, tokens)), "\n") {
		if m := tokenLine.FindStringSubmatch(l); m != nil && m[2] == "sam" {
			samToken = m[1]
		}
	}
	out := filepath.Join(t.TempDir(), "persons", "sam.kubeconfig")
	vars := map[string]string{"KUBECONFIG": e.dedicated(t)}
	stdout, err := e.runScript(t, "hack/kind-people.sh", vars, "kubeconfig", "kind-fleetops-test-1", "sam", tokens, out)
	if err != nil {
		t.Fatalf("kubeconfig: %v\n%s", err, stdout)
	}
	if strings.TrimSpace(stdout) != "sam@fleetops-test-1" {
		t.Errorf("printed %q, want the context name sam@fleetops-test-1", stdout)
	}
	if strings.Contains(stdout, samToken) {
		t.Error("the token was printed")
	}
	if calls, _ := os.ReadFile(e.calls); strings.Contains(string(calls), samToken) {
		t.Error("the token is on a command line")
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("kubeconfig mode %v, want 0600", fi.Mode().Perm())
	}
	var kc struct {
		Clusters []struct {
			Name    string
			Cluster struct {
				Server                   string
				CertificateAuthorityData string `json:"certificate-authority-data"`
			}
		}
		Users []struct {
			Name string
			User struct{ Token string }
		}
		Contexts []struct {
			Name    string
			Context struct{ Cluster, User string }
		}
	}
	if err := yaml.Unmarshal([]byte(readFile(t, out)), &kc); err != nil {
		t.Fatal(err)
	}
	if len(kc.Users) != 1 || kc.Users[0].Name != "sam" || kc.Users[0].User.Token != samToken {
		t.Errorf("users %+v: want one user, sam, with sam's token", redactedUsers(kc.Users))
	}
	if len(kc.Contexts) != 1 || kc.Contexts[0].Name != "sam@fleetops-test-1" || kc.Contexts[0].Context.User != "sam" || kc.Contexts[0].Context.Cluster != "fleetops-test-1" {
		t.Errorf("contexts %+v", kc.Contexts)
	}
	if len(kc.Clusters) != 1 || kc.Clusters[0].Cluster.Server != "https://127.0.0.1:40123" || kc.Clusters[0].Cluster.CertificateAuthorityData != base64.StdEncoding.EncodeToString([]byte("fake-ca")) {
		t.Errorf("clusters %+v", kc.Clusters)
	}
	// A person that is not in the token file gets nothing.
	if out, err := e.runScript(t, "hack/kind-people.sh", vars, "kubeconfig", "kind-fleetops-test-1", "mallory", tokens, filepath.Join(t.TempDir(), "m")); err == nil || !strings.Contains(out, "no token for 'mallory'") {
		t.Errorf("unknown person: err=%v\n%s", err, out)
	}
}

func redactedUsers(us []struct {
	Name string
	User struct{ Token string }
}) []string {
	var out []string
	for _, u := range us {
		out = append(out, u.Name)
	}
	return out
}

func TestKindPeopleRefuses(t *testing.T) {
	e := newEnv(t)
	e.kubectlStub(t, "https://127.0.0.1:1")
	dir := filepath.Join(t.TempDir(), "people")
	if _, err := e.runScript(t, "hack/kind-people.sh", nil, "gen", dir); err != nil {
		t.Fatal(err)
	}
	tokens := filepath.Join(dir, "tokens.csv")
	out := filepath.Join(t.TempDir(), "x")
	for _, c := range []struct {
		name, ctx, kubeconfig, want string
	}{
		{"context not fleetops", "kind-prod", e.dedicated(t), "CONTEXT must be kind-fleetops-*"},
		{"docker-desktop", "docker-desktop", e.dedicated(t), "CONTEXT must be kind-fleetops-*"},
		{"default kubeconfig", "kind-fleetops-test-1", filepath.Join(e.home, ".kube", "config"), "resolves to ~/.kube/config"},
		{"kubeconfig unset", "kind-fleetops-test-1", "", "KUBECONFIG must be set"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_ = os.Remove(e.calls)
			vars := map[string]string{}
			if c.kubeconfig != "" {
				vars["KUBECONFIG"] = c.kubeconfig
			}
			o, err := e.runScript(t, "hack/kind-people.sh", vars, "kubeconfig", c.ctx, "sam", tokens, out)
			if err == nil || !strings.Contains(o, c.want) {
				t.Fatalf("want a refusal containing %q, got err=%v\n%s", c.want, err, o)
			}
			if calls, _ := os.ReadFile(e.calls); len(calls) > 0 {
				t.Errorf("refused, but kubectl ran: %s", calls)
			}
			if _, err := os.Stat(out); err == nil {
				t.Error("a kubeconfig was written")
			}
		})
	}
}

// The kind configuration demo-up.sh renders mounts the token directory into the
// node and passes --token-auth-file to the API server, with no placeholder
// left.
func TestKindConfigTemplate(t *testing.T) {
	tmpl := readFile(t, "../../hack/kind/demo-cluster.yaml.tmpl")
	rendered := strings.ReplaceAll(tmpl, "@PEOPLE_DIR@", "/work/demo/people")
	if strings.Contains(rendered, "@") {
		t.Error("a placeholder is left after rendering")
	}
	var cfg struct {
		Kind  string
		Nodes []struct {
			Role        string
			ExtraMounts []struct {
				HostPath, ContainerPath string
				ReadOnly                bool
			}
		}
		KubeadmConfigPatches []string
	}
	if err := yaml.Unmarshal([]byte(rendered), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Kind != "Cluster" || len(cfg.Nodes) != 1 || cfg.Nodes[0].Role != "control-plane" {
		t.Fatalf("config %+v", cfg)
	}
	m := cfg.Nodes[0].ExtraMounts
	if len(m) != 1 || m[0].HostPath != "/work/demo/people" || !m[0].ReadOnly {
		t.Fatalf("extraMounts %+v: want the token directory, read-only", m)
	}
	if len(cfg.KubeadmConfigPatches) != 1 {
		t.Fatalf("%d kubeadm patches, want 1", len(cfg.KubeadmConfigPatches))
	}
	var patch struct {
		Kind      string
		APIServer struct {
			ExtraArgs    map[string]string
			ExtraVolumes []struct {
				HostPath, MountPath string
				ReadOnly            bool
			}
		} `json:"apiServer"`
	}
	if err := yaml.Unmarshal([]byte(cfg.KubeadmConfigPatches[0]), &patch); err != nil {
		t.Fatal(err)
	}
	if patch.Kind != "ClusterConfiguration" || patch.APIServer.ExtraArgs["token-auth-file"] != m[0].ContainerPath+"/tokens.csv" {
		t.Errorf("patch %+v: the API server must read %s/tokens.csv", patch, m[0].ContainerPath)
	}
	v := patch.APIServer.ExtraVolumes
	if len(v) != 1 || v[0].HostPath != m[0].ContainerPath || v[0].MountPath != m[0].ContainerPath || !v[0].ReadOnly {
		t.Errorf("extraVolumes %+v: the API server pod must mount the node's %s read-only", v, m[0].ContainerPath)
	}
}

// make demo with DEMO_DRY_RUN=1 prints the plan: the steps in order, the
// kubeconfig and context on every kubectl and helm line, Swarmada's webhook
// overlay, the chart, and creates the token file and kind configuration.
func TestDemoUpDryRunPlan(t *testing.T) {
	e := newEnv(t)
	work := t.TempDir()
	kc := e.dedicated(t)
	out, err := e.runScript(t, "hack/demo-up.sh", map[string]string{
		"CLUSTER": "fleetops-test-1", "KUBECONFIG": kc, "DEMO_DIR": work, "DEMO_DRY_RUN": "1", "DEMO_TRACING": "1",
	})
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	order := []string{
		"people tokens", "kind create cluster --name fleetops-test-1", "--config " + filepath.Join(work, "kind-config.yaml"),
		"kind-people.sh kubeconfig kind-fleetops-test-1 sam", "warehouse-quickstart/run.sh --scenario healthy-fleet",
		"apply -k " + filepath.Join(work, "swarmada", "config", "controlstream-tls"), "demo-preflight.sh",
		"hack/demo-sim-fleet.sh start",
		"make -C", "kind load docker-image fleet-ops-copilot:dev --name fleetops-test-1",
		"upgrade --install fleetops", "tracing.backend.enabled=true", "kind-people.sh bind kind-fleetops-test-1 warehouse-a",
		"rollout status deploy/fleetops-mcp deploy/fleetops-approvals", "dry run finished",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("plan lacks %q after offset %d; output:\n%s", want, at, out)
		}
		at += i + len(want)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "[dry-run] kubectl ") || strings.Contains(l, "[dry-run] helm ") {
			hasCtx := strings.Contains(l, "--context kind-fleetops-test-1 ") || strings.Contains(l, "--kube-context kind-fleetops-test-1 ")
			if !strings.Contains(l, "--kubeconfig "+kc) || !hasCtx {
				t.Errorf("a cluster command without the dedicated kubeconfig and explicit context: %s", l)
			}
		}
	}
	if strings.Contains(out, ".kube/config") {
		t.Errorf("the plan mentions ~/.kube/config:\n%s", out)
	}
	if fi, err := os.Stat(filepath.Join(work, "people", "tokens.csv")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("tokens.csv: %v %v", fi, err)
	}
	cfg := readFile(t, filepath.Join(work, "kind-config.yaml"))
	if !strings.Contains(cfg, "hostPath: "+filepath.Join(work, "people")) || strings.Contains(cfg, "@PEOPLE_DIR@") {
		t.Errorf("kind configuration not rendered with the token directory:\n%s", cfg)
	}
	if calls, _ := os.ReadFile(e.calls); len(calls) > 0 {
		t.Errorf("a dry run ran external commands: %s", calls)
	}
	if !strings.Contains(readFile(t, filepath.Join(work, "last-run.txt")), "dry-run=1") {
		t.Error("last-run.txt does not say dry-run=1")
	}
}

// The tokens, the kubeconfigs and the Swarmada copy never go in the work tree:
// demo-up.sh, demo-down.sh and kind-people gen refuse a directory inside the repository.
func TestTokenDirectoryMustBeOutsideRepo(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(repo, "local", "demo-test-should-not-exist")
	t.Cleanup(func() { _ = os.RemoveAll(inside) })
	e := newEnv(t)
	kc := e.dedicated(t)
	vars := map[string]string{"CLUSTER": "fleetops-test-1", "KUBECONFIG": kc, "DEMO_DIR": inside, "DEMO_DRY_RUN": "1"}
	for _, c := range []struct {
		name, script string
		args         []string
	}{
		{"demo-up", "hack/demo-up.sh", nil},
		{"demo-down", "hack/demo-down.sh", nil},
		{"kind-people gen", "hack/kind-people.sh", []string{"gen", filepath.Join(inside, "people")}},
		{"repository root", "hack/kind-people.sh", []string{"gen", repo}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := e.runScript(t, c.script, vars, c.args...)
			if err == nil || !strings.Contains(out, "inside the repository") {
				t.Fatalf("want a refusal naming the repository, got err=%v\n%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(inside, "people", "tokens.csv")); err == nil {
				t.Error("a token file was written inside the repository")
			}
		})
	}
	// A symlink out of the temp directory into the repository is the same directory.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	if out, err := e.runScript(t, "hack/kind-people.sh", nil, "gen", filepath.Join(link, "local", "x")); err == nil || !strings.Contains(out, "inside the repository") {
		t.Errorf("symlink into the repository: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(repo, "local", "x")); err == nil {
		t.Error("a refused run still created a directory inside the repository")
	}
	if _, err := os.Stat(inside); err == nil {
		t.Error("a refused run still created DEMO_DIR inside the repository")
	}
}

// Without DEMO_DIR the default is outside the repository.
func TestDemoUpDefaultDirectoryIsOutsideRepo(t *testing.T) {
	e := newEnv(t)
	out, err := e.runScript(t, "hack/demo-up.sh", map[string]string{"CLUSTER": "fleetops-test-1", "KUBECONFIG": e.dedicated(t), "DEMO_DRY_RUN": "1"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := filepath.Join(e.home, ".local", "state", "fleet-ops-copilot", "demo")
	if _, err := os.Stat(filepath.Join(want, "people", "tokens.csv")); err != nil {
		t.Errorf("tokens.csv not under %s: %v\n%s", want, err, out)
	}
}

// D82: a real make demo run stops at the tools stage, before any cluster
// command, when the Python the quickstart will use cannot import grpc.
func TestDemoUpStopsWhenQuickstartPythonLacksGrpc(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"docker", "helm", "jq", "openssl"} {
		stub := "#!/bin/sh\necho \"" + name + " $*\" >> '" + e.calls + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(e.stubs, name), []byte(stub), 0o700); err != nil { //nolint:gosec // G306: executable stub in t.TempDir()
			t.Fatal(err)
		}
	}
	py := "#!/bin/sh\necho \"python3 $*\" >> '" + e.calls + "'\necho \"ModuleNotFoundError: No module named 'grpc'\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(e.stubs, "python3"), []byte(py), 0o700); err != nil { //nolint:gosec // G306: executable stub in t.TempDir()
		t.Fatal(err)
	}
	out, err := e.runScript(t, "hack/demo-up.sh", map[string]string{"CLUSTER": "fleetops-test-1", "KUBECONFIG": e.dedicated(t), "DEMO_DIR": t.TempDir()})
	if err == nil || !strings.Contains(out, "cannot import grpc and google.protobuf") || !strings.Contains(out, "pip install grpcio protobuf") {
		t.Fatalf("want a stop naming the missing packages, got err=%v\n%s", err, out)
	}
	calls := readFile(t, e.calls)
	for _, l := range strings.Split(strings.TrimSpace(calls), "\n") {
		if !strings.HasPrefix(l, "python3 ") {
			t.Errorf("a command ran before the stop: %s", l)
		}
	}
}
