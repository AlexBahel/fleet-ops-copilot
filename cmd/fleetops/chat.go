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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"crypto/sha256"

	"k8s.io/client-go/tools/clientcmd"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// chat sends one message to fleetops-agent's /api/chat with the caller's own
// bearer token from their kubeconfig, so fleetops-mcp records the authenticated
// person as the requester (R39). A kubeconfig user with no bearer token (for
// example a client certificate) sends none, and any request is then
// unverified and can never be approved.
func chat(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	agent := fs.String("agent", "http://127.0.0.1:8090", "fleetops-agent URL (kubectl port-forward svc/fleetops-agent 8090:8090)")
	kctx := fs.String("context", "", "kubeconfig context whose user's token is sent (default: the current context)")
	operator := fs.String("operator", "", "name shown in the request if the token is not verified")
	session := fs.String("session", "", "session id to continue (printed by a previous fleetops chat)")
	newConv := fs.Bool("new", false, "start a new conversation instead of continuing the one kept for this context")
	followUp := fs.String("follow-up", "", "start a conversation at the preview of the follow-up fleetops-approvals prepared for this ApprovalRequest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	msg := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if msg == "" && *followUp == "" {
		return errors.New("usage: fleetops chat [-agent URL] [--context NAME] [-operator NAME] [-session ID] [-new] [-follow-up REQUEST] MESSAGE")
	}
	if err := tokenSafeURL(*agent); err != nil {
		return err
	}
	cfg, err := config.GetConfigWithContext(*kctx)
	if err != nil {
		return err
	}
	rt, sent, err := bearerTransport(cfg)
	if err != nil {
		return err
	}
	key := conversationKey(*kctx, *agent)
	conv := ""
	if !*newConv && *followUp == "" {
		conv = loadConversation(key)
	}
	body, _ := json.Marshal(map[string]string{"operator": *operator, "sessionId": *session, "conversationId": conv, "followUp": *followUp, "message": msg})
	ctx, cancel := context.WithTimeout(ctx, 11*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(*agent, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if !*sent {
		_, _ = fmt.Fprintln(stderr, "fleetops: this kubeconfig user has no bearer token (client certificate?); the request is unverified and any request it files can never be approved")
	}
	var out struct {
		SessionID      string `json:"sessionId"`
		ConversationID string `json:"conversationId"`
		Notice         string `json:"notice"`
		Answer         string `json:"answer"`
		Error          string `json:"error"`
		ToolCalls      []struct {
			Name, PolicyCode, Created string
			Refused, IsError          bool
		} `json:"toolCalls"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return fmt.Errorf("fleetops-agent: HTTP %d: %w", res.StatusCode, err)
	}
	for _, c := range out.ToolCalls {
		line := "  tool " + c.Name
		if c.PolicyCode != "" {
			line += " " + c.PolicyCode
		}
		if c.Created != "" {
			line += " -> " + c.Created
		}
		_, _ = fmt.Fprintln(stdout, line)
	}
	if out.ConversationID != "" {
		if err := saveConversation(key, out.ConversationID); err != nil {
			_, _ = fmt.Fprintf(stderr, "fleetops: could not keep the conversation id: %v\n", err)
		}
	}
	if out.Notice != "" {
		_, _ = fmt.Fprintln(stderr, "fleetops: "+out.Notice)
	}
	if out.Error != "" {
		return fmt.Errorf("fleetops-agent: %s", out.Error)
	}
	_, _ = fmt.Fprintf(stdout, "%s\n(session %s)\n", out.Answer, out.SessionID)
	return nil
}

// conversationKey names the kept conversation of a kubeconfig context and a
// fleetops-agent URL (D75): another context, or another fleetops-agent, starts its own.
func conversationKey(kctx, agent string) string {
	if kctx == "" {
		if raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), nil).RawConfig(); err == nil {
			kctx = raw.CurrentContext
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(kctx+"\x00"+agent)))[:32]
}

// conversationFile is under the user's state directory: $XDG_STATE_HOME/fleetops,
// else ~/.local/state/fleetops. The id is only useful to its owner (fleetops-agent
// checks the requester), and the file is mode 600.
func conversationFile(key string) (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "fleetops", "conversations", key), nil
}

func loadConversation(key string) string {
	p, err := conversationFile(key)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func saveConversation(key, id string) error {
	p, err := conversationFile(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(id+"\n"), 0o600)
}

// tokenSafeURL allows the person's token to go only over https or to a
// loopback address (a kubectl port-forward).
func tokenSafeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("-agent %q: not a URL", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("-agent %q: your token is sent only over https or to a loopback address (use kubectl port-forward)", raw)
}

// bearerTransport wraps the default transport with the kubeconfig user's
// bearer-token authentication (static token, token file, or exec plugin
// that returns a token) and reports whether a token was attached. Any other
// Authorization (a basic-auth password) is removed, never sent to fleetops-agent.
func bearerTransport(cfg *rest.Config) (http.RoundTripper, *bool, error) {
	sent := new(bool)
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*sent = strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ")
		if !*sent && r.Header.Get("Authorization") != "" {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	rt, err := rest.HTTPWrappersForConfig(cfg, inner)
	return rt, sent, err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
