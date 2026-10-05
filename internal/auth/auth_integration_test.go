// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package auth_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
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
	"github.com/muster-io/muster/internal/users"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// setup migrates a new database, creates the Organization, the Audit log partition of October 2026 and the bootstrap
// Admin ops@example.org, and returns the pool and the Organization's id.
func setup(t *testing.T, s dbtest.Server) (*db.DB, int64) {
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
	w := audit.NewWriter(log, clock.NewManual(t0))
	if err := users.EnsureBootstrapAdmin(ctx, users.NewStore(d.Pool), w, log, org.ID,
		users.Bootstrap{Email: "ops@example.org", Password: "ops-bootstrap-pass"}, t0); err != nil {
		t.Fatal(err)
	}
	return d, org.ID
}

// referenceMatrix reads the allocation of Permissions to Roles from the table of reference.md.
func referenceMatrix(t *testing.T) map[string][]auth.Permission {
	t.Helper()
	f, err := os.Open("../../design/prd/l1/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string][]auth.Permission{}
	inTable := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "| Permission | Allows | Admin | Responder | Viewer |"):
			inTable = true
			continue
		case !inTable:
			continue
		case !strings.HasPrefix(line, "|"):
			inTable = false
			continue
		case strings.HasPrefix(line, "|---"):
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		var perms []auth.Permission
		for _, part := range strings.Split(cells[0], "`") {
			if p := strings.TrimSpace(part); strings.Contains(p, ":") && !strings.ContainsAny(p, " ,") {
				perms = append(perms, auth.Permission(p))
			}
		}
		for i, role := range auth.RoleNames {
			if strings.TrimSpace(cells[2+i]) == "✓" {
				out[role] = append(out[role], perms...)
			}
		}
	}
	for _, perms := range out {
		slices.Sort(perms)
	}
	return out
}

// TestIntegrationRoleMatrix is C-03.FR-2: the allocation seeded by the migration is the matrix of reference.md —
// 31, 12 and 8 Permissions.
func TestIntegrationRoleMatrix(t *testing.T) {
	want := referenceMatrix(t)
	if len(want[auth.RoleAdmin]) != 31 || len(want[auth.RoleResponder]) != 12 || len(want[auth.RoleViewer]) != 8 {
		t.Fatalf("reference.md matrix: %d, %d, %d", len(want[auth.RoleAdmin]), len(want[auth.RoleResponder]),
			len(want[auth.RoleViewer]))
	}
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d, _ := setup(t, s)
		roles, err := auth.LoadRoles(t.Context(), auth.NewStore(d.Pool))
		if err != nil {
			t.Fatal(err)
		}
		for _, role := range auth.RoleNames {
			if got := roles.Permissions(role); !slices.Equal(got, want[role]) {
				t.Errorf("%s:\n seeded %v\nmatrix %v", role, got, want[role])
			}
		}
	})
}

// TestIntegrationSessions runs sign-in against PostgreSQL with a manual clock: the login in any case, the CSRF token,
// the throttle sequence with the metric and the Audit log, the idle expiry and the password change.
func TestIntegrationSessions(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		d, orgID := setup(t, s)
		store := auth.NewStore(d.Pool)
		roles, err := auth.LoadRoles(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		k, err := keyring.Load(ctx, keyring.Env{Keys: keyring.DevelopmentKey, Source: keyring.SecretKeysVar}, true)
		if err != nil {
			t.Fatal(err)
		}
		log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		st, err := k.Establish(ctx, keyring.NewStore(d.Pool), t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := k.Open(ctx, log, st); err != nil {
			t.Fatal(err)
		}
		c := clock.NewManual(t0)
		svc := auth.NewService(orgID, store, k, audit.NewWriter(log, c), c, roles)
		addr := netip.MustParseAddr("198.51.100.4")
		signIn := func(login, password string) (auth.Session, error) {
			return svc.SignIn(ctx, auth.SignInRequest{Login: login, Password: password, Address: addr, UserAgent: "it"})
		}

		sess, err := signIn("OPS@Example.org", "ops-bootstrap-pass")
		if err != nil {
			t.Fatal(err)
		}
		if sess.User.Role != auth.RoleAdmin || len(svc.Permissions(sess)) != 31 {
			t.Errorf("session %+v with %d permissions", sess.User, len(svc.Permissions(sess)))
		}
		token, err := svc.CSRFToken(sess)
		if err != nil {
			t.Fatal(err)
		}
		again, err := svc.Authenticate(ctx, sess.Cookie())
		if err != nil || !svc.CheckCSRF(again, token) || svc.CheckCSRF(again, "") {
			t.Fatalf("authenticate %v; the CSRF check does not hold", err)
		}

		counter := metrics.LoginFailures.With(auth.MethodLocal)
		before := counter.Get()
		var waits []int
		for evaluated := 0; evaluated < 6; {
			_, err := signIn("ops@example.org", "wrong")
			if te, ok := errors.AsType[*auth.ThrottledError](err); ok {
				waits = append(waits, te.Seconds())
				c.Advance(te.RetryAfter)
				continue
			}
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatal(err)
			}
			evaluated++
		}
		if !slices.Equal(waits, []int{1, 2, 4}) || counter.Get()-before != 6 {
			t.Errorf("waits %v, metric +%d", waits, counter.Get()-before)
		}
		var failed int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id = $1
			AND action = 'session.sign_in_failed'`, orgID).Scan(&failed); err != nil || failed != 6 {
			t.Errorf("%d session.sign_in_failed entries, %v", failed, err)
		}
		c.Advance(auth.ThrottleMaxDelay)
		if _, err := signIn("ops@example.org", "ops-bootstrap-pass"); err != nil {
			t.Fatal(err)
		}
		var throttles int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM sign_in_throttles WHERE org_id = $1`,
			orgID).Scan(&throttles); err != nil || throttles != 0 {
			t.Errorf("%d throttle rows after a success, %v", throttles, err)
		}

		other, err := signIn("ops@example.org", "ops-bootstrap-pass")
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.ChangePassword(ctx, sess, auth.PasswordChange{Current: "ops-bootstrap-pass",
			New: "a brand new password"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Authenticate(ctx, other.Cookie()); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("the other session after the password change: %v", err)
		}
		list, err := svc.ListSessions(ctx, sess)
		if err != nil || len(list) != 1 || !list[0].Current || list[0].Address != addr {
			t.Errorf("sessions %+v, %v", list, err)
		}

		c.Advance(auth.SessionIdleTimeout)
		if _, err := svc.Authenticate(ctx, sess.Cookie()); !errors.Is(err, auth.ErrSessionExpired) {
			t.Errorf("after the idle timeout: %v", err)
		}
		var reason string
		if err := d.Pool.QueryRow(ctx, `SELECT end_reason FROM sessions WHERE org_id = $1 AND public_id = $2`,
			orgID, sess.PublicID).Scan(&reason); err != nil || reason != auth.EndExpired {
			t.Errorf("end reason %q, %v", reason, err)
		}
		fresh, err := signIn("ops@example.org", "a brand new password")
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.SignOutEverywhere(ctx, fresh, addr); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Authenticate(ctx, fresh.Cookie()); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("after signing out everywhere: %v", err)
		}
	})
}
