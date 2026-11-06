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

package conversation

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// D75: a person cannot continue another person's conversation, even with
// its id.
func TestOwnerBound(t *testing.T) {
	s := New()
	id, c := s.Start("sam", nil)
	if !IDPattern.MatchString(id) {
		t.Fatalf("id %q", id)
	}
	if got, ok := s.Get(id, "sam"); !ok || got != c {
		t.Fatal("owner cannot continue")
	}
	for _, other := range []string{"dana", "unverified:sam", ""} {
		if _, ok := s.Get(id, other); ok {
			t.Fatalf("%q continued sam's conversation", other)
		}
	}
	if _, ok := s.Get("aaaaaaaaaaaaaaaaaaaaaaaaaa", "sam"); ok {
		t.Fatal("unknown id continued")
	}
}

// The id is never stored: the map holds its SHA-256 only.
func TestIDNotStored(t *testing.T) {
	s := New()
	id, _ := s.Start("sam", nil)
	for k := range s.m {
		if strings.Contains(k, id) || len(k) != 64 {
			t.Fatalf("key %q", k)
		}
	}
	if strings.Contains(strings.ToLower(reflectString(s)), id) {
		t.Fatal("id found in the store")
	}
}

func reflectString(s *Store) string {
	var b strings.Builder
	for k, c := range s.m {
		b.WriteString(k + c.Owner)
	}
	return b.String()
}

func TestTTLAndBounds(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := New()
	s.Now = func() time.Time { return now }
	id, _ := s.Start("sam", nil)
	now = now.Add(31 * time.Minute)
	if _, ok := s.Get(id, "sam"); ok {
		t.Fatal("expired conversation continued")
	}
	var ids []string
	for i := 0; i < 7; i++ {
		now = now.Add(time.Second)
		id, _ := s.Start("dana", nil)
		ids = append(ids, id)
	}
	if s.Len() != DefaultPerOwner {
		t.Fatalf("%d conversations, want %d per owner", s.Len(), DefaultPerOwner)
	}
	if _, ok := s.Get(ids[0], "dana"); ok {
		t.Fatal("oldest not dropped")
	}
	s.Max = 6
	for i := 0; i < 3; i++ {
		now = now.Add(time.Second)
		s.Start("eve", nil)
	}
	if s.Len() > 6 {
		t.Fatalf("%d conversations over Max 6", s.Len())
	}
}

func TestHistoryBoundedAndRedacted(t *testing.T) {
	c := &Conversation{}
	red := func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }
	for i := 0; i < 20; i++ {
		c.Append("user", "token sk-secret", red)
	}
	if len(c.History) != MaxMessages || strings.Contains(c.History[0].Text, "sk-secret") {
		t.Fatalf("history %+v", c.History)
	}
	c.Append("assistant", strings.Repeat("x", 20<<10), nil)
	if total(c.History) > MaxTextBytes {
		t.Fatalf("%d bytes kept", total(c.History))
	}
	c.State.Ask = "inspect"
	c.Reset()
	if !reflect.DeepEqual(c.State.Ask, "") || c.Preview != nil || len(c.History) == 0 {
		t.Fatal("reset")
	}
}
