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

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// simFleet sets up stubs for hack/foc-sim-fleet.sh: kubectl answers the
// reads it makes (secrets, phases) and runs port-forward until killed;
// python3 stands for sim_adapter.py and runs until killed. phase is what
// every Robot reports.
func simFleet(t *testing.T, phase string) (env, string, map[string]string) {
	t.Helper()
	e := newEnv(t)
	applied := filepath.Join(filepath.Dir(e.home), "applied.yaml")
	kubectl := `#!/bin/sh
echo "kubectl $*" >> '` + e.calls + `'
case "$*" in
  *" apply -f -"*) cat >> '` + applied + `' ;;
  *"port-forward service/swarmada-controlstream"*)
    echo "Forwarding from 127.0.0.1:19443 -> 9445"
    trap 'kill $! 2>/dev/null; exit 0' TERM
    sleep 300 & wait ;;
  *"get secret"*"-o jsonpath"*) printf 'cert-material' | base64 ;;
  *"get robot"*"status.phase"*) printf '` + phase + `' ;;
  *"get fleetadapter"*"status.phase"*) printf 'Connected' ;;
  *"get fleetadapter sim-fleet-adapter -o json"*) echo '{"apiVersion":"swarmada.io/v1","kind":"FleetAdapter","metadata":{"namespace":"warehouse-a"},"spec":{}}' ;;
esac
exit 0
`
	python := `#!/bin/sh
echo "python3 $*" >> '` + e.calls + `'
trap 'kill $! 2>/dev/null; exit 0' TERM
sleep 300 & wait
`
	jq := "#!/bin/sh\necho \"jq $*\" >> '" + e.calls + "'\ncat\n"
	for name, body := range map[string]string{"kubectl": kubectl, "python3": python, "jq": jq} {
		if err := os.WriteFile(filepath.Join(e.stubs, name), []byte(body), 0o700); err != nil { //nolint:gosec // G306: executable stub in t.TempDir()
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "swarmada", "adapters", "simulation"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "swarmada", "adapters", "simulation", "sim_adapter.py"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"CLUSTER": "foc-test-1", "KUBECONFIG": e.dedicated(t), "FOC_UP_DIR": work, "FOC_SIM_READY_TIMEOUT": "3"}
	return e, work, vars
}

type pidLine struct {
	pid     int
	pattern string
}

func pids(t *testing.T, pidfile string) []pidLine {
	t.Helper()
	var out []pidLine
	for _, l := range strings.Split(strings.TrimSpace(readFile(t, pidfile)), "\n") {
		p, pat, _ := strings.Cut(l, " ")
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("pidfile line %q", l)
		}
		out = append(out, pidLine{n, pat})
	}
	return out
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitDead(pid int) bool {
	for range 50 {
		if !alive(pid) {
			return true
		}
		// A killed child stays a zombie until its parent (gone with the
		// script) is reaped by init; ps shows no command for it.
		if out, _ := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output(); strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// R73: start keeps one adapter per robot and one port-forward running in the
// background, records their PIDs and logs under FOC_UP_DIR, creates the
// labelled RobotClass and the two extra FleetAdapters, and stop ends exactly
// those processes.
func TestSimFleetStartStop(t *testing.T) {
	e, work, vars := simFleet(t, "Idle")
	out, err := e.runScript(t, "hack/foc-sim-fleet.sh", vars, "start")
	if err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	pidfile := filepath.Join(work, "sim-fleet", "pids")
	ps := pids(t, pidfile)
	t.Cleanup(func() {
		for _, p := range ps {
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
		}
	})
	if len(ps) != 4 || ps[0].pattern != "port-forward service/swarmada-controlstream" {
		t.Fatalf("pidfile %+v", ps)
	}
	for _, p := range ps {
		if !alive(p.pid) {
			t.Errorf("PID %d (%s) is not running after start", p.pid, p.pattern)
		}
	}
	// The adapters start in the background: wait for all three to record.
	calls := readFile(t, e.calls)
	for i := 0; i < 50 && strings.Count(calls, "sim_adapter.py --endpoint") < 3; i++ {
		time.Sleep(100 * time.Millisecond)
		calls = readFile(t, e.calls)
	}
	for i, r := range []string{"sim-robot-001", "sim-robot-002", "sim-robot-003"} {
		n := strconv.Itoa(i + 1)
		want := "adapters/simulation/sim_adapter.py --endpoint 127.0.0.1:19443 --namespace warehouse-a --robot-id " + r
		if !strings.Contains(calls, want) || !strings.Contains(calls, filepath.Join(work, "sim-fleet", "tls-"+n, "tls.crt")) {
			t.Errorf("no adapter for %s with tls-%s:\n%s", r, n, calls)
		}
		if _, err := os.Stat(filepath.Join(work, "logs", "sim-adapter-"+r+".log")); err != nil {
			t.Errorf("no log for %s: %v", r, err)
		}
		fi, err := os.Stat(filepath.Join(work, "sim-fleet", "tls-"+n, "tls.key"))
		if err != nil || fi.Mode().Perm() != 0o600 || readFile(t, filepath.Join(work, "sim-fleet", "tls-"+n, "tls.key")) != "cert-material" {
			t.Errorf("tls-%s/tls.key: %v %v", n, fi, err)
		}
	}
	for _, want := range []string{
		"demo-ab-live/adapter-client-cert-2.yaml", "patch robot sim-robot-002 --type=json", `"value":"sim-fleet-adapter-2"`,
		"annotate robot sim-robot-003 swarmada.io/robot-id=sim-robot-003", "jq --arg n sim-fleet-adapter-3",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(calls), "\n") {
		if strings.HasPrefix(l, "kubectl ") && (!strings.Contains(l, "--kubeconfig "+vars["KUBECONFIG"]) || !strings.Contains(l, "--context kind-foc-test-1")) {
			t.Errorf("kubectl without the dedicated kubeconfig and context: %s", l)
		}
	}
	applied := readFile(t, filepath.Join(filepath.Dir(e.home), "applied.yaml"))
	if !strings.Contains(applied, "kind: RobotClass") || !strings.Contains(applied, "name: simbot-v1") ||
		!strings.Contains(applied, "fleetops.alexbahel.github.io/demo-workaround") || !strings.Contains(applied, "does not ship it") {
		t.Errorf("RobotClass not created as a labelled workaround:\n%s", applied)
	}
	if !strings.Contains(out, "every Robot Idle, every FleetAdapter Connected") {
		t.Errorf("start output:\n%s", out)
	}

	out, err = e.runScript(t, "hack/foc-sim-fleet.sh", vars, "stop")
	if err != nil || !strings.Contains(out, "stopped 4 process(es)") {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	for _, p := range ps {
		if !waitDead(p.pid) {
			t.Errorf("PID %d (%s) still running after stop", p.pid, p.pattern)
		}
	}
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Errorf("pidfile left behind: %v", err)
	}
	// stop again: nothing to do.
	if out, err := e.runScript(t, "hack/foc-sim-fleet.sh", vars, "stop"); err != nil || !strings.Contains(out, "not running") {
		t.Fatalf("second stop: %v\n%s", err, out)
	}
}

// R73: stop never kills a process that no longer runs what was recorded (a
// reused PID).
func TestSimFleetStopLeavesOtherProcessesAlone(t *testing.T) {
	e, work, vars := simFleet(t, "Idle")
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _, _ = other.Process.Wait() })
	if err := os.MkdirAll(filepath.Join(work, "sim-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	line := strconv.Itoa(other.Process.Pid) + " sim_adapter.py\n"
	if err := os.WriteFile(filepath.Join(work, "sim-fleet", "pids"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := e.runScript(t, "hack/foc-sim-fleet.sh", vars, "stop")
	if err != nil || !strings.Contains(out, "left alone") || !strings.Contains(out, "stopped 0 process(es)") {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	if !alive(other.Process.Pid) {
		t.Fatal("stop killed a process it did not start")
	}
}

// R73: a start whose fleet never becomes live fails, says what is missing,
// and leaves nothing running.
func TestSimFleetStartFailsAndCleansUp(t *testing.T) {
	e, work, vars := simFleet(t, "Offline")
	out, err := e.runScript(t, "hack/foc-sim-fleet.sh", vars, "start")
	if err == nil || !strings.Contains(out, "not live after 3s: Robot/sim-robot-001") {
		t.Fatalf("want a failure naming the Robots, got %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(work, "sim-fleet", "pids")); !os.IsNotExist(err) {
		t.Errorf("pidfile left after a failed start: %v", err)
	}
	var started []int
	out2, _ := exec.Command("pgrep", "-f", filepath.Join(work, "sim-fleet")).Output()
	for _, f := range strings.Fields(string(out2)) {
		n, _ := strconv.Atoi(f)
		started = append(started, n)
	}
	for _, p := range started {
		if !waitDead(p) {
			t.Errorf("PID %d still running after a failed start", p)
		}
	}
}
