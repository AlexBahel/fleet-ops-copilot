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
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/approval"
	"github.com/AlexBahel/fleet-ops-copilot/internal/feasibility"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// activator is what fleetops activate needs from the cluster, as the person.
type activator struct {
	C        client.Client
	Identity func(ctx context.Context) (string, []string, error)
	Host     string
	Now      func() time.Time
}

// activate runs an approved ActivateTask request with the person's own
// credentials (DESIGN.md §3.10.9, D79): it refuses a ServiceAccount, a
// request that is not approved, a task that is no longer a draft, and a
// fleet check with a blocker; then it creates an ApprovalDecision of type
// Activation naming the person (the fleetops-decisions admission policy binds
// it to the authenticated caller) and sends the plain spec.draft=false patch.
// Swarmada checks the person's own activate verb; nothing is added to the
// FleetTask. fleetops-approvals links the record when the task leaves Draft.
func activate(ctx context.Context, a activator, ns, request, kctx string, stdout io.Writer) error {
	user, groups, err := a.Identity(ctx)
	if err != nil {
		return err
	}
	if (policy.Principal{Username: user, Groups: groups}).IsServiceAccount() {
		return fmt.Errorf("%s is a ServiceAccount; Swarmada lets only a person activate a draft", user)
	}
	var req fleetopsv1.ApprovalRequest
	if err := a.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: request}, &req); err != nil {
		return err
	}
	switch {
	case req.Spec.Type != fleetopsv1.RequestActivateTask || req.Spec.ActivateTask == nil:
		return fmt.Errorf("%s is a %s request, not an activation", request, req.Spec.Type)
	case req.Status.State == fleetopsv1.StateExecuted:
		return fmt.Errorf("%s is already Executed: the task was activated (by %s)", request, req.Status.ActivatedBy)
	case req.Status.State != fleetopsv1.StateApproved:
		return fmt.Errorf("%s is %q, not Approved; a named person approves it first (fleetops approve %s -n %s)", request, req.Status.State, request, ns)
	}
	name := req.Spec.ActivateTask.TaskName
	var task swarmv1.FleetTask
	if err := a.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &task); err != nil {
		return err
	}
	if !task.Spec.Draft {
		return fmt.Errorf("FleetTask %s is not a draft (status.phase=%s); nothing to activate", name, task.Status.Phase)
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	rep := feasibility.Evaluate(feasibility.Proposal{Task: &task}, feasibility.Load(ctx, a.C, ns, now()))
	_, _ = fmt.Fprintf(stdout, "activating FleetTask %s for %s, as %s, against:\n  API server: %s\n", name, request, user, a.Host)
	if kctx != "" {
		_, _ = fmt.Fprintf(stdout, "  context:    %s\n", kctx)
	}
	_, _ = fmt.Fprintf(stdout, "  namespace:  %s\nchecks: %s\n", ns, rep.Verdict)
	for _, b := range rep.Blockers {
		_, _ = fmt.Fprintf(stdout, "  blocker %s %s: %s\n", b.Code, b.Member, b.Reason)
	}
	for _, w := range rep.Warnings {
		_, _ = fmt.Fprintf(stdout, "  warning %s %s: %s\n", w.Code, w.Member, w.Reason)
	}
	for _, alt := range rep.Alternatives {
		_, _ = fmt.Fprintf(stdout, "  alternative: %s\n", alt)
	}
	for _, n := range rep.NotChecked {
		_, _ = fmt.Fprintf(stdout, "  not checked: %s\n", n)
	}
	_, _ = fmt.Fprintln(stdout, feasibility.Statement)
	if rep.Verdict == feasibility.Blocked {
		return errors.New("not activated: a check is blocked (above)")
	}
	rec := &fleetopsv1.ApprovalDecision{
		ObjectMeta: metav1.ObjectMeta{GenerateName: request + "-activation-", Namespace: ns, Labels: map[string]string{fleet.RequestLabel: request}},
		Spec:       fleetopsv1.ApprovalDecisionSpec{RequestRef: request, Type: fleetopsv1.DecisionActivation, DecidedBy: user, DecidedByGroups: groups},
	}
	if err := a.C.Create(ctx, rec); err != nil {
		return fmt.Errorf("not activated: recording the activation (an ApprovalDecision of type Activation; the fleetops:approver role grants create): %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "recorded ApprovalDecision %s (type Activation, by %s)\n", rec.Name, user)
	if err := a.C.Patch(ctx, &task, client.RawPatch(types.MergePatchType, []byte(approval.ActivationPatch))); err != nil {
		return fmt.Errorf("not activated: %w (the Activation record %s stays; it is linked only if the task leaves Draft)", err, rec.Name)
	}
	_, _ = fmt.Fprintf(stdout, "activated FleetTask %s as %s; fleetops-approvals links %s to %s\n", name, user, rec.Name, request)
	return nil
}

func activateCmd(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: fleetops activate REQUEST [-n NAMESPACE] [--context NAME]")
	}
	fs := flag.NewFlagSet("activate", flag.ContinueOnError)
	ns := fs.String("n", "warehouse-a", "namespace")
	kctx := fs.String("context", "", "kubeconfig context (default: the current context)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := getConfig(*kctx)
	if err != nil {
		return err
	}
	ap, err := approval.NewApprover(cfg)
	if err != nil {
		return err
	}
	return activate(ctx, activator{C: ap.C, Identity: ap.Identity, Host: cfg.Host}, *ns, args[0], *kctx, stdout)
}
