// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc/dbgen"
)

const (
	// LinkCallbackPath is the redirect URI of linking below MUSTER_PUBLIC_URL.
	LinkCallbackPath = "api/v1/me/oidc-identity/callback"
	// ProfilePage is the page of the SPA a link returns to, with ?error=<code> after a failure.
	ProfilePage = "/profile"

	// ErrorIdentityLinkedElsewhere is the callback error of a link whose identity belongs to another user.
	ErrorIdentityLinkedElsewhere = "identity_linked_elsewhere"

	// The Audit log actions of linking.
	ActionLinked      = "user.oidc_linked"
	ActionLinkRefused = "user.oidc_link_refused"

	purposeLink = "link"
)

// LinkStart is the authorization URL of a link and the time by which its callback must come back.
type LinkStart struct {
	URL       string
	ExpiresAt time.Time
}

// StartLink starts linking the account of the web session sess to an identity at the identity provider
// (C-03.FR-29): it stores a link request bound to the account and to the session — with the state, the nonce, the
// PKCE verifier encrypted and offline_access requested, as a sign-in — and returns the authorization URL. OIDC that is
// off is ErrNotEnabled; an account that signs in through OIDC already, or has no password, is ErrAlreadyLinked.
func (s *Service) StartLink(ctx context.Context, sess auth.Session) (LinkStart, error) {
	st, row, err := s.read(ctx, s.cfg.Store)
	if err != nil {
		return LinkStart{}, err
	}
	if !st.Configured || !st.Enabled {
		return LinkStart{}, ErrNotEnabled
	}
	u, err := s.cfg.Store.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: sess.User.ID})
	if err != nil {
		return LinkStart{}, fmt.Errorf("read the account of the session: %w", err)
	}
	if u.HasIdentity || !u.HasPassword {
		return LinkStart{}, ErrAlreadyLinked
	}
	p, err := s.providerFor(row, st)
	if err != nil {
		return LinkStart{}, err
	}
	d, err := p.Discover(ctx)
	if err != nil {
		return LinkStart{}, err
	}
	state, nonce, verifier := randomToken(), randomToken(), randomToken()
	ct, keyID, err := s.cfg.Keyring.Encrypt(fieldCodeVerifier, []byte(verifier))
	if err != nil {
		return LinkStart{}, fmt.Errorf("encrypt the PKCE verifier: %w", err)
	}
	now := s.cfg.Clocks.Business.Now().UTC()
	hash := sha256.Sum256([]byte(state))
	n, err := s.cfg.Store.InsertAuthRequest(ctx, dbgen.InsertAuthRequestParams{
		StateHash: hash[:], OrgID: s.cfg.OrgID, Purpose: purposeLink, Nonce: nonce, CodeVerifierCiphertext: ct,
		CodeVerifierKeyID: keyID, LinkUserID: pgtype.Int8{Int64: sess.User.ID, Valid: true},
		LinkSessionID: pgtype.Int8{Int64: sess.ID, Valid: true}, CreatedAt: now, ExpiresAt: now.Add(AuthRequestTTL),
		MaxPending: MaxPendingRequests,
	})
	if err != nil {
		return LinkStart{}, fmt.Errorf("store the link request: %w", err)
	}
	if n == 0 {
		return LinkStart{}, ErrTooManyRequests
	}
	authURL, err := p.AuthURL(d, AuthRequest{RedirectURI: s.linkRedirectURI(), Scopes: st.Scopes, State: state,
		Nonce: nonce, Verifier: verifier})
	if err != nil {
		return LinkStart{}, err
	}
	return LinkStart{URL: authURL, ExpiresAt: now.Add(AuthRequestTTL)}, nil
}

func (s *Service) linkRedirectURI() string {
	u := s.cfg.PublicURL.JoinPath(LinkCallbackPath)
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

func linkFailure(code string) Outcome {
	return Outcome{Redirect: ProfilePage + "?error=" + code, Error: code}
}

// CompleteLink finishes a link in the web session sess (C-03.FR-29, FR-30): it takes the link request of the state
// only when sess started it, exchanges the code, verifies the ID token and adds the identity to the account, wiping
// the password in the same update; the TOTP enrolment stays. The user's other sessions end (oidc_linked) and sess
// continues as an OIDC session; an offline token is kept as at a sign-in. An identity that another user holds is
// refused with identity_linked_elsewhere. Every outcome is a redirect to the profile; a failure of the identity
// provider or of Muster itself is idp_error and is logged as oidc_sign_in_failed.
func (s *Service) CompleteLink(ctx context.Context, sess auth.Session, cb Callback) Outcome {
	out, reason, err := s.completeLink(ctx, sess, cb)
	if err != nil {
		s.cfg.Log.Log(ctx, logging.OIDCSignInFailed, logging.F("reason", "link "+reason),
			logging.F("error", masked(err)))
		return linkFailure(ErrorIDPError)
	}
	return out
}

func (s *Service) completeLink(ctx context.Context, sess auth.Session, cb Callback) (Outcome, string, error) {
	if cb.State == "" {
		return linkFailure(ErrorInvalidRequest), "", nil
	}
	hash := sha256.Sum256([]byte(cb.State))
	req, err := s.cfg.Store.TakeLinkRequest(ctx, dbgen.TakeLinkRequestParams{OrgID: s.cfg.OrgID, StateHash: hash[:],
		SessionID: pgtype.Int8{Int64: sess.ID, Valid: true}, UserID: pgtype.Int8{Int64: sess.User.ID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return linkFailure(ErrorInvalidRequest), "", nil
	}
	if err != nil {
		return Outcome{}, "request", fmt.Errorf("take the link request: %w", err)
	}
	if !s.cfg.Clocks.Business.Now().Before(req.ExpiresAt) {
		return linkFailure(ErrorInvalidRequest), "", nil
	}
	st, row, err := s.read(ctx, s.cfg.Store)
	if err != nil {
		return Outcome{}, "settings", err
	}
	if !st.Configured || !st.Enabled {
		return linkFailure(ErrorOIDCDisabled), "", nil
	}
	if cb.Error != "" {
		return Outcome{}, "authorization", &BackChannelError{Step: "authorization",
			msg: "the identity provider answered the error " + printable(cb.Error)}
	}
	if cb.Code == "" {
		return linkFailure(ErrorInvalidRequest), "", nil
	}
	verifier, err := s.cfg.Keyring.Decrypt(fieldCodeVerifier, req.CodeVerifierKeyID, req.CodeVerifierCiphertext)
	if err != nil {
		return Outcome{}, "request", err
	}
	p, err := s.providerFor(row, st)
	if err != nil {
		return Outcome{}, "back channel", err
	}
	d, err := p.Discover(ctx)
	if err != nil {
		return Outcome{}, "discovery", err
	}
	tokens, err := p.Exchange(ctx, d, cb.Code, string(verifier), s.linkRedirectURI())
	if err != nil {
		return Outcome{}, "token exchange", err
	}
	idt, err := p.Verify(ctx, d, tokens.IDToken, req.Nonce)
	if errors.Is(err, ErrNonce) {
		return linkFailure(ErrorInvalidRequest), "", nil
	}
	if err != nil {
		return Outcome{}, "id token", err
	}
	return s.link(ctx, sess, cb, identityOf(d.Issuer, idt, nil, offlineToken(tokens)))
}

var (
	// errLinkedElsewhere is an identity that another account holds, found by the check or by the unique index.
	errLinkedElsewhere = errors.New("the identity belongs to another account")
	// errSessionEnded is a link whose web session ended before the identity was added.
	errSessionEnded = errors.New("the session that started the link ended")
)

// link adds the identity to the account of sess in one transaction and records user.oidc_linked; an identity that
// another account holds is recorded as user.oidc_link_refused after the transaction.
func (s *Service) link(ctx context.Context, sess auth.Session, cb Callback, id identity) (Outcome, string, error) {
	now := s.cfg.Clocks.Business.Now().UTC()
	mfa := assertsMFA(id.idt.AMR)
	var user dbgen.LockLinkUserRow
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		var err error
		user, err = q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: sess.User.ID})
		if err != nil {
			return fmt.Errorf("lock the account: %w", err)
		}
		if user.Status != "active" || user.HasIdentity || !user.HasPassword {
			return ErrAlreadyLinked
		}
		holder, err := q.GetIdentityUser(ctx, dbgen.GetIdentityUserParams{OrgID: s.cfg.OrgID,
			Issuer: optText(id.issuer), Subject: optText(id.idt.Subject)})
		switch {
		case err == nil && holder.ID != user.ID:
			return errLinkedElsewhere
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("find the account of the identity: %w", err)
		}
		n, err := q.LinkIdentity(ctx, dbgen.LinkIdentityParams{OrgID: s.cfg.OrgID, ID: user.ID,
			Issuer: optText(id.issuer), Subject: optText(id.idt.Subject), Now: now})
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == identityIndex {
			return errLinkedElsewhere
		}
		if err != nil {
			return fmt.Errorf("add the identity: %w", err)
		}
		if n == 0 {
			return ErrAlreadyLinked
		}
		if err := s.keepOfflineToken(ctx, q, user.ID, id.offline, now); err != nil {
			return err
		}
		ended, err := q.EndOtherUserSessions(ctx, dbgen.EndOtherUserSessionsParams{OrgID: s.cfg.OrgID,
			UserID: user.ID, KeepID: sess.ID, Now: now, EndReason: optText(auth.EndOIDCLinked)})
		if err != nil {
			return fmt.Errorf("end the other sessions: %w", err)
		}
		limit := now.Add(auth.SessionLifetime)
		if id.offline == "" {
			limit = now.Add(FallbackSessionLifetime)
		}
		n, err = q.ContinueAsOIDCSession(ctx, dbgen.ContinueAsOIDCSessionParams{OrgID: s.cfg.OrgID, ID: sess.ID,
			IdpMfa: mfa, ExpiresAt: limit})
		if err != nil {
			return fmt.Errorf("continue the session as an OIDC session: %w", err)
		}
		if n == 0 { // the session ended meanwhile: nothing is linked
			return errSessionEnded
		}
		return s.cfg.Audit.Record(ctx, q, audit.Entry{
			OrgID: s.cfg.OrgID, Actor: audit.User(user.ID, user.PublicID), Transport: audit.TransportUI,
			Action:   ActionLinked,
			Resource: audit.Resource{Type: audit.ResourceUser, PublicID: user.PublicID, Name: user.Name},
			Diff: []audit.Change{{Pointer: "/sign_in_method", Before: auth.MethodLocal, After: auth.MethodOIDC},
				{Pointer: "/password", SecretChanged: true}},
			Details:       map[string]any{"issuer": id.issuer, "sessions_ended": ended, "offline_access": id.offline != ""},
			SourceAddress: cb.Address,
		})
	})
	switch {
	case errors.Is(err, errLinkedElsewhere):
		return s.refuseLink(ctx, sess, cb, id)
	case errors.Is(err, ErrAlreadyLinked) || errors.Is(err, errSessionEnded):
		return linkFailure(ErrorInvalidRequest), "", nil
	case err != nil:
		return Outcome{}, "account", err
	}
	return Outcome{Redirect: ProfilePage}, "", nil
}

// refuseLink records user.oidc_link_refused for an identity that another account holds; the account of the session
// is unchanged.
func (s *Service) refuseLink(ctx context.Context, sess auth.Session, cb Callback, id identity) (Outcome, string,
	error) {
	if err := s.cfg.Audit.Record(ctx, s.cfg.Store, audit.Entry{
		OrgID: s.cfg.OrgID, Actor: audit.User(sess.User.ID, sess.User.PublicID), Transport: audit.TransportUI,
		Action:        ActionLinkRefused,
		Resource:      audit.Resource{Type: audit.ResourceUser, PublicID: sess.User.PublicID, Name: sess.User.Name},
		Details:       map[string]any{"reason": ErrorIdentityLinkedElsewhere, "issuer": id.issuer},
		SourceAddress: cb.Address,
	}); err != nil {
		return Outcome{}, "audit", err
	}
	return linkFailure(ErrorIdentityLinkedElsewhere), "", nil
}
