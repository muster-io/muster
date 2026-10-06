// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package totp

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/totp/dbgen"
)

const orgID = 7

var (
	t0   = time.Date(2026, 10, 5, 12, 0, 10, 0, time.UTC)
	addr = netip.MustParseAddr("192.0.2.10")
)

type fakeUser struct {
	dbgen.GetTOTPUserRow
}

type fakeTOTP struct {
	dbgen.GetTOTPRow
}

type fakeCode struct {
	id, userID int64
	hash       string
	used       bool
}

type fakeSession struct {
	id, userID int64
	state      string
	ended      bool
}

// fakeStore keeps the rows of the package in memory; fail makes the named method fail.
type fakeStore struct {
	users    []fakeUser
	totp     map[int64]*fakeTOTP
	codes    []*fakeCode
	sessions []*fakeSession
	audit    []auditdb.InsertAuditEntryParams
	fail     map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users: []fakeUser{
			{dbgen.GetTOTPUserRow{ID: 1, PublicID: "SRAAAAAAAAAAAA", Login: "Alice@Example.org", Name: "Alice",
				Status: "active"}},
			{dbgen.GetTOTPUserRow{ID: 2, PublicID: "SRBBBBBBBBBBBB", Login: "bob", Name: "Bob", Status: "deleted"}},
		},
		totp: map[int64]*fakeTOTP{}, fail: map[string]error{},
	}
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	if err := s.fail["InTx"]; err != nil {
		return err
	}
	return f(s)
}

func (s *fakeStore) user(match func(fakeUser) bool) (dbgen.GetTOTPUserRow, error) {
	for _, u := range s.users {
		if match(u) {
			return u.GetTOTPUserRow, nil
		}
	}
	return dbgen.GetTOTPUserRow{}, pgx.ErrNoRows
}

func (s *fakeStore) GetTOTPUser(_ context.Context, arg dbgen.GetTOTPUserParams) (dbgen.GetTOTPUserRow, error) {
	if err := s.fail["GetTOTPUser"]; err != nil {
		return dbgen.GetTOTPUserRow{}, err
	}
	return s.user(func(u fakeUser) bool { return u.ID == arg.ID && arg.OrgID == orgID })
}

func (s *fakeStore) GetTOTPUserByPublicID(_ context.Context, arg dbgen.GetTOTPUserByPublicIDParams) (
	dbgen.GetTOTPUserByPublicIDRow, error) {
	if err := s.fail["GetTOTPUserByPublicID"]; err != nil {
		return dbgen.GetTOTPUserByPublicIDRow{}, err
	}
	u, err := s.user(func(u fakeUser) bool { return u.PublicID == arg.PublicID && arg.OrgID == orgID })
	return dbgen.GetTOTPUserByPublicIDRow(u), err
}

func (s *fakeStore) GetTOTPUserByLogin(_ context.Context, arg dbgen.GetTOTPUserByLoginParams) (
	dbgen.GetTOTPUserByLoginRow, error) {
	u, err := s.user(func(u fakeUser) bool { return strings.EqualFold(u.Login, arg.Login) && arg.OrgID == orgID })
	return dbgen.GetTOTPUserByLoginRow(u), err
}

func (s *fakeStore) GetTOTP(_ context.Context, arg dbgen.GetTOTPParams) (dbgen.GetTOTPRow, error) {
	if err := s.fail["GetTOTP"]; err != nil {
		return dbgen.GetTOTPRow{}, err
	}
	t, ok := s.totp[arg.UserID]
	if !ok || arg.OrgID != orgID {
		return dbgen.GetTOTPRow{}, pgx.ErrNoRows
	}
	return t.GetTOTPRow, nil
}

func (s *fakeStore) SetPendingSeed(_ context.Context, arg dbgen.SetPendingSeedParams) (int64, error) {
	if err := s.fail["SetPendingSeed"]; err != nil {
		return 0, err
	}
	t, ok := s.totp[arg.UserID]
	if !ok {
		t = &fakeTOTP{}
		s.totp[arg.UserID] = t
	}
	if t.SeedCiphertext != nil {
		return 0, nil
	}
	t.PendingSeedCiphertext, t.PendingSeedKeyID = arg.Ciphertext, pgtype.Text{String: arg.KeyID, Valid: true}
	return 1, nil
}

func (s *fakeStore) ActivateSeed(_ context.Context, arg dbgen.ActivateSeedParams) (int64, error) {
	if err := s.fail["ActivateSeed"]; err != nil {
		return 0, err
	}
	t, ok := s.totp[arg.UserID]
	if !ok || t.SeedCiphertext != nil || !bytes.Equal(t.PendingSeedCiphertext, arg.PendingCiphertext) {
		return 0, nil
	}
	t.SeedCiphertext, t.SeedKeyID = arg.Ciphertext, pgtype.Text{String: arg.KeyID, Valid: true}
	t.EnrolledAt = pgtype.Timestamptz{Time: arg.Now, Valid: true}
	t.PendingSeedCiphertext, t.PendingSeedKeyID = nil, pgtype.Text{}
	t.LastUsedStep = pgtype.Int8{Int64: arg.Step, Valid: true}
	return 1, nil
}

func (s *fakeStore) UseStep(_ context.Context, arg dbgen.UseStepParams) (int64, error) {
	if err := s.fail["UseStep"]; err != nil {
		return 0, err
	}
	t := s.totp[arg.UserID]
	if t == nil || t.SeedCiphertext == nil || (t.LastUsedStep.Valid && t.LastUsedStep.Int64 >= arg.Step) {
		return 0, nil
	}
	t.LastUsedStep = pgtype.Int8{Int64: arg.Step, Valid: true}
	return 1, nil
}

func (s *fakeStore) DeleteTOTP(_ context.Context, arg dbgen.DeleteTOTPParams) (int64, error) {
	if err := s.fail["DeleteTOTP"]; err != nil {
		return 0, err
	}
	if _, ok := s.totp[arg.UserID]; !ok {
		return 0, nil
	}
	delete(s.totp, arg.UserID)
	return 1, nil
}

func (s *fakeStore) ListUnusedRecoveryCodes(_ context.Context, arg dbgen.ListUnusedRecoveryCodesParams) (
	[]dbgen.ListUnusedRecoveryCodesRow, error) {
	if err := s.fail["ListUnusedRecoveryCodes"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListUnusedRecoveryCodesRow
	for _, c := range s.codes {
		if c.userID == arg.UserID && !c.used {
			out = append(out, dbgen.ListUnusedRecoveryCodesRow{ID: c.id, CodeHash: c.hash})
		}
	}
	return out, nil
}

func (s *fakeStore) CountUnusedRecoveryCodes(_ context.Context, arg dbgen.CountUnusedRecoveryCodesParams) (int64,
	error) {
	if err := s.fail["CountUnusedRecoveryCodes"]; err != nil {
		return 0, err
	}
	rows, _ := s.ListUnusedRecoveryCodes(context.Background(), dbgen.ListUnusedRecoveryCodesParams(arg))
	return int64(len(rows)), nil
}

func (s *fakeStore) UseRecoveryCode(_ context.Context, arg dbgen.UseRecoveryCodeParams) (int64, error) {
	if err := s.fail["UseRecoveryCode"]; err != nil {
		return 0, err
	}
	for _, c := range s.codes {
		if c.id == arg.ID && !c.used {
			c.used = true
			return 1, nil
		}
	}
	return 0, nil
}

func (s *fakeStore) InsertRecoveryCode(_ context.Context, arg dbgen.InsertRecoveryCodeParams) error {
	if err := s.fail["InsertRecoveryCode"]; err != nil {
		return err
	}
	s.codes = append(s.codes, &fakeCode{id: int64(len(s.codes) + 1), userID: arg.UserID, hash: arg.CodeHash})
	return nil
}

func (s *fakeStore) DeleteUnusedRecoveryCodes(_ context.Context, arg dbgen.DeleteUnusedRecoveryCodesParams) (int64,
	error) {
	if err := s.fail["DeleteUnusedRecoveryCodes"]; err != nil {
		return 0, err
	}
	n := len(s.codes)
	s.codes = slices.DeleteFunc(s.codes, func(c *fakeCode) bool { return c.userID == arg.UserID && !c.used })
	return int64(n - len(s.codes)), nil
}

func (s *fakeStore) DeleteRecoveryCodes(_ context.Context, arg dbgen.DeleteRecoveryCodesParams) (int64, error) {
	if err := s.fail["DeleteRecoveryCodes"]; err != nil {
		return 0, err
	}
	n := len(s.codes)
	s.codes = slices.DeleteFunc(s.codes, func(c *fakeCode) bool { return c.userID == arg.UserID })
	return int64(n - len(s.codes)), nil
}

func (s *fakeStore) ActivateSession(_ context.Context, arg dbgen.ActivateSessionParams) (int64, error) {
	if err := s.fail["ActivateSession"]; err != nil {
		return 0, err
	}
	for _, ss := range s.sessions {
		if ss.id == arg.ID && ss.state == string(auth.StateTOTPEnrolmentRequired) && !ss.ended {
			ss.state = string(auth.StateActive)
			return 1, nil
		}
	}
	return 0, nil
}

func (s *fakeStore) EndSessionsOfUser(_ context.Context, arg dbgen.EndSessionsOfUserParams) (int64, error) {
	if err := s.fail["EndSessionsOfUser"]; err != nil {
		return 0, err
	}
	var n int64
	for _, ss := range s.sessions {
		if ss.userID == arg.UserID && !ss.ended {
			ss.ended = true
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func (s *fakeStore) actions() []string {
	var out []string
	for _, a := range s.audit {
		out = append(out, a.Action)
	}
	return out
}

// fakeAttempts is the sign-in throttle of internal/auth without the throttle: it runs the check and counts the
// attempts by method; the password is "alice-password".
type fakeAttempts struct {
	methods []string
	err     error
}

func (a *fakeAttempts) Attempt(ctx context.Context, _ auth.Session, _ netip.Addr, method string,
	check func(context.Context) (bool, error)) error {
	a.methods = append(a.methods, method)
	if a.err != nil {
		return a.err
	}
	ok, err := check(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return auth.ErrInvalidCredentials
	}
	return nil
}

func (a *fakeAttempts) CheckPassword(_ context.Context, _ auth.Session, password string) (bool, error) {
	return password == "alice-password", nil
}

// keyStore is keyring_state in memory, enough to make a Keyring's first key active.
type keyStore struct {
	keyring.Store
	row *kdb.GetKeyringStateRow
}

func (s *keyStore) GetKeyringState(context.Context) (kdb.GetKeyringStateRow, error) {
	if s.row == nil {
		return kdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *keyStore) CreateKeyringState(_ context.Context, arg kdb.CreateKeyringStateParams) (int64, error) {
	s.row = &kdb.GetKeyringStateRow{ActiveKeyID: arg.ActiveKeyID, ActivatedAt: arg.ActivatedAt,
		CanaryCiphertext: arg.CanaryCiphertext, CanaryKeyID: arg.ActiveKeyID}
	return 1, nil
}

func activeKeyring(t *testing.T) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{'k'}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), &keyStore{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	return k
}

type harness struct {
	svc      *Service
	store    *fakeStore
	attempts *fakeAttempts
	real     *clock.Manual
	business *clock.Manual
	log      *bytes.Buffer
	alice    auth.Session
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := newFakeStore()
	realClock, business := clock.NewManual(t0), clock.NewManual(t0.Add(time.Hour))
	var log bytes.Buffer
	a := &fakeAttempts{}
	w := audit.NewWriter(logging.New(&log, logging.LevelInfo), business)
	svc := New(orgID, store, activeKeyring(t), w, clock.Clocks{Business: business, Real: realClock}, a)
	store.sessions = []*fakeSession{{id: 5, userID: 1, state: string(auth.StateActive)}}
	// A recovery code costs no argon2id here, except in TestEnrolment, which checks the real hashes.
	svc.hash = func(_ context.Context, code string) (string, error) { return "plain:" + code, nil }
	svc.verifyHash = func(_ context.Context, encoded, code string) (bool, error) {
		if !strings.HasPrefix(encoded, "plain:") {
			return auth.VerifyPassword(t.Context(), encoded, code)
		}
		return encoded == "plain:"+code, nil
	}
	return &harness{svc: svc, store: store, attempts: a, real: realClock, business: business, log: &log,
		alice: auth.Session{ID: 5, PublicID: "SNAAAAAAAAAAAA", State: auth.StateActive,
			User: auth.Principal{ID: 1, PublicID: "SRAAAAAAAAAAAA", Name: "Alice"}}}
}

func decodeSeed(t *testing.T, secret string) []byte {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return seed
}

// enrol begins and confirms the enrolment of Alice and returns her seed and recovery codes.
func (h *harness) enrol(t *testing.T) ([]byte, []string) {
	t.Helper()
	e, err := h.svc.Begin(t.Context(), h.alice)
	if err != nil {
		t.Fatal(err)
	}
	seed := decodeSeed(t, e.Secret)
	codes, err := h.svc.Confirm(t.Context(), h.alice, Code(seed, h.step()), addr)
	if err != nil {
		t.Fatal(err)
	}
	return seed, codes
}

func (h *harness) step() int64 {
	return h.real.Now().Unix() / 30
}

// TestCode checks the codes against the SHA-1 test vectors of RFC 6238, appendix B, cut to six digits.
func TestCode(t *testing.T) {
	seed := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037",
		20000000000: "353130",
	} {
		if got := Code(seed, unix/30); got != want {
			t.Errorf("Code at %d = %s, want %s", unix, got, want)
		}
	}
}

// TestMatch: a code of the current step or one step either way matches, with its step; any other does not.
func TestMatch(t *testing.T) {
	seed := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	cur := now.Unix() / 30
	for d := int64(-1); d <= 1; d++ {
		if step, ok := match(seed, Code(seed, cur+d), now); !ok || step != cur+d {
			t.Errorf("a code %d steps away = %d, %v", d, step, ok)
		}
	}
	for _, c := range []string{Code(seed, cur-2), Code(seed, cur+2), "", "12345", "1234567", "abcdef"} {
		if _, ok := match(seed, c, now); ok {
			t.Errorf("%q matched", c)
		}
	}
	if _, ok := match(seed, " "+Code(seed, cur)[:3]+" "+Code(seed, cur)[3:], now); !ok {
		t.Error("a code with spaces did not match")
	}
}

// TestEnrolment is C-03.FR-10: the enrolment returns a base32 seed and its otpauth URI, stores only its ciphertext as
// the pending seed, and a current code confirms it with RecoveryCodes single-use recovery codes, recorded as
// totp.enrolled; the confirming code's step is used.
func TestEnrolment(t *testing.T) {
	h := newHarness(t)
	h.svc.hash = auth.HashPassword
	if st, err := h.svc.Status(t.Context(), 1); err != nil || st.Enrolled || st.Pending {
		t.Fatalf("Status before = %+v, %v", st, err)
	}
	if _, err := h.svc.Confirm(t.Context(), h.alice, "123456", addr); !errors.Is(err, ErrNotStarted) {
		t.Errorf("a confirmation without an enrolment: %v", err)
	}
	e, err := h.svc.Begin(t.Context(), h.alice)
	if err != nil {
		t.Fatal(err)
	}
	seed := decodeSeed(t, e.Secret)
	if len(seed) != seedBytes ||
		e.URI != "otpauth://totp/Muster:Alice@Example.org?secret="+e.Secret+"&issuer=Muster" {
		t.Errorf("enrolment = %+v", e)
	}
	stored := h.store.totp[1]
	if bytes.Contains(stored.PendingSeedCiphertext, seed) || stored.SeedCiphertext != nil {
		t.Error("the pending seed is stored in clear or as the seed")
	}
	if st, _ := h.svc.Status(t.Context(), 1); st.Enrolled || !st.Pending {
		t.Errorf("Status while pending = %+v", st)
	}
	if strings.Contains(h.log.String(), e.Secret) {
		t.Error("the seed reached the log")
	}
	// A new enrolment replaces the pending seed; a code of the old one is wrong.
	again, err := h.svc.Begin(t.Context(), h.alice)
	if err != nil || again.Secret == e.Secret {
		t.Fatalf("a second Begin = %+v, %v", again, err)
	}
	if _, err := h.svc.Confirm(t.Context(), h.alice, Code(seed, h.step()), addr); !errors.Is(err,
		auth.ErrInvalidCredentials) {
		t.Errorf("a code of the replaced seed: %v", err)
	}
	seed = decodeSeed(t, again.Secret)
	codes, err := h.svc.Confirm(t.Context(), h.alice, Code(seed, h.step()-1), addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodes || len(h.store.codes) != RecoveryCodes {
		t.Fatalf("%d codes, %d stored", len(codes), len(h.store.codes))
	}
	format := regexp.MustCompile(`^[0-9a-hjkmnp-tv-z]{5}-[0-9a-hjkmnp-tv-z]{5}$`)
	for i, c := range codes {
		if !format.MatchString(c) || strings.Contains(h.store.codes[i].hash, c) ||
			!strings.HasPrefix(h.store.codes[i].hash, "$argon2id$") {
			t.Errorf("code %q, hash %q", c, h.store.codes[i].hash)
		}
	}
	stored = h.store.totp[1]
	if stored.SeedCiphertext == nil || stored.PendingSeedCiphertext != nil || !stored.EnrolledAt.Time.Equal(
		h.business.Now()) || stored.LastUsedStep.Int64 != h.step()-1 || stored.SeedKeyID.String == "" {
		t.Errorf("stored after the confirmation = %+v", stored)
	}
	st, err := h.svc.Status(t.Context(), 1)
	if err != nil || !st.Enrolled || st.Pending || st.RecoveryCodesRemaining != RecoveryCodes ||
		!st.EnrolledAt.Equal(h.business.Now()) {
		t.Errorf("Status after = %+v, %v", st, err)
	}
	if got := h.store.actions(); !slices.Equal(got, []string{ActionEnrolled}) {
		t.Errorf("audit = %v", got)
	}
	if !slices.Equal(h.attempts.methods, []string{auth.FailureTOTP, auth.FailureTOTP}) {
		t.Errorf("the codes were not checked as attempts: %v", h.attempts.methods)
	}
	if _, err := h.svc.Begin(t.Context(), h.alice); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Errorf("Begin when enrolled: %v", err)
	}
	if _, err := h.svc.Confirm(t.Context(), h.alice, Code(seed, h.step()), addr); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Errorf("Confirm when enrolled: %v", err)
	}
	if h.store.sessions[0].state != string(auth.StateActive) {
		t.Error("an active session changed")
	}
}

// TestEnrolmentActivatesTheSession: the confirmation of a session in the state totp_enrolment_required makes it
// active (C-03.FR-10, FR-20).
func TestEnrolmentActivatesTheSession(t *testing.T) {
	h := newHarness(t)
	h.store.sessions[0].state = string(auth.StateTOTPEnrolmentRequired)
	h.alice.State = auth.StateTOTPEnrolmentRequired
	h.enrol(t)
	if h.store.sessions[0].state != string(auth.StateActive) {
		t.Errorf("the session is %s after the confirmation", h.store.sessions[0].state)
	}
}

// TestVerify is the second step of sign-in: a current code works once per time step — a replay, or an older step,
// is refused — and each recovery code works once, in any letter case and with or without the dash.
func TestVerify(t *testing.T) {
	h := newHarness(t)
	if ok, err := h.svc.Verify(t.Context(), 1, auth.Proof{TOTPCode: "123456"}); ok || err != nil {
		t.Errorf("a user without TOTP: %v, %v", ok, err)
	}
	seed, codes := h.enrol(t)
	verify := func(p auth.Proof) bool {
		t.Helper()
		ok, err := h.svc.Verify(t.Context(), 1, p)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if verify(auth.Proof{TOTPCode: Code(seed, h.step())}) {
		t.Error("the confirming code was accepted again in its step")
	}
	h.real.Advance(Period)
	if !verify(auth.Proof{TOTPCode: Code(seed, h.step())}) {
		t.Error("a code of the next step was refused")
	}
	if verify(auth.Proof{TOTPCode: Code(seed, h.step())}) || verify(auth.Proof{TOTPCode: Code(seed, h.step()-1)}) {
		t.Error("a replayed or older code was accepted")
	}
	h.business.Advance(24 * time.Hour)
	if verify(auth.Proof{TOTPCode: Code(seed, h.step()+2)}) {
		t.Error("the business clock moved the time steps")
	}
	if !verify(auth.Proof{RecoveryCode: strings.ToUpper(strings.ReplaceAll(codes[3], "-", " "))}) {
		t.Error("a recovery code was refused")
	}
	if verify(auth.Proof{RecoveryCode: codes[3]}) || verify(auth.Proof{RecoveryCode: "0000000000"}) ||
		verify(auth.Proof{RecoveryCode: "short"}) {
		t.Error("a used, wrong or malformed recovery code was accepted")
	}
	if st, _ := h.svc.Status(t.Context(), 1); st.RecoveryCodesRemaining != RecoveryCodes-1 {
		t.Errorf("%d recovery codes remain", st.RecoveryCodesRemaining)
	}
}

// TestRegenerateRecoveryCodes: a current code replaces the unused recovery codes, recorded as
// totp.recovery_codes_regenerated; a wrong code changes nothing.
func TestRegenerateRecoveryCodes(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.RegenerateRecoveryCodes(t.Context(), h.alice, "123456", addr); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("without TOTP: %v", err)
	}
	seed, old := h.enrol(t)
	if ok, _ := h.svc.Verify(t.Context(), 1, auth.Proof{RecoveryCode: old[0]}); !ok {
		t.Fatal("a recovery code was refused")
	}
	h.real.Advance(Period)
	if _, err := h.svc.RegenerateRecoveryCodes(t.Context(), h.alice, "000000", addr); !errors.Is(err,
		auth.ErrInvalidCredentials) {
		t.Errorf("a wrong code: %v", err)
	}
	codes, err := h.svc.RegenerateRecoveryCodes(t.Context(), h.alice, Code(seed, h.step()), addr)
	if err != nil || len(codes) != RecoveryCodes {
		t.Fatalf("= %v, %v", codes, err)
	}
	if ok, _ := h.svc.Verify(t.Context(), 1, auth.Proof{RecoveryCode: old[1]}); ok {
		t.Error("an old recovery code still works")
	}
	if ok, _ := h.svc.Verify(t.Context(), 1, auth.Proof{RecoveryCode: codes[0]}); !ok {
		t.Error("a new recovery code was refused")
	}
	if n := len(h.store.codes); n != RecoveryCodes+1 {
		t.Errorf("%d codes stored, want the new ones and the used one", n)
	}
	if got := h.store.actions(); !slices.Equal(got, []string{ActionEnrolled, ActionRecoveryCodesRegenerated}) {
		t.Errorf("audit = %v", got)
	}
}

// TestRemove is C-03.FR-27 and C-03.AC-15: a wrong password or code is refused and keeps TOTP; the right password, a
// current code or a recovery code removes it with its recovery codes, recorded as totp.removed with the kind of proof.
func TestRemove(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.Remove(t.Context(), h.alice, Removal{Password: "alice-password"}, addr); !errors.Is(err,
		ErrNotEnrolled) {
		t.Errorf("without TOTP: %v", err)
	}
	seed, codes := h.enrol(t)
	for _, p := range []Removal{{Password: "wrong-password"}, {TOTPCode: "000000"}, {RecoveryCode: "0000000000"}} {
		if err := h.svc.Remove(t.Context(), h.alice, p, addr); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("%+v: %v", p, err)
		}
	}
	if st, _ := h.svc.Status(t.Context(), 1); !st.Enrolled {
		t.Fatal("a wrong proof removed TOTP")
	}
	if !slices.Equal(h.attempts.methods[1:], []string{auth.MethodLocal, auth.FailureTOTP, auth.FailureTOTP}) {
		t.Errorf("attempts = %v", h.attempts.methods)
	}
	h.real.Advance(Period)
	for _, kind := range []string{"password", "totp_code", "recovery_code"} {
		p := map[string]Removal{"password": {Password: "alice-password"}, "totp_code": {TOTPCode: Code(seed, h.step())},
			"recovery_code": {RecoveryCode: codes[0]}}[kind]
		if err := h.svc.Remove(t.Context(), h.alice, p, addr); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		if _, ok := h.store.totp[1]; ok || len(h.store.codes) != 0 {
			t.Fatalf("%+v left TOTP or codes", p)
		}
		seed, codes = h.enrol(t)
		h.real.Advance(Period)
	}
	var proofs []string
	for _, a := range h.store.audit {
		if a.Action == ActionRemoved {
			proofs = append(proofs, string(a.Details))
		}
	}
	if len(proofs) != 3 || !strings.Contains(proofs[0], `"password"`) || !strings.Contains(proofs[1], `"totp_code"`) ||
		!strings.Contains(proofs[2], `"recovery_code"`) {
		t.Errorf("totp.removed entries = %v", proofs)
	}
	// A begun enrolment alone is removed with the password.
	if err := h.svc.Remove(t.Context(), h.alice, Removal{Password: "alice-password"}, addr); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Begin(t.Context(), h.alice); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Remove(t.Context(), h.alice, Removal{Password: "alice-password"}, addr); err != nil {
		t.Errorf("a pending enrolment: %v", err)
	}
	h.enrol(t)
	h.attempts.err = &auth.ThrottledError{RetryAfter: time.Second}
	if err := h.svc.Remove(t.Context(), h.alice, Removal{Password: "alice-password"}, addr); err == nil {
		t.Error("a throttled removal went through")
	}
}

// TestReset is C-03.FR-11: an Admin's reset removes TOTP and the recovery codes, ends the user's sessions and is
// recorded as totp.reset; the CLI does the same with the --actor name; a user without TOTP is left alone.
func TestReset(t *testing.T) {
	h := newHarness(t)
	h.enrol(t)
	admin := audit.User(9, "SRZZZZZZZZZZZZ")
	for _, id := range []string{"nonsense", "SRCCCCCCCCCCCC", "SRBBBBBBBBBBBB"} {
		if err := h.svc.Reset(t.Context(), admin, audit.TransportUI, addr, id); !errors.Is(err, ErrUserNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
	if err := h.svc.Reset(t.Context(), audit.User(1, "SRAAAAAAAAAAAA"), audit.TransportUI, addr,
		"SRAAAAAAAAAAAA"); !errors.Is(err, ErrOwnTOTP) {
		t.Errorf("an Admin's own TOTP: %v", err)
	}
	if err := h.svc.Reset(t.Context(), admin, audit.TransportUI, addr, "sraaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.store.totp[1]; ok || len(h.store.codes) != 0 || !h.store.sessions[0].ended {
		t.Error("the reset left TOTP, codes or a session")
	}
	last := h.store.audit[len(h.store.audit)-1]
	if last.Action != ActionReset || last.ActorUserID.Int64 != 9 || last.ResourcePublicID.String != "SRAAAAAAAAAAAA" ||
		!strings.Contains(string(last.Details), `"sessions_ended":1`) {
		t.Errorf("entry = %+v", last)
	}
	n := len(h.store.audit)
	if err := h.svc.Reset(t.Context(), admin, audit.TransportUI, addr, "SRAAAAAAAAAAAA"); err != nil ||
		len(h.store.audit) != n {
		t.Errorf("a reset without TOTP: %v, %d entries", err, len(h.store.audit)-n)
	}
	if _, _, err := h.svc.ResetByLogin(t.Context(), " ", "alice@example.org"); err == nil {
		t.Error("a CLI reset without --actor")
	}
	if _, _, err := h.svc.ResetByLogin(t.Context(), "ops", "nobody"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("an unknown login: %v", err)
	}
	h.store.sessions = append(h.store.sessions, &fakeSession{id: 6, userID: 1, state: string(auth.StateActive)})
	h.alice.ID, h.alice.State = 1, auth.StateActive
	h.enrol(t)
	id, removed, err := h.svc.ResetByLogin(t.Context(), "ops", "ALICE@example.org")
	if err != nil || id != "SRAAAAAAAAAAAA" || !removed {
		t.Fatalf("= %s, %v, %v", id, removed, err)
	}
	last = h.store.audit[len(h.store.audit)-1]
	if last.Action != ActionReset || last.ActorKind != "cli" || last.ActorName.String != "ops" || last.Transport != "cli" {
		t.Errorf("CLI entry = %+v", last)
	}
	if _, removed, err := h.svc.ResetByLogin(t.Context(), "ops", "alice@example.org"); removed || err != nil {
		t.Errorf("a second CLI reset = %v, %v", removed, err)
	}
}

// TestStoreFailures: a failing query or Secret fails the operation, and nothing is half done in the store.
func TestStoreFailures(t *testing.T) {
	boom := errors.New("db down")
	for _, method := range []string{"GetTOTPUser", "SetPendingSeed"} {
		h := newHarness(t)
		h.store.fail[method] = boom
		if _, err := h.svc.Begin(t.Context(), h.alice); !errors.Is(err, boom) {
			t.Errorf("Begin with %s failing: %v", method, err)
		}
	}
	for _, method := range []string{"GetTOTP", "ActivateSeed", "DeleteUnusedRecoveryCodes", "InsertRecoveryCode",
		"ActivateSession", "InsertAuditEntry"} {
		h := newHarness(t)
		e, _ := h.svc.Begin(t.Context(), h.alice)
		h.store.fail[method] = boom
		if _, err := h.svc.Confirm(t.Context(), h.alice, Code(decodeSeed(t, e.Secret), h.step()), addr); !errors.Is(err,
			boom) {
			t.Errorf("Confirm with %s failing: %v", method, err)
		}
	}
	h := newHarness(t)
	e, _ := h.svc.Begin(t.Context(), h.alice)
	h.store.totp[1].PendingSeedCiphertext = append([]byte{}, h.store.totp[1].PendingSeedCiphertext...)
	h.store.totp[1].PendingSeedCiphertext[20] ^= 1
	if _, err := h.svc.Confirm(t.Context(), h.alice, Code(decodeSeed(t, e.Secret), h.step()), addr); err == nil ||
		errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("a pending seed that does not decrypt: %v", err)
	}
	for _, method := range []string{"GetTOTP", "UseStep", "ListUnusedRecoveryCodes", "UseRecoveryCode",
		"CountUnusedRecoveryCodes"} {
		h := newHarness(t)
		seed, codes := h.enrol(t)
		h.real.Advance(Period)
		h.store.fail[method] = boom
		_, err1 := h.svc.Verify(t.Context(), 1, auth.Proof{TOTPCode: Code(seed, h.step())})
		_, err2 := h.svc.Verify(t.Context(), 1, auth.Proof{RecoveryCode: codes[0]})
		_, err3 := h.svc.Status(t.Context(), 1)
		if !errors.Is(err1, boom) && !errors.Is(err2, boom) && !errors.Is(err3, boom) {
			t.Errorf("Verify and Status with %s failing: %v, %v, %v", method, err1, err2, err3)
		}
	}
	for _, method := range []string{"GetTOTP", "DeleteUnusedRecoveryCodes", "InsertAuditEntry"} {
		h := newHarness(t)
		seed, _ := h.enrol(t)
		h.real.Advance(Period)
		h.store.fail[method] = boom
		if _, err := h.svc.RegenerateRecoveryCodes(t.Context(), h.alice, Code(seed, h.step()), addr); !errors.Is(err,
			boom) {
			t.Errorf("Regenerate with %s failing: %v", method, err)
		}
	}
	for _, method := range []string{"GetTOTP", "DeleteTOTP", "DeleteRecoveryCodes", "InsertAuditEntry"} {
		h := newHarness(t)
		h.enrol(t)
		h.store.fail[method] = boom
		if err := h.svc.Remove(t.Context(), h.alice, Removal{Password: "alice-password"}, addr); !errors.Is(err, boom) {
			t.Errorf("Remove with %s failing: %v", method, err)
		}
	}
	for _, method := range []string{"GetTOTPUserByPublicID", "DeleteTOTP", "DeleteRecoveryCodes", "EndSessionsOfUser",
		"InsertAuditEntry", "InTx"} {
		h := newHarness(t)
		h.enrol(t)
		h.store.fail[method] = boom
		if err := h.svc.Reset(t.Context(), audit.System, audit.TransportUI, addr, "SRAAAAAAAAAAAA"); !errors.Is(err, boom) {
			t.Errorf("Reset with %s failing: %v", method, err)
		}
	}
	h = newHarness(t)
	h.enrol(t)
	h.store.totp[1].SeedKeyID = pgtype.Text{String: "k-unknown", Valid: true}
	h.real.Advance(Period)
	if _, err := h.svc.Verify(t.Context(), 1, auth.Proof{TOTPCode: "123456"}); err == nil {
		t.Error("a seed of an unknown key: no error")
	}
}
