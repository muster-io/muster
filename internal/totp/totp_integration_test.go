// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package totp_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/totp"
	"github.com/muster-io/muster/internal/users"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 5, 12, 0, 10, 0, time.UTC)

type env struct {
	d        *db.DB
	orgID    int64
	sessions *auth.Service
	factors  *totp.Service
	org      *organization.Service
	real     *clock.Manual
	business *clock.Manual
}

// setup migrates a new database with the Organization, the Audit log partition of October 2026 and the bootstrap
// Admin ops@example.org, and wires sign-in and TOTP as the runtime does, with manual clocks.
func setup(t *testing.T, s dbtest.Server) env {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := d.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), log, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	realClock, business := clock.NewManual(t0), clock.NewManual(t0)
	w := audit.NewWriter(log, business)
	if err := users.EnsureBootstrapAdmin(ctx, users.NewStore(d.Pool), w, log, org.ID,
		users.Bootstrap{Email: "ops@example.org", Password: "ops-bootstrap-pass"}, t0); err != nil {
		t.Fatal(err)
	}
	k, err := keyring.Load(ctx, keyring.Env{Keys: keyring.DevelopmentKey, Source: keyring.SecretKeysVar}, true)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(ctx, keyring.NewStore(d.Pool), t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(ctx, log, st); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(d.Pool)
	roles, err := auth.LoadRoles(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewService(org.ID, store, k, w, business, roles)
	factors := totp.New(org.ID, totp.NewStore(d.Pool), k, w, clock.Clocks{Business: business, Real: realClock}, sessions)
	sessions.UseSecondFactor(factors)
	return env{d: d, orgID: org.ID, sessions: sessions, factors: factors, real: realClock, business: business,
		org: organization.NewService(org.ID, organization.NewSettingsStore(d.Pool), w, business)}
}

var addr = netip.MustParseAddr("198.51.100.4")

func (e env) signIn(t *testing.T, p auth.Proof) (auth.Session, error) {
	t.Helper()
	return e.sessions.SignIn(t.Context(), auth.SignInRequest{Login: "ops@example.org", Password: "ops-bootstrap-pass",
		Proof: p, Address: addr})
}

func (e env) code(t *testing.T, secret string) string {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totp.Code(seed, e.real.Now().Unix()/30)
}

func (e env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegrationTOTP runs C-03.FR-10, FR-11, FR-24 and FR-27 against PostgreSQL: the enrolment stores only
// ciphertext, the confirmation issues argon2id-hashed recovery codes, a sign-in then waits for the code, a replayed
// code is refused and counted, a code of the next step and a recovery code complete sessions once, the removal needs
// its proof, and the reset ends the sessions and is audited.
func TestIntegrationTOTP(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		sess, err := e.signIn(t, auth.Proof{})
		if err != nil || sess.State != auth.StateActive {
			t.Fatalf("sign-in without TOTP = %+v, %v", sess, err)
		}
		enrolment, err := e.factors.Begin(ctx, sess)
		if err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM user_totp WHERE org_id = $1 AND pending_seed_ciphertext IS NOT NULL
			AND position(convert_to($2, 'UTF8') IN pending_seed_ciphertext) = 0`, e.orgID, enrolment.Secret); n != 1 {
			t.Fatalf("%d pending seeds stored as ciphertext", n)
		}
		if _, err := e.factors.Begin(ctx, sess); err != nil {
			t.Fatalf("a second Begin replaces the pending seed: %v", err)
		}
		again, err := e.factors.Begin(ctx, sess)
		if err != nil {
			t.Fatal(err)
		}
		codes, err := e.factors.Confirm(ctx, sess, e.code(t, again.Secret), addr)
		if err != nil || len(codes) != totp.RecoveryCodes {
			t.Fatalf("Confirm = %v, %v", codes, err)
		}
		if _, err := e.factors.Begin(ctx, sess); !errors.Is(err, totp.ErrAlreadyEnrolled) {
			t.Errorf("Begin when enrolled: %v", err)
		}
		if n := e.count(t, `SELECT count(*) FROM user_recovery_codes WHERE org_id = $1 AND used_at IS NULL
			AND code_hash LIKE '$argon2id$%'`, e.orgID); n != totp.RecoveryCodes {
			t.Errorf("%d hashed recovery codes", n)
		}

		limited, err := e.signIn(t, auth.Proof{})
		if err != nil || limited.State != auth.StateTOTPRequired {
			t.Fatalf("the second step = %+v, %v", limited, err)
		}
		failures := metrics.LoginFailures.With(auth.FailureTOTP)
		before := failures.Get()
		if _, err := e.sessions.SubmitSecondFactor(ctx, limited, auth.Proof{TOTPCode: e.code(t, again.Secret)},
			addr); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("the confirming code replayed: %v", err)
		}
		if failures.Get()-before != 1 {
			t.Error("the replay was not counted")
		}
		e.real.Advance(totp.Period)
		active, err := e.sessions.SubmitSecondFactor(ctx, limited, auth.Proof{TOTPCode: e.code(t, again.Secret)}, addr)
		if err != nil || active.State != auth.StateActive {
			t.Fatalf("a code of the next step = %+v, %v", active, err)
		}
		if live, err := e.sessions.LiveSessions(ctx, []int64{active.ID, sess.ID}); err != nil || len(live) != 2 {
			t.Errorf("live sessions = %v, %v", live, err)
		}
		withCode, err := e.signIn(t, auth.Proof{RecoveryCode: codes[0]})
		if err != nil || withCode.State != auth.StateActive {
			t.Fatalf("a recovery code = %+v, %v", withCode, err)
		}
		if _, err := e.signIn(t, auth.Proof{RecoveryCode: codes[0]}); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("a used recovery code: %v", err)
		}
		if st, err := e.factors.Status(ctx, sess.User.ID); err != nil || st.RecoveryCodesRemaining != 9 {
			t.Errorf("Status = %+v, %v", st, err)
		}

		e.business.Advance(time.Minute) // past the throttle of the failures above
		if err := e.factors.Remove(ctx, sess, totp.Removal{Password: "wrong-password"}, addr); !errors.Is(err,
			auth.ErrInvalidCredentials) {
			t.Errorf("a wrong password: %v", err)
		}
		if st, _ := e.factors.Status(ctx, sess.User.ID); !st.Enrolled {
			t.Fatal("a wrong proof removed TOTP")
		}
		e.business.Advance(time.Minute)
		if err := e.factors.Remove(ctx, sess, totp.Removal{Password: "ops-bootstrap-pass"}, addr); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM user_recovery_codes WHERE org_id = $1`, e.orgID); n != 0 {
			t.Errorf("%d recovery codes after the removal", n)
		}

		// Under the policy everyone, the next sign-in demands enrolment; confirming it makes the session active.
		o, err := e.org.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in := organization.Input{Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel,
			SeverityMapping: o.SeverityMapping, SeverityStyles: o.SeverityStyles, CriticalIsUrgent: o.CriticalIsUrgent,
			InstanceLabels: o.InstanceLabels, Retention: o.Retention, TOTPRequired: organization.TOTPEveryone,
			OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace / time.Second)}
		if _, err := e.org.Update(ctx, audit.User(sess.User.ID, sess.User.PublicID), audit.TransportUI, addr,
			&o.Version, in); err != nil {
			t.Fatal(err)
		}
		enrol, err := e.signIn(t, auth.Proof{})
		if err != nil || enrol.State != auth.StateTOTPEnrolmentRequired {
			t.Fatalf("under everyone = %+v, %v", enrol, err)
		}
		if live, _ := e.sessions.LiveSessions(ctx, []int64{enrol.ID}); len(live) != 0 {
			t.Error("a limited session counts as live")
		}
		third, err := e.factors.Begin(ctx, enrol)
		if err != nil {
			t.Fatal(err)
		}
		e.real.Advance(totp.Period)
		if _, err := e.factors.Confirm(ctx, enrol, e.code(t, third.Secret), addr); err != nil {
			t.Fatal(err)
		}
		if got, err := e.sessions.Authenticate(ctx, enrol.Cookie()); err != nil || got.State != auth.StateActive {
			t.Errorf("after the confirmation = %+v, %v", got, err)
		}

		if err := e.factors.Reset(ctx, audit.User(sess.User.ID, sess.User.PublicID), audit.TransportUI, addr,
			sess.User.PublicID); !errors.Is(err, totp.ErrOwnTOTP) {
			t.Errorf("an Admin's reset of their own TOTP: %v", err)
		}
		if err := e.factors.Reset(ctx, audit.CLI("ops"), audit.TransportCLI, netip.Addr{}, sess.User.PublicID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.sessions.Authenticate(ctx, enrol.Cookie()); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("a session after the reset: %v", err)
		}
		if n := e.count(t, `SELECT count(*) FROM sessions WHERE org_id = $1 AND end_reason = 'totp_reset'`,
			e.orgID); n == 0 {
			t.Error("no session ended with totp_reset")
		}
		if _, err := e.factors.Begin(ctx, enrol); err != nil {
			t.Fatal(err)
		}
		id, removed, err := e.factors.ResetByLogin(ctx, "ops-cli", "OPS@example.org")
		if err != nil || !removed || id != sess.User.PublicID {
			t.Errorf("the CLI reset of a begun enrolment = %s, %v, %v", id, removed, err)
		}
		var actions []string
		rows, err := e.d.Pool.Query(ctx, `SELECT action FROM audit_log WHERE org_id = $1 AND
			(action LIKE 'totp.%' OR action IN ('organization.updated', 'session.second_factor_failed')) ORDER BY at, id`,
			e.orgID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				t.Fatal(err)
			}
			actions = append(actions, a)
		}
		want := []string{totp.ActionEnrolled, auth.ActionSecondFactorFailed, auth.ActionSecondFactorFailed,
			totp.ActionRemoved, organization.ActionUpdated, totp.ActionEnrolled, totp.ActionReset, totp.ActionReset}
		if !slices.Equal(actions, want) {
			t.Errorf("audit = %v\nwant %v", actions, want)
		}
	})
}
