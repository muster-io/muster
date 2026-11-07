// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
)

// hmacKeys sign with HMAC-SHA256 under each key id, the first one active, so that a changed byte does not verify.
type hmacKeys struct{ ids []string }

func (k hmacKeys) mac(id string, msg []byte) []byte {
	h := hmac.New(sha256.New, []byte(id))
	h.Write(msg)
	return h.Sum(nil)
}

func (k hmacKeys) Sign(_ keyring.Purpose, msg []byte) ([]byte, string, error) {
	return k.mac(k.ids[0], msg), k.ids[0], nil
}

func (k hmacKeys) VerifyPrefix(_ keyring.Purpose, id string, msg, prefix []byte) (bool, error) {
	return hmac.Equal(k.mac(id, msg)[:len(prefix)], prefix), nil
}

// KeyIDs is a copy, as the Keyring's, which buttons.Verify filters in place.
func (k hmacKeys) KeyIDs() []string { return slices.Clone(k.ids) }

var ackKeys = hmacKeys{ids: []string{"k-0123456789abcdef"}}

// ackCall is the call CA0000000000P1 of the gateway 10 to petr, a Responder, as GetAcknowledgedCall reads it.
var ackCall = dbgen.GetAcknowledgedCallRow{ID: 5, PublicID: "CA0000000000P1", State: CallAccepted, UserID: 7,
	UserPublicID: "SR0000000000PE", UserRole: "responder", UserStatus: "active",
	UserLanguage: pgtype.Text{String: "en", Valid: true}}

// ackQueries are the reads of the tests: the call and its Alert Groups, or the error of a read.
type ackQueries struct {
	call   *dbgen.GetAcknowledgedCallRow
	groups []dbgen.ListCallGroupsRow
	fail   map[string]error
	asked  dbgen.GetAcknowledgedCallParams
}

func (q *ackQueries) GetAcknowledgedCall(_ context.Context, a dbgen.GetAcknowledgedCallParams) (
	dbgen.GetAcknowledgedCallRow, error) {
	q.asked = a
	if err := q.fail["GetAcknowledgedCall"]; err != nil {
		return dbgen.GetAcknowledgedCallRow{}, err
	}
	if q.call == nil || a.OrgID != 1 || a.ConnectionID != 10 || a.PublicID != q.call.PublicID {
		return dbgen.GetAcknowledgedCallRow{}, pgx.ErrNoRows
	}
	return *q.call, nil
}

func (q *ackQueries) ListCallGroups(_ context.Context, a dbgen.ListCallGroupsParams) ([]dbgen.ListCallGroupsRow,
	error) {
	if err := q.fail["ListCallGroups"]; err != nil {
		return nil, err
	}
	if a.OrgID != 1 || a.CallID != q.call.ID {
		return nil, errors.New("the wrong call")
	}
	return q.groups, nil
}

// ackCommands answer the Acknowledge of each Alert Group with its scripted error, and record the callers.
type ackCommands struct {
	errs    map[string]error
	callers []groups.Caller
	groups  []string
}

func (c *ackCommands) Acknowledge(_ context.Context, caller groups.Caller, publicID string) (groups.Result, error) {
	c.callers, c.groups = append(c.callers, caller), append(c.groups, publicID)
	if err := c.errs[publicID]; err != nil {
		return groups.Result{}, err
	}
	return groups.Result{Outcome: groups.OutcomeDone}, nil
}

type ackEnv struct {
	a   *Acknowledgements
	q   *ackQueries
	cmd *ackCommands
	log *bytes.Buffer
}

func newAck(groupNumbers ...int64) *ackEnv {
	call := ackCall
	e := &ackEnv{q: &ackQueries{call: &call, fail: map[string]error{}}, cmd: &ackCommands{errs: map[string]error{}},
		log: &bytes.Buffer{}}
	for _, n := range groupNumbers {
		e.q.groups = append(e.q.groups, dbgen.ListCallGroupsRow{ID: n, PublicID: groupID(n), Number: n})
	}
	e.a = &Acknowledgements{OrgID: 1, Queries: e.q, Commands: e.cmd, Keys: ackKeys,
		Roles: auth.Roles{"responder": {groups.PermissionAcknowledge}, "viewer": {"alert-groups:read"}},
		Log:   logging.New(e.log, logging.LevelInfo)}
	return e
}

// groupID is the public_id of the Alert Group #n of the tests.
func groupID(n int64) string { return "AG00000000000" + string(rune('0'+n%10)) }

// data is the action data a call's attempt carries, signed with keys.
func data(t *testing.T, keys buttons.Keys, a buttons.Action) string {
	t.Helper()
	d, _, err := buttons.Sign(keys, a)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func callData(t *testing.T) string {
	return data(t, ackKeys, buttons.Action{Subject: buttons.SubjectCall, PublicID: ackCall.PublicID,
		Command: buttons.CommandAcknowledge})
}

func (e *ackEnv) run(t *testing.T, d string) string {
	t.Helper()
	answer, err := e.a.Acknowledged(t.Context(), bridge.Conn{ID: 10, PublicID: "CN00000000GW10"},
		bridge.Event{ID: "e1", Type: bridge.TypeNotificationAcknowledged, NotificationID: "n1", Data: d})
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

// TestAcknowledged is C-30.FR-6, C-30.AC-4 and C-12.FR-3 at the gateways: verified data of a call of the Connection
// runs Acknowledge for each of its Alert Groups as the person the call was for, with the Transport gateway and the
// Permissions of their Role, and the answer names what was acknowledged, who has the Alert Groups already taken, what
// is resolved and what else was refused, by #N when the call is about more than one, in the person's profile
// language, Russian plurals included.
func TestAcknowledged(t *testing.T) {
	e := newAck(2)
	if got := e.run(t, callData(t)); got != "Acknowledged: #2" {
		t.Errorf("answer %q", got)
	}
	c := e.cmd.callers[0]
	if c.Actor != audit.User(7, "SR0000000000PE") || c.Transport != audit.TransportGateway || c.Gateway != 10 ||
		len(c.Permissions) != 1 || c.Permissions[0] != groups.PermissionAcknowledge || e.cmd.groups[0] != groupID(2) {
		t.Errorf("caller %+v of %v", c, e.cmd.groups)
	}
	if e.q.asked != (dbgen.GetAcknowledgedCallParams{OrgID: 1, ConnectionID: 10, PublicID: ackCall.PublicID}) {
		t.Errorf("asked %+v", e.q.asked)
	}

	taken := &groups.RefusedError{Code: groups.CodeOwnedByOther, OwnerName: "Ivan"}
	resolved := &groups.RefusedError{Code: groups.CodeAlreadyResolved}
	for _, c := range []struct {
		name    string
		numbers []int64
		errs    map[int64]error
		lang    string
		want    string
	}{
		{"two", []int64{3, 2}, nil, "en", "Acknowledged: #2, #3"},
		{"taken", []int64{2}, map[int64]error{2: taken}, "en", "Already taken by Ivan"},
		{"taken by the same person", []int64{2},
			map[int64]error{2: &groups.RefusedError{Code: groups.CodeOwnedByOther, OwnerName: "Petr"}}, "en",
			"Already taken by Petr"},
		{"taken by an owner without a name", []int64{2},
			map[int64]error{2: &groups.RefusedError{Code: groups.CodeOwnedByOther}}, "en",
			"Already taken by another user"},
		{"russian taken by an owner without a name", []int64{2, 3},
			map[int64]error{2: &groups.RefusedError{Code: groups.CodeOwnedByOther},
				3: &groups.RefusedError{Code: groups.CodeOwnedByOther}}, "ru",
			"Уже взяты другим пользователем: #2, #3"},
		{"resolved", []int64{2}, map[int64]error{2: resolved}, "en", "Already resolved"},
		{"gone", []int64{2}, map[int64]error{2: groups.ErrNotFound}, "en", "Nothing to acknowledge"},
		{"none left", nil, nil, "ru", "Подтверждать нечего"},
		{"disabled midway", []int64{1, 2, 3}, map[int64]error{
			2: &groups.ForbiddenError{Permission: groups.PermissionAcknowledge},
			3: &groups.ForbiddenError{Permission: groups.PermissionAcknowledge}}, "en",
			"Acknowledged: #1. Not acknowledged: #2, #3"},
		{"mixed", []int64{1, 2, 3, 4, 5, 6}, map[int64]error{2: taken, 3: resolved, 4: groups.ErrNotFound,
			5: &groups.RefusedError{Code: groups.CodeOwnerMustBeUser},
			6: &groups.RefusedError{Code: groups.CodeOwnedByOther, OwnerName: "Alice"}}, "en",
			"Acknowledged: #1. Already taken by Ivan: #2. Already taken by Alice: #6. Already resolved: #3. " +
				"Not acknowledged: #5"},
		{"russian one", []int64{2}, nil, "ru", "Подтверждена: #2"},
		{"russian two", []int64{2, 3}, map[int64]error{}, "ru", "Подтверждены: #2, #3"},
		{"russian taken", []int64{2}, map[int64]error{2: taken}, "ru", "Уже взята пользователем Ivan"},
		{"russian resolved", []int64{2}, map[int64]error{2: resolved}, "ru", "Уже закрыта"},
		{"russian many", []int64{1, 2, 3}, map[int64]error{1: taken, 2: taken, 3: resolved}, "ru",
			"Уже взяты пользователем Ivan: #1, #2. Уже закрыта: #3"},
		{"no language", []int64{2}, map[int64]error{2: resolved}, "", "Already resolved"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAck(c.numbers...)
			e.q.call.UserLanguage = pgtype.Text{String: c.lang, Valid: c.lang != ""}
			for n, err := range c.errs {
				e.cmd.errs[groupID(n)] = err
			}
			if got := e.run(t, callData(t)); got != c.want {
				t.Errorf("answer %q, want %q", got, c.want)
			}
			if len(e.cmd.groups) != len(c.numbers) {
				t.Errorf("acknowledged %v", e.cmd.groups)
			}
			if strings.Contains(e.log.String(), "gateway_acknowledge_refused") {
				t.Errorf("a refusal of the commands was logged as the gateway's: %s", e.log)
			}
		})
	}
}

// TestAcknowledgedRefused is C-30.FR-6, ADR-0013 and C-18.FR-8 at the gateways: data that is not Muster's signature of
// a call — tampered, too long, of another subject or Command, or signed with a key that left the Keyring — and the data
// of a call of another Connection or Organization run nothing, are logged as gateway_acknowledge_refused with the call
// and the reason and answered in English, that it expired for a key that left the Keyring; a disabled or deleted person
// runs nothing and is answered in their language; a Viewer is refused by the dispatcher and answered once, without
// trying the other Alert Groups.
func TestAcknowledgedRefused(t *testing.T) {
	good := callData(t)
	// A character in the middle, of the call's public_id: every bit of it is signed.
	tampered := []byte(good)
	tampered[10] = map[bool]byte{true: 'A', false: 'B'}[tampered[10] != 'A']
	old := hmacKeys{ids: []string{"k-fedcba9876543210"}}
	for _, c := range []struct {
		name, data, call, reason string
	}{
		{"tampered", string(tampered), "", AckNotVerified},
		{"too long", strings.Repeat("A", buttons.MaxLen+1), "", AckNotVerified},
		{"not signed", "e2e-data", "", AckNotVerified},
		{"a root message", data(t, ackKeys, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AG000000000002",
			Command: buttons.CommandAcknowledge}), "", AckNotVerified},
		{"another command", data(t, ackKeys, buttons.Action{Subject: buttons.SubjectCall, PublicID: ackCall.PublicID,
			Command: buttons.CommandResolve}), "", AckNotVerified},
		{"expired", data(t, old, buttons.Action{Subject: buttons.SubjectCall, PublicID: ackCall.PublicID,
			Command: buttons.CommandAcknowledge}), ackCall.PublicID, AckExpired},
		{"another connection", data(t, ackKeys, buttons.Action{Subject: buttons.SubjectCall,
			PublicID: "CA0000000000X9", Command: buttons.CommandAcknowledge}), "CA0000000000X9", AckUnknownCall},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAck(2)
			e.q.call.UserLanguage = pgtype.Text{String: "ru", Valid: true}
			answer := "This call could not be verified; nothing was changed"
			if c.reason == AckExpired {
				answer = "This call has expired; nothing was changed"
			}
			if got := e.run(t, c.data); got != answer {
				t.Errorf("answer %q", got)
			}
			want := `"event":"gateway_acknowledge_refused","call":"` + c.call +
				`","connection":"CN00000000GW10","reason":"` + c.reason + `"`
			if len(e.cmd.groups) != 0 || !strings.Contains(e.log.String(), want) {
				t.Errorf("ran %v, logged %s", e.cmd.groups, e.log)
			}
		})
	}
	for _, c := range []struct{ status, reason, lang, want string }{
		{"disabled", AckDisabled, "en", "Your Muster account is disabled; nothing was changed"},
		{"deleted", AckDeleted, "ru", "Ваша учётная запись Muster отключена; ничего не изменилось"},
	} {
		e := newAck(2)
		e.q.call.UserStatus, e.q.call.UserLanguage = c.status, pgtype.Text{String: c.lang, Valid: true}
		if got := e.run(t, callData(t)); got != c.want || len(e.cmd.groups) != 0 ||
			!strings.Contains(e.log.String(), `"call":"CA0000000000P1","connection":"CN00000000GW10","reason":"`+
				c.reason+`"`) {
			t.Errorf("%s: answer %q, ran %v, logged %s", c.status, got, e.cmd.groups, e.log)
		}
	}
	e := newAck(2, 3)
	e.q.call.UserRole = "viewer"
	e.cmd.errs[groupID(2)] = &groups.ForbiddenError{Permission: groups.PermissionAcknowledge}
	if got := e.run(t, callData(t)); got != "You are not permitted to acknowledge Alert Groups; nothing was changed" ||
		len(e.cmd.groups) != 1 || e.cmd.callers[0].Permissions[0] != "alert-groups:read" {
		t.Errorf("a viewer: %q, ran %v", got, e.cmd.groups)
	}
}

// TestAcknowledgedFailures: a read that fails, or a Command that fails otherwise than by a refusal, fails the event,
// so that it comes again.
func TestAcknowledgedFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, q := range []string{"GetAcknowledgedCall", "ListCallGroups", "command"} {
		e := newAck(2)
		if q == "command" {
			e.cmd.errs[groupID(2)] = boom
		} else {
			e.q.fail[q] = boom
		}
		if _, err := e.a.Acknowledged(t.Context(), bridge.Conn{ID: 10, PublicID: "CN00000000GW10"},
			bridge.Event{ID: "e1", Data: callData(t)}); !errors.Is(err, boom) {
			t.Errorf("%s failing = %v", q, err)
		}
	}
	if NewAckQueries(nil) == nil {
		t.Error("no queries")
	}
	// The Keyring keeps the key of a call as long as bridge.event_max_age lets its acknowledgement act.
	if keyring.CallAcknowledgeWindow != bridge.EventMaxAge {
		t.Errorf("keyring.CallAcknowledgeWindow %v, bridge.EventMaxAge %v", keyring.CallAcknowledgeWindow,
			bridge.EventMaxAge)
	}
}
