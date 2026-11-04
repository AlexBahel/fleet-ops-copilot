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

package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/authn"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
)

type tokens map[string]string

func (m tokens) Authenticate(_ context.Context, tok string) (string, error) {
	if u, ok := m[tok]; ok {
		return u, nil
	}
	return "", authn.ErrNotAuthenticated
}

// Over streamable HTTP the requester is the user of the Authorization
// bearer token; the operator named in _meta changes nothing except the name
// inside an unverified requester (R39).
func TestRequesterFromBearerTokenNotMeta(t *testing.T) {
	p, err := policy.Load("../../policies/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	zone := &swarmv1.FleetZone{ObjectMeta: metav1.ObjectMeta{Name: "aisle-b", Namespace: "warehouse-a"}}
	c := fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithObjects(zone).Build()
	svc := &tools.Service{C: c, Engine: policy.New(p), Identity: "system:serviceaccount:foc-system:foc-mcp", DefaultNamespace: "warehouse-a",
		Authn: tokens{"sam-token": "sam"}}
	ts := httptest.NewServer(Handler(New(svc)))
	defer ts.Close()
	ctx := context.Background()

	file := func(token, operator, session string) string {
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ClientTransport(ts.URL, token), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cs.Close() }()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Meta: mcp.Meta{MetaOperator: operator, MetaSession: session},
			Name: "request_zone_stop", Arguments: map[string]any{"zoneName": "aisle-b", "reason": "spill"},
		})
		if err != nil || res.IsError {
			t.Fatalf("call: %v %+v", err, res)
		}
		b, _ := json.Marshal(res.StructuredContent)
		var out tools.RequestResult
		_ = json.Unmarshal(b, &out)
		var r focv1.ApprovalRequest
		if err := c.Get(ctx, clientKey(strings.TrimPrefix(out.Created, "ApprovalRequest/")), &r); err != nil {
			t.Fatalf("%+v: %v", out, err)
		}
		return r.Spec.RequestedFor
	}
	for _, tc := range []struct{ name, token, operator, want string }{
		{"verified token, forged operator name", "sam-token", "dana", "sam"},
		{"verified token, no name", "sam-token", "", "sam"},
		{"no token", "", "dana", "unverified:dana"},
		{"token the API server rejects", "forged-token", "dana", "unverified:dana"},
		{"nothing at all", "", "", "unverified:anonymous"},
	} {
		if got := file(tc.token, tc.operator, "s-"+strings.ReplaceAll(tc.name, " ", "-")[:8]); got != tc.want {
			t.Errorf("%s: requestedFor %q, want %q", tc.name, got, tc.want)
		}
	}
}

func clientKey(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: "warehouse-a", Name: name}
}
