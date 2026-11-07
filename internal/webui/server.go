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

// Package webui serves fleetops-agent's web chat and its pending-requests panel
// (docs/DESIGN.md §3.9): embedded static files, no front-end build step.
//
// The UI does not approve. For each pending ApprovalRequest it shows the
// exact write and the `fleetops approve` / `fleetops reject` command a person runs
// with their own kubeconfig. fleetops-agent holds no permission to decide or to
// write anything (its ServiceAccount can only get and list ApprovalRequests).
//
// There is no login. fleetops-mcp decides the requester from the caller's bearer
// token (R39). The page sends none, so the name typed in it is recorded as
// requestedFor "unverified:<name>", and the policy refuses to approve any
// request with an unverified requester (R38): a request filed from the page
// can be rejected, or left to expire, but never approved. A client that sends
// "Authorization: Bearer <the person's token>" to /api/chat (fleetops chat) has it
// forwarded to fleetops-mcp, which verifies it with a TokenReview.
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
	"github.com/AlexBahel/fleet-ops-copilot/internal/authn"
	"github.com/AlexBahel/fleet-ops-copilot/internal/conversation"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/requestview"
)

//go:embed static
var staticFiles embed.FS

// Runner answers one operator request.
type Runner interface {
	// Run answers one message of a conversation.
	Run(ctx context.Context, in ChatInput) (ChatOutput, error)
	// Ready reports whether a request could be answered now (fleetops-mcp
	// reachable).
	Ready(ctx context.Context) error
	// Backend names the model backend, for the page header.
	Backend() string
}

// Server is the HTTP side of fleetops-agent.
type Server struct {
	Runner Runner
	// Requests reads ApprovalRequests with fleetops-agent's own, read-only
	// credentials.
	Requests   client.Reader
	Namespaces []string
	// MaxConcurrent chats; further ones get 429. 0 means 4.
	MaxConcurrent int
	// ChatTimeout bounds one request end to end. 0 means 10 minutes.
	ChatTimeout time.Duration
	Redactor    *redact.Redactor
	Log         *log.Logger

	sem chan struct{}
}

// Limits on what the page may send.
const (
	maxBody       = 64 << 10
	maxMessage    = 8000
	readyzTimeout = 3 * time.Second
)

var (
	operatorRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:+-]{0,127}$`)
	// Session ids become a label value on ApprovalRequests (fleet.SessionLabel).
	sessionRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{6,61}[a-z0-9])$`)
	requestRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
)

// Handler returns the HTTP handler: the page, its API, /healthz and /readyz.
func (s *Server) Handler() http.Handler {
	n := s.MaxConcurrent
	if n <= 0 {
		n = 4
	}
	s.sem = make(chan struct{}, n)
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // embedded at build time
	}
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/requests", s.requests)
	mux.HandleFunc("POST /api/chat", s.chat)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", s.readyz)
	return securityHeaders(mux)
}

// securityHeaders: the page loads only its own files, cannot be framed, and
// sends no referrer.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Print(s.Redactor.String(fmt.Sprintf(format, args...)))
	}
}

type infoResponse struct {
	Backend    string   `json:"backend"`
	Namespaces []string `json:"namespaces"`
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, infoResponse{Backend: s.Runner.Backend(), Namespaces: s.Namespaces})
}

type requestsResponse struct {
	Namespaces []string           `json:"namespaces"`
	Items      []requestview.View `json:"items"`
	Error      string             `json:"error,omitempty"`
}

func (s *Server) requests(w http.ResponseWriter, r *http.Request) {
	items, err := requestview.List(r.Context(), s.Requests, s.Namespaces)
	if err != nil {
		s.logf("listing ApprovalRequests: %v", err)
		writeJSON(w, http.StatusBadGateway, requestsResponse{Namespaces: s.Namespaces, Error: s.Redactor.String(err.Error())})
		return
	}
	if items == nil {
		items = []requestview.View{}
	}
	writeJSON(w, http.StatusOK, requestsResponse{Namespaces: s.Namespaces, Items: items})
}

type chatRequest struct {
	Operator       string `json:"operator"`
	SessionID      string `json:"sessionId"`
	ConversationID string `json:"conversationId"`
	FollowUp       string `json:"followUp"`
	Message        string `json:"message"`
}

// ToolCall is one tool call as the page lists it: no arguments or results,
// only what the policy said.
type ToolCall struct {
	Name       string `json:"name"`
	IsError    bool   `json:"isError"`
	Refused    bool   `json:"refused,omitempty"`
	PolicyCode string `json:"policyCode,omitempty"`
	Created    string `json:"created,omitempty"`
}

type chatResponse struct {
	SessionID string `json:"sessionId"`
	// ConversationID continues this conversation; the client keeps it (D75).
	ConversationID string `json:"conversationId,omitempty"`
	Notice         string `json:"notice,omitempty"`
	// Dialogue is what the message did in a guided request: a question, a
	// preview awaiting confirmation, a request filed, ...
	Dialogue *agent.DialogueEvent `json:"dialogue,omitempty"`
	// Requester is what any request filed by this message records as
	// requestedFor when no token was sent; with a token fleetops-mcp decides it.
	Requester string     `json:"requester,omitempty"`
	Answer    string     `json:"answer,omitempty"`
	Error     string     `json:"error,omitempty"`
	Turns     int        `json:"turns"`
	LatencyMs float64    `json:"latencyMs"`
	ToolCalls []ToolCall `json:"toolCalls"`
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	// A cross-site form cannot send application/json without a CORS
	// preflight, which this server never answers; Sec-Fetch-Site is checked
	// as well where the browser sends it.
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, chatResponse{Error: "Content-Type must be application/json"})
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeJSON(w, http.StatusForbidden, chatResponse{Error: "cross-site request refused"})
		return
	}
	var in chatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "invalid JSON body"})
		return
	}
	in.Operator, in.Message = strings.TrimSpace(in.Operator), strings.TrimSpace(in.Message)
	token := authn.BearerToken(r.Header.Get("Authorization"))
	switch {
	case (token == "" || in.Operator != "") && !operatorRE.MatchString(in.Operator):
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "operator: enter your Kubernetes username (letters, digits and . _ @ : + -, at most 128)"})
		return
	case (in.Message == "" && in.FollowUp == "") || len(in.Message) > maxMessage:
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "message: between 1 and 8000 characters"})
		return
	case in.SessionID != "" && !sessionRE.MatchString(in.SessionID):
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "sessionId: lower-case letters, digits and -, 8 to 63 characters"})
		return
	case in.ConversationID != "" && !conversation.IDPattern.MatchString(in.ConversationID):
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "conversationId: the id a previous answer returned"})
		return
	case in.FollowUp != "" && !requestRE.MatchString(in.FollowUp):
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "followUp: an ApprovalRequest name"})
		return
	}
	if in.SessionID == "" {
		in.SessionID = newSessionID()
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		writeJSON(w, http.StatusTooManyRequests, chatResponse{SessionID: in.SessionID, Error: "too many requests in progress; try again shortly"})
		return
	}
	timeout := s.ChatTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	// fleetops-mcp decides the requester from the token (R39). Without one, the
	// typed name is only asserted and every request it files is unverified.
	requester := ""
	if token == "" {
		requester = policy.UnverifiedPrefix + in.Operator
	}
	res, err := s.Runner.Run(ctx, ChatInput{Token: token, Operator: in.Operator, SessionID: in.SessionID, ConversationID: in.ConversationID, FollowUp: in.FollowUp, Message: in.Message})
	tr := res.Trace
	out := chatResponse{SessionID: in.SessionID, ConversationID: res.ConversationID, Notice: res.Notice, Dialogue: tr.Dialogue,
		Requester: requester, Answer: tr.Final, Turns: tr.Turns, LatencyMs: tr.LatencyMs, ToolCalls: toolCalls(tr)}
	s.logf("chat operator=%s bearer=%t session=%s turns=%d tools=%d latency_ms=%.0f err=%v", in.Operator, token != "", in.SessionID, tr.Turns, len(tr.Calls), tr.LatencyMs, err)
	if errors.Is(err, ErrBusy) {
		out.Error = err.Error()
		writeJSON(w, http.StatusConflict, out)
		return
	}
	if err != nil {
		out.Error = s.Redactor.String(err.Error())
		writeJSON(w, http.StatusBadGateway, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func toolCalls(tr agent.Trace) []ToolCall {
	out := make([]ToolCall, 0, len(tr.Calls))
	for _, c := range tr.Calls {
		tc := ToolCall{Name: c.Name, IsError: c.IsError}
		var res struct {
			Refused bool   `json:"refused"`
			Code    string `json:"policy.code"`
			Created string `json:"created"`
		}
		if len(c.Result) > 0 && json.Unmarshal(c.Result, &res) == nil {
			tc.Refused, tc.PolicyCode, tc.Created = res.Refused, res.Code, res.Created
		}
		out = append(out, tc)
	}
	return out
}

type check struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// readyz is ready when fleetops-mcp answers and ApprovalRequests can be read in
// every configured namespace.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
	defer cancel()
	checks := []check{{Name: "mcp"}, {Name: "approvalrequests"}}
	if err := s.Runner.Ready(ctx); err != nil {
		checks[0].Error = s.Redactor.String(err.Error())
	} else {
		checks[0].OK = true
	}
	if err := listOne(ctx, s.Requests, s.Namespaces); err != nil {
		checks[1].Error = s.Redactor.String(err.Error())
	} else {
		checks[1].OK = true
	}
	code := http.StatusOK
	for _, c := range checks {
		if !c.OK {
			code = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, code, map[string]any{"checks": checks})
}

func listOne(ctx context.Context, c client.Reader, namespaces []string) error {
	if len(namespaces) == 0 {
		return errors.New("no namespaces configured")
	}
	for _, ns := range namespaces {
		if err := c.List(ctx, &fleetopsv1.ApprovalRequestList{}, client.InNamespace(ns), client.Limit(1)); err != nil {
			return err
		}
	}
	return nil
}

func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "web-" + hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
