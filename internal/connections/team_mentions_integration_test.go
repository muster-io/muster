// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package connections_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/teams"
)

// teamsBy is who changes Teams in these tests.
var teamsBy = teams.Requester{Actor: audit.System, Transport: audit.TransportSystem}

// TestIntegrationDeleteTeamMentionsLocks is C-25.FR-11 against PostgreSQL 14 and 17: the deletion of a Connection
// takes the Teams of its Team mentions before the Connection, the order of every path that takes both, so no save
// deadlocks with it, and it deletes every Team mention of the Connection with an Audit log entry and a new version for
// each Team.
func TestIntegrationDeleteTeamMentionsLocks(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupLiveEnv(t, s)
		svc := teams.New(teams.Config{OrgID: e.orgID, Store: teams.NewStore(e.d.Pool),
			Audit: audit.NewWriter(logging.New(e.log, logging.LevelInfo), e.business), Business: e.business})
		t.Run("a save of the team", func(t *testing.T) { e.saveDuringDeletion(t, svc, 0, "FOR NO KEY UPDATE") })
		t.Run("a save of a destination", func(t *testing.T) { e.saveDuringDeletion(t, svc, 1, "FOR SHARE") })
		t.Run("a first team mention while the deletion waits", func(t *testing.T) { e.newMentionDuringDeletion(t, svc) })
		t.Run("a save of a team while the deletion holds the connection", func(t *testing.T) {
			e.saveAfterConnectionLock(t, svc)
		})
		t.Run("a save that removes the team mention while the deletion waits", func(t *testing.T) {
			e.removedDuringDeletion(t, svc)
		})
	})
}

// waiting counts the sessions of the database that wait for a lock in a statement whose text contains like.
func (e *liveEnv) waiting(t *testing.T, like string) int64 {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
		AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%'`, like)
}

// mentioned creates a Connection and a Team through svc whose Team mention it is.
func (e *liveEnv) mentioned(t *testing.T, svc *teams.Service, n int) (connections.Connection, teams.Team) {
	t.Helper()
	c, err := e.conns.Create(t.Context(), by, input(fmt.Sprintf("locks-%d", n), e.fake.URL()))
	if err != nil {
		t.Fatal(err)
	}
	team, err := svc.Create(t.Context(), teamsBy, teams.Input{Name: fmt.Sprintf("Locks %d", n),
		Slug:     fmt.Sprintf("locks-%d", n),
		Mentions: &[]teams.MentionInput{{ConnectionID: c.PublicID, Group: "payments_team"}}})
	if err != nil {
		t.Fatal(err)
	}
	return c, team
}

// deleteAsync deletes c in the background; the channel gets its error.
func (e *liveEnv) deleteAsync(t *testing.T, c connections.Connection) <-chan error {
	deleted := make(chan error, 1)
	go func() { deleted <- e.conns.Delete(t.Context(), by, c.PublicID, nil) }()
	return deleted
}

// audited is the team.updated entry of the deletion of a Connection in the Audit log of the Team publicID: its Team
// mentions before and after, by group, and the Connection it names.
func (e *liveEnv) audited(t *testing.T, publicID string) ([]string, []string, string) {
	t.Helper()
	var diff, details []byte
	err := e.d.Pool.QueryRow(t.Context(), `SELECT diff, details FROM audit_log WHERE org_id = $1
		AND action = 'team.updated' AND resource_public_id = $2 AND details->>'reason' = 'connection_deleted'`,
		e.orgID, publicID).Scan(&diff, &details)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("no team.updated entry of the deletion for %s", publicID)
	}
	if err != nil {
		t.Fatal(err)
	}
	var changes []struct {
		Pointer string `json:"pointer"`
		Before  []struct {
			Group string `json:"group"`
		} `json:"before"`
		After []struct {
			Group string `json:"group"`
		} `json:"after"`
	}
	var why struct {
		Connection string `json:"connection"`
	}
	if err := json.Unmarshal(diff, &changes); err != nil || json.Unmarshal(details, &why) != nil ||
		len(changes) != 1 || changes[0].Pointer != "/team_mentions" {
		t.Fatalf("the audit diff %s, %v", diff, err)
	}
	var before, after []string
	for _, m := range changes[0].Before {
		before = append(before, m.Group)
	}
	for _, m := range changes[0].After {
		after = append(after, m.Group)
	}
	return before, after, why.Connection
}

// gone checks that the Connection c has no Team mention left and that the Team team has the version want.
func (e *liveEnv) gone(t *testing.T, c connections.Connection, team teams.Team, want int64) {
	t.Helper()
	if n := e.count(t, `SELECT count(*) FROM team_mentions WHERE org_id = $1 AND connection_id = $2`, e.orgID,
		c.ID); n != 0 {
		t.Errorf("%d team mentions of the deleted connection", n)
	}
	if got := e.count(t, `SELECT version FROM teams WHERE id = $1`, team.ID); got != want {
		t.Errorf("the version of %s is %d, want %d", team.Name, got, want)
	}
}

// saveDuringDeletion deletes a Connection with a Team mention while a transaction holds the Team's row with teamLock
// and then takes the Connection FOR SHARE and changes the Team mention, as the save of a Team or of a Destination
// does: the save ends while the deletion waits for the Team, and the deletion then deletes the Team mention as the save
// left it.
func (e *liveEnv) saveDuringDeletion(t *testing.T, svc *teams.Service, n int, teamLock string) {
	ctx := t.Context()
	c, team := e.mentioned(t, svc, n)
	tx, err := e.d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM teams WHERE org_id = $1 AND id = $2 `+teamLock, e.orgID,
		team.ID); err != nil {
		t.Fatal(err)
	}
	deleted := e.deleteAsync(t, c)
	waitUntil(t, "the deletion waiting for the team", func() bool { return e.waiting(t, "FROM teams t") == 1 })
	if _, err := tx.Exec(ctx, `SELECT id FROM connections WHERE org_id = $1 AND id = $2 FOR SHARE`, e.orgID,
		c.ID); err != nil {
		t.Fatalf("the save after the deletion started: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE team_mentions SET group_name = 'changed_team' WHERE org_id = $1
		AND team_id = $2`, e.orgID, team.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-deleted:
		t.Fatalf("the deletion ended before the save: %v", err)
	default:
	}
	if e.waiting(t, "FROM teams t") != 1 {
		t.Fatal("the deletion does not wait for the team")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("the save: %v", err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("the deletion: %v", err)
	}
	e.gone(t, c, team, 2)
	if before, after, conn := e.audited(t, team.PublicID); fmt.Sprint(before) != "[changed_team]" || len(after) != 0 ||
		conn != c.PublicID {
		t.Errorf("the audit diff: before %v, after %v, connection %s", before, after, conn)
	}
}

// newMentionDuringDeletion gives the Connection a first Team mention of another Team through teams.Update while its
// deletion waits for the Team of its first Team mention: the deletion, which locked only that Team, finds the new one
// once it holds the Connection, runs again, and deletes both, each with its Audit log entry and a new version.
func (e *liveEnv) newMentionDuringDeletion(t *testing.T, svc *teams.Service) {
	ctx := t.Context()
	c, x := e.mentioned(t, svc, 2)
	y, err := svc.Create(ctx, teamsBy, teams.Input{Name: "Locks Y", Slug: "locks-y"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := e.d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM teams WHERE org_id = $1 AND id = $2 FOR NO KEY UPDATE`, e.orgID,
		x.ID); err != nil {
		t.Fatal(err)
	}
	deleted := e.deleteAsync(t, c)
	waitUntil(t, "the deletion waiting for the first team", func() bool { return e.waiting(t, "FROM teams t") == 1 })
	if _, err := svc.Update(ctx, teamsBy, y.PublicID, nil, teams.Input{Name: y.Name, Slug: y.Slug,
		Mentions: &[]teams.MentionInput{{ConnectionID: c.PublicID, Group: "devops_team"}}}); err != nil {
		t.Fatalf("the first team mention of the second team: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("the deletion: %v", err)
	}
	e.gone(t, c, x, 2)
	e.gone(t, c, y, 3)
	for _, tc := range []struct {
		team  teams.Team
		group string
	}{{x, "payments_team"}, {y, "devops_team"}} {
		if before, after, conn := e.audited(t, tc.team.PublicID); fmt.Sprint(before) != "["+tc.group+"]" ||
			len(after) != 0 || conn != c.PublicID {
			t.Errorf("the audit diff of %s: before %v, after %v, connection %s", tc.team.Name, before, after, conn)
		}
	}
}

// saveAfterConnectionLock saves a Team mention through teams.Update while the deletion of its Connection holds the
// Connection and waits for a row of team_mentions: the save waits for the Connection, which its
// ResolveMentionConnections takes FOR SHARE after the Team's row, and then finds it deleted, so no Team mention of a
// deleted Connection is left.
func (e *liveEnv) saveAfterConnectionLock(t *testing.T, svc *teams.Service) {
	ctx := t.Context()
	c, x := e.mentioned(t, svc, 3)
	y, err := svc.Create(ctx, teamsBy, teams.Input{Name: "Locks Z", Slug: "locks-z"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := e.d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT team_id FROM team_mentions WHERE org_id = $1 AND team_id = $2 FOR UPDATE`,
		e.orgID, x.ID); err != nil {
		t.Fatal(err)
	}
	deleted := e.deleteAsync(t, c)
	waitUntil(t, "the deletion holding the connection", func() bool {
		return e.waiting(t, "DELETE FROM team_mentions") == 1
	})
	saved := make(chan error, 1)
	go func() {
		_, err := svc.Update(ctx, teamsBy, y.PublicID, nil, teams.Input{Name: y.Name, Slug: y.Slug,
			Mentions: &[]teams.MentionInput{{ConnectionID: c.PublicID, Group: "devops_team"}}})
		saved <- err
	}()
	waitUntil(t, "the save waiting for the connection", func() bool { return e.waiting(t, "FOR SHARE OF c") == 1 })
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("the deletion: %v", err)
	}
	err = <-saved
	if f, ok := errors.AsType[*teams.FieldError](err); !ok || f.Code != teams.CodeUnknownID ||
		f.Pointer != "/team_mentions/0/connection_id" {
		t.Fatalf("the save after the deletion = %v", err)
	}
	e.gone(t, c, x, 2)
	e.gone(t, c, y, 1)
}

// removedDuringDeletion removes the Team mention for the Connection of a Team that keeps another, in a save that holds
// the Team while the Connection's deletion waits for it: the deletion, which locked the Team, finds it without a Team
// mention for the Connection once it holds the Connection and leaves it as it is — no Audit log entry, no new version —
// and the Team keeps its other Team mention.
func (e *liveEnv) removedDuringDeletion(t *testing.T, svc *teams.Service) {
	ctx := t.Context()
	c, x := e.mentioned(t, svc, 4)
	other, err := e.conns.Create(ctx, by, input("locks-4-other", e.fake.URL()))
	if err != nil {
		t.Fatal(err)
	}
	x, err = svc.Update(ctx, teamsBy, x.PublicID, nil, teams.Input{Name: x.Name, Slug: x.Slug,
		Mentions: &[]teams.MentionInput{{ConnectionID: c.PublicID, Group: "payments_team"},
			{ConnectionID: other.PublicID, Group: "devops_team"}}})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := e.d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM teams WHERE org_id = $1 AND id = $2 FOR NO KEY UPDATE`, e.orgID,
		x.ID); err != nil {
		t.Fatal(err)
	}
	deleted := e.deleteAsync(t, c)
	waitUntil(t, "the deletion waiting for the team", func() bool { return e.waiting(t, "FROM teams t") == 1 })
	// The save keeps the other Connection's Team mention and removes the one of the Connection being deleted.
	if _, err := tx.Exec(ctx, `SELECT id FROM connections WHERE org_id = $1 AND id = $2 FOR SHARE`, e.orgID,
		other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM team_mentions WHERE org_id = $1 AND team_id = $2 AND connection_id = $3`,
		e.orgID, x.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("the deletion: %v", err)
	}
	e.gone(t, c, x, x.Version)
	if n := e.count(t, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'team.updated'
		AND resource_public_id = $2 AND details->>'reason' = 'connection_deleted'`, e.orgID, x.PublicID); n != 0 {
		t.Errorf("%d team.updated entries of the deletion for a team without its team mention", n)
	}
	if n := e.count(t, `SELECT count(*) FROM team_mentions WHERE org_id = $1 AND team_id = $2 AND connection_id = $3`,
		e.orgID, x.ID, other.ID); n != 1 {
		t.Errorf("the other team mention: %d rows", n)
	}
}
