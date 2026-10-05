// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
)

type fakeStore struct {
	rows []dbgen.InsertAuditEntryParams
	err  error
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg dbgen.InsertAuditEntryParams) error {
	if s.err != nil {
		return s.err
	}
	s.rows = append(s.rows, arg)
	return nil
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newWriter() (*Writer, *bytes.Buffer) {
	var log bytes.Buffer
	return NewWriter(logging.New(&log, logging.LevelInfo), clock.NewManual(t0)), &log
}

// TestRecord is C-03.FR-14's writer: the entry is appended with its actor, Transport, action, resource, diff and
// details on the business clock, and copied to the log as audit_entry.
func TestRecord(t *testing.T) {
	w, log := newWriter()
	s := &fakeStore{}
	addr := netip.MustParseAddr("192.0.2.1")
	err := w.Record(t.Context(), s, Entry{
		OrgID: 7, Actor: Actor{Kind: ActorUser, ID: 3, PublicID: "SRAAAAAAAAAAAA", TokenID: 9, TokenName: "ci"},
		Transport: TransportUI, Action: ActionProfileUpdated,
		Resource: Resource{Type: ResourceUser, PublicID: "SRAAAAAAAAAAAA", Name: "Alice"},
		Diff:     Changed("/name", "A", "Alice"), Details: map[string]any{"n": 1}, SourceAddress: addr,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := s.rows[0]
	if r.OrgID != 7 || !strings.HasPrefix(r.PublicID, "AE") || !r.At.Equal(t0) || r.ActorKind != "user" ||
		r.ActorUserID.Int64 != 3 || r.ActorServiceAccountID.Valid || r.ApiTokenID.Int64 != 9 ||
		r.TokenName.String != "ci" || r.Transport != "ui" || r.Action != "user.profile_updated" ||
		r.ResourceType.String != "user" || r.ResourceName.String != "Alice" || *r.SourceAddress != addr {
		t.Errorf("row = %+v", r)
	}
	if string(r.Diff) != `[{"pointer":"/name","before":"A","after":"Alice"}]` || string(r.Details) != `{"n":1}` {
		t.Errorf("diff %s details %s", r.Diff, r.Details)
	}
	var line map[string]any
	if err := json.Unmarshal(log.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{"event": "audit_entry", "level": "INFO", "actor_kind": "user",
		"actor": "SRAAAAAAAAAAAA", "action": "user.profile_updated", "resource_name": "Alice", "transport": "ui",
		"source_address": "192.0.2.1", "token_name": "ci", "entry": r.PublicID} {
		if line[k] != v {
			t.Errorf("log %s = %v, want %v", k, line[k], v)
		}
	}
}

func TestRecordEmptyDiffAndDetails(t *testing.T) {
	w, log := newWriter()
	s := &fakeStore{}
	if err := w.Record(t.Context(), s, Entry{OrgID: 1, Actor: Bootstrap, Transport: TransportSystem,
		Action: ActionUserCreated}); err != nil {
		t.Fatal(err)
	}
	if r := s.rows[0]; string(r.Diff) != "[]" || string(r.Details) != "{}" || r.ActorUserID.Valid ||
		r.SourceAddress != nil || r.ResourceType.Valid {
		t.Errorf("row = %+v", r)
	}
	if strings.Contains(log.String(), `"diff"`) || strings.Contains(log.String(), `"resource"`) {
		t.Errorf("empty fields were logged: %s", log)
	}
	sa := Actor{Kind: ActorServiceAccount, ID: 4}
	if err := w.Record(t.Context(), s, Entry{OrgID: 1, Actor: sa, Transport: TransportAPI, Action: "x.y"}); err != nil {
		t.Fatal(err)
	}
	if r := s.rows[1]; r.ActorServiceAccountID.Int64 != 4 || r.ActorUserID.Valid {
		t.Errorf("service account row = %+v", r)
	}
	cli := Actor{Kind: ActorCLI, Name: "ops-alice"}
	if err := w.Record(t.Context(), s, Entry{OrgID: 1, Actor: cli, Transport: TransportCLI, Action: "x.y"}); err != nil {
		t.Fatal(err)
	}
	if r := s.rows[2]; r.ActorName.String != "ops-alice" {
		t.Errorf("cli row = %+v", r)
	}
}

// TestRecordRefuses entries that break the rules of audit_log before writing anything.
func TestRecordRefuses(t *testing.T) {
	w, _ := newWriter()
	for name, e := range map[string]Entry{
		"action":            {Actor: System, Transport: TransportSystem, Action: "Created"},
		"user without id":   {Actor: Actor{Kind: ActorUser}, Transport: TransportUI, Action: "a.b"},
		"cli without name":  {Actor: Actor{Kind: ActorCLI}, Transport: TransportCLI, Action: "a.b"},
		"cli over ui":       {Actor: Actor{Kind: ActorCLI, Name: "x"}, Transport: TransportUI, Action: "a.b"},
		"system with id":    {Actor: Actor{Kind: ActorSystem, ID: 1}, Transport: TransportSystem, Action: "a.b"},
		"unknown actor":     {Actor: Actor{Kind: "robot"}, Transport: TransportSystem, Action: "a.b"},
		"unknown transport": {Actor: System, Transport: "smoke", Action: "a.b"},
		"details":           {Actor: System, Transport: TransportSystem, Action: "a.b", Details: map[string]any{"f": func() {}}},
		"diff":              {Actor: System, Transport: TransportSystem, Action: "a.b", Diff: []Change{{After: func() {}}}},
	} {
		s := &fakeStore{}
		if err := w.Record(t.Context(), s, e); err == nil || len(s.rows) != 0 {
			t.Errorf("%s: %v, %d rows", name, err, len(s.rows))
		}
	}
	boom := errors.New("boom")
	if err := w.Record(t.Context(), &fakeStore{err: boom}, Entry{Actor: System, Transport: TransportSystem,
		Action: "a.b"}); !errors.Is(err, boom) {
		t.Errorf("a failed insert: %v", err)
	}
}

func TestChangedAndActors(t *testing.T) {
	if Changed("/a", 1, 1) != nil || len(Changed("/a", "x", "y")) != 1 {
		t.Error("Changed")
	}
	if a := User(5, "SRX"); a.Kind != ActorUser || a.ID != 5 || a.PublicID != "SRX" {
		t.Errorf("User = %+v", a)
	}
	if NewStore(nil) == nil {
		t.Error("NewStore")
	}
}
