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

// Package audit writes the copilot's audit trail: an append-only JSONL file in
// which each line carries the SHA-256 of the previous line, and Kubernetes
// Events on the objects involved.
package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
)

// Kind classifies an audit entry.
type Kind string

const (
	KindToolCall        Kind = "tool_call"
	KindPolicyDecision  Kind = "policy_decision"
	KindDraftCreated    Kind = "draft_created"
	KindRequestCreated  Kind = "request_created"
	KindDecisionSeen    Kind = "decision_seen"
	KindApproved        Kind = "approved"
	KindRejected        Kind = "rejected"
	KindDecisionRefused Kind = "decision_refused"
	KindExecuted        Kind = "executed"
	KindExecutionFailed Kind = "execution_failed"
	KindExpired         Kind = "expired"
)

// Entry is one audit line.
type Entry struct {
	Seq       int64          `json:"seq"`
	Time      time.Time      `json:"time"`
	Kind      Kind           `json:"kind"`
	Actor     string         `json:"actor,omitempty"`
	SessionID string         `json:"sessionID,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	Object    string         `json:"object,omitempty"`
	Code      string         `json:"code,omitempty"`
	Message   string         `json:"message,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
	Prev      string         `json:"prev"`
	Hash      string         `json:"hash"`
}

// genesis is the Prev value of the first entry.
const genesis = "0000000000000000000000000000000000000000000000000000000000000000"

func hashOf(e Entry) (string, error) {
	e.Hash = ""
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Writer appends entries to a JSONL file. Safe for concurrent use.
type Writer struct {
	mu   sync.Mutex
	w    io.Writer
	f    *os.File
	seq  int64
	prev string
	red  *redact.Redactor
	now  func() time.Time
}

// Open opens (or creates) an audit file and continues its chain. It refuses
// to append to a file whose chain does not verify.
func Open(path string, red *redact.Redactor) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	n, last, err := verify(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("existing audit file %s does not verify: %w", path, err)
	}
	w := &Writer{w: f, f: f, seq: n, prev: genesis, red: red, now: time.Now}
	if n > 0 {
		w.prev = last
	}
	return w, nil
}

// NewWriter writes to any io.Writer, starting a new chain. Used in tests.
func NewWriter(out io.Writer, red *redact.Redactor) *Writer {
	return &Writer{w: out, prev: genesis, red: red, now: time.Now}
}

// Append redacts, chains, and writes one entry.
func (w *Writer) Append(e Entry) (Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e.Message = w.red.String(e.Message)
	if e.Detail != nil {
		e.Detail = w.red.Value(e.Detail).(map[string]any)
	}
	e.Seq = w.seq + 1
	if e.Time.IsZero() {
		e.Time = w.now().UTC()
	}
	e.Prev = w.prev
	h, err := hashOf(e)
	if err != nil {
		return e, err
	}
	e.Hash = h
	b, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	if _, err := w.w.Write(append(b, '\n')); err != nil {
		return e, err
	}
	if w.f != nil {
		if err := w.f.Sync(); err != nil {
			return e, err
		}
	}
	w.seq, w.prev = e.Seq, e.Hash
	return e, nil
}

// Close closes the underlying file, if any.
func (w *Writer) Close() error {
	if w.f != nil {
		return w.f.Close()
	}
	return nil
}

// ErrChain reports a break in the hash chain.
var ErrChain = errors.New("audit chain broken")

// Verify checks every line of an audit stream and returns the number of entries.
func Verify(r io.Reader) (int64, error) {
	n, _, err := verify(r)
	return n, err
}

func verify(r io.Reader) (int64, string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	prev, n := genesis, int64(0)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			return n, prev, fmt.Errorf("%w: line %d: %v", ErrChain, n+1, err)
		}
		if e.Seq != n+1 {
			return n, prev, fmt.Errorf("%w: line %d has seq %d", ErrChain, n+1, e.Seq)
		}
		if e.Prev != prev {
			return n, prev, fmt.Errorf("%w: seq %d prev hash does not match", ErrChain, e.Seq)
		}
		h, err := hashOf(e)
		if err != nil {
			return n, prev, err
		}
		if h != e.Hash {
			return n, prev, fmt.Errorf("%w: seq %d content does not match its hash", ErrChain, e.Seq)
		}
		prev, n = e.Hash, n+1
	}
	return n, prev, sc.Err()
}

// EventSink writes core/v1 Events about an object.
type EventSink struct {
	C         client.Client
	Scheme    *runtime.Scheme
	Component string
	Red       *redact.Redactor
}

// Emit creates one Event. Event write failures are returned so callers can
// record them in the JSONL trail; they never block the operation itself.
func (s *EventSink) Emit(ctx context.Context, obj client.Object, eventType, reason, message string) error {
	gvk, err := apiutil.GVKForObject(obj, s.Scheme)
	if err != nil {
		return err
	}
	now := metav1.NewTime(time.Now())
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{GenerateName: obj.GetName() + ".", Namespace: obj.GetNamespace()},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind,
			Name: obj.GetName(), Namespace: obj.GetNamespace(), UID: obj.GetUID(),
			ResourceVersion: obj.GetResourceVersion(),
		},
		Reason:              reason,
		Message:             s.Red.String(message),
		Type:                eventType,
		Source:              corev1.EventSource{Component: s.Component},
		ReportingController: "fleetops.alexbahel.github.io/" + s.Component,
		ReportingInstance:   s.Component,
		FirstTimestamp:      now,
		LastTimestamp:       now,
		Count:               1,
	}
	return s.C.Create(ctx, ev)
}
