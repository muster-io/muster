// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package totp is the second factor of the users (C-03.FR-10, FR-11, FR-27): TOTP of RFC 6238 — six digits, 30-second
// steps, HMAC-SHA-1 for authenticator compatibility and one step of tolerance either way — with the seed stored as a
// Secret through the Keyring, a pending seed between the start of an enrolment and its confirmation, a replay guard
// that accepts each time step once, and single-use recovery codes hashed with argon2id. Time steps follow the real
// clock, so that the development clock never moves them away from the authenticator app's; timestamps follow the
// business clock.
package totp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // G505: RFC 6238 with SHA-1 is what authenticator apps support (C-03.FR-10)
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/totp/dbgen"
)

// The parameters of the codes (RFC 6238).
const (
	Digits = 6
	Period = 30 * time.Second
	// Skew is how many steps before and after the current one a code may come from.
	Skew = 1
	// Issuer names Muster in the authenticator app.
	Issuer = "Muster"
	// seedBytes is the size of a seed: 160 bits, the size of an HMAC-SHA-1 key that RFC 4226 recommends.
	seedBytes = 20
)

// The Secret fields of the Keyring that hold the seeds.
const (
	fieldSeed        = "user_totp.seed"
	fieldPendingSeed = "user_totp.pending_seed"
)

// The Audit log actions of TOTP (C-03.FR-14).
const (
	ActionEnrolled                 = "totp.enrolled"
	ActionRemoved                  = "totp.removed"
	ActionRecoveryCodesRegenerated = "totp.recovery_codes_regenerated"
	ActionReset                    = "totp.reset"
)

var (
	// ErrAlreadyEnrolled is an enrolment of a user who has TOTP.
	ErrAlreadyEnrolled = errors.New("TOTP is already enrolled")
	// ErrNotStarted is a confirmation without a begun enrolment.
	ErrNotStarted = errors.New("no TOTP enrolment was begun")
	// ErrNotEnrolled is a change of TOTP that the user does not have.
	ErrNotEnrolled = errors.New("the user has no TOTP")
	// ErrUserNotFound is a user that does not exist in the Organization or was deleted.
	ErrUserNotFound = errors.New("no such user")
	// ErrOwnTOTP is an Admin's reset of their own TOTP, which needs the proof of a removal (C-03.FR-27).
	ErrOwnTOTP = errors.New("remove your own TOTP from your profile, with a proof")
	// errNoActor is a CLI reset without the --actor name.
	errNoActor = errors.New("the reset needs the --actor name")
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	GetTOTPUser(ctx context.Context, arg dbgen.GetTOTPUserParams) (dbgen.GetTOTPUserRow, error)
	GetTOTPUserByPublicID(ctx context.Context, arg dbgen.GetTOTPUserByPublicIDParams) (dbgen.GetTOTPUserByPublicIDRow,
		error)
	GetTOTPUserByLogin(ctx context.Context, arg dbgen.GetTOTPUserByLoginParams) (dbgen.GetTOTPUserByLoginRow, error)
	GetTOTP(ctx context.Context, arg dbgen.GetTOTPParams) (dbgen.GetTOTPRow, error)
	SetPendingSeed(ctx context.Context, arg dbgen.SetPendingSeedParams) (int64, error)
	ActivateSeed(ctx context.Context, arg dbgen.ActivateSeedParams) (int64, error)
	UseStep(ctx context.Context, arg dbgen.UseStepParams) (int64, error)
	DeleteTOTP(ctx context.Context, arg dbgen.DeleteTOTPParams) (int64, error)
	ListUnusedRecoveryCodes(ctx context.Context, arg dbgen.ListUnusedRecoveryCodesParams) (
		[]dbgen.ListUnusedRecoveryCodesRow, error)
	CountUnusedRecoveryCodes(ctx context.Context, arg dbgen.CountUnusedRecoveryCodesParams) (int64, error)
	UseRecoveryCode(ctx context.Context, arg dbgen.UseRecoveryCodeParams) (int64, error)
	InsertRecoveryCode(ctx context.Context, arg dbgen.InsertRecoveryCodeParams) error
	DeleteUnusedRecoveryCodes(ctx context.Context, arg dbgen.DeleteUnusedRecoveryCodesParams) (int64, error)
	DeleteRecoveryCodes(ctx context.Context, arg dbgen.DeleteRecoveryCodesParams) (int64, error)
	ActivateSession(ctx context.Context, arg dbgen.ActivateSessionParams) (int64, error)
	EndSessionsOfUser(ctx context.Context, arg dbgen.EndSessionsOfUserParams) (int64, error)
	audit.Store
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: pgQueries{Queries: dbgen.New(pool), Store: audit.NewStore(pool)}, pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
}

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(pgQueries{Queries: dbgen.New(tx), Store: audit.NewStore(tx)})
	})
}

// Attempts is what the changes of a user's own TOTP need of internal/auth: proofs checked under the sign-in throttle.
type Attempts interface {
	Attempt(ctx context.Context, sess auth.Session, addr netip.Addr, method string,
		check func(context.Context) (bool, error)) error
	CheckPassword(ctx context.Context, sess auth.Session, password string) (bool, error)
}

// Service keeps the TOTP of the users of the Organization.
type Service struct {
	orgID    int64
	store    Store
	keyring  *keyring.Keyring
	audit    *audit.Writer
	business clock.Clock
	real     clock.Clock
	attempts Attempts
	// hash and verifyHash are the argon2id hashing of the recovery codes; tests replace them with a cheaper one.
	hash       func(ctx context.Context, code string) (string, error)
	verifyHash func(ctx context.Context, encoded, code string) (bool, error)
}

// New returns the Service of the Organization orgID. The Keyring encrypts and decrypts the seeds; attempts throttles
// the proofs a user gives on their own TOTP. A Service that only resets TOTP, as the CLI does, needs neither.
func New(orgID int64, s Store, k *keyring.Keyring, w *audit.Writer, clocks clock.Clocks, a Attempts) *Service {
	return &Service{orgID: orgID, store: s, keyring: k, audit: w, business: clocks.Business, real: clocks.Real,
		attempts: a, hash: auth.HashPassword, verifyHash: auth.VerifyPassword}
}

// Status is the TOTP of a user as the profile shows it.
type Status struct {
	Enrolled               bool
	EnrolledAt             *time.Time
	Pending                bool
	RecoveryCodesRemaining int64
}

// Status reads the TOTP of the user userID.
func (s *Service) Status(ctx context.Context, userID int64) (Status, error) {
	row, err := s.totpOf(ctx, s.store, userID)
	if err != nil {
		return Status{}, err
	}
	st := Status{Enrolled: row.SeedCiphertext != nil, Pending: row.PendingSeedCiphertext != nil}
	if row.EnrolledAt.Valid {
		t := row.EnrolledAt.Time
		st.EnrolledAt = &t
	}
	if st.Enrolled {
		if st.RecoveryCodesRemaining, err = s.store.CountUnusedRecoveryCodes(ctx, dbgen.CountUnusedRecoveryCodesParams{
			OrgID: s.orgID, UserID: userID,
		}); err != nil {
			return Status{}, fmt.Errorf("count the recovery codes: %w", err)
		}
	}
	return st, nil
}

// totpOf reads the TOTP row of a user; a user without one has an empty row.
func (s *Service) totpOf(ctx context.Context, q Queries, userID int64) (dbgen.GetTOTPRow, error) {
	row, err := q.GetTOTP(ctx, dbgen.GetTOTPParams{OrgID: s.orgID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.GetTOTPRow{}, nil
	}
	if err != nil {
		return dbgen.GetTOTPRow{}, fmt.Errorf("read the TOTP: %w", err)
	}
	return row, nil
}

// Enrolment is a begun enrolment: the seed in base32 for manual entry and the otpauth URI of the QR code.
type Enrolment struct {
	Secret string
	URI    string
}

// Begin starts the enrolment of sess's user with a new pending seed, which replaces a pending one; it is
// ErrAlreadyEnrolled when the user has TOTP. The seed is returned once and stored only as a Secret.
func (s *Service) Begin(ctx context.Context, sess auth.Session) (Enrolment, error) {
	u, err := s.store.GetTOTPUser(ctx, dbgen.GetTOTPUserParams{OrgID: s.orgID, ID: sess.User.ID})
	if err != nil {
		return Enrolment{}, fmt.Errorf("read the user %s: %w", sess.User.PublicID, err)
	}
	seed := make([]byte, seedBytes)
	_, _ = rand.Read(seed) // crypto/rand.Read never fails
	ciphertext, keyID, err := s.keyring.Encrypt(fieldPendingSeed, seed)
	if err != nil {
		return Enrolment{}, err
	}
	n, err := s.store.SetPendingSeed(ctx, dbgen.SetPendingSeedParams{
		UserID: sess.User.ID, OrgID: s.orgID, Ciphertext: ciphertext, KeyID: keyID, Now: s.business.Now().UTC(),
	})
	if err != nil {
		return Enrolment{}, fmt.Errorf("store the pending seed of %s: %w", sess.User.PublicID, err)
	}
	if n == 0 {
		return Enrolment{}, ErrAlreadyEnrolled
	}
	secret := base32NoPad.EncodeToString(seed)
	return Enrolment{Secret: secret, URI: otpauthURI(u.Login, secret)}, nil
}

// otpauthURI is the key URI of an authenticator app: otpauth://totp/Muster:<login>?secret=…&issuer=Muster, with the
// defaults SHA-1, six digits and 30 seconds.
func otpauthURI(login, secret string) string {
	return "otpauth://totp/" + Issuer + ":" + url.PathEscape(login) + "?secret=" + secret + "&issuer=" +
		url.QueryEscape(Issuer)
}

// Confirm completes the enrolment of sess's user with a current code of the pending seed and returns new recovery
// codes, shown once. Without a begun enrolment it is ErrNotStarted, with TOTP already on ErrAlreadyEnrolled. The code
// is checked under the sign-in throttle; a wrong one is auth.ErrInvalidCredentials. The time step of the code is used,
// and a session in the state totp_enrolment_required becomes active.
func (s *Service) Confirm(ctx context.Context, sess auth.Session, code string, addr netip.Addr) ([]string, error) {
	row, err := s.totpOf(ctx, s.store, sess.User.ID)
	switch {
	case err != nil:
		return nil, err
	case row.SeedCiphertext != nil:
		return nil, ErrAlreadyEnrolled
	case row.PendingSeedCiphertext == nil:
		return nil, ErrNotStarted
	}
	seed, err := s.keyring.Decrypt(fieldPendingSeed, row.PendingSeedKeyID.String, row.PendingSeedCiphertext)
	if err != nil {
		return nil, err
	}
	var step int64
	if err := s.attempts.Attempt(ctx, sess, addr, auth.FailureTOTP, func(context.Context) (bool, error) {
		var ok bool
		step, ok = match(seed, code, s.real.Now())
		return ok, nil
	}); err != nil {
		return nil, err
	}
	ciphertext, keyID, err := s.keyring.Encrypt(fieldSeed, seed)
	if err != nil {
		return nil, err
	}
	codes, hashes, err := s.newRecoveryCodes(ctx)
	if err != nil {
		return nil, err
	}
	now := s.business.Now().UTC()
	err = s.store.InTx(ctx, func(q Queries) error {
		n, err := q.ActivateSeed(ctx, dbgen.ActivateSeedParams{
			Ciphertext: ciphertext, KeyID: keyID, Now: now, Step: step, OrgID: s.orgID, UserID: sess.User.ID,
			PendingCiphertext: row.PendingSeedCiphertext,
		})
		if err != nil {
			return fmt.Errorf("activate the TOTP of %s: %w", sess.User.PublicID, err)
		}
		if n == 0 {
			return ErrNotStarted // another enrolment began or completed meanwhile
		}
		if err := s.replaceRecoveryCodes(ctx, q, sess.User.ID, hashes, now); err != nil {
			return err
		}
		if _, err := q.ActivateSession(ctx, dbgen.ActivateSessionParams{OrgID: s.orgID, ID: sess.ID}); err != nil {
			return fmt.Errorf("activate the session %s: %w", sess.PublicID, err)
		}
		return s.record(ctx, q, sess, ActionEnrolled, addr, map[string]any{"recovery_codes": len(codes)})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// RegenerateRecoveryCodes replaces the unused recovery codes of sess's user with new ones, shown once, after a
// current TOTP code that was not used before; without TOTP it is ErrNotEnrolled, with a wrong code
// auth.ErrInvalidCredentials.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, sess auth.Session, code string,
	addr netip.Addr) ([]string, error) {
	row, err := s.totpOf(ctx, s.store, sess.User.ID)
	if err != nil {
		return nil, err
	}
	if row.SeedCiphertext == nil {
		return nil, ErrNotEnrolled
	}
	if err := s.attempts.Attempt(ctx, sess, addr, auth.FailureTOTP, func(ctx context.Context) (bool, error) {
		return s.verifyCode(ctx, sess.User.ID, row, code)
	}); err != nil {
		return nil, err
	}
	codes, hashes, err := s.newRecoveryCodes(ctx)
	if err != nil {
		return nil, err
	}
	now := s.business.Now().UTC()
	err = s.store.InTx(ctx, func(q Queries) error {
		if err := s.replaceRecoveryCodes(ctx, q, sess.User.ID, hashes, now); err != nil {
			return err
		}
		return s.record(ctx, q, sess, ActionRecoveryCodesRegenerated, addr,
			map[string]any{"recovery_codes": len(codes)})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// Removal is the proof a user gives to remove their TOTP: the current password, a current TOTP code or a recovery
// code (C-03.FR-27). The password is checked when it is given.
type Removal struct {
	Password     string
	TOTPCode     string
	RecoveryCode string
}

// Remove removes the TOTP of sess's user, an enrolment that is only begun included, with its recovery codes, after
// a right proof; a wrong proof is auth.ErrInvalidCredentials and changes nothing, and a user without TOTP is
// ErrNotEnrolled. The proof is checked under the sign-in throttle. Under a TOTP policy that covers the user, the next
// sign-in demands enrolment again.
func (s *Service) Remove(ctx context.Context, sess auth.Session, p Removal, addr netip.Addr) error {
	row, err := s.totpOf(ctx, s.store, sess.User.ID)
	if err != nil {
		return err
	}
	if row.SeedCiphertext == nil && row.PendingSeedCiphertext == nil {
		return ErrNotEnrolled
	}
	method, proof := auth.FailureTOTP, "totp_code"
	check := func(ctx context.Context) (bool, error) {
		return s.Verify(ctx, sess.User.ID, auth.Proof{TOTPCode: p.TOTPCode, RecoveryCode: p.RecoveryCode})
	}
	switch {
	case p.Password != "":
		method, proof = auth.MethodLocal, "password"
		check = func(ctx context.Context) (bool, error) { return s.attempts.CheckPassword(ctx, sess, p.Password) }
	case p.TOTPCode == "":
		proof = "recovery_code"
	}
	if err := s.attempts.Attempt(ctx, sess, addr, method, check); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(q Queries) error {
		if err := s.delete(ctx, q, sess.User.ID, sess.User.PublicID); err != nil {
			return err
		}
		return s.record(ctx, q, sess, ActionRemoved, addr, map[string]any{"proof": proof})
	})
}

// Reset removes the TOTP of the user with publicID and its recovery codes for an Admin (C-03.FR-11), and ends the
// user's sessions, so that the user signs in again and, under a TOTP policy that covers them, enrols again. A user that
// does not exist or was deleted is ErrUserNotFound; a user without TOTP is left as it is and nothing is recorded. An
// Admin's own TOTP is ErrOwnTOTP: removing it needs the proof of Remove, so that a stolen session cannot drop it.
func (s *Service) Reset(ctx context.Context, actor audit.Actor, t audit.Transport, addr netip.Addr,
	publicID string) error {
	norm, err := publicid.Parse(publicid.User, publicID)
	if err != nil {
		return ErrUserNotFound
	}
	row, err := s.store.GetTOTPUserByPublicID(ctx, dbgen.GetTOTPUserByPublicIDParams{OrgID: s.orgID, PublicID: norm})
	if err != nil {
		return userError(err)
	}
	if actor.Kind == audit.ActorUser && actor.ID == row.ID {
		return ErrOwnTOTP
	}
	_, err = s.reset(ctx, target(dbgen.GetTOTPUserRow(row)), actor, t, addr)
	return err
}

// ResetByLogin is `muster admin reset-totp --actor <name> <login>`: Reset of the account with the login, compared
// lowercased, recorded with the --actor name and the Transport cli. It returns the public_id of the account and
// whether it had TOTP.
func (s *Service) ResetByLogin(ctx context.Context, actorName, login string) (string, bool, error) {
	if strings.TrimSpace(actorName) == "" {
		return "", false, errNoActor
	}
	row, err := s.store.GetTOTPUserByLogin(ctx, dbgen.GetTOTPUserByLoginParams{OrgID: s.orgID, Login: login})
	if err != nil {
		return "", false, userError(err)
	}
	u := target(dbgen.GetTOTPUserRow(row))
	removed, err := s.reset(ctx, u, audit.CLI(actorName), audit.TransportCLI, netip.Addr{})
	return u.PublicID, removed, err
}

// resetTarget is the user whose TOTP is reset.
type resetTarget struct {
	ID       int64
	PublicID string
	Name     string
	deleted  bool
}

func target(row dbgen.GetTOTPUserRow) resetTarget {
	return resetTarget{ID: row.ID, PublicID: row.PublicID, Name: row.Name, deleted: row.Status == "deleted"}
}

func userError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	return fmt.Errorf("find the user: %w", err)
}

func (s *Service) reset(ctx context.Context, u resetTarget, actor audit.Actor, t audit.Transport,
	addr netip.Addr) (bool, error) {
	if u.deleted {
		return false, ErrUserNotFound
	}
	removed := false
	err := s.store.InTx(ctx, func(q Queries) error {
		n, err := q.DeleteTOTP(ctx, dbgen.DeleteTOTPParams{OrgID: s.orgID, UserID: u.ID})
		if err != nil {
			return fmt.Errorf("remove the TOTP of %s: %w", u.PublicID, err)
		}
		if n == 0 {
			return nil
		}
		removed = true
		if _, err := q.DeleteRecoveryCodes(ctx, dbgen.DeleteRecoveryCodesParams{OrgID: s.orgID, UserID: u.ID}); err != nil {
			return fmt.Errorf("remove the recovery codes of %s: %w", u.PublicID, err)
		}
		ended, err := q.EndSessionsOfUser(ctx, dbgen.EndSessionsOfUserParams{
			Now: s.business.Now().UTC(), OrgID: s.orgID, UserID: u.ID,
		})
		if err != nil {
			return fmt.Errorf("end the sessions of %s: %w", u.PublicID, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: actor, Transport: t, Action: ActionReset,
			Resource: audit.Resource{Type: audit.ResourceUser, PublicID: u.PublicID, Name: u.Name},
			Details:  map[string]any{"sessions_ended": ended}, SourceAddress: addr,
		})
	})
	return removed, err
}

// delete removes the TOTP of a user and its recovery codes.
func (s *Service) delete(ctx context.Context, q Queries, userID int64, publicID string) error {
	if _, err := q.DeleteTOTP(ctx, dbgen.DeleteTOTPParams{OrgID: s.orgID, UserID: userID}); err != nil {
		return fmt.Errorf("remove the TOTP of %s: %w", publicID, err)
	}
	if _, err := q.DeleteRecoveryCodes(ctx, dbgen.DeleteRecoveryCodesParams{OrgID: s.orgID, UserID: userID}); err != nil {
		return fmt.Errorf("remove the recovery codes of %s: %w", publicID, err)
	}
	return nil
}

// record writes an Audit log entry of sess's user about their own TOTP.
func (s *Service) record(ctx context.Context, q Queries, sess auth.Session, action string, addr netip.Addr,
	details map[string]any) error {
	return s.audit.Record(ctx, q, audit.Entry{
		OrgID: s.orgID, Actor: audit.User(sess.User.ID, sess.User.PublicID), Transport: audit.TransportUI,
		Action:   action,
		Resource: audit.Resource{Type: audit.ResourceUser, PublicID: sess.User.PublicID, Name: sess.User.Name},
		Details:  details, SourceAddress: addr,
	})
}

// Verify is the second factor of sign-in (auth.SecondFactor): a current TOTP code of the user's seed whose time step
// was not used before, or else an unused recovery code; a right one is used up. A user without TOTP has no right
// code.
func (s *Service) Verify(ctx context.Context, userID int64, p auth.Proof) (bool, error) {
	row, err := s.totpOf(ctx, s.store, userID)
	if err != nil || row.SeedCiphertext == nil {
		return false, err
	}
	if p.TOTPCode != "" {
		return s.verifyCode(ctx, userID, row, p.TOTPCode)
	}
	return s.useRecoveryCode(ctx, userID, p.RecoveryCode)
}

// verifyCode checks a code against the user's seed and uses its time step; a step used before refuses the code.
func (s *Service) verifyCode(ctx context.Context, userID int64, row dbgen.GetTOTPRow, code string) (bool, error) {
	seed, err := s.keyring.Decrypt(fieldSeed, row.SeedKeyID.String, row.SeedCiphertext)
	if err != nil {
		return false, err
	}
	step, ok := match(seed, code, s.real.Now())
	if !ok {
		return false, nil
	}
	n, err := s.store.UseStep(ctx, dbgen.UseStepParams{Step: step, OrgID: s.orgID, UserID: userID})
	if err != nil {
		return false, fmt.Errorf("use the TOTP step: %w", err)
	}
	return n == 1, nil
}

// match returns the time step within Skew of now whose code is code. Every candidate is compared in constant time.
func match(seed []byte, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != Digits {
		return 0, false
	}
	current := now.Unix() / int64(Period/time.Second)
	var found int64
	ok := false
	for d := int64(-Skew); d <= Skew; d++ {
		if subtle.ConstantTimeCompare([]byte(Code(seed, current+d)), []byte(code)) == 1 && !ok {
			found, ok = current+d, true
		}
	}
	return found, ok
}

// Code is the code of seed at the time step step (RFC 4226 with the step as the counter, RFC 6238).
func Code(seed []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // G115: steps are positive
	mac := hmac.New(sha1.New, seed)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, value%1_000_000)
}
