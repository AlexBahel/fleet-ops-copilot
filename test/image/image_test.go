//go:build image

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

// Package image checks the runtime image (D83): whatever the umask of the
// machine that built the binaries, each binary in the image runs as the
// image's non-root user, and the files it reads under /etc/fleetops are
// readable by that user.
package image

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var binaries = []string{"fleetops-mcp", "fleetops-approvals", "fleetops-agent"}

func run(t *testing.T, dir string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err := cmd.Run()
	return b.String(), err
}

func TestBinariesRunAsNonRootWhateverTheUmask(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		if os.Getenv("FLEETOPS_IMAGE_TEST_REQUIRED") == "1" {
			t.Fatal("docker is required (FLEETOPS_IMAGE_TEST_REQUIRED=1)")
		}
		t.Skip("docker not found")
	}
	if out, err := run(t, ".", "docker", "info"); err != nil {
		if os.Getenv("FLEETOPS_IMAGE_TEST_REQUIRED") == "1" {
			t.Fatalf("docker daemon: %v\n%s", err, out)
		}
		t.Skip("docker daemon not reachable")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	arch := runtime.GOARCH
	out := filepath.Join(repo, "bin", "linux-"+arch)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	// Build under umask 077: without --chmod the files would be 0700 and
	// owned by the builder, so user 65532 could not run them.
	build := "umask 077 && CGO_ENABLED=0 GOOS=linux GOARCH=" + arch + " go build -o " + out + "/ ./cmd/fleetops-mcp ./cmd/fleetops-approvals ./cmd/fleetops-agent"
	if o, err := run(t, repo, "sh", "-c", build); err != nil {
		t.Fatalf("build: %v\n%s", err, o)
	}
	for _, b := range binaries {
		fi, err := os.Stat(filepath.Join(out, b))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s built with mode %v: the umask did not apply, the test proves nothing", b, fi.Mode())
		}
	}
	tag := "fleet-ops-copilot:image-test"
	if o, err := run(t, repo, "docker", "build", "--platform", "linux/"+arch, "-t", tag, "."); err != nil {
		t.Fatalf("docker build: %v\n%s", err, o)
	}
	t.Cleanup(func() { _, _ = run(t, repo, "docker", "rmi", "-f", tag) })
	ls, err := run(t, repo, "docker", "run", "--rm", "--entrypoint", "/busybox/sh", tag, "-c", "ls -ln /usr/local/bin")
	if err != nil {
		t.Fatalf("ls: %v\n%s", err, ls)
	}
	for _, b := range binaries {
		ok := false
		for _, l := range strings.Split(ls, "\n") {
			if strings.HasSuffix(strings.TrimSpace(l), " "+b) && strings.HasPrefix(l, "-rwxr-xr-x") {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s is not -rwxr-xr-x in the image:\n%s", b, ls)
		}
		// Run it as the image's user. -h prints usage; what matters is that
		// it executes (exit 126 is "permission denied").
		o, err := run(t, repo, "docker", "run", "--rm", "--user", "65532:65532", "--entrypoint", "/usr/local/bin/"+b, tag, "-h")
		if ee, isExit := err.(*exec.ExitError); (isExit && (ee.ExitCode() == 126 || ee.ExitCode() == 127)) || strings.Contains(strings.ToLower(o), "permission denied") {
			t.Errorf("%s does not run as 65532: %v\n%s", b, err, o)
		}
		if !strings.Contains(strings.ToLower(o), "usage") {
			t.Errorf("%s -h printed no usage (did it run?):\n%s", b, o)
		}
	}
	// The image's own policy, and files mounted under /etc/fleetops as the
	// chart mounts its ConfigMaps (policy, catalogue), are readable by user
	// 65532: COPY --chmod also sets the parent directories' mode.
	if o, err := run(t, repo, "docker", "run", "--rm", "--user", "65532:65532", "--entrypoint", "/busybox/cat", tag, "/etc/fleetops/policies/default.yaml"); err != nil {
		t.Errorf("the image's policy is not readable as 65532: %v\n%s", err, o)
	}
	mnt := t.TempDir()
	if err := os.Chmod(mnt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "default.yaml"), []byte("x: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{"/etc/fleetops/policy", "/etc/fleetops/catalogue"} {
		o, err := run(t, repo, "docker", "run", "--rm", "--user", "65532:65532", "-v", mnt+":"+at+":ro", "--entrypoint", "/busybox/cat", tag, at+"/default.yaml")
		if err != nil {
			t.Errorf("a file mounted at %s is not readable as 65532: %v\n%s", at, err, o)
		}
	}
}
