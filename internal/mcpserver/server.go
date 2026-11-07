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

// Package mcpserver exposes the copilot's tools over MCP.
package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/AlexBahel/fleet-ops-copilot/internal/authn"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tracing"
)

// Meta keys the agent loop sets on every tools/call. The model cannot set
// them: they are request metadata, not tool arguments. MetaOperator is only
// the name the client asserts: the requester is decided from the caller's
// bearer token (internal/authn, R39), and the asserted name appears only in
// an unverified requester.
const (
	MetaSession  = "fleetops.alexbahel.github.io/session"
	MetaOperator = "fleetops.alexbahel.github.io/operator"
	// MetaConfirmedPreview carries the id of the preview the requester
	// confirmed. fleetops-agent sets it, never the model, and only on the call it
	// makes after the person's confirmation (D74).
	MetaConfirmedPreview = "fleetops.alexbahel.github.io/confirmedPreview"
)

// Version is reported to MCP clients.
const Version = "0.0.1-foc0"

// ReadTools and RequestTools list the tool names, in registration order.
var (
	ReadTools = []string{
		"list_robots", "get_robot", "list_fleet_tasks", "get_fleet_task", "list_fleet_actions", "get_fleet_action",
		"list_zones", "get_zone", "list_zone_maintenances", "list_discovered_robots", "list_fleet_adapters",
		"list_task_triggers", "get_events", "get_request", "check_task", "whoami",
	}
	RequestTools = []string{"request_draft_task", "request_activate_task", "request_zone_maintenance", "request_zone_stop"}
	// NotForModel are served to fleetops-agent but never offered to a model: the
	// request tools, which fleetops-agent calls itself after a confirmation
	// (D74), and whoami (D75).
	NotForModel = append(append([]string{}, RequestTools...), "whoami")
)

// ForModel reports whether a served tool may be offered to a model.
func ForModel(name string) bool {
	for _, n := range NotForModel {
		if n == name {
			return false
		}
	}
	return true
}

// session builds the tool session of one call: the session id from _meta,
// and the requester from the HTTP Authorization header (TokenReview through
// svc.Authn). The asserted operator name in _meta never becomes a verified
// requester. Transports without HTTP headers (stdio, in-memory) carry no
// token, so their requester is unverified.
func session(ctx context.Context, svc *tools.Service, req *mcp.CallToolRequest) tools.Session {
	var s tools.Session
	var asserted, token string
	if req != nil && req.Params != nil && req.Params.Meta != nil {
		if v, ok := req.Params.Meta[MetaSession].(string); ok {
			s.ID = v
		}
		if v, ok := req.Params.Meta[MetaOperator].(string); ok {
			asserted = v
		}
		if v, ok := req.Params.Meta[MetaConfirmedPreview].(string); ok {
			s.ConfirmedPreview = v
		}
	}
	if req != nil && req.Extra != nil && req.Extra.Header != nil {
		token = authn.BearerToken(req.Extra.Header.Get("Authorization"))
	}
	id := authn.Resolve(ctx, svc.Authn, token, asserted)
	s.Operator, s.Verified, s.Unverified = id.Requester, id.Verified, id.Reason
	return s
}

// Handler serves srv over streamable HTTP. Each request's headers reach the
// tool handlers, which is how the caller's bearer token arrives.
func Handler(srv *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
}

// ClientTransport connects to a streamable HTTP endpoint and, when token is
// set, sends it as "Authorization: Bearer <token>" on every request.
func ClientTransport(endpoint, token string) mcp.Transport {
	t := &mcp.StreamableClientTransport{Endpoint: endpoint}
	if token != "" {
		t.HTTPClient = &http.Client{Transport: bearer{token: token, next: http.DefaultTransport}}
	}
	return t
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

func boolp(b bool) *bool { return &b }

// NamespaceDescription is the description of every tool's namespace
// parameter (R46, v3): it names the namespace the tools use when the
// parameter is omitted and says that other namespaces are refused (requests
// by policy, DENY_NAMESPACE; reads by the API server, since fleetops-mcp holds no
// permission there). A model that does not know the namespace otherwise
// guesses one (FOC-2r pilot: "default").
func NamespaceDescription(svc *tools.Service) string {
	var allowed []string
	if svc.Engine != nil && svc.Engine.P != nil {
		allowed = svc.Engine.P.Namespaces
	}
	switch {
	case svc.DefaultNamespace == "":
		return "Kubernetes namespace. Omit it; other namespaces are refused."
	case len(allowed) > 1:
		return fmt.Sprintf("Kubernetes namespace. Omit it to use %q, the fleet's namespace. Only %s are served; other namespaces are refused.", svc.DefaultNamespace, quoteList(allowed))
	default:
		return fmt.Sprintf("Kubernetes namespace. Omit it: the fleet's namespace is %q, and other namespaces are refused.", svc.DefaultNamespace)
	}
}

func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(q, ", ")
}

// inputSchema is the schema the SDK would infer for In, with the namespace
// parameter described by NamespaceDescription.
func inputSchema[In any](nsDesc string) (*jsonschema.Schema, error) {
	s, err := jsonschema.For[In](nil)
	if err != nil {
		return nil, err
	}
	if p, ok := s.Properties["namespace"]; ok {
		p.Description = nsDesc
	}
	return s, nil
}

// add registers one tool. Each call runs in a server span that continues the
// caller's trace when _meta carries a W3C traceparent (internal/tracing).
func add[In, Out any](srv *mcp.Server, svc *tools.Service, tr trace.Tracer, name, desc string, readOnly bool, fn func(context.Context, tools.Session, In) (Out, error)) {
	schema, err := inputSchema[In](NamespaceDescription(svc))
	if err != nil {
		panic(fmt.Sprintf("tool %s: input schema: %v", name, err)) // a programming error, like mcp.AddTool's own
	}
	t := &mcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: schema,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: boolp(false), OpenWorldHint: boolp(false)},
	}
	mcp.AddTool(srv, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		if req != nil && req.Params != nil {
			ctx = tracing.ExtractMeta(ctx, req.Params.Meta)
		}
		ctx, span := tr.Start(ctx, tracing.SpanMCPTool, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(tracing.AttrToolName.String(name)))
		defer span.End()
		out, err := fn(ctx, session(ctx, svc, req), in)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		return nil, out, err
	})
}

// New builds an MCP server for a tools.Service.
func New(svc *tools.Service) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fleet-ops-copilot", Version: Version}, &mcp.ServerOptions{
		Instructions: "Tools for a Swarmada robot fleet. Read tools return Swarmada API fields by JSON path; cite them. " +
			"The request_* tools create an ApprovalRequest that a named person decides, and only for a preview the requester confirmed; " +
			"none changes running work. There is no tool to clear a stop, release or resume a robot, delete, or approve.",
	})
	tr := tracing.OrNoop(svc.Tracer)
	add(srv, svc, tr, "list_robots", "List Robots with phase, estop state, health, battery, zone and capabilities.", true, svc.ListRobots)
	add(srv, svc, tr, "get_robot", "Get one Robot's spec and status.", true, svc.GetRobot)
	add(srv, svc, tr, "list_fleet_tasks", "List FleetTasks with draft flag, phase and members.", true, svc.ListFleetTasks)
	add(srv, svc, tr, "get_fleet_task", "Get one FleetTask, including each member's projected phase and assigned robot.", true, svc.GetFleetTask)
	add(srv, svc, tr, "list_fleet_actions", "List FleetActions with phase, assigned robot and failure reason.", true, svc.ListFleetActions)
	add(srv, svc, tr, "get_fleet_action", "Get one FleetAction.", true, svc.GetFleetAction)
	add(srv, svc, tr, "list_zones", "List FleetZones with estop status and robot count.", true, svc.ListZones)
	add(srv, svc, tr, "get_zone", "Get one FleetZone.", true, svc.GetZone)
	add(srv, svc, tr, "list_zone_maintenances", "List ZoneMaintenance windows with phase and paused robots.", true, svc.ListZoneMaintenances)
	add(srv, svc, tr, "list_discovered_robots", "List DiscoveredRobots awaiting admission.", true, svc.ListDiscoveredRobots)
	add(srv, svc, tr, "list_fleet_adapters", "List FleetAdapters with phase, conformance and heartbeat.", true, svc.ListFleetAdapters)
	add(srv, svc, tr, "list_task_triggers", "List TaskTriggers (RFC-0003) and the drafts they produced.", true, svc.ListTaskTriggers)
	add(srv, svc, tr, "get_events", "Get Kubernetes Events for one object.", true, svc.GetEvents)
	add(srv, svc, tr, "get_request", "Get one ApprovalRequest and its state.", true, svc.GetRequest)
	add(srv, svc, tr, "check_task", "Check a request against current fleet state without filing it: an eligible robot now (Swarmada's candidate filters that a read can evaluate), zone state, location, dependencies, timing and policy. Returns 'likely feasible' or 'blocked', blockers, warnings, alternatives and what was not checked; never a guarantee.", true, svc.CheckTask)
	add(srv, svc, tr, "whoami", "The requester as fleetops-mcp authenticated this call. Used by fleetops-agent, not offered to a model.", true, svc.Whoami)

	add(srv, svc, tr, "request_draft_task", "Create a FleetTask held as a draft (spec.draft: true). Nothing is dispatched; a person must activate it.", false, svc.RequestDraftTask)
	add(srv, svc, tr, "request_activate_task", "Ask a named person to activate a draft FleetTask. Creates an ApprovalRequest only.", false, svc.RequestActivateTask)
	add(srv, svc, tr, "request_zone_maintenance", "Ask a named person to approve a ZoneMaintenance window (autoResumeAfterMinutes is always 0). Creates an ApprovalRequest only.", false, svc.RequestZoneMaintenance)
	add(srv, svc, tr, "request_zone_stop", "Ask a named person to approve a zone emergency stop. Creates an ApprovalRequest only. Not an emergency path.", false, svc.RequestZoneStop)
	return srv
}

// Check verifies that the registered tool names are exactly ReadTools+RequestTools.
func Check(ctx context.Context, srv *mcp.Server) error {
	tl, err := ListTools(ctx, srv)
	if err != nil {
		return err
	}
	res := &mcp.ListToolsResult{Tools: tl}
	want := map[string]bool{}
	for _, n := range append(append([]string{}, ReadTools...), RequestTools...) {
		want[n] = true
	}
	if len(res.Tools) != len(want) {
		return fmt.Errorf("server registers %d tools, want %d", len(res.Tools), len(want))
	}
	for _, t := range res.Tools {
		if !want[t.Name] {
			return fmt.Errorf("unexpected tool %q", t.Name)
		}
	}
	return nil
}

// ListTools returns the tools srv serves, as a client sees them.
func ListTools(ctx context.Context, srv *mcp.Server) ([]*mcp.Tool, error) {
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "check"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// ToolsDigest is the SHA-256 of the tools srv serves that a model is offered
// (name, description and input schema of each, sorted by name), together
// with extra tools the agent adds (propose_task): what the model is told
// besides the system prompt. Evaluation results record it (R46).
func ToolsDigest(ctx context.Context, srv *mcp.Server, extra ...*mcp.Tool) (string, error) {
	served, err := ListTools(ctx, srv)
	if err != nil {
		return "", err
	}
	var tl []*mcp.Tool
	for _, t := range served {
		if ForModel(t.Name) {
			tl = append(tl, t)
		}
	}
	tl = append(tl, extra...)
	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema any    `json:"inputSchema"`
	}
	es := make([]entry, 0, len(tl))
	for _, t := range tl {
		es = append(es, entry{t.Name, t.Description, t.InputSchema})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	b, err := json.Marshal(es)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
