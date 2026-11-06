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

// Package requestview shows an ApprovalRequest as the web UI presents it:
// the exact Swarmada write it leads to, and the commands a person runs to
// decide it. The objects come from the executor's own builders
// (internal/approval), so what is shown is what foc-approvals sends.
//
// The view never decides anything. Approving or rejecting is done by a
// person with `foc approve` / `foc reject` and their own kubeconfig.
package requestview

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/approval"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// ApproverPlaceholder stands for the approver's username in an object shown
// before anyone has decided; foc-approvals writes the real name there.
const ApproverPlaceholder = "<username of the person who approves>"

// Line is one line of a diff: Op is "+" (added), "-" (removed) or " ".
type Line struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// Change is the single Swarmada write a request leads to.
type Change struct {
	// Writer is who sends the write.
	Writer string `json:"writer"`
	// Operation is the API operation, e.g. "create" or "JSON merge patch".
	Operation string `json:"operation"`
	// Target is Kind namespace/name.
	Target string `json:"target"`
	// Diff is the change to the target, line by line.
	Diff []Line `json:"diff"`
	// Body is the request body: for a create, the object as YAML without
	// metadata.creationTimestamp (null) and status (the executor sets none);
	// for a patch, the exact JSON sent.
	Body string `json:"body"`
	// Command is what the approver runs for a write the copilot never makes.
	Command string   `json:"command,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// Policy is the decision recorded when the request was created.
type Policy struct {
	Effect  string `json:"effect"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// View is one ApprovalRequest.
type View struct {
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	Type         string    `json:"type"`
	State        string    `json:"state"`
	RequestedFor string    `json:"requestedFor"`
	RequestedBy  string    `json:"requestedBy"`
	SessionID    string    `json:"sessionId,omitempty"`
	Created      time.Time `json:"created"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Policy       Policy    `json:"policy"`
	DecidedBy    string    `json:"decidedBy,omitempty"`
	Message      string    `json:"message,omitempty"`
	// Instruction is status.instruction: the command an approved activation
	// waits for.
	Instruction string `json:"instruction,omitempty"`
	Change      Change `json:"change"`
	// Approve and Reject are the commands for a pending request; empty once
	// it is decided. Approve is also empty when the requester is unverified.
	Approve string `json:"approve,omitempty"`
	Reject  string `json:"reject,omitempty"`
	// Unapprovable explains why a pending request cannot be approved: its
	// requester was typed into the web UI and not verified (R38).
	Unapprovable string `json:"unapprovable,omitempty"`
}

// List reads the ApprovalRequests in each namespace and returns their views,
// pending first, newest first within a state.
func List(ctx context.Context, c client.Reader, namespaces []string) ([]View, error) {
	var out []View
	for _, ns := range namespaces {
		var l focv1.ApprovalRequestList
		if err := c.List(ctx, &l, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("listing ApprovalRequests in %s: %w", ns, err)
		}
		for i := range l.Items {
			out = append(out, Of(&l.Items[i]))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].State == string(focv1.StatePending), out[j].State == string(focv1.StatePending)
		if pi != pj {
			return pi
		}
		return out[i].Created.After(out[j].Created)
	})
	return out, nil
}

// Of builds the view of one request.
func Of(r *focv1.ApprovalRequest) View {
	state := r.Status.State
	if state == "" {
		state = focv1.StatePending
	}
	v := View{
		Name: r.Name, Namespace: r.Namespace, Type: string(r.Spec.Type), State: string(state),
		RequestedFor: r.Spec.RequestedFor, RequestedBy: r.Spec.RequestedBy, SessionID: r.Spec.SessionID,
		Created: r.CreationTimestamp.UTC(), ExpiresAt: r.Spec.ExpiresAt.UTC(),
		Policy:    Policy{Effect: r.Spec.Policy.Effect, Code: r.Spec.Policy.Code, Message: r.Spec.Policy.Message},
		DecidedBy: r.Status.DecidedBy, Message: r.Status.Message, Instruction: r.Status.Instruction,
		Change: ChangeOf(r),
	}
	if state == focv1.StatePending {
		v.Reject = fmt.Sprintf("foc reject %s -n %s", r.Name, r.Namespace)
		if policy.IsUnverified(r.Spec.RequestedFor) {
			v.Unapprovable = "The requester was typed into the web UI and is not verified, so approval is refused (" + string(policy.CodeRequesterUnverified) + "). Reject it, or let it expire, and file the request again with an authenticated identity."
		} else {
			v.Approve = fmt.Sprintf("foc approve %s -n %s", r.Name, r.Namespace)
		}
	}
	return v
}

const executor = "foc-approvals, as ServiceAccount foc-system/foc-approvals, after a person approves"

// ChangeOf returns the write the request leads to.
func ChangeOf(r *focv1.ApprovalRequest) Change {
	approver := r.Status.DecidedBy
	if approver == "" {
		approver = ApproverPlaceholder
	}
	switch r.Spec.Type {
	case focv1.RequestDraftTask:
		if r.Spec.DraftTask == nil {
			break
		}
		obj, err := approval.DraftTaskObject(r, approver)
		if err != nil {
			break
		}
		c := create(obj)
		c.Notes = append([]string{"spec.draft is true: Swarmada creates no FleetAction for this task, and nothing is dispatched until a person activates it (a separate ActivateTask request)."}, c.Notes...)
		return c
	case focv1.RequestZoneMaintenance:
		if r.Spec.ZoneMaintenance == nil {
			break
		}
		c := create(approval.MaintenanceObject(r, approver))
		c.Notes = append([]string{"autoResumeAfterMinutes is 0 (left out of the body, which is how 0 is sent): robots stay paused until a person ends the window. The copilot never ends it."}, c.Notes...)
		return c
	case focv1.RequestZoneStop:
		if r.Spec.ZoneStop == nil {
			break
		}
		body, err := approval.ZoneStopPatch(r)
		if err != nil {
			return Change{Writer: executor, Operation: "JSON merge patch", Notes: []string{"cannot build the patch: " + err.Error()}}
		}
		return Change{
			Writer: executor, Operation: "JSON merge patch (metadata.annotations only)",
			Target: fmt.Sprintf("FleetZone %s/%s", r.Namespace, r.Spec.ZoneStop.ZoneName),
			Diff: []Line{
				{Op: " ", Text: "metadata:"},
				{Op: " ", Text: "  annotations:"},
				{Op: "+", Text: "    " + fleet.EstopAnnotation + ": " + yamlScalar(r.Spec.ZoneStop.Reason)},
			},
			Body: string(body),
			Notes: []string{
				"Nothing is sent if the zone already carries " + fleet.EstopAnnotation + ".",
				"Swarmada's FleetZone mutating webhook adds swarmada.io/estop-actor and the swarmada.io/estop-in-force finalizer to the same write.",
				"This waits for a person's approval; it is not an emergency path. For an emergency use the physical E-stop or swarmctl estop trigger.",
			},
		}
	case focv1.RequestActivateTask:
		if r.Spec.ActivateTask == nil {
			break
		}
		cmd := approval.ActivationCommand(r.Namespace, r.Name)
		return Change{
			Writer:    "a person Swarmada allows to activate, with their own credentials, after approval (the copilot never activates)",
			Operation: "JSON merge patch (spec.draft only)",
			Target:    fmt.Sprintf("FleetTask %s/%s", r.Namespace, r.Spec.ActivateTask.TaskName),
			Diff:      []Line{{Op: " ", Text: "spec:"}, {Op: "-", Text: "  draft: true"}, {Op: "+", Text: "  draft: false"}},
			Body:      approval.ActivationPatch,
			Command:   cmd,
			Notes: []string{
				"Before the patch, foc activate re-checks the fleet and creates an ApprovalDecision of type Activation naming " + ActivatorPlaceholder + " (the fleetops-decisions admission policy binds it to the authenticated person). Nothing is added to the FleetTask.",
				"Swarmada refuses this patch to every ServiceAccount and checks the activate verb of the person who sends it. When the task leaves Draft, foc-approvals links the Activation record to this request (D79).",
			},
		}
	}
	return Change{Notes: []string{fmt.Sprintf("request type %q has no parameters the UI can show", r.Spec.Type)}}
}

// ActivatorPlaceholder stands for the person who runs foc activate.
const ActivatorPlaceholder = "<you>"

// create shows a new object: every line added.
func create(obj client.Object) Change {
	gvk := obj.GetObjectKind().GroupVersionKind()
	body, err := objectYAML(obj)
	c := Change{
		Writer: executor, Operation: "create",
		Target: fmt.Sprintf("%s %s/%s", gvk.Kind, obj.GetNamespace(), obj.GetName()),
		Body:   body,
	}
	if err != nil {
		c.Notes = append(c.Notes, "cannot render the object: "+err.Error())
		return c
	}
	for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		c.Diff = append(c.Diff, Line{Op: "+", Text: l})
	}
	c.Notes = append(c.Notes, "The API server fills in the "+gvk.Kind+" CRD's defaults for fields this body leaves out; they are not shown.")
	return c
}

// objectYAML renders an object as it is sent on create: without
// metadata.creationTimestamp (null until the server sets it) and without
// status (the executor never writes status).
func objectYAML(obj client.Object) (string, error) {
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	delete(m, "status")
	if md, ok := m["metadata"].(map[string]any); ok {
		delete(md, "creationTimestamp")
	}
	y, err := yaml.Marshal(m)
	return string(y), err
}

// yamlScalar renders one string value as YAML would.
func yamlScalar(s string) string {
	b, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimRight(string(b), "\n")
}
