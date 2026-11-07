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

// Package conversation is fleetops-agent's bounded, per-owner conversation store
// (docs/DESIGN.md §3.10.7, D75). A conversation is continued only by the
// requester who started it; its id is returned once and stored only as a
// SHA-256; it holds bounded person and assistant text and the dialogue state,
// never a token or a tool result.
package conversation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AlexBahel/fleet-ops-copilot/internal/dialogue"
	"github.com/AlexBahel/fleet-ops-copilot/internal/llm"
)

// Bounds (D75).
const (
	DefaultTTL      = 30 * time.Minute
	DefaultPerOwner = 5
	DefaultMax      = 1000
	MaxMessages     = 16
	MaxTextBytes    = 16 << 10
)

// IDPattern is the shape of a conversation id: 128 random bits, base32.
var IDPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// Message is one person or assistant message.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Conversation is one person's conversation.
type Conversation struct {
	Owner    string
	Created  time.Time
	LastUsed time.Time
	History  []Message
	State    dialogue.State
	Preview  *dialogue.Preview
	// Backend serves this conversation's model turns (a scripted backend
	// keeps its place across messages). Not a credential.
	Backend llm.Backend

	busy sync.Mutex
}

// Lock holds the conversation for one message; TryLock fails while another
// message of the same conversation is in flight.
func (c *Conversation) TryLock() bool { return c.busy.TryLock() }

// Unlock releases it.
func (c *Conversation) Unlock() { c.busy.Unlock() }

// Append adds a message, redacted, and keeps the last MaxMessages and at
// most MaxTextBytes of text.
func (c *Conversation) Append(role, text string, redact func(string) string) {
	if redact != nil {
		text = redact(text)
	}
	c.History = append(c.History, Message{Role: role, Text: text})
	if len(c.History) > MaxMessages {
		c.History = c.History[len(c.History)-MaxMessages:]
	}
	for total(c.History) > MaxTextBytes && len(c.History) > 1 {
		c.History = c.History[1:]
	}
	if len(c.History) == 1 && len(c.History[0].Text) > MaxTextBytes {
		c.History[0].Text = c.History[0].Text[len(c.History[0].Text)-MaxTextBytes:]
	}
}

func total(ms []Message) int {
	n := 0
	for _, m := range ms {
		n += len(m.Text)
	}
	return n
}

// LLMHistory is the history as model messages.
func (c *Conversation) LLMHistory() []llm.Message {
	out := make([]llm.Message, 0, len(c.History))
	for _, m := range c.History {
		out = append(out, llm.Message{Role: m.Role, Text: m.Text})
	}
	return out
}

// Reset ends the request under way (after filing, cancelling or a
// statement that it is not possible); the history stays.
func (c *Conversation) Reset() {
	c.State = dialogue.State{}
	c.Preview = nil
}

// Store keeps conversations in memory.
type Store struct {
	TTL      time.Duration
	PerOwner int
	Max      int
	Now      func() time.Time

	mu sync.Mutex
	m  map[string]*Conversation
}

// New returns a store with the bounds of D75.
func New() *Store {
	return &Store{TTL: DefaultTTL, PerOwner: DefaultPerOwner, Max: DefaultMax, m: map[string]*Conversation{}}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func key(id string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(id))) }

// Start opens a conversation for owner and returns its id, which is not
// stored anywhere but in the caller's hands.
func (s *Store) Start(owner string, backend llm.Backend) (string, *Conversation) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	id := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	now := s.now()
	c := &Conversation{Owner: owner, Created: now, LastUsed: now, Backend: backend}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]*Conversation{}
	}
	s.expire(now)
	s.evict(owner)
	s.m[key(id)] = c
	return id, c
}

// Get returns the conversation only when the id and the owner both match
// and it has not expired.
func (s *Store) Get(id, owner string) (*Conversation, bool) {
	if !IDPattern.MatchString(id) {
		return nil, false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(now)
	c, ok := s.m[key(id)]
	if !ok || c.Owner != owner {
		return nil, false
	}
	c.LastUsed = now
	return c, true
}

// Len is the number of conversations held.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

func (s *Store) expire(now time.Time) {
	for k, c := range s.m {
		if now.Sub(c.LastUsed) > s.TTL {
			delete(s.m, k)
		}
	}
}

// evict makes room for one more conversation of owner: the owner's oldest
// beyond PerOwner-1, and the oldest overall beyond Max-1.
func (s *Store) evict(owner string) {
	type kv struct {
		k string
		c *Conversation
	}
	var mine, all []kv
	for k, c := range s.m {
		all = append(all, kv{k, c})
		if c.Owner == owner {
			mine = append(mine, kv{k, c})
		}
	}
	oldest := func(xs []kv) {
		sort.Slice(xs, func(i, j int) bool { return xs[i].c.LastUsed.Before(xs[j].c.LastUsed) })
	}
	oldest(mine)
	for len(mine) >= s.PerOwner && len(mine) > 0 {
		delete(s.m, mine[0].k)
		mine = mine[1:]
	}
	all = all[:0]
	for k, c := range s.m {
		all = append(all, kv{k, c})
	}
	oldest(all)
	for len(all) >= s.Max && len(all) > 0 {
		delete(s.m, all[0].k)
		all = all[1:]
	}
}
