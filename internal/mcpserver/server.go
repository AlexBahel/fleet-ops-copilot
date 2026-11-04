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
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// Meta keys the agent loop sets on every tools/call. The model cannot set
// them: they are request metadata, not tool arguments.
const (
	MetaSession  = "fleetops.alexbahel.github.io/session"
	MetaOperator = "fleetops.alexbahel.github.io/operator"
)

// Version is reported to MCP clients.
const Version = "0.0.1-foc0"

// ReadTools and RequestTools list the tool names, in registration order.
var (
	ReadTools = []string{
		"list_robots", "get_robot", "list_fleet_tasks", "get_fleet_task", "list_fleet_actions", "get_fleet_action",
		"list_zones", "get_zone", "list_zone_maintenances", "list_discovered_robots", "list_fleet_adapters",
		"list_task_triggers", "get_events", "get_request",
	}
	RequestTools = []string{"request_draft_task", "request_activate_task", "request_zone_maintenance", "request_zone_stop"}
)

func session(req *mcp.CallToolRequest) tools.Session {
	var s tools.Session
	if req == nil || req.Params == nil || req.Params.Meta == nil {
		return s
	}
	if v, ok := req.Params.Meta[MetaSession].(string); ok {
		s.ID = v
	}
	if v, ok := req.Params.Meta[MetaOperator].(string); ok {
		s.Operator = v
	}
	return s
}

func boolp(b bool) *bool { return &b }

func add[In, Out any](srv *mcp.Server, name, desc string, readOnly bool, fn func(context.Context, tools.Session, In) (Out, error)) {
	t := &mcp.Tool{
		Name:        name,
		Description: desc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: boolp(false), OpenWorldHint: boolp(false)},
	}
	mcp.AddTool(srv, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := fn(ctx, session(req), in)
		return nil, out, err
	})
}

// New builds an MCP server for a tools.Service.
func New(svc *tools.Service) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fleet-ops-copilot", Version: Version}, &mcp.ServerOptions{
		Instructions: "Tools for a Swarmada robot fleet. Read tools return Swarmada API fields by JSON path; cite them. " +
			"Write tools never change running work: request_draft_task creates a FleetTask held as a draft, and the other request_* tools " +
			"create an ApprovalRequest that a named person decides. There is no tool to clear a stop, release or resume a robot, delete, or approve.",
	})
	add(srv, "list_robots", "List Robots with phase, estop state, health, battery, zone and capabilities.", true, svc.ListRobots)
	add(srv, "get_robot", "Get one Robot's spec and status.", true, svc.GetRobot)
	add(srv, "list_fleet_tasks", "List FleetTasks with draft flag, phase and members.", true, svc.ListFleetTasks)
	add(srv, "get_fleet_task", "Get one FleetTask, including each member's projected phase and assigned robot.", true, svc.GetFleetTask)
	add(srv, "list_fleet_actions", "List FleetActions with phase, assigned robot and failure reason.", true, svc.ListFleetActions)
	add(srv, "get_fleet_action", "Get one FleetAction.", true, svc.GetFleetAction)
	add(srv, "list_zones", "List FleetZones with estop status and robot count.", true, svc.ListZones)
	add(srv, "get_zone", "Get one FleetZone.", true, svc.GetZone)
	add(srv, "list_zone_maintenances", "List ZoneMaintenance windows with phase and paused robots.", true, svc.ListZoneMaintenances)
	add(srv, "list_discovered_robots", "List DiscoveredRobots awaiting admission.", true, svc.ListDiscoveredRobots)
	add(srv, "list_fleet_adapters", "List FleetAdapters with phase, conformance and heartbeat.", true, svc.ListFleetAdapters)
	add(srv, "list_task_triggers", "List TaskTriggers (RFC-0003) and the drafts they produced.", true, svc.ListTaskTriggers)
	add(srv, "get_events", "Get Kubernetes Events for one object.", true, svc.GetEvents)
	add(srv, "get_request", "Get one ApprovalRequest and its state.", true, svc.GetRequest)

	add(srv, "request_draft_task", "Create a FleetTask held as a draft (spec.draft: true). Nothing is dispatched; a person must activate it.", false, svc.RequestDraftTask)
	add(srv, "request_activate_task", "Ask a named person to activate a draft FleetTask. Creates an ApprovalRequest only.", false, svc.RequestActivateTask)
	add(srv, "request_zone_maintenance", "Ask a named person to approve a ZoneMaintenance window (autoResumeAfterMinutes is always 0). Creates an ApprovalRequest only.", false, svc.RequestZoneMaintenance)
	add(srv, "request_zone_stop", "Ask a named person to approve a zone emergency stop. Creates an ApprovalRequest only. Not an emergency path.", false, svc.RequestZoneStop)
	return srv
}

// Check verifies that the registered tool names are exactly ReadTools+RequestTools.
func Check(ctx context.Context, srv *mcp.Server) error {
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return err
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "check"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return err
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return err
	}
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
