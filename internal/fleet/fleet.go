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

// Package fleet holds the Kubernetes scheme and the cluster reads the policy
// engine depends on. It reads Swarmada objects with the public api/v1 types.
package fleet

import (
	"context"
	"fmt"
	"slices"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// Swarmada API facts used by the copilot (cited in docs/DESIGN.md).
const (
	// EstopAnnotation triggers an estop on its carrier object; removing it is the clear
	// (RFC-0001 #safety-estop-scopes; cmd/swarmctl/lifecycle.go).
	EstopAnnotation = "swarmada.io/estop-triggered"
	// SwarmadaGroup is the API group of every Swarmada kind.
	SwarmadaGroup = "swarmada.io"
	// CreatedByLabel marks objects the copilot created. The foc-mcp-drafts-only
	// admission policy requires it on every FleetTask foc-mcp creates.
	CreatedByLabel = "fleetops.alexbahel.github.io/created-by"
	// SessionLabel links a copilot-created object to its session.
	SessionLabel = "fleetops.alexbahel.github.io/session"
	// RequestLabel links an object to the ApprovalRequest that produced it.
	RequestLabel = "fleetops.alexbahel.github.io/request"
)

// NewScheme returns a scheme with core Kubernetes, Swarmada v1 and the copilot's types.
func NewScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(swarmv1.AddToScheme(s))
	utilruntime.Must(focv1.AddToScheme(s))
	return s
}

// DraftGate reports whether a fail-closed validating webhook covers UPDATE of
// fleettasks.swarmada.io. Only then does Swarmada enforce the activate verb, so
// only then is spec.draft an authorization boundary (RFC-0003 #resources-tasktrigger).
// It returns the webhook's name when found.
func DraftGate(ctx context.Context, c client.Reader) (bool, string, error) {
	var list admissionv1.ValidatingWebhookConfigurationList
	if err := c.List(ctx, &list); err != nil {
		return false, "", fmt.Errorf("listing validating webhook configurations: %w", err)
	}
	for _, cfg := range list.Items {
		for _, wh := range cfg.Webhooks {
			// admissionregistration/v1 defaults failurePolicy to Fail.
			if wh.FailurePolicy != nil && *wh.FailurePolicy != admissionv1.Fail {
				continue
			}
			for _, r := range wh.Rules {
				if covers(r.APIGroups, SwarmadaGroup) && covers(r.Resources, "fleettasks") && coversOp(r.Operations, admissionv1.Update) {
					return true, cfg.Name + "/" + wh.Name, nil
				}
			}
		}
	}
	return false, "", nil
}

func covers(list []string, want string) bool {
	return slices.Contains(list, want) || slices.Contains(list, "*")
}

func coversOp(ops []admissionv1.OperationType, want admissionv1.OperationType) bool {
	return slices.Contains(ops, want) || slices.Contains(ops, admissionv1.OperationAll)
}

// ZoneSnapshot reads the facts a zone operation is decided on.
func ZoneSnapshot(ctx context.Context, c client.Reader, ns, zone string) (policy.Snapshot, error) {
	var s policy.Snapshot
	if zone == "" {
		return s, nil
	}
	var z swarmv1.FleetZone
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: zone}, &z)
	if apierrors.IsNotFound(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.ZoneExists = true
	s.ZoneEstopStatus = string(z.Status.EstopStatus)
	_, s.ZoneAnnotated = z.Annotations[EstopAnnotation]
	return s, nil
}

// TaskSnapshot reads the facts an activation is decided on.
func TaskSnapshot(ctx context.Context, c client.Reader, ns, task string) (policy.Snapshot, error) {
	var s policy.Snapshot
	var t swarmv1.FleetTask
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: task}, &t)
	if apierrors.IsNotFound(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.TaskExists = true
	s.TaskDraft = t.Spec.Draft
	return s, nil
}

// CountSessionDraftRequests counts the DraftTask ApprovalRequests a session made.
func CountSessionDraftRequests(ctx context.Context, c client.Reader, ns, session string) (int, error) {
	if session == "" {
		return 0, nil
	}
	var list focv1.ApprovalRequestList
	if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{SessionLabel: session}); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range list.Items {
		if r.Spec.Type == focv1.RequestDraftTask {
			n++
		}
	}
	return n, nil
}
