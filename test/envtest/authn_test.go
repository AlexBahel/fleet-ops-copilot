//go:build envtest

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

package envtest

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	fleetopsv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

// fileStop calls request_zone_stop on fleetops-mcp over streamable HTTP with the
// given bearer token and _meta operator, and returns the request's name and
// spec.requestedFor.
func fileStop(t *testing.T, url, token, operator, zone string) (string, string) {
	t.Helper()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, mcpserver.ClientTransport(url, token), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	id, _ := tools.PreviewIDFor(tools.ZoneStopInput{ZoneName: zone, Reason: "authn test"})
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Meta: mcp.Meta{mcpserver.MetaOperator: operator, mcpserver.MetaSession: "s-authn", mcpserver.MetaConfirmedPreview: id},
		Name: "request_zone_stop", Arguments: map[string]any{"zoneName": zone, "reason": "authn test"},
	})
	if err != nil || res.IsError {
		t.Fatalf("request_zone_stop: %v %+v", err, res)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var out tools.RequestResult
	_ = json.Unmarshal(b, &out)
	name := reqName(t, out)
	var r fleetopsv1.ApprovalRequest
	if err := admin.Get(ctx, client.ObjectKey{Namespace: swarmNS, Name: name}, &r); err != nil {
		t.Fatal(err)
	}
	return name, r.Spec.RequestedFor
}

// waitRefused waits until fleetops-approvals lists decision in refusedDecisions
// and returns the request.
func waitRefused(t *testing.T, name, decision string) *fleetopsv1.ApprovalRequest {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var r fleetopsv1.ApprovalRequest
		if err := admin.Get(context.Background(), client.ObjectKey{Namespace: swarmNS, Name: name}, &r); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(r.Status.RefusedDecisions, decision) {
			return &r
		}
		if time.Now().After(deadline) {
			t.Fatalf("decision %s on %s not refused: state %q message %q", decision, name, r.Status.State, r.Status.Message)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func zoneStopped(t *testing.T, zone string) bool {
	t.Helper()
	var z swarmv1.FleetZone
	if err := admin.Get(context.Background(), client.ObjectKey{Namespace: swarmNS, Name: zone}, &z); err != nil {
		t.Fatal(err)
	}
	_, ok := z.Annotations[fleet.EstopAnnotation]
	return ok
}

// R39 on a real API server: fleetops-mcp verifies the caller's bearer token with
// a TokenReview (RBAC: create tokenreviews) and records the authenticated
// username as requestedFor; the _meta operator name has no effect.
func TestRequesterIsAuthenticated(t *testing.T) {
	ctx := context.Background()
	f := newUIFixture(t, []string{swarmNS})
	samTok := env.PersonToken("sam")

	// (1) + (3): sam's token with a forged _meta operator "dana": the
	// requester is sam. sam cannot approve their own request.
	ensureZone(t, "authn-zone-1")
	name, requester := fileStop(t, f.mcp.URL, samTok, "dana", "authn-zone-1")
	if requester != "sam" {
		t.Fatalf("requestedFor %q, want sam (the forged operator name must have no effect)", requester)
	}
	out, err := approverFor(t, "sam").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, "")
	if err != nil {
		t.Fatal(err)
	}
	r := waitRefused(t, name, out.Decision)
	if r.Status.State != fleetopsv1.StatePending || !strings.Contains(r.Status.Message, "asked for this change") || zoneStopped(t, "authn-zone-1") {
		t.Fatalf("self-approval: state %q message %q", r.Status.State, r.Status.Message)
	}
	if !strings.Contains(string(f.h.auditBuf.Bytes()), `"DENY_SELF_APPROVAL"`) {
		t.Fatal("audit has no DENY_SELF_APPROVAL")
	}

	// (2) a different verified person, dana, can approve it.
	if _, err := approverFor(t, "dana").Decide(ctx, swarmNS, name, fleetopsv1.VerdictApprove, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, name, fleetopsv1.StateExecuted)
	if !zoneStopped(t, "authn-zone-1") {
		t.Fatal("approved stop not executed")
	}

	// (3) a forged operator name without a token, even the approver's own,
	// gives an unverified requester that nobody can approve.
	ensureZone(t, "authn-zone-2")
	name2, requester2 := fileStop(t, f.mcp.URL, "", "dana", "authn-zone-2")
	if requester2 != "unverified:dana" {
		t.Fatalf("requestedFor %q, want unverified:dana", requester2)
	}
	out2, err := approverFor(t, "eve").Decide(ctx, swarmNS, name2, fleetopsv1.VerdictApprove, "")
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRefused(t, name2, out2.Decision); !strings.Contains(r.Status.Message, "never approved") || zoneStopped(t, "authn-zone-2") {
		t.Fatalf("unverified: state %q message %q", r.Status.State, r.Status.Message)
	}

	// A token the API server does not accept is the same as none.
	ensureZone(t, "authn-zone-3")
	if _, requester3 := fileStop(t, f.mcp.URL, "not-a-token", "sam", "authn-zone-3"); requester3 != "unverified:sam" {
		t.Fatalf("rejected token: requestedFor %q", requester3)
	}

	// A TokenRequest-minted ServiceAccount token authenticates as that
	// ServiceAccount (the eval harness uses this).
	saTok, err := env.MintToken(ctx, testenv.OperatorNS, "authn-sa")
	if err != nil {
		t.Fatal(err)
	}
	ensureZone(t, "authn-zone-4")
	if _, requester4 := fileStop(t, f.mcp.URL, saTok, "dana", "authn-zone-4"); requester4 != testenv.OperatorIdentity("authn-sa") {
		t.Fatalf("TokenRequest token: requestedFor %q", requester4)
	}
}

// (4) Through fleetops-agent: a chat that carries the person's token (fleetops chat)
// is filed for that person; the page's chat (no token) stays unverified and
// unapprovable (TestWebUIRequestCannotBeApproved covers the refusal).
func TestFleetopsAgentForwardsTheCallersToken(t *testing.T) {
	f := newUIFixture(t, []string{swarmNS})
	ensureZone(t, "authn-zone-5")
	step := func(zone string) []llm.Step { return []llm.Step{proposeStop(zone, "from fleetops chat")} }
	name := f.chatAs(t, env.PersonToken("sam"), "dana", "stop authn-zone-5", step("authn-zone-5")...)
	if v := f.views(t)[name]; v.RequestedFor != "sam" || v.Approve == "" || v.Unapprovable != "" {
		t.Fatalf("token chat view %+v", v)
	}
	ensureZone(t, "authn-zone-6")
	name2 := f.chat(t, "sam", "stop authn-zone-6", step("authn-zone-6")...)
	if v := f.views(t)[name2]; v.RequestedFor != "unverified:sam" || v.Approve != "" || v.Unapprovable == "" {
		t.Fatalf("page chat view %+v", v)
	}
}
