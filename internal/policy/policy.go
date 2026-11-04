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

// Package policy decides whether a copilot operation is allowed, needs a
// person's approval, or is refused. Decide is a pure function: it reads only
// its arguments. Hard invariants are compiled in; site rules come from a YAML
// policy file (see policies/default.yaml).
package policy

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Effect is the outcome class of a decision.
type Effect string

const (
	Allow           Effect = "Allow"
	RequireApproval Effect = "RequireApproval"
	Deny            Effect = "Deny"
)

// Code is a stable reason code, safe to match on in tests and audit queries.
type Code string

const (
	CodeRead                   Code = "ALLOW_READ"
	CodeApprovalRequired       Code = "REQUIRE_APPROVAL"
	CodeForbiddenOperation     Code = "DENY_FORBIDDEN_OPERATION"
	CodeNamespace              Code = "DENY_NAMESPACE"
	CodeField                  Code = "DENY_FIELD"
	CodeRelease                Code = "DENY_RELEASE"
	CodeZoneNotFound           Code = "DENY_ZONE_NOT_FOUND"
	CodeTaskNotFound           Code = "DENY_TASK_NOT_FOUND"
	CodeTaskNotDraft           Code = "DENY_TASK_NOT_DRAFT"
	CodeDraftGateAbsent        Code = "DENY_DRAFT_GATE_ABSENT"
	CodeDraftLimit             Code = "DENY_DRAFT_LIMIT"
	CodeAlreadyStopped         Code = "DENY_ALREADY_STOPPED"
	CodeZoneStopDisabled       Code = "DENY_ZONE_STOP_DISABLED"
	CodeMalformed              Code = "DENY_MALFORMED"
	CodeServiceAccountApprover Code = "DENY_SERVICE_ACCOUNT_APPROVER"
	CodeSelfApproval           Code = "DENY_SELF_APPROVAL"
	CodeApproverNotAuthorized  Code = "DENY_APPROVER_NOT_AUTHORIZED"
	CodeRequestExpired         Code = "DENY_EXPIRED"
	CodeRequestNotPending      Code = "DENY_NOT_PENDING"
	CodeApproverMismatch       Code = "DENY_DECIDER_MISMATCH"
	CodeRequesterUnverified    Code = "DENY_REQUESTER_UNVERIFIED"
	CodeApprovalAccepted       Code = "ALLOW_APPROVAL"
)

// UnverifiedPrefix marks a requester (spec.requestedFor) whose identity no
// one authenticated, such as a name typed into foc-agent's web UI, which has
// no login. Approval of such a request is always refused (R38); rejection is
// allowed. A hard invariant: no policy file setting changes it.
const UnverifiedPrefix = "unverified:"

// IsUnverified reports whether a requester carries UnverifiedPrefix.
func IsUnverified(requestedFor string) bool { return strings.HasPrefix(requestedFor, UnverifiedPrefix) }

// Decision is the engine's answer.
type Decision struct {
	Effect  Effect `json:"effect"`
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func allow(c Code, msg string) Decision { return Decision{Effect: Allow, Code: c, Message: msg} }
func needApproval(msg string) Decision {
	return Decision{Effect: RequireApproval, Code: CodeApprovalRequired, Message: msg}
}
func deny(c Code, format string, a ...any) Decision {
	return Decision{Effect: Deny, Code: c, Message: fmt.Sprintf(format, a...)}
}

// Operation is what the caller wants to do.
type Operation string

const (
	OpRead            Operation = "read"
	OpDraftTask       Operation = "draft_task"
	OpActivateTask    Operation = "activate_task"
	OpZoneMaintenance Operation = "zone_maintenance"
	OpZoneStop        Operation = "zone_stop"

	// Operations the copilot never performs (DESIGN.md §3.1). They have no
	// tool; they are listed so that any path that could express one is
	// refused by the same code.
	OpClearStop       Operation = "clear_stop"
	OpReleaseRobot    Operation = "release_robot"
	OpDelete          Operation = "delete"
	OpApprove         Operation = "approve"
	OpEditRunningWork Operation = "edit_running_work"
	OpOtherVerb       Operation = "other_custom_verb"
)

// Forbidden lists the operations refused as hard invariants.
var Forbidden = map[Operation]string{
	OpClearStop:       "clearing an emergency stop is reserved to a person holding estop-clear",
	OpReleaseRobot:    "releasing or resuming a robot is reserved to a person",
	OpDelete:          "the copilot never deletes",
	OpApprove:         "the copilot never approves a request",
	OpEditRunningWork: "the copilot never edits work that exists in the cluster",
	OpOtherVerb:       "this custom verb is reserved to a person",
}

// DraftAction is the part of a drafted FleetTask member the policy inspects.
type DraftAction struct {
	Name     string
	Type     string
	Priority string
	Zone     string
	Executor bool
}

// DraftTaskParams describes a FleetTask the copilot would create.
type DraftTaskParams struct {
	Name    string
	Draft   bool
	Actions []DraftAction
}

// ActivateTaskParams names the task to activate.
type ActivateTaskParams struct{ TaskName string }

// ZoneMaintenanceParams carries every ZoneMaintenance field a caller may ask
// for, including the ones the policy refuses, so the refusal has a reason.
type ZoneMaintenanceParams struct {
	ScopeType                     string
	ZoneName                      string
	Mode                          string
	Reason                        string
	AutoResumeAfterMinutes        int32
	RequireEstopClearBeforeResume *bool
}

// ZoneStopParams names the zone to stop.
type ZoneStopParams struct {
	ZoneName string
	Reason   string
}

// Request is one operation to decide.
type Request struct {
	Op        Operation
	Namespace string
	// SessionID identifies the copilot session (for per-session limits).
	SessionID string

	DraftTask       *DraftTaskParams
	ActivateTask    *ActivateTaskParams
	ZoneMaintenance *ZoneMaintenanceParams
	ZoneStop        *ZoneStopParams
}

// Snapshot is the cluster state the decision depends on, read by the caller.
type Snapshot struct {
	ZoneExists      bool
	ZoneEstopStatus string // FleetZone status.estopStatus; "" means Clear
	ZoneAnnotated   bool   // swarmada.io/estop-triggered present on the zone

	TaskExists bool
	TaskDraft  bool // FleetTask spec.draft

	DraftGateServed   bool // a Fail-closed webhook covers UPDATE fleettasks.swarmada.io
	DraftsThisSession int
}

// File is the YAML policy file.
type File struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Namespaces []string `json:"namespaces"`
	DraftTask  struct {
		AllowedActionTypes  []string `json:"allowedActionTypes"`
		AllowedPriorities   []string `json:"allowedPriorities"`
		MaxActions          int      `json:"maxActions"`
		MaxDraftsPerSession int      `json:"maxDraftsPerSession"`
	} `json:"draftTask"`
	ZoneMaintenance struct {
		AllowedModes        []string `json:"allowedModes"`
		AllowNamespaceScope bool     `json:"allowNamespaceScope"`
	} `json:"zoneMaintenance"`
	ZoneStop struct {
		Enabled bool `json:"enabled"`
	} `json:"zoneStop"`
	Approval struct {
		TTL               Duration `json:"ttl"`
		AllowSelfApproval bool     `json:"allowSelfApproval"`
	} `json:"approval"`
}

// Duration is a time.Duration that reads "30m" style strings from YAML.
type Duration struct{ time.Duration }

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return []byte(`"` + d.String() + `"`), nil }

const (
	policyAPIVersion = "fleetops.alexbahel.github.io/v1alpha1"
	policyKind       = "CopilotPolicy"
)

// Parse reads and validates a policy file.
func Parse(data []byte) (*File, error) {
	var f File
	if err := yaml.UnmarshalStrict(data, &f); err != nil {
		return nil, fmt.Errorf("parsing policy: %w", err)
	}
	if f.APIVersion != policyAPIVersion || f.Kind != policyKind {
		return nil, fmt.Errorf("policy must be apiVersion %s kind %s, got %q %q", policyAPIVersion, policyKind, f.APIVersion, f.Kind)
	}
	if len(f.Namespaces) == 0 {
		return nil, fmt.Errorf("policy lists no namespaces")
	}
	if f.Approval.TTL.Duration <= 0 {
		return nil, fmt.Errorf("policy approval.ttl must be positive")
	}
	if f.DraftTask.MaxActions <= 0 || f.DraftTask.MaxDraftsPerSession <= 0 {
		return nil, fmt.Errorf("policy draftTask.maxActions and maxDraftsPerSession must be positive")
	}
	return &f, nil
}

// Load reads a policy file from disk.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Engine evaluates requests against a policy file plus the hard invariants.
type Engine struct{ P *File }

// New returns an engine for a parsed policy.
func New(p *File) *Engine { return &Engine{P: p} }

// NamespaceAllowed reports whether the policy covers a namespace. Callers
// check it before reading cluster state for a request.
func (e *Engine) NamespaceAllowed(ns string) bool { return slices.Contains(e.P.Namespaces, ns) }

// Decide evaluates one request. Hard invariants are checked before the
// policy file is consulted, so no policy file can enable a forbidden operation.
func (e *Engine) Decide(r Request, s Snapshot) Decision {
	if why, bad := Forbidden[r.Op]; bad {
		return deny(CodeForbiddenOperation, "%s refused: %s", r.Op, why)
	}
	switch r.Op {
	case OpRead:
		return allow(CodeRead, "read-only")
	case OpDraftTask, OpActivateTask, OpZoneMaintenance, OpZoneStop:
	default:
		return deny(CodeForbiddenOperation, "unknown operation %q refused", r.Op)
	}
	if !slices.Contains(e.P.Namespaces, r.Namespace) {
		return deny(CodeNamespace, "namespace %q is not in the policy's namespaces", r.Namespace)
	}
	switch r.Op {
	case OpDraftTask:
		return e.decideDraft(r, s)
	case OpActivateTask:
		return e.decideActivate(r, s)
	case OpZoneMaintenance:
		return e.decideMaintenance(r, s)
	default:
		return e.decideZoneStop(r, s)
	}
}

func (e *Engine) decideDraft(r Request, s Snapshot) Decision {
	p := r.DraftTask
	if p == nil || p.Name == "" || len(p.Actions) == 0 {
		return deny(CodeMalformed, "draft task needs a name and at least one action")
	}
	if !p.Draft {
		return deny(CodeField, "a task the copilot creates must have spec.draft: true; only a person activates work")
	}
	if !s.DraftGateServed {
		return deny(CodeDraftGateAbsent, "no fail-closed admission webhook covers UPDATE of fleettasks.swarmada.io, so spec.draft would not be an authorization boundary (RFC-0003 #resources-tasktrigger); no draft created")
	}
	if len(p.Actions) > e.P.DraftTask.MaxActions {
		return deny(CodeField, "draft has %d actions; policy allows at most %d", len(p.Actions), e.P.DraftTask.MaxActions)
	}
	if s.DraftsThisSession >= e.P.DraftTask.MaxDraftsPerSession {
		return deny(CodeDraftLimit, "this session already created %d drafts; policy allows %d", s.DraftsThisSession, e.P.DraftTask.MaxDraftsPerSession)
	}
	for _, a := range p.Actions {
		if a.Executor {
			return deny(CodeField, "action %q targets an external executor; the copilot drafts robot actions only", a.Name)
		}
		if !slices.Contains(e.P.DraftTask.AllowedActionTypes, a.Type) {
			return deny(CodeField, "action %q type %q is not in the policy's allowed action types %v", a.Name, a.Type, e.P.DraftTask.AllowedActionTypes)
		}
		prio := a.Priority
		if prio == "" {
			prio = "Normal"
		}
		if !slices.Contains(e.P.DraftTask.AllowedPriorities, prio) {
			return deny(CodeField, "action %q priority %q is not in the policy's allowed priorities %v", a.Name, prio, e.P.DraftTask.AllowedPriorities)
		}
	}
	return needApproval("a named person approves creating the draft; foc-approvals then creates it with spec.draft: true, and it dispatches nothing until a person activates it")
}

func (e *Engine) decideActivate(r Request, s Snapshot) Decision {
	if r.ActivateTask == nil || r.ActivateTask.TaskName == "" {
		return deny(CodeMalformed, "activation needs a task name")
	}
	if !s.TaskExists {
		return deny(CodeTaskNotFound, "FleetTask %q not found in %q", r.ActivateTask.TaskName, r.Namespace)
	}
	if !s.TaskDraft {
		return deny(CodeTaskNotDraft, "FleetTask %q is not a draft; changing it would edit work that already exists", r.ActivateTask.TaskName)
	}
	if !s.DraftGateServed {
		return deny(CodeDraftGateAbsent, "no fail-closed admission webhook covers UPDATE of fleettasks.swarmada.io; activation would not be checked against the activate verb")
	}
	return needApproval("activation is performed by the approving person with their own credentials")
}

func (e *Engine) decideMaintenance(r Request, s Snapshot) Decision {
	p := r.ZoneMaintenance
	if p == nil || p.Reason == "" {
		return deny(CodeMalformed, "zone maintenance needs scope, mode and reason")
	}
	// Hard invariants (DESIGN.md D10).
	if p.AutoResumeAfterMinutes != 0 {
		return deny(CodeRelease, "autoResumeAfterMinutes=%d would resume robots without a person; the copilot only creates windows with autoResumeAfterMinutes: 0", p.AutoResumeAfterMinutes)
	}
	if p.RequireEstopClearBeforeResume != nil && !*p.RequireEstopClearBeforeResume {
		return deny(CodeField, "requireEstopClearBeforeResume=false would let a robot resume with its estop not Clear; refused")
	}
	switch p.ScopeType {
	case "Zone":
		if p.ZoneName == "" {
			return deny(CodeMalformed, "scope Zone needs a zone name")
		}
		if !s.ZoneExists {
			return deny(CodeZoneNotFound, "FleetZone %q not found in %q", p.ZoneName, r.Namespace)
		}
	case "Namespace":
		if !e.P.ZoneMaintenance.AllowNamespaceScope {
			return deny(CodeField, "namespace-scope maintenance is not allowed by policy")
		}
	default:
		return deny(CodeMalformed, "scope type must be Zone or Namespace, got %q", p.ScopeType)
	}
	mode := p.Mode
	if mode == "" {
		mode = "Graceful"
	}
	if !slices.Contains(e.P.ZoneMaintenance.AllowedModes, mode) {
		return deny(CodeField, "mode %q is not in the policy's allowed modes %v", mode, e.P.ZoneMaintenance.AllowedModes)
	}
	return needApproval("creating a ZoneMaintenance pauses robots; a named person must approve")
}

func (e *Engine) decideZoneStop(r Request, s Snapshot) Decision {
	p := r.ZoneStop
	if p == nil || p.ZoneName == "" || p.Reason == "" {
		return deny(CodeMalformed, "zone stop needs a zone name and a reason")
	}
	if !e.P.ZoneStop.Enabled {
		return deny(CodeZoneStopDisabled, "zone stop requests are disabled by policy")
	}
	if !s.ZoneExists {
		return deny(CodeZoneNotFound, "FleetZone %q not found in %q", p.ZoneName, r.Namespace)
	}
	if s.ZoneAnnotated || s.ZoneEstopStatus == "Triggered" {
		return deny(CodeAlreadyStopped, "FleetZone %q already has an active estop (status.estopStatus=%q); no new request", p.ZoneName, s.ZoneEstopStatus)
	}
	return needApproval("a zone estop stops every robot in the zone tree; a named person must approve")
}

// Principal is an authenticated identity.
type Principal struct {
	Username string
	Groups   []string
}

// IsServiceAccount reports whether the principal is a Kubernetes ServiceAccount.
func (p Principal) IsServiceAccount() bool {
	if strings.HasPrefix(p.Username, "system:serviceaccount:") {
		return true
	}
	return slices.Contains(p.Groups, "system:serviceaccounts")
}

// ApprovalCheck is everything needed to accept or refuse one decision.
type ApprovalCheck struct {
	Decider      Principal
	DecidedBy    string // the decision's spec.decidedBy
	RequestedFor string
	RequestedBy  string
	State        string
	Expired      bool
	// DeciderAuthorized is the SubjectAccessReview result for the underlying
	// Swarmada operation, evaluated for the decider.
	DeciderAuthorized bool
	Approve           bool
}

// DecideApproval accepts or refuses a person's decision. The service-account,
// copilot-identity and unverified-requester rules are hard invariants;
// self-approval by the requesting operator can be allowed by the policy file
// (dev clusters only).
func (e *Engine) DecideApproval(c ApprovalCheck) Decision {
	if c.Decider.Username == "" || c.Decider.Username != c.DecidedBy {
		return deny(CodeApproverMismatch, "decision names %q but was made by %q", c.DecidedBy, c.Decider.Username)
	}
	if c.Decider.IsServiceAccount() {
		return deny(CodeServiceAccountApprover, "%q is a ServiceAccount; only a person can decide", c.Decider.Username)
	}
	if c.Decider.Username == c.RequestedBy {
		return deny(CodeSelfApproval, "%q created the request and cannot decide it", c.Decider.Username)
	}
	if c.State != "" && c.State != "Pending" {
		return deny(CodeRequestNotPending, "request is %s, not Pending", c.State)
	}
	if c.Expired {
		return deny(CodeRequestExpired, "request expired before the decision")
	}
	if !c.Approve {
		return allow(CodeApprovalAccepted, "rejection recorded")
	}
	if IsUnverified(c.RequestedFor) {
		return deny(CodeRequesterUnverified, "the requester %q was not authenticated (typed into the web UI); such a request can be rejected but never approved", c.RequestedFor)
	}
	if c.Decider.Username == c.RequestedFor && !e.P.Approval.AllowSelfApproval {
		return deny(CodeSelfApproval, "%q asked for this change and cannot also approve it", c.Decider.Username)
	}
	if !c.DeciderAuthorized {
		return deny(CodeApproverNotAuthorized, "%q is not permitted to perform this Swarmada operation themselves", c.Decider.Username)
	}
	return allow(CodeApprovalAccepted, "approval accepted")
}
