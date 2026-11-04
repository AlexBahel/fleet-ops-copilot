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

package authn

import (
	"context"
	"errors"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

// reviewer answers TokenReviews from a token -> username table and counts
// calls.
func reviewer(users map[string]string, calls *int, apiErr error) *TokenReviewer {
	c := fake.NewClientBuilder().WithScheme(fleet.NewScheme()).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			*calls++
			if apiErr != nil {
				return apiErr
			}
			tr := obj.(*authnv1.TokenReview)
			if u, ok := users[tr.Spec.Token]; ok {
				tr.Status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: u}}
			} else {
				tr.Status = authnv1.TokenReviewStatus{Error: "invalid bearer token"}
			}
			return nil
		},
	}).Build()
	return &TokenReviewer{C: c}
}

func TestResolve(t *testing.T) {
	calls := 0
	a := reviewer(map[string]string{"sam-token": "sam", "odd-token": "unverified:x"}, &calls, nil)
	ctx := context.Background()
	for _, c := range []struct {
		name, token, asserted, want string
		verified                    bool
	}{
		{"verified token wins over the asserted name", "sam-token", "dana", "sam", true},
		{"verified without an asserted name", "sam-token", "", "sam", true},
		{"no token", "", "dana", "unverified:dana", false},
		{"no token, no name", "", "  ", "unverified:anonymous", false},
		{"rejected token", "forged", "dana", "unverified:dana", false},
		{"asserted name already marked is not doubled", "", "unverified:jane", "unverified:jane", false},
		{"username carrying the mark stays unverified", "odd-token", "x", "unverified:x", false},
	} {
		got := Resolve(ctx, a, c.token, c.asserted)
		if got.Requester != c.want || got.Verified != c.verified || (got.Verified == (got.Reason != "")) {
			t.Errorf("%s: %+v, want %s verified=%v", c.name, got, c.want, c.verified)
		}
	}
	if got := Resolve(ctx, nil, "sam-token", "sam"); got.Verified || got.Requester != "unverified:sam" {
		t.Errorf("no authenticator: %+v", got)
	}
}

func TestTokenReviewerCachesBySHA(t *testing.T) {
	calls := 0
	now := time.Unix(1000, 0)
	a := reviewer(map[string]string{"sam-token": "sam"}, &calls, nil)
	a.Now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if u, err := a.Authenticate(ctx, "sam-token"); err != nil || u != "sam" {
			t.Fatal(u, err)
		}
	}
	if _, err := a.Authenticate(ctx, "forged"); !errors.Is(err, ErrNotAuthenticated) {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "forged"); !errors.Is(err, ErrNotAuthenticated) {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("%d reviews, want 2 (one per distinct token)", calls)
	}
	now = now.Add(31 * time.Second)
	_, _ = a.Authenticate(ctx, "sam-token")
	if calls != 3 {
		t.Fatalf("%d reviews after expiry, want 3", calls)
	}
	for k := range a.cache {
		if string(k[:]) == "sam-token" {
			t.Fatal("token stored in clear")
		}
	}
}

func TestTokenReviewerAPIErrorIsNotCached(t *testing.T) {
	calls := 0
	a := reviewer(nil, &calls, errors.New("connection refused"))
	ctx := context.Background()
	if got := Resolve(ctx, a, "sam-token", "sam"); got.Verified || got.Reason != "TokenReview failed" {
		t.Fatalf("%+v", got)
	}
	_, _ = a.Authenticate(ctx, "sam-token")
	if calls != 2 {
		t.Fatalf("%d calls; API errors must not be cached", calls)
	}
	if _, err := a.Authenticate(ctx, ""); !errors.Is(err, ErrNotAuthenticated) || calls != 2 {
		t.Fatal("empty token reviewed")
	}
}

func TestBearerToken(t *testing.T) {
	for in, want := range map[string]string{
		"Bearer abc":   "abc",
		"bearer  abc ": "abc",
		"Basic abc":    "",
		"Bearer":       "",
		"":             "",
	} {
		if got := BearerToken(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
