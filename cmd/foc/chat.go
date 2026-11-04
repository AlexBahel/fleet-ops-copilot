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
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// chat sends one message to foc-agent's /api/chat with the caller's own
// bearer token from their kubeconfig, so foc-mcp records the authenticated
// person as the requester (R39). A kubeconfig user with no bearer token (for
// example a client certificate) sends none, and any request is then
// unverified and can never be approved.
func chat(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	agent := fs.String("agent", "http://127.0.0.1:8090", "foc-agent URL (kubectl port-forward svc/foc-agent 8090:8090)")
	kctx := fs.String("context", "", "kubeconfig context whose user's token is sent (default: the current context)")
	operator := fs.String("operator", "", "name shown in the request if the token is not verified")
	session := fs.String("session", "", "session id to continue (printed by a previous foc chat)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	msg := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if msg == "" {
		return errors.New("usage: foc chat [-agent URL] [--context NAME] [-operator NAME] [-session ID] MESSAGE")
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
	body, _ := json.Marshal(map[string]string{"operator": *operator, "sessionId": *session, "message": msg})
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
		_, _ = fmt.Fprintln(stderr, "foc: this kubeconfig user has no bearer token (client certificate?); the request is unverified and any request it files can never be approved")
	}
	var out struct {
		SessionID string `json:"sessionId"`
		Answer    string `json:"answer"`
		Error     string `json:"error"`
		ToolCalls []struct {
			Name, PolicyCode, Created string
			Refused, IsError          bool
		} `json:"toolCalls"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return fmt.Errorf("foc-agent: HTTP %d: %w", res.StatusCode, err)
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
	if out.Error != "" {
		return fmt.Errorf("foc-agent: %s", out.Error)
	}
	_, _ = fmt.Fprintf(stdout, "%s\n(session %s)\n", out.Answer, out.SessionID)
	return nil
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
// Authorization (a basic-auth password) is removed, never sent to foc-agent.
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
