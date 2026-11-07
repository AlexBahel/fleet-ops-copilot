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

package tools

import (
	"context"
	"fmt"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/draft"
	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// CheckTaskInput is a request that would be filed; exactly one is set.
type CheckTaskInput struct {
	Namespace       string                `json:"namespace,omitempty"`
	DraftTask       *DraftTaskInput       `json:"draftTask,omitempty"`
	ActivateTask    *ActivateTaskInput    `json:"activateTask,omitempty"`
	ZoneMaintenance *ZoneMaintenanceInput `json:"zoneMaintenance,omitempty"`
	ZoneStop        *ZoneStopInput        `json:"zoneStop,omitempty"`
}

// PolicyResult is the policy engine's decision on the request.
type PolicyResult struct {
	Effect  string `json:"effect"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CheckTaskResult is feasibility.Report plus the policy decision.
type CheckTaskResult struct {
	feasibility.Report
	Policy PolicyResult `json:"policy"`
}

// CheckTask evaluates a request against current fleet state without filing
// or writing anything (DESIGN.md §3.10.4). A policy refusal is a blocker.
func (s *Service) CheckTask(ctx context.Context, sess Session, in CheckTaskInput) (CheckTaskResult, error) {
	ns := s.ns(in.Namespace)
	s.readAudit(sess, "check_task", ns, "")
	var (
		prop feasibility.Proposal
		r    policy.Request
		snap policy.Snapshot
		err  error
	)
	set := 0
	switch {
	case in.DraftTask != nil:
		set++
		in.DraftTask.Namespace = ns
		spec := DraftSpec(*in.DraftTask)
		task, berr := draft.Task(ns, spec)
		if berr != nil {
			return CheckTaskResult{}, fmt.Errorf("draft task: %w", berr)
		}
		prop = feasibility.Proposal{Task: task, Creating: true}
		r = policy.Request{Op: policy.OpDraftTask, Namespace: ns, SessionID: sess.ID, DraftTask: &policy.DraftTaskParams{Name: spec.TaskName, Draft: true}}
		for _, a := range spec.Actions {
			r.DraftTask.Actions = append(r.DraftTask.Actions, policy.DraftAction{Name: a.Name, Type: a.Type, Priority: a.Priority, Zone: a.Zone})
		}
		if snap, err = fleet.TaskSnapshot(ctx, s.C, ns, spec.TaskName); err != nil {
			return CheckTaskResult{}, err
		}
		if snap.DraftGateServed, _, err = fleet.DraftGate(ctx, s.C); err != nil {
			return CheckTaskResult{}, err
		}
		if snap.DraftsThisSession, err = fleet.CountSessionDraftRequests(ctx, s.C, ns, sess.ID); err != nil {
			return CheckTaskResult{}, err
		}
	}
	if in.ActivateTask != nil {
		set++
		prop = feasibility.Proposal{ActivateTask: in.ActivateTask.TaskName}
		r = policy.Request{Op: policy.OpActivateTask, Namespace: ns, ActivateTask: &policy.ActivateTaskParams{TaskName: in.ActivateTask.TaskName}}
		if snap, err = fleet.TaskSnapshot(ctx, s.C, ns, in.ActivateTask.TaskName); err != nil {
			return CheckTaskResult{}, err
		}
		if snap.DraftGateServed, _, err = fleet.DraftGate(ctx, s.C); err != nil {
			return CheckTaskResult{}, err
		}
	}
	if in.ZoneMaintenance != nil {
		set++
		p, merr := maintenanceSpec(*in.ZoneMaintenance)
		if merr != nil {
			return CheckTaskResult{}, merr
		}
		prop = feasibility.Proposal{Maintenance: p}
		r = policy.Request{Op: policy.OpZoneMaintenance, Namespace: ns, ZoneMaintenance: &policy.ZoneMaintenanceParams{
			ScopeType: p.ScopeType, ZoneName: p.ZoneName, Mode: p.Mode, Reason: p.Reason,
			AutoResumeAfterMinutes: in.ZoneMaintenance.AutoResumeAfterMinutes, RequireEstopClearBeforeResume: in.ZoneMaintenance.RequireEstopClearBeforeResume,
		}}
		if snap, err = fleet.ZoneSnapshot(ctx, s.C, ns, p.ZoneName); err != nil {
			return CheckTaskResult{}, err
		}
	}
	if in.ZoneStop != nil {
		set++
		prop = feasibility.Proposal{ZoneStop: &fleetopsv1.ZoneStopParams{ZoneName: in.ZoneStop.ZoneName, Reason: in.ZoneStop.Reason}}
		r = policy.Request{Op: policy.OpZoneStop, Namespace: ns, ZoneStop: &policy.ZoneStopParams{ZoneName: in.ZoneStop.ZoneName, Reason: in.ZoneStop.Reason}}
		if snap, err = fleet.ZoneSnapshot(ctx, s.C, ns, in.ZoneStop.ZoneName); err != nil {
			return CheckTaskResult{}, err
		}
	}
	if set != 1 {
		return CheckTaskResult{}, fmt.Errorf("check_task needs exactly one of draftTask, activateTask, zoneMaintenance, zoneStop")
	}
	if !s.Engine.NamespaceAllowed(ns) {
		d := s.Engine.Decide(r, policy.Snapshot{})
		rep := feasibility.Report{Verdict: feasibility.Blocked, Statement: feasibility.Statement, Blockers: []feasibility.Finding{{Code: string(d.Code), Reason: d.Message}}}
		return CheckTaskResult{Report: rep, Policy: PolicyResult{string(d.Effect), string(d.Code), d.Message}}, nil
	}
	rep := feasibility.Evaluate(prop, feasibility.Load(ctx, s.C, ns, s.now()))
	d := s.Engine.Decide(r, snap)
	if d.Effect != policy.RequireApproval {
		rep.Blockers = append(rep.Blockers, feasibility.Finding{Code: string(d.Code), Reason: d.Message})
		rep.Verdict = feasibility.Blocked
	}
	s.record(audit.Entry{Kind: audit.KindToolCall, Actor: sess.Operator, SessionID: sess.ID, Tool: "check_task", Namespace: ns, Code: rep.Verdict})
	return CheckTaskResult{Report: rep, Policy: PolicyResult{string(d.Effect), string(d.Code), d.Message}}, nil
}

// WhoamiInput is empty.
type WhoamiInput struct{}

// WhoamiResult is the requester fleetops-mcp authenticated for this call.
type WhoamiResult struct {
	Requester string `json:"requester"`
	Verified  bool   `json:"verified"`
}

// Whoami returns the caller as fleetops-mcp verified it (TokenReview), so
// fleetops-agent can bind a conversation to it without a permission of its own
// (D75). It is not offered to a model.
func (s *Service) Whoami(_ context.Context, sess Session, _ WhoamiInput) (WhoamiResult, error) {
	return WhoamiResult{Requester: sess.Operator, Verified: sess.Verified}, nil
}
