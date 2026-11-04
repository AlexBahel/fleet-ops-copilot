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

// Package authn authenticates the person behind an MCP call (ruling R39).
// The client sends the person's own Kubernetes bearer token; foc-mcp
// verifies it with a TokenReview and uses the username the API server
// returns as the requester (ApprovalRequest spec.requestedFor). A name the
// client asserts is never trusted: without a token, or when the review
// fails, the requester is "unverified:<asserted name or anonymous>", which
// the policy refuses at approval (DENY_REQUESTER_UNVERIFIED).
//
// Tokens are never logged, audited, traced or stored; only a SHA-256 of a
// token is kept, as a cache key, for a short time.
package authn

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
)

// Anonymous stands for a caller that asserted no name.
const Anonymous = "anonymous"

// ErrNotAuthenticated means the API server did not accept the token.
var ErrNotAuthenticated = errors.New("token not authenticated")

// Authenticator returns the username a bearer token belongs to.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (string, error)
}

// TokenReviewer authenticates with the TokenReview API, as the
// ServiceAccount the client runs as (foc-mcp needs create on tokenreviews).
// Results are cached for TTL by the token's SHA-256.
type TokenReviewer struct {
	C client.Client
	// Audiences for the review; empty means the API server's own.
	Audiences []string
	TTL       time.Duration
	Now       func() time.Time

	mu    sync.Mutex
	cache map[[32]byte]cached
}

type cached struct {
	user    string
	err     error
	expires time.Time
}

const maxCache = 1024

func (t *TokenReviewer) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Authenticate implements Authenticator.
func (t *TokenReviewer) Authenticate(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrNotAuthenticated
	}
	key := sha256.Sum256([]byte(token))
	ttl := t.TTL
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	t.mu.Lock()
	if c, ok := t.cache[key]; ok && t.now().Before(c.expires) {
		t.mu.Unlock()
		return c.user, c.err
	}
	t.mu.Unlock()

	tr := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token, Audiences: t.Audiences}}
	if err := t.C.Create(ctx, tr); err != nil {
		// An API error is not cached: the next call tries again.
		return "", fmt.Errorf("TokenReview: %w", err)
	}
	var user string
	var err error
	switch {
	case !tr.Status.Authenticated:
		err = ErrNotAuthenticated
		if tr.Status.Error != "" {
			err = fmt.Errorf("%w: %s", ErrNotAuthenticated, tr.Status.Error)
		}
	case tr.Status.User.Username == "":
		err = fmt.Errorf("%w: no username in the review", ErrNotAuthenticated)
	default:
		user = tr.Status.User.Username
	}
	t.mu.Lock()
	if t.cache == nil || len(t.cache) >= maxCache {
		t.cache = map[[32]byte]cached{}
	}
	t.cache[key] = cached{user: user, err: err, expires: t.now().Add(ttl)}
	t.mu.Unlock()
	return user, err
}

// Identity is the requester of one call.
type Identity struct {
	// Requester becomes spec.requestedFor: the authenticated username, or
	// policy.UnverifiedPrefix + the asserted name.
	Requester string
	Verified  bool
	// Reason says why the requester is unverified; empty when verified.
	Reason string
}

// Resolve decides the requester of a call from its bearer token. asserted
// (the operator name in MCP _meta) is used only inside an unverified
// requester, so a forged name can never make a request approvable or
// attribute it to someone else.
func Resolve(ctx context.Context, a Authenticator, token, asserted string) Identity {
	name := strings.TrimSpace(strings.TrimPrefix(asserted, policy.UnverifiedPrefix))
	if name == "" {
		name = Anonymous
	}
	unverified := func(reason string) Identity {
		return Identity{Requester: policy.UnverifiedPrefix + name, Reason: reason}
	}
	switch {
	case token == "":
		return unverified("no bearer token")
	case a == nil:
		return unverified("no authenticator configured")
	}
	user, err := a.Authenticate(ctx, token)
	if err != nil {
		if errors.Is(err, ErrNotAuthenticated) {
			return unverified("token not accepted by the API server")
		}
		return unverified("TokenReview failed")
	}
	if policy.IsUnverified(user) {
		// A username the API server returns cannot carry the mark.
		return unverified("authenticated username carries the unverified prefix")
	}
	return Identity{Requester: user, Verified: true}
}

// BearerToken extracts the token from an Authorization header value.
func BearerToken(header string) string {
	const p = "bearer "
	if len(header) > len(p) && strings.EqualFold(header[:len(p)], p) {
		return strings.TrimSpace(header[len(p):])
	}
	return ""
}
