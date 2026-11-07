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

package approval

import (
	"context"
	"fmt"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

// Approver acts with a person's own credentials (their kubeconfig). It is
// used by `fleetops approve` and `fleetops reject`; no copilot process holds these
// credentials.
type Approver struct {
	C        client.Client
	Identity func(ctx context.Context) (string, []string, error)
	// Poll and Timeout bound the wait for an activation to be Approved.
	Poll    time.Duration
	Timeout time.Duration
}

// NewApprover builds an Approver from a person's rest.Config.
func NewApprover(cfg *rest.Config) (*Approver, error) {
	c, err := client.New(cfg, client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Approver{C: c, Poll: time.Second, Timeout: 60 * time.Second, Identity: func(ctx context.Context) (string, []string, error) {
		r, err := cs.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
		if err != nil {
			return "", nil, fmt.Errorf("SelfSubjectReview: %w", err)
		}
		return r.Status.UserInfo.Username, r.Status.UserInfo.Groups, nil
	}}, nil
}

// Outcome reports what Decide did.
type Outcome struct {
	Decision string
	State    fleetopsv1.RequestState
	// Instruction is the command the approver must run themselves (activation).
	Instruction string
	Message     string
}

// Decide records the caller's verdict on a request. It never writes to
// Swarmada. For an approved activation it waits for fleetops-approvals to accept
// the decision and returns the exact command the approver runs with their own
// credentials (Swarmada refuses activate to every ServiceAccount).
func (a *Approver) Decide(ctx context.Context, ns, request string, verdict fleetopsv1.Verdict, comment string) (Outcome, error) {
	user, groups, err := a.Identity(ctx)
	if err != nil {
		return Outcome{}, err
	}
	var req fleetopsv1.ApprovalRequest
	if err := a.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: request}, &req); err != nil {
		return Outcome{}, err
	}
	d := &fleetopsv1.ApprovalDecision{
		ObjectMeta: metav1.ObjectMeta{GenerateName: request + "-", Namespace: ns, Labels: map[string]string{fleet.RequestLabel: request}},
		Spec:       fleetopsv1.ApprovalDecisionSpec{RequestRef: request, Verdict: verdict, DecidedBy: user, DecidedByGroups: groups, Comment: comment},
	}
	if err := a.C.Create(ctx, d); err != nil {
		return Outcome{}, fmt.Errorf("creating ApprovalDecision: %w", err)
	}
	out := Outcome{Decision: d.Name}
	if verdict != fleetopsv1.VerdictApprove || req.Spec.Type != fleetopsv1.RequestActivateTask {
		out.State, out.Message = req.Status.State, "decision recorded; fleetops-approvals will process it"
		return out, nil
	}
	deadline := time.Now().Add(a.Timeout)
	for {
		if err := a.C.Get(ctx, client.ObjectKey{Namespace: ns, Name: request}, &req); err != nil {
			return out, err
		}
		st := req.Status.State
		if st == fleetopsv1.StateApproved && req.Status.DecisionRef == d.Name {
			out.State, out.Instruction = st, req.Status.Instruction
			out.Message = "approved. The copilot does not activate; run this with your own credentials: " + req.Status.Instruction
			return out, nil
		}
		if st.IsTerminal() || containsName(req.Status.RefusedDecisions, d.Name) {
			out.State, out.Message = st, "not approved: "+req.Status.Message
			return out, nil
		}
		if time.Now().After(deadline) {
			out.State, out.Message = st, "timed out waiting for fleetops-approvals to accept the decision"
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(a.Poll):
		}
	}
}

func containsName(list []string, n string) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}
