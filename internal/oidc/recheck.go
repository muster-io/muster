// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/oidc/dbgen"
)

// The background re-checks (C-03.FR-30). The interval is a setting; the poll, the budget and the lease are constants
// of the worker, like the Leader's intervals.
const (
	// RecheckInterval is auth.oidc_recheck_interval: a user who holds an offline token is re-checked this often.
	RecheckInterval = 15 * time.Minute
	// RecheckPoll is how often every replica looks for due re-checks.
	RecheckPoll = 2 * time.Second
	// RecheckBudget bounds the attempts of one refresh at the identity provider.
	RecheckBudget = 10 * time.Second
	// RecheckLease is how long a claimed re-check is held on the real clock; it outlasts the budget, so that no other
	// replica refreshes a token that a refresh in flight is about to rotate.
	RecheckLease = time.Minute
	// recheckBatch is how many re-checks a replica claims at a time; they run at once, so that each finishes within
	// its budget and well inside the lease.
	recheckBatch = 8
	// recheckMaxBackoff bounds the wait after rounds that failed, such as while the database is unavailable.
	recheckMaxBackoff = time.Minute
	// recordTimeout bounds the transaction that records an outcome; it runs even when the worker stops, so that a
	// refresh token the identity provider rotated is not lost at a shutdown.
	recordTimeout = 10 * time.Second

	// ActionUserRefused is the Audit log action of a user the identity provider refused at a re-check.
	ActionUserRefused = "user.oidc_refused"

	// fieldOfflineToken binds the ciphertext of the offline token, as the Keyring listing names it.
	fieldOfflineToken = "users.oidc_offline_token" //nolint:gosec // G101: the name of a field, not a credential
	offlineAccess     = "offline_access"
	refreshStep       = "refresh"
	reasonNoAccess    = "no_access"
)

// The outcomes of a re-check, as muster_oidc_checks_total and oidc_checks.last_outcome name them.
const (
	CheckOK          = "ok"
	CheckRefused     = "refused"
	CheckUnavailable = "unavailable"
	CheckSkipped     = "skipped"
)

var (
	// errLeaseLost is a re-check whose lease ran out or was reset before it recorded its outcome; the outcome is
	// dropped and the re-check runs again.
	errLeaseLost = errors.New("the lease of the re-check was lost")
	// errStale is a re-check of an offline token that a sign-in, a link, a disable or a conversion replaced meanwhile.
	errStale = errors.New("the offline token changed during the re-check")
)

// Claimer claims up to limit due re-checks of the Organization orgID and returns the ids of their users, leased.
type Claimer func(ctx context.Context, orgID int64, limit int32) ([]int64, error)

// NewClaimer is the Claimer over the main pool with the shared claim of internal/db: the due rows on the business
// clock, the lease on the real clock.
func NewClaimer(b db.Beginner, lease db.Lease) Claimer {
	return func(ctx context.Context, orgID int64, limit int32) ([]int64, error) {
		return db.Claim(ctx, b, lease, limit, func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]int64, error) {
			q := dbgen.New(tx)
			ids, err := q.DueChecks(ctx, dbgen.DueChecksParams{OrgID: orgID, Due: p.Due, Now: p.Now,
				BatchSize: p.Limit})
			if err != nil || len(ids) == 0 {
				return ids, err
			}
			return ids, q.LeaseChecks(ctx, dbgen.LeaseChecksParams{OrgID: orgID, Owner: optText(p.Owner),
				LeaseUntil: p.LeaseUntil, UserIds: ids})
		})
	}
}

// Rechecker is the worker of the background re-checks on every replica: every RecheckPoll it goes through the
// Organizations and runs the re-checks due in each.
type Rechecker struct {
	Claim Claimer
	// Owner names this replica in lease_owner.
	Owner         string
	Organizations func(ctx context.Context) ([]int64, error)
	// Service is the OIDC service of an Organization; false skips the Organization.
	Service func(orgID int64) (*Service, bool)
	Log     *logging.Logger
	// Poller paces the rounds; its zero value polls every RecheckPoll.
	Poller db.Poller
}

// Run runs rounds until ctx ends.
func (r Rechecker) Run(ctx context.Context) {
	// The series exist from the start, so that an increase is seen from zero.
	for _, outcome := range []string{CheckOK, CheckRefused, CheckUnavailable, CheckSkipped} {
		metrics.OIDCChecks.With(outcome)
	}
	p := r.Poller
	if p.Interval == 0 {
		p.Interval, p.MaxBackoff = RecheckPoll, recheckMaxBackoff
	}
	p.Run(ctx, r.Round)
}

// Round runs the due re-checks of every Organization once; a failure is logged as oidc_check_failed and returned, so
// that the next round backs off.
func (r Rechecker) Round(ctx context.Context) error {
	orgs, err := r.Organizations(ctx)
	if err != nil {
		r.Log.Log(ctx, logging.OIDCCheckFailed, logging.F("user", ""), logging.F("reason", "organizations"),
			logging.F("error", err.Error()))
		return err
	}
	var errs []error
	for _, org := range orgs {
		svc, ok := r.Service(org)
		if !ok {
			continue
		}
		if _, err := svc.Recheck(ctx, r.Claim, r.Owner); err != nil {
			r.Log.Log(ctx, logging.OIDCCheckFailed, logging.F("user", ""), logging.F("reason", "claim"),
				logging.F("error", err.Error()))
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Recheck claims the due re-checks of the Organization and runs them at once, each within RecheckBudget; it returns
// how many it claimed.
func (s *Service) Recheck(ctx context.Context, claim Claimer, owner string) (int, error) {
	ids, err := claim(ctx, s.cfg.OrgID, recheckBatch)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() { s.check(ctx, owner, id) })
	}
	wg.Wait()
	return len(ids), nil
}

// check re-checks one user who holds an offline token: it refreshes the token at the identity provider and records
// the outcome, unless the user has neither a live session nor a usable Personal access token (skipped) or OIDC is off.
func (s *Service) check(ctx context.Context, owner string, userID int64) {
	now := s.cfg.Clocks.Business.Now().UTC()
	u, err := s.cfg.Store.GetCheckUser(ctx, dbgen.GetCheckUserParams{OrgID: s.cfg.OrgID, ID: userID, Now: now})
	if err != nil {
		s.failed(ctx, "", "read", err)
		return
	}
	if u.Status != "active" || !u.OidcSubject.Valid || u.OidcOfflineTokenCiphertext == nil {
		// The token is gone, so the row is stale: nothing to re-check. The delete holds the lease, so that a row a new
		// sign-in scheduled meanwhile stays.
		if _, err := s.cfg.Store.DropCheck(ctx, dbgen.DropCheckParams{OrgID: s.cfg.OrgID, UserID: userID,
			Owner: optText(owner), Now: s.cfg.Clocks.Real.Now().UTC()}); err != nil {
			s.failed(ctx, u.PublicID, "record", err)
		}
		return
	}
	st, row, err := s.read(ctx, s.cfg.Store)
	if err != nil {
		s.failed(ctx, u.PublicID, "settings", err)
		return
	}
	if !st.Configured || !st.Enabled || (!u.LiveSession && !u.UsableToken) {
		s.finish(ctx, owner, u, CheckSkipped)
		return
	}
	token, err := s.cfg.Keyring.Decrypt(fieldOfflineToken, u.OidcOfflineTokenKeyID.String, u.OidcOfflineTokenCiphertext)
	if err != nil {
		s.unavailable(ctx, owner, u, "token", err)
		return
	}
	p, err := s.providerFor(row, st)
	if err != nil {
		s.unavailable(ctx, owner, u, "back channel", err)
		return
	}
	bctx, cancel := context.WithTimeout(ctx, cmp.Or(s.cfg.RecheckBudget, RecheckBudget))
	defer cancel()
	d, err := p.Discover(bctx)
	if err != nil {
		s.unavailable(ctx, owner, u, "discovery", err)
		return
	}
	tokens, err := p.Refresh(bctx, d, logging.Secret(token))
	if reason, ok := refusedBy(err); ok {
		s.refuseAtCheck(ctx, owner, u, map[string]any{"reason": reason})
		return
	}
	if err != nil {
		s.unavailable(ctx, owner, u, refreshStep, err)
		return
	}
	s.granted(ctx, owner, u, st, p, d, tokens)
}

// granted records a refresh the identity provider granted: the rotated token, the contact and, with oidc.sync_role and
// a groups claim in the refreshed ID token, the Role (D247). An ID token that does not verify, or names another
// subject, decides nothing; the rotated token is still kept when it is the user's.
func (s *Service) granted(ctx context.Context, owner string, u dbgen.GetCheckUserRow, st Settings, p *Provider,
	d Discovery, tokens Tokens) {
	var (
		groups []string
		found  bool
	)
	if tokens.IDToken != "" {
		idt, err := p.VerifyRefreshed(ctx, d, tokens.IDToken)
		switch {
		case err == nil && idt.Subject != u.OidcSubject.String:
			s.unavailable(ctx, owner, u, "id token", errors.New("the refreshed ID token names another subject"))
			return
		case err != nil:
			s.undecided(ctx, owner, u, tokens.RefreshToken, err)
			return
		}
		if v, ok := idt.Claims[st.GroupsClaim]; ok {
			groups, found = stringList(v), true
		}
	}
	sync := found && st.SyncRole
	role, mapped := MapRole(groups, st.GroupMappings, st.UnmatchedRole)
	if sync && !mapped {
		s.refuseAtCheck(ctx, owner, u, map[string]any{"reason": reasonNoAccess, "groups": auditGroups(groups)})
		return
	}
	now := s.cfg.Clocks.Business.Now().UTC()
	rctx, cancel := recordContext(ctx)
	defer cancel()
	err := s.cfg.Store.InTx(rctx, func(q Queries) error {
		ctx := rctx
		if sync {
			// The active Admins are locked before the user, as every change that may remove one does.
			if _, err := q.LockActiveAdmins(ctx, s.cfg.OrgID); err != nil {
				return fmt.Errorf("lock the active admins: %w", err)
			}
		}
		cur, err := s.lockCheck(ctx, q, u)
		if err != nil {
			return err
		}
		if err := s.finishIn(ctx, q, owner, u.ID, CheckOK, now); err != nil {
			return err
		}
		if err := s.keepRotated(ctx, q, u.ID, tokens.RefreshToken, now); err != nil {
			return err
		}
		if err := q.RecordContact(ctx, dbgen.RecordContactParams{OrgID: s.cfg.OrgID, ID: u.ID, Now: now}); err != nil {
			return fmt.Errorf("record the contact with the identity provider: %w", err)
		}
		if !sync || role == cur.Role {
			return nil
		}
		return s.syncRole(ctx, q, dbgen.GetIdentityUserRow{ID: u.ID, PublicID: u.PublicID, Name: u.Name}, role, now,
			syncAtRecheck)
	})
	if errors.Is(err, errLeaseLost) {
		s.rescueRotated(rctx, u, tokens.RefreshToken)
	}
	if s.discarded(ctx, u, err) {
		return
	}
	metrics.OIDCChecks.With(CheckOK).Inc()
}

// recordContext is the context of the transaction that records an outcome: it does not end with the worker, and is
// bounded by recordTimeout.
func recordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
}

// rescueRotated keeps a refresh token the identity provider rotated for a re-check that lost its lease, as long as
// the user still holds the token the re-check refreshed: the token it used no longer works at the identity provider,
// so dropping the rotated one would make the next re-check a refusal.
func (s *Service) rescueRotated(ctx context.Context, u dbgen.GetCheckUserRow, rotated logging.Secret) {
	if rotated == "" {
		return
	}
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		if _, err := s.lockCheck(ctx, q, u); err != nil {
			return err
		}
		return s.keepRotated(ctx, q, u.ID, rotated, s.cfg.Clocks.Business.Now().UTC())
	})
	if err != nil && !errors.Is(err, errStale) {
		s.failed(ctx, u.PublicID, "record", err)
	}
}

// undecided records a granted refresh whose ID token decides nothing as unavailable, keeping the rotated token, so
// that the next re-check refreshes the token the identity provider issued last.
func (s *Service) undecided(ctx context.Context, owner string, u dbgen.GetCheckUserRow, rotated logging.Secret,
	cause error) {
	s.failed(ctx, u.PublicID, "id token", cause)
	now := s.cfg.Clocks.Business.Now().UTC()
	rctx, cancel := recordContext(ctx)
	defer cancel()
	err := s.cfg.Store.InTx(rctx, func(q Queries) error {
		if _, err := s.lockCheck(rctx, q, u); err != nil {
			return err
		}
		if err := s.finishIn(rctx, q, owner, u.ID, CheckUnavailable, now); err != nil {
			return err
		}
		return s.keepRotated(rctx, q, u.ID, rotated, now)
	})
	if errors.Is(err, errLeaseLost) {
		s.rescueRotated(rctx, u, rotated)
	}
	if s.discarded(ctx, u, err) {
		return
	}
	metrics.OIDCChecks.With(CheckUnavailable).Inc()
}

// keepRotated stores the refresh token a refresh returned in place of the one it used; an answer without one keeps
// the stored token.
func (s *Service) keepRotated(ctx context.Context, q Queries, userID int64, rotated logging.Secret,
	now time.Time) error {
	if rotated == "" {
		return nil
	}
	return s.storeToken(ctx, q, userID, rotated, now)
}

// refuseAtCheck records a refusal of the identity provider (C-03.FR-30): all of the user's sessions end (idp_refused, so that
// their next request answers oidc_session_ended), oidc_refused_at is set, the offline token is wiped, the re-check is
// removed and user.oidc_refused is recorded.
func (s *Service) refuseAtCheck(ctx context.Context, owner string, u dbgen.GetCheckUserRow, details map[string]any) {
	now := s.cfg.Clocks.Business.Now().UTC()
	rctx, cancel := recordContext(ctx)
	defer cancel()
	err := s.cfg.Store.InTx(rctx, func(q Queries) error {
		ctx := rctx
		if _, err := s.lockCheck(ctx, q, u); err != nil {
			return err
		}
		n, err := q.DropCheck(ctx, dbgen.DropCheckParams{OrgID: s.cfg.OrgID, UserID: u.ID, Owner: optText(owner),
			Now: s.cfg.Clocks.Real.Now().UTC()})
		if err != nil {
			return fmt.Errorf("remove the re-check: %w", err)
		}
		if n == 0 {
			return errLeaseLost
		}
		ended, err := q.EndUserSessions(ctx, dbgen.EndUserSessionsParams{OrgID: s.cfg.OrgID, UserID: u.ID, Now: now,
			EndReason: optText(auth.EndIDPRefused)})
		if err != nil {
			return fmt.Errorf("end the sessions of %s: %w", u.PublicID, err)
		}
		if err := q.RefuseUser(ctx, dbgen.RefuseUserParams{OrgID: s.cfg.OrgID, ID: u.ID, Now: now}); err != nil {
			return fmt.Errorf("record the refusal of %s: %w", u.PublicID, err)
		}
		details["sessions_ended"] = ended
		return s.cfg.Audit.Record(ctx, q, audit.Entry{
			OrgID: s.cfg.OrgID, Actor: audit.System, Transport: audit.TransportSystem, Action: ActionUserRefused,
			Resource: audit.Resource{Type: audit.ResourceUser, PublicID: u.PublicID, Name: u.Name}, Details: details,
		})
	})
	if s.discarded(ctx, u, err) {
		return
	}
	metrics.OIDCChecks.With(CheckRefused).Inc()
}

// lockCheck locks the user of a re-check and refuses to go on when the offline token is not the one the re-check
// refreshed.
func (s *Service) lockCheck(ctx context.Context, q Queries, u dbgen.GetCheckUserRow) (dbgen.LockCheckUserRow, error) {
	cur, err := q.LockCheckUser(ctx, dbgen.LockCheckUserParams{OrgID: s.cfg.OrgID, ID: u.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return cur, errStale
	}
	if err != nil {
		return cur, fmt.Errorf("lock the user %s: %w", u.PublicID, err)
	}
	a, b := cur.OidcOfflineTokenUpdatedAt, u.OidcOfflineTokenUpdatedAt
	if cur.Status != "active" || a.Valid != b.Valid || !a.Time.Equal(b.Time) {
		return cur, errStale
	}
	return cur, nil
}

// discarded reports whether a re-check ended without a recorded outcome: its lease was lost or its token replaced,
// so it runs again or is no longer due, or the database failed, which oidc_check_failed logs.
func (s *Service) discarded(ctx context.Context, u dbgen.GetCheckUserRow, err error) bool {
	if err == nil {
		return false
	}
	s.failed(ctx, u.PublicID, "record", err)
	return true
}

// unavailable records a re-check that reached no decision: nothing changes, the next attempt is one interval later,
// and oidc_check_failed says why.
func (s *Service) unavailable(ctx context.Context, owner string, u dbgen.GetCheckUserRow, reason string, err error) {
	s.failed(ctx, u.PublicID, reason, err)
	s.finish(ctx, owner, u, CheckUnavailable)
}

// finish records a re-check that changes nothing for the user — skipped or unavailable — and makes it due one interval
// later.
func (s *Service) finish(ctx context.Context, owner string, u dbgen.GetCheckUserRow, outcome string) {
	rctx, cancel := recordContext(ctx)
	defer cancel()
	err := s.finishIn(rctx, s.cfg.Store, owner, u.ID, outcome, s.cfg.Clocks.Business.Now().UTC())
	if s.discarded(ctx, u, err) {
		return
	}
	metrics.OIDCChecks.With(outcome).Inc()
}

// finishIn records outcome on the re-check, due again RecheckInterval after now, and releases the lease;
// errLeaseLost when the lease is no longer this replica's.
func (s *Service) finishIn(ctx context.Context, q Queries, owner string, userID int64, outcome string,
	now time.Time) error {
	n, err := q.FinishCheck(ctx, dbgen.FinishCheckParams{OrgID: s.cfg.OrgID, UserID: userID, Owner: optText(owner),
		Now: s.cfg.Clocks.Real.Now().UTC(), Deadline: now.Add(RecheckInterval), Outcome: optText(outcome),
		UpdatedAt: now})
	if err != nil {
		return fmt.Errorf("record the re-check: %w", err)
	}
	if n == 0 {
		return errLeaseLost
	}
	return nil
}

// failed logs oidc_check_failed for the user with the public_id user, with the error masked.
func (s *Service) failed(ctx context.Context, user, reason string, err error) {
	s.cfg.Log.Log(ctx, logging.OIDCCheckFailed, logging.F("user", user), logging.F("reason", reason),
		logging.F("error", masked(err)))
}

// refusedBy says whether a failed refresh is a refusal of the identity provider — invalid_grant or another 4xx answer
// except 408 and 429 — and names it; anything else, the budget spent included, is unavailability. A 407 comes from the
// OIDC proxy, not from the identity provider, so it is unavailability too.
func refusedBy(err error) (string, bool) {
	be, ok := errors.AsType[*BackChannelError](err)
	if !ok || be.Step != refreshStep || be.Status < 400 || be.Status > 499 || be.Status == 408 || be.Status == 429 ||
		be.Status == 407 {
		return "", false
	}
	if be.Code != "" {
		return be.Code, true
	}
	return "http_" + strconv.Itoa(be.Status), true
}

// offlineToken is the offline token of a token answer: its refresh token when the granted scope includes
// offline_access — an answer without a scope granted the scope requested (RFC 6749 §5.1), which includes it — and
// none otherwise.
func offlineToken(t Tokens) logging.Secret {
	if t.RefreshToken == "" || (len(t.Scope) > 0 && !slices.Contains(t.Scope, offlineAccess)) {
		return ""
	}
	return t.RefreshToken
}

// keepOfflineToken stores the offline token of a sign-in or a link on the user, encrypted, in place of the previous
// one, and makes the user's re-check due one interval later; without one it wipes the previous token and the
// re-check, and the user's OIDC sessions fall back to auth.oidc_fallback_session_lifetime.
func (s *Service) keepOfflineToken(ctx context.Context, q Queries, userID int64, token logging.Secret,
	now time.Time) error {
	if token == "" {
		if err := q.WipeOfflineToken(ctx, dbgen.WipeOfflineTokenParams{OrgID: s.cfg.OrgID, ID: userID}); err != nil {
			return fmt.Errorf("wipe the offline token: %w", err)
		}
		if err := q.DeleteCheck(ctx, dbgen.DeleteCheckParams{OrgID: s.cfg.OrgID, UserID: userID}); err != nil {
			return fmt.Errorf("remove the re-check: %w", err)
		}
		// The sessions opened with the token wiped here are no longer re-checked, so they get the fallback lifetime.
		if _, err := q.CapOIDCSessions(ctx, dbgen.CapOIDCSessionsParams{OrgID: s.cfg.OrgID, UserID: userID,
			ExpiresAt: now.Add(FallbackSessionLifetime)}); err != nil {
			return fmt.Errorf("bound the OIDC sessions: %w", err)
		}
		return nil
	}
	if err := s.storeToken(ctx, q, userID, token, now); err != nil {
		return err
	}
	if err := q.ScheduleCheck(ctx, dbgen.ScheduleCheckParams{OrgID: s.cfg.OrgID, UserID: userID,
		Deadline: now.Add(RecheckInterval), Now: now}); err != nil {
		return fmt.Errorf("schedule the re-check: %w", err)
	}
	return nil
}

func (s *Service) storeToken(ctx context.Context, q Queries, userID int64, token logging.Secret, now time.Time) error {
	ct, keyID, err := s.cfg.Keyring.Encrypt(fieldOfflineToken, []byte(token))
	if err != nil {
		return fmt.Errorf("encrypt the offline token: %w", err)
	}
	if err := q.StoreOfflineToken(ctx, dbgen.StoreOfflineTokenParams{OrgID: s.cfg.OrgID, ID: userID, Ciphertext: ct,
		KeyID: optText(keyID), Now: now}); err != nil {
		return fmt.Errorf("store the offline token: %w", err)
	}
	return nil
}
