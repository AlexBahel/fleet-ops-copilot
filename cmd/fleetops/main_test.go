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

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivationBanner(t *testing.T) {
	const instr = `kubectl patch fleettask t1 -n warehouse-a --type merge -p '{"spec":{"draft":false}}'`
	t.Run("current context", func(t *testing.T) {
		got := activationBanner("https://127.0.0.1:6443", "warehouse-a", "", instr)
		for _, want := range []string{"API server: https://127.0.0.1:6443\n", "namespace:  warehouse-a\n", "  " + instr + "\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("banner lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "context:") {
			t.Errorf("no context given, banner names one:\n%s", got)
		}
		if strings.Index(got, "API server:") > strings.Index(got, "kubectl ") {
			t.Errorf("API server must come before the command:\n%s", got)
		}
	})
	t.Run("fleetops activate with a context", func(t *testing.T) {
		got := activationBanner("https://127.0.0.1:6443", "warehouse-a", "kind-fleetops-x", "fleetops activate areq-1 -n warehouse-a")
		if !strings.Contains(got, "  fleetops activate areq-1 -n warehouse-a --context kind-fleetops-x\n") {
			t.Errorf("banner:\n%s", got)
		}
	})
	t.Run("explicit context", func(t *testing.T) {
		got := activationBanner("https://127.0.0.1:6443", "warehouse-a", "kind-fleetops-x", instr)
		want := `  kubectl --context kind-fleetops-x patch fleettask t1 -n warehouse-a --type merge -p '{"spec":{"draft":false}}'` + "\n"
		if !strings.Contains(got, want) || !strings.Contains(got, "context:    kind-fleetops-x\n") {
			t.Errorf("banner:\n%s", got)
		}
	})
}

func writeKubeconfig(t *testing.T, user string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "kubeconfig")
	kc := `apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "https://127.0.0.1:6443"}}]
users: [{name: u, user: ` + user + `}]
contexts: [{name: kind-fleetops-test, context: {cluster: c, user: u}}]
current-context: kind-fleetops-test
`
	if err := os.WriteFile(p, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// fleetops chat sends the kubeconfig user's bearer token to fleetops-agent (R39) and
// warns when there is none.
func TestChatSendsTheKubeconfigToken(t *testing.T) {
	var gotAuth, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"sessionId":"web-1234567890abcdef","answer":"Requested.","toolCalls":[{"name":"request_zone_stop","policyCode":"REQUIRE_APPROVAL","created":"ApprovalRequest/areq-1"}]}`))
	}))
	defer ts.Close()

	t.Setenv("KUBECONFIG", writeKubeconfig(t, `{token: person-token-1}`))
	var out, errOut bytes.Buffer
	if err := chat(context.Background(), []string{"-agent", ts.URL, "stop", "aisle-b"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer person-token-1" || !strings.Contains(gotBody, `"message":"stop aisle-b"`) || errOut.Len() != 0 {
		t.Fatalf("auth %q body %s stderr %q", gotAuth, gotBody, errOut.String())
	}
	if !strings.Contains(out.String(), "REQUIRE_APPROVAL -> ApprovalRequest/areq-1") || !strings.Contains(out.String(), "Requested.") {
		t.Fatalf("output %q", out.String())
	}

	t.Setenv("KUBECONFIG", writeKubeconfig(t, `{username: u, password: p}`))
	out.Reset()
	if err := chat(context.Background(), []string{"-agent", ts.URL, "-operator", "sam", "hi"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	// A basic-auth user's password is not sent; no token means unverified.
	if gotAuth != "" || !strings.Contains(errOut.String(), "no bearer token") {
		t.Fatalf("auth %q stderr %q", gotAuth, errOut.String())
	}
}

// D75: fleetops chat keeps the conversation id per context and sends it back;
// -new starts a fresh one.
func TestChatKeepsTheConversation(t *testing.T) {
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		_, _ = w.Write([]byte(`{"sessionId":"web-1234567890abcdef","conversationId":"abcdefghijklmnopqrstuvwxyz","answer":"ok"}`))
	}))
	defer ts.Close()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("KUBECONFIG", writeKubeconfig(t, `{token: person-token-1}`))
	var out, errOut bytes.Buffer
	for _, args := range [][]string{{"-agent", ts.URL, "inspect the dock"}, {"-agent", ts.URL, "1"}, {"-agent", ts.URL, "-new", "hello"}} {
		if err := chat(context.Background(), args, &out, &errOut); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(bodies[0], `"conversationId":"abc`) || !strings.Contains(bodies[1], `"conversationId":"abcdefghijklmnopqrstuvwxyz"`) || strings.Contains(bodies[2], `"conversationId":"abc`) {
		t.Fatalf("bodies %v", bodies)
	}
	files, _ := filepath.Glob(filepath.Join(state, "fleetops", "conversations", "*"))
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	if fi, err := os.Stat(files[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
}

func TestChatRefusesToSendTheTokenInClearToRemoteHosts(t *testing.T) {
	for raw, ok := range map[string]bool{
		"http://127.0.0.1:8090": true, "http://localhost:8090": true, "http://[::1]:8090": true,
		"https://fleetops.example": true, "http://fleetops-agent.fleetops-system:8090": false, "http://10.0.0.5:8090": false, "ftp://x": false,
	} {
		if err := tokenSafeURL(raw); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
}
