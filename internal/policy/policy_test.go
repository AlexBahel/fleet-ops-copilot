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

package policy

import (
	"strings"
	"testing"
)

func loadDefault(t *testing.T) *Engine {
	t.Helper()
	p, err := Load("../../policies/default.yaml")
	if err != nil {
		t.Fatalf("load default policy: %v", err)
	}
	return New(p)
}

func ptr[T any](v T) *T { return &v }

func goodDraft() *DraftTaskParams {
	return &DraftTaskParams{Name: "move-pallet", Draft: true, Actions: []DraftAction{{Name: "go", Type: "Navigate", Priority: "Normal", Zone: "warehouse-a"}}}
}

func goodMaint() *ZoneMaintenanceParams {
	return &ZoneMaintenanceParams{ScopeType: "Zone", ZoneName: "warehouse-a", Mode: "Graceful", Reason: "floor cleaning"}
}

func TestDecide(t *testing.T) {
	e := loadDefault(t)
	served := Snapshot{DraftGateServed: true, ZoneExists: true}
	cases := []struct {
		name string
		req  Request
		snap Snapshot
		want Effect
		code Code
	}{
		// Reads.
		{"read allowed", Request{Op: OpRead}, Snapshot{}, Allow, CodeRead},

		// Hard invariants: every forbidden operation is refused, whatever the snapshot.
		{"clear stop refused", Request{Op: OpClearStop, Namespace: "warehouse-a"}, served, Deny, CodeForbiddenOperation},
		{"release robot refused", Request{Op: OpReleaseRobot, Namespace: "warehouse-a"}, served, Deny, CodeForbiddenOperation},
		{"delete refused", Request{Op: OpDelete, Namespace: "warehouse-a"}, served, Deny, CodeForbiddenOperation},
		{"approve refused", Request{Op: OpApprove, Namespace: "warehouse-a"}, served, Deny, CodeForbiddenOperation},
		{"edit running work refused", Request{Op: OpEditRunningWork, Namespace: "warehouse-a"}, served, Deny, CodeForbiddenOperation},
		{"other custom verb refused", Request{Op: OpOtherVerb, Namespace: "warehouse-a"}, served, Deny, CodeForbiddenOperation},
		{"unknown operation refused", Request{Op: "patch_robot", Namespace: "warehouse-a"}, served, Deny, CodeForbiddenOperation},

		// Namespace scope.
		{"namespace outside policy", Request{Op: OpZoneStop, Namespace: "other", ZoneStop: &ZoneStopParams{ZoneName: "z", Reason: "r"}}, served, Deny, CodeNamespace},

		// Drafts.
		{"draft needs approval", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: goodDraft()}, served, RequireApproval, CodeApprovalRequired},
		{"non-draft task refused", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: &DraftTaskParams{Name: "x", Draft: false, Actions: goodDraft().Actions}}, served, Deny, CodeField},
		{"draft gate absent", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: goodDraft()}, Snapshot{}, Deny, CodeDraftGateAbsent},
		{"draft without actions", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: &DraftTaskParams{Name: "x", Draft: true}}, served, Deny, CodeMalformed},
		{"draft critical priority", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: &DraftTaskParams{Name: "x", Draft: true, Actions: []DraftAction{{Name: "a", Type: "Navigate", Priority: "Critical"}}}}, served, Deny, CodeField},
		{"draft external executor", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: &DraftTaskParams{Name: "x", Draft: true, Actions: []DraftAction{{Name: "a", Type: "External", Executor: true}}}}, served, Deny, CodeField},
		{"draft type not allowed", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: &DraftTaskParams{Name: "x", Draft: true, Actions: []DraftAction{{Name: "a", Type: "External"}}}}, served, Deny, CodeField},
		{"draft session limit", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: goodDraft()}, Snapshot{DraftGateServed: true, DraftsThisSession: 5}, Deny, CodeDraftLimit},
		{"draft default priority is Normal", Request{Op: OpDraftTask, Namespace: "warehouse-a", DraftTask: &DraftTaskParams{Name: "x", Draft: true, Actions: []DraftAction{{Name: "a", Type: "Patrol"}}}}, served, RequireApproval, CodeApprovalRequired},

		// Activation.
		{"activate draft needs approval", Request{Op: OpActivateTask, Namespace: "warehouse-a", ActivateTask: &ActivateTaskParams{TaskName: "t"}}, Snapshot{DraftGateServed: true, TaskExists: true, TaskDraft: true}, RequireApproval, CodeApprovalRequired},
		{"activate non-draft refused", Request{Op: OpActivateTask, Namespace: "warehouse-a", ActivateTask: &ActivateTaskParams{TaskName: "t"}}, Snapshot{DraftGateServed: true, TaskExists: true, TaskDraft: false}, Deny, CodeTaskNotDraft},
		{"activate missing task", Request{Op: OpActivateTask, Namespace: "warehouse-a", ActivateTask: &ActivateTaskParams{TaskName: "t"}}, Snapshot{DraftGateServed: true}, Deny, CodeTaskNotFound},
		{"activate without draft gate", Request{Op: OpActivateTask, Namespace: "warehouse-a", ActivateTask: &ActivateTaskParams{TaskName: "t"}}, Snapshot{TaskExists: true, TaskDraft: true}, Deny, CodeDraftGateAbsent},

		// Zone maintenance.
		{"maintenance needs approval", Request{Op: OpZoneMaintenance, Namespace: "warehouse-a", ZoneMaintenance: goodMaint()}, served, RequireApproval, CodeApprovalRequired},
		{"maintenance auto-resume refused", Request{Op: OpZoneMaintenance, Namespace: "warehouse-a", ZoneMaintenance: func() *ZoneMaintenanceParams { p := goodMaint(); p.AutoResumeAfterMinutes = 30; return p }()}, served, Deny, CodeRelease},
		{"maintenance estop gate off refused", Request{Op: OpZoneMaintenance, Namespace: "warehouse-a", ZoneMaintenance: func() *ZoneMaintenanceParams {
			p := goodMaint()
			p.RequireEstopClearBeforeResume = ptr(false)
			return p
		}()}, served, Deny, CodeField},
		{"maintenance unknown zone", Request{Op: OpZoneMaintenance, Namespace: "warehouse-a", ZoneMaintenance: goodMaint()}, Snapshot{}, Deny, CodeZoneNotFound},
		{"maintenance namespace scope off", Request{Op: OpZoneMaintenance, Namespace: "warehouse-a", ZoneMaintenance: &ZoneMaintenanceParams{ScopeType: "Namespace", Mode: "Graceful", Reason: "r"}}, served, Deny, CodeField},
		{"maintenance bad mode", Request{Op: OpZoneMaintenance, Namespace: "warehouse-a", ZoneMaintenance: func() *ZoneMaintenanceParams { p := goodMaint(); p.Mode = "Hard"; return p }()}, served, Deny, CodeField},
		{"maintenance no reason", Request{Op: OpZoneMaintenance, Namespace: "warehouse-a", ZoneMaintenance: &ZoneMaintenanceParams{ScopeType: "Zone", ZoneName: "warehouse-a"}}, served, Deny, CodeMalformed},

		// Zone stop.
		{"zone stop needs approval", Request{Op: OpZoneStop, Namespace: "warehouse-a", ZoneStop: &ZoneStopParams{ZoneName: "warehouse-a", Reason: "spill"}}, served, RequireApproval, CodeApprovalRequired},
		{"zone stop already triggered", Request{Op: OpZoneStop, Namespace: "warehouse-a", ZoneStop: &ZoneStopParams{ZoneName: "warehouse-a", Reason: "spill"}}, Snapshot{ZoneExists: true, ZoneEstopStatus: "Triggered"}, Deny, CodeAlreadyStopped},
		{"zone stop annotation present", Request{Op: OpZoneStop, Namespace: "warehouse-a", ZoneStop: &ZoneStopParams{ZoneName: "warehouse-a", Reason: "spill"}}, Snapshot{ZoneExists: true, ZoneAnnotated: true}, Deny, CodeAlreadyStopped},
		{"zone stop unknown zone", Request{Op: OpZoneStop, Namespace: "warehouse-a", ZoneStop: &ZoneStopParams{ZoneName: "nope", Reason: "spill"}}, Snapshot{}, Deny, CodeZoneNotFound},
		{"zone stop no reason", Request{Op: OpZoneStop, Namespace: "warehouse-a", ZoneStop: &ZoneStopParams{ZoneName: "warehouse-a"}}, served, Deny, CodeMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := e.Decide(tc.req, tc.snap)
			if got.Effect != tc.want || got.Code != tc.code {
				t.Fatalf("Decide = %s/%s (%s), want %s/%s", got.Effect, got.Code, got.Message, tc.want, tc.code)
			}
			if got.Message == "" {
				t.Fatalf("decision %s has no message", got.Code)
			}
		})
	}
}

// A policy file cannot enable a forbidden operation: the invariants run first.
func TestPolicyFileCannotEnableForbidden(t *testing.T) {
	e := loadDefault(t)
	e.P.Approval.AllowSelfApproval = true
	e.P.ZoneMaintenance.AllowNamespaceScope = true
	for op := range Forbidden {
		d := e.Decide(Request{Op: op, Namespace: "warehouse-a"}, Snapshot{DraftGateServed: true, ZoneExists: true, TaskExists: true, TaskDraft: true})
		if d.Effect != Deny || d.Code != CodeForbiddenOperation {
			t.Errorf("%s: got %s/%s", op, d.Effect, d.Code)
		}
	}
	d := e.Decide(Request{Op: OpZoneMaintenance, Namespace: "warehouse-a", ZoneMaintenance: &ZoneMaintenanceParams{ScopeType: "Namespace", Mode: "Graceful", Reason: "r", AutoResumeAfterMinutes: 1}}, Snapshot{})
	if d.Code != CodeRelease {
		t.Errorf("auto-resume with permissive policy: got %s", d.Code)
	}
}

func TestZoneStopDisabled(t *testing.T) {
	e := loadDefault(t)
	e.P.ZoneStop.Enabled = false
	d := e.Decide(Request{Op: OpZoneStop, Namespace: "warehouse-a", ZoneStop: &ZoneStopParams{ZoneName: "warehouse-a", Reason: "spill"}}, Snapshot{ZoneExists: true})
	if d.Effect != Deny || d.Code != CodeZoneStopDisabled {
		t.Fatalf("Decide = %s/%s, want Deny/%s", d.Effect, d.Code, CodeZoneStopDisabled)
	}
}

func TestDecideApproval(t *testing.T) {
	e := loadDefault(t)
	person := Principal{Username: "dana", Groups: []string{"system:authenticated", "fleet-ops"}}
	base := ApprovalCheck{Decider: person, DecidedBy: "dana", RequestedFor: "sam", RequestedBy: "system:serviceaccount:fleetops-system:fleetops-mcp", State: "Pending", DeciderAuthorized: true, Approve: true}
	cases := []struct {
		name   string
		mutate func(*ApprovalCheck)
		effect Effect
		code   Code
	}{
		{"person approves", func(*ApprovalCheck) {}, Allow, CodeApprovalAccepted},
		{"person rejects without authority", func(c *ApprovalCheck) { c.Approve = false; c.DeciderAuthorized = false }, Allow, CodeApprovalAccepted},
		{"service account by name", func(c *ApprovalCheck) {
			c.Decider = Principal{Username: "system:serviceaccount:fleetops-system:fleetops-approvals"}
			c.DecidedBy = c.Decider.Username
		}, Deny, CodeServiceAccountApprover},
		{"service account by group", func(c *ApprovalCheck) {
			c.Decider = Principal{Username: "robot", Groups: []string{"system:serviceaccounts"}}
			c.DecidedBy = "robot"
		}, Deny, CodeServiceAccountApprover},
		{"copilot identity approves its own request", func(c *ApprovalCheck) {
			c.Decider = Principal{Username: "copilot"}
			c.DecidedBy = "copilot"
			c.RequestedBy = "copilot"
		}, Deny, CodeSelfApproval},
		{"requester approves own change", func(c *ApprovalCheck) { c.RequestedFor = "dana" }, Deny, CodeSelfApproval},
		{"decidedBy forged", func(c *ApprovalCheck) { c.DecidedBy = "someone-else" }, Deny, CodeApproverMismatch},
		{"expired", func(c *ApprovalCheck) { c.Expired = true }, Deny, CodeRequestExpired},
		{"already executed", func(c *ApprovalCheck) { c.State = "Executed" }, Deny, CodeRequestNotPending},
		{"approver lacks the Swarmada verb", func(c *ApprovalCheck) { c.DeciderAuthorized = false }, Deny, CodeApproverNotAuthorized},
		{"unverified requester: approve refused", func(c *ApprovalCheck) { c.RequestedFor = "unverified:sam" }, Deny, CodeRequesterUnverified},
		{"unverified requester: reject accepted", func(c *ApprovalCheck) { c.RequestedFor = "unverified:sam"; c.Approve = false }, Allow, CodeApprovalAccepted},
		{"unverified requester naming the approver", func(c *ApprovalCheck) { c.RequestedFor = "unverified:dana" }, Deny, CodeRequesterUnverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			got := e.DecideApproval(c)
			if got.Effect != tc.effect || got.Code != tc.code {
				t.Fatalf("DecideApproval = %s/%s (%s), want %s/%s", got.Effect, got.Code, got.Message, tc.effect, tc.code)
			}
		})
	}
}

// No policy file setting lets an unverified requester's request be approved.
func TestUnverifiedRequesterIsAHardInvariant(t *testing.T) {
	e := loadDefault(t)
	e.P.Approval.AllowSelfApproval = true
	c := ApprovalCheck{Decider: Principal{Username: "dana"}, DecidedBy: "dana", RequestedFor: UnverifiedPrefix + "dana", RequestedBy: "system:serviceaccount:fleetops-system:fleetops-mcp", State: "Pending", DeciderAuthorized: true, Approve: true}
	if d := e.DecideApproval(c); d.Code != CodeRequesterUnverified {
		t.Fatalf("with allowSelfApproval: %s %s", d.Code, d.Message)
	}
	if !IsUnverified("unverified:x") || IsUnverified("x") || IsUnverified("Unverified:x") {
		t.Fatal("IsUnverified")
	}
}

func TestSelfApprovalCanBeAllowedOnlyForRequester(t *testing.T) {
	e := loadDefault(t)
	e.P.Approval.AllowSelfApproval = true
	c := ApprovalCheck{Decider: Principal{Username: "dana"}, DecidedBy: "dana", RequestedFor: "dana", RequestedBy: "system:serviceaccount:fleetops-system:fleetops-mcp", State: "Pending", DeciderAuthorized: true, Approve: true}
	if d := e.DecideApproval(c); d.Effect != Allow {
		t.Fatalf("dev self-approval: %s %s", d.Code, d.Message)
	}
	c.Decider = Principal{Username: "system:serviceaccount:fleetops-system:fleetops-mcp"}
	c.DecidedBy = c.Decider.Username
	if d := e.DecideApproval(c); d.Code != CodeServiceAccountApprover {
		t.Fatalf("copilot SA must stay refused: %s", d.Code)
	}
}

func TestParseRejects(t *testing.T) {
	bad := map[string]string{
		"wrong kind":      "apiVersion: fleetops.alexbahel.github.io/v1alpha1\nkind: Other\nnamespaces: [a]\n",
		"no namespaces":   "apiVersion: fleetops.alexbahel.github.io/v1alpha1\nkind: CopilotPolicy\napproval: {ttl: 1m}\ndraftTask: {maxActions: 1, maxDraftsPerSession: 1}\n",
		"unknown field":   "apiVersion: fleetops.alexbahel.github.io/v1alpha1\nkind: CopilotPolicy\nnamespaces: [a]\nallowClearStop: true\n",
		"bad ttl":         "apiVersion: fleetops.alexbahel.github.io/v1alpha1\nkind: CopilotPolicy\nnamespaces: [a]\napproval: {ttl: soon}\n",
		"zero draft caps": "apiVersion: fleetops.alexbahel.github.io/v1alpha1\nkind: CopilotPolicy\nnamespaces: [a]\napproval: {ttl: 1m}\n",
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		} else if !strings.Contains(err.Error(), "polic") && !strings.Contains(err.Error(), "duration") {
			t.Errorf("%s: unhelpful error %v", name, err)
		}
	}
}

// D74 is a hard invariant compiled in: no confirmation, or a confirmation of
// another preview, refuses whatever the policy file says.
func TestConfirmation(t *testing.T) {
	if d := Confirmation("", "abc"); d == nil || d.Code != CodeNotConfirmed {
		t.Fatalf("no confirmation: %+v", d)
	}
	if d := Confirmation("abd", "abc"); d == nil || d.Code != CodePreviewMismatch {
		t.Fatalf("mismatch: %+v", d)
	}
	if d := Confirmation("abc", "abc"); d != nil {
		t.Fatalf("match: %+v", d)
	}
}
