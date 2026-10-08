// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// ListMattermostDestinations names each Destination of targets dest-<id>.
func (s *memStore) ListMattermostDestinations(_ context.Context, org int64) ([]dbgen.ListMattermostDestinationsRow,
	error) {
	if err := s.fail["ListMattermostDestinations"]; err != nil || org != 1 {
		return nil, err
	}
	var out []dbgen.ListMattermostDestinationsRow
	for _, id := range slices.Sorted(maps.Keys(s.targets)) {
		d := s.targets[id]
		out = append(out, dbgen.ListMattermostDestinationsRow{ID: id, PublicID: fmt.Sprintf("DS%012d", id),
			Name: fmt.Sprintf("dest-%d", id), ConnectionID: pgtype.Int8{Int64: d.connection, Valid: true},
			MattermostTeamID:    pgtype.Text{String: d.teamID, Valid: true},
			MattermostChannelID: pgtype.Text{String: d.channelID, Valid: true}})
	}
	return out, nil
}

func findings(t *testing.T, svc *connections.Service, q connections.DoctorQueries) string {
	t.Helper()
	found, err := svc.Doctor(t.Context(), q, 1500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range found {
		line := f.Kind + " " + f.Name + ": ok"
		if f.OK() && f.Warning != "" {
			line = f.Kind + " " + f.Name + ": warning: " + f.Warning
		}
		if !f.OK() {
			line = f.Kind + " " + f.Name + ": " + f.Message
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestDoctor is C-02.FR-14: muster doctor checks every Mattermost Connection and Destination that is not deleted, a
// Telegram one aside, in the background class, and records nothing; a failing step gives its message, and a bot that
// may not make ephemeral posts the hint that press answers show in the Thread (D284).
func TestDoctor(t *testing.T) {
	e := newEnv(t)
	mm := e.create(t, "mm")
	broken := e.create(t, "broken")
	e.store.rows[broken.ID].row.BotTokenCiphertext = []byte("garbage")
	gone := e.create(t, "gone")
	e.store.rows[gone.ID].deleted = true
	e.store.addTelegram()
	e.store.targets[7] = memTarget{connection: mm.ID, teamID: fakemattermost.TeamID,
		channelID: fakemattermost.ChannelAlerts}
	e.store.targets[8] = memTarget{connection: mm.ID, teamID: fakemattermost.TeamID,
		channelID: fakemattermost.ChannelNoBot}
	e.store.targets[9] = memTarget{connection: broken.ID, teamID: "t", channelID: "c"}
	e.store.targets[10] = memTarget{connection: gone.ID, teamID: "t", channelID: "c"}
	e.store.targets[11] = memTarget{connection: mm.ID, teamID: "team-other", channelID: fakemattermost.ChannelAlerts}
	background := metrics.ClientRequests.With(string(outbound.ClassBackground), string(outbound.OutcomeOK))
	before, audits, txs := background.Get(), len(e.store.audit), e.store.tx

	got := findings(t, e.svc, e.store)
	open := "open the bot token of the connection " + broken.PublicID + ": cannot decrypt"
	want := []string{"connection mm: warning: " + mattermost.HintPressAnswersInThread, "connection broken: " + open, "destination dest-7: ok",
		"destination dest-8: " + mattermost.MessageNotMember, "destination dest-9: its Connection failed its check: " + open,
		"destination dest-10: its Connection is deleted", "destination dest-11: " + mattermost.MessageOtherTeam}
	lines := strings.Split(got, "\n")
	if len(lines) != len(want) {
		t.Fatalf("findings:\n%s", got)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
	if n := background.Get() - before; n < 5 {
		t.Errorf("%d background requests", n)
	}
	if len(e.store.audit) != audits || e.store.tx != txs || len(e.store.bots) != 0 || len(e.path.subjects) != 0 {
		t.Errorf("the doctor wrote: audit %d, transactions %d, bots %d, interactive %d", len(e.store.audit)-audits,
			e.store.tx-txs, len(e.store.bots), len(e.path.subjects))
	}

	e.configure(t, `{"bot_system_admin":true}`)
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection mm: ok\n") {
		t.Errorf("an admin bot:\n%s", got)
	}
	e.configure(t, `{"bot_system_admin":false}`)
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/v4/roles/names", Status: 503, Times: 100}); err != nil {
		t.Fatal(err)
	}
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection mm: ok\n") {
		t.Errorf("roles unreadable:\n%s", got)
	}
	e.fake.ResetFaults()
	e.configure(t, `{"revoked_tokens":["`+botToken+`"]}`)
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection mm: "+mattermost.MessageTokenInvalid) ||
		!strings.Contains(got, "destination dest-7: its Connection failed its check: "+
			mattermost.MessageTokenInvalid) {
		t.Errorf("revoked:\n%s", got)
	}
	e.configure(t, `{"revoked_tokens":[]}`)
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/v4/users/me", Status: 503, Times: 100}); err != nil {
		t.Fatal(err)
	}
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection mm: ") ||
		!strings.Contains(strings.SplitN(got, "\n", 2)[0], "answered 503") {
		t.Errorf("unavailable:\n%s", got)
	}
	if strings.Contains(e.log.String(), botToken) {
		t.Error("the bot token reached the log")
	}

	for _, name := range []string{"ListConnections", "ListMattermostDestinations"} {
		e.store.fail[name] = errors.New("down")
		if _, err := e.svc.Doctor(t.Context(), e.store, time.Second); err == nil || !strings.Contains(err.Error(), "down") {
			t.Errorf("%s failing = %v", name, err)
		}
		delete(e.store.fail, name)
	}

	noKeys := connections.New(connections.Config{OrgID: 1})
	if got := findings(t, noKeys, e.store); !strings.HasPrefix(got,
		"connection mm: the bot token cannot be opened: the master keys could not be loaded\n") {
		t.Errorf("without a keyring:\n%s", got)
	}
}
