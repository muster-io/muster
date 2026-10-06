// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build lint

package archlint

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/api"
	"github.com/muster-io/muster/internal/audit"
	adb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/auth"
	authdb "github.com/muster-io/muster/internal/auth/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/doctor"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/runtime"
	"github.com/muster-io/muster/internal/totp"
	tdb "github.com/muster-io/muster/internal/totp/dbgen"
	"github.com/muster-io/muster/internal/users"
	udb "github.com/muster-io/muster/internal/users/dbgen"
)

const (
	probeSecrets  = 3
	maxErrorDepth = 64

	// secretSuffix makes every secret change under URL escaping, so an escaped copy is not the plain one.
	secretSuffix = "+/="
)

// Probe pushes known secrets through one code path. Run hands the secrets to the code under test, sends its log
// output to log and returns the error the code path returned; ctx is the context of the test that runs it.
type Probe struct {
	Name string
	Run  func(ctx context.Context, secrets []string, log io.Writer) error
}

// registry holds the probes of rule 5. A story whose code carries secrets (the logger, outbound HTTP, the keyring, the
// messenger adapters, ...) adds a probe for that path here.
var registry = []Probe{
	{Name: "domain_logger", Run: probeDomainLogger},
	{Name: "bootstrap_settings", Run: probeBootstrapSettings},
	{Name: "keyring", Run: probeKeyring},
	{Name: "doctor", Run: probeDoctor},
	{Name: "outbound_http", Run: probeOutbound},
	{Name: "sign_in", Run: probeSignIn},
	{Name: "totp", Run: probeTOTP},
}

// masterKey is a master key whose material holds secret, padded to keyring.KeySize, in base64 as MUSTER_SECRET_KEYS
// takes it: a leak of the key in any encoding shows the secret in that encoding.
func masterKey(secret string) string {
	m := make([]byte, keyring.KeySize)
	copy(m, secret)
	return base64.StdEncoding.EncodeToString(m)
}

// probeBootstrapSettings gives the secrets to the bootstrap settings — inside database URLs that are refused, as the
// database password, inside the master keys and as the bootstrap Admin password — and runs the server and muster
// migrate against an address where nothing listens: the startup log and the startup errors must not carry them.
func probeBootstrapSettings(ctx context.Context, secrets []string, log io.Writer) error {
	base := []string{
		"MUSTER_PUBLIC_URL=http://localhost:8080",
		"MUSTER_SECRET_KEYS=" + masterKey(secrets[1]),
		"MUSTER_BOOTSTRAP_ADMIN_EMAIL=admin@example.org",
		"MUSTER_BOOTSTRAP_ADMIN_PASSWORD=" + secrets[2],
		"MUSTER_LISTEN_APP=127.0.0.1:0",
		"MUSTER_LISTEN_INGEST=127.0.0.1:0",
		"MUSTER_LISTEN_INTERNAL=127.0.0.1:0",
	}
	refused := append(slices.Clone(base),
		"MUSTER_DATABASE_URL=mysql://muster:"+url.QueryEscape(secrets[0])+"@db.example/muster",
		"MUSTER_DATABASE_SESSION_URL=http://muster:"+secrets[1]+"@db.example/muster")
	unreachable := append(slices.Clone(base),
		"MUSTER_DATABASE_HOST=127.0.0.1", "MUSTER_DATABASE_PORT=1", "MUSTER_DATABASE_NAME=muster",
		"MUSTER_DATABASE_USER=muster", "MUSTER_DATABASE_PASSWORD="+secrets[0], "MUSTER_DATABASE_SSLMODE=disable")
	return errors.Join(
		runtime.Run(ctx, runtime.Options{Environ: refused, Stdout: log}),
		runtime.Run(ctx, runtime.Options{Environ: unreachable, Stdout: log}),
		runtime.Migrate(ctx, runtime.Options{Environ: unreachable, Stdout: log}),
	)
}

// probeDoctor runs muster doctor with the secrets as the database password, inside the master keys and as an entry
// of MUSTER_SECRET_KEYS that is not a key, against an address where nothing listens and with refused database URLs:
// the lines it prints and the error it returns must not carry them.
func probeDoctor(ctx context.Context, secrets []string, log io.Writer) error {
	base := []string{"MUSTER_PUBLIC_URL=http://localhost:8080"}
	unreachable := append(slices.Clone(base),
		"MUSTER_SECRET_KEYS="+masterKey(secrets[1])+","+secrets[2],
		"MUSTER_DATABASE_HOST=127.0.0.1", "MUSTER_DATABASE_PORT=1", "MUSTER_DATABASE_NAME=muster",
		"MUSTER_DATABASE_USER=muster", "MUSTER_DATABASE_PASSWORD="+secrets[0], "MUSTER_DATABASE_SSLMODE=disable")
	refused := append(slices.Clone(base), "MUSTER_SECRET_KEYS="+masterKey(secrets[1]),
		"MUSTER_DATABASE_URL=mysql://muster:"+url.QueryEscape(secrets[0])+"@db.example/muster")
	_, errUnreachable := doctor.Run(ctx, doctor.Options{Environ: unreachable, Out: log})
	_, errRefused := doctor.Run(ctx, doctor.Options{Environ: refused, Out: log})
	return errors.Join(errUnreachable, errRefused)
}

// probeKeyring loads master keys made of the secrets — also as an entry that is not a key, from the variable and from
// a file — writes and checks the key canary, encrypts a secret and opens it for the right field, another field, with
// altered bytes and with an unknown key, then refuses a Keyring that cannot read the canary and an active key that is
// not held: neither the key material nor the decrypted secret may reach a log line or an error.
func probeKeyring(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	keys := masterKey(secrets[0]) + "," + masterKey(secrets[1])
	_, errVar := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(keys + "," + secrets[2]),
		Source: keyring.SecretKeysVar}, false)
	_, errFile := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(masterKey(secrets[0]) + "\n" + secrets[1] + "\n"),
		Source: keyring.SecretKeysFileVar}, false)
	_, errRepeated := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(keys + "," + masterKey(secrets[0])),
		Source: keyring.SecretKeysVar}, false)
	k, err := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(keys), Source: keyring.SecretKeysVar}, false)
	if err != nil {
		return errors.Join(errVar, errFile, errRepeated, err)
	}
	store := &probeStore{}
	st, err := k.Establish(ctx, store, time.Unix(0, 0))
	if err != nil {
		return err
	}
	errs := []error{errVar, errFile, errRepeated, k.Open(ctx, logger, st)}

	const field = "destinations.bot_token"
	ct, id, err := k.Encrypt(field, []byte(secrets[2]))
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	altered := bytes.Clone(ct)
	altered[len(altered)-1] ^= 1
	for _, f := range []func() ([]byte, error){
		func() ([]byte, error) { return k.Decrypt(field, id, ct) },
		func() ([]byte, error) { return k.Decrypt("oidc_settings.client_secret", id, ct) },
		func() ([]byte, error) { return k.Decrypt(field, id, altered) },
		func() ([]byte, error) { return k.Decrypt(field, "k-unknown", ct) },
	} {
		_, err := f()
		errs = append(errs, err)
	}

	other, err := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(masterKey(secrets[2])),
		Source: keyring.SecretKeysVar}, false)
	if err == nil {
		errs = append(errs, other.Open(ctx, logger, st))
	}
	store.active = "k-unknown"
	r := keyring.NewRecorder(k, store, clock.Real{}, logger, "probe", "probe", "dev")
	return errors.Join(append(errs, err, r.Start(ctx), r.Refresh(ctx))...)
}

// probeStore is keyring_state and replicas in memory for probeKeyring.
type probeStore struct {
	state  *dbgen.GetKeyringStateRow
	active string
}

func (s *probeStore) GetKeyringState(context.Context) (dbgen.GetKeyringStateRow, error) {
	if s.state == nil {
		return dbgen.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.state, nil
}

func (s *probeStore) CreateKeyringState(_ context.Context, arg dbgen.CreateKeyringStateParams) (int64, error) {
	s.state = &dbgen.GetKeyringStateRow{ActiveKeyID: arg.ActiveKeyID, ActivatedAt: arg.ActivatedAt,
		CanaryCiphertext: arg.CanaryCiphertext, CanaryKeyID: arg.ActiveKeyID}
	return 1, nil
}

func (s *probeStore) GetActiveKeyID(context.Context) (string, error) { return s.active, nil }

func (s *probeStore) RecordReplica(context.Context, dbgen.RecordReplicaParams) error { return nil }

func (s *probeStore) DeleteReplica(context.Context, string) error { return nil }

func (s *probeStore) ListLiveReplicas(context.Context, time.Time) ([]dbgen.Replica, error) {
	return nil, nil
}

// probeOutbound registers the secrets with outbound clients and sends them: in the path and the query of a request
// that fails with a network error, of a request the outbound address policy blocks and of a request whose answer is
// a redirect to a URL that carries them; in an error text the provider answers with; as the password of a SOCKS5
// proxy that refuses it and of an HTTP proxy that cannot be reached; and in a proxy address that does not parse.
// Neither the log output nor the returned errors may carry them.
func probeOutbound(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	registered := []logging.Secret{logging.Secret(secrets[0]), logging.Secret(secrets[1])}
	client := func(proxy *outbound.Proxy) (*outbound.Client, error) {
		return outbound.New(outbound.Config{Class: outbound.ClassDelivery, ConnectTimeout: 2 * time.Second,
			Timeout: 5 * time.Second, Proxy: proxy, Secrets: registered, Policy: outbound.StaticPolicy(policy),
			Logger: logger, Clock: clock.Real{}})
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/answer" {
				http.Error(w, "unknown token "+secrets[0]+" "+url.QueryEscape(secrets[1]), http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, "/bot"+url.PathEscape(secrets[0])+"/x?token="+url.QueryEscape(secrets[1]),
				http.StatusFound)
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	socks, err := fakeproxy.Start(ctx, fakeproxy.SOCKS5, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "other"})
	if err != nil {
		return err
	}
	defer func() { _ = socks.Close() }()

	var errs []error
	path := "/bot" + secrets[0] + "/getMe?token=" + url.QueryEscape(secrets[1])
	direct, err := client(nil)
	if err != nil {
		return err
	}
	for _, target := range []string{"http://127.0.0.1:1" + path, "http://169.254.169.254" + path,
		"http://" + ln.Addr().String() + path, "http://" + ln.Addr().String() + "/answer"} {
		_, err := direct.Do(ctx, outbound.Request{URL: target})
		errs = append(errs, err)
	}
	for _, proxy := range []*outbound.Proxy{
		{Type: outbound.ProxySOCKS5, Address: socks.Addr(), Username: "muster", Password: logging.Secret(secrets[2])},
		{Type: outbound.ProxyHTTP, Address: "127.0.0.1:1", Username: "muster", Password: logging.Secret(secrets[2])},
	} {
		c, err := client(proxy)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, err = c.Do(ctx, outbound.Request{URL: "http://127.0.0.1:1" + path})
		errs = append(errs, err)
	}
	_, err = client(&outbound.Proxy{Type: outbound.ProxyHTTP, Address: secrets[1]})
	return errors.Join(append(errs, err)...)
}

// probeDomainLogger logs the secrets as logging.Secret values through the domain logger: as a field, inside a value
// encoded as JSON and inside an error.
func probeDomainLogger(ctx context.Context, secrets []string, log io.Writer) error {
	type credentials struct {
		User     string         `json:"user"`
		Password logging.Secret `json:"password"`
	}
	logger := logging.New(log, logging.LevelInfo)
	logger.Log(ctx, logging.ProcessStarted,
		logging.F("version", logging.Secret(secrets[0])),
		logging.F("commit", credentials{User: "muster", Password: logging.Secret(secrets[1])}))
	logger.Log(ctx, logging.ProcessStarted,
		logging.F("version", fmt.Errorf("dial with %v: %w", logging.Secret(secrets[2]), io.ErrUnexpectedEOF)))
	return nil
}

// probeSignIn sends the secrets through sign-in and the profile: the first as the bootstrap Admin's password, which
// then signs in; the second as a wrong password of a known and an unknown login and as a wrong current password; the
// third as a session cookie, a CSRF token and the new password, and the second again while the throttle blocks. It
// also posts the second as a password to the API, once while the database fails and once with a value of the wrong
// type, and sends the third to the API as a cookie and as the CSRF token of a real session. Neither the log, the
// errors nor the API's answers, which the probe returns as its error, may carry them.
func probeSignIn(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	business := clock.NewManual(now)
	w := audit.NewWriter(logger, business)
	us := &probeUsers{}
	errs := []error{
		users.EnsureBootstrapAdmin(ctx, us, w, logger, 1, users.Bootstrap{Email: "probe@example.org",
			Password: logging.Secret(secrets[0])}, now),
	}
	if len(us.created) != 1 {
		return errors.Join(append(errs, errors.New("the bootstrap Admin was not created"))...)
	}
	us.admins = 1
	errs = append(errs, users.EnsureBootstrapAdmin(ctx, us, w, logger, 1, users.Bootstrap{Email: "probe@example.org",
		Password: logging.Secret(secrets[0])}, now))

	k, err := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(masterKey("probe")), Source: keyring.SecretKeysVar},
		false)
	if err != nil {
		return err
	}
	st, err := k.Establish(ctx, &probeStore{}, now)
	if err != nil {
		return err
	}
	errs = append(errs, k.Open(ctx, logger, st))
	as := &probeAuth{hash: us.created[0].PasswordHash.String}
	roles := auth.Roles{auth.RoleAdmin: {"users:read"}, auth.RoleResponder: {"alerts:read"},
		auth.RoleViewer: {"alerts:read"}}
	svc := auth.NewService(1, as, k, w, business, roles)
	addr := netip.MustParseAddr("192.0.2.1")
	for _, login := range []string{"probe@example.org", "nobody@example.org"} {
		_, err := svc.SignIn(ctx, auth.SignInRequest{Login: login, Password: secrets[1], Address: addr})
		errs = append(errs, err)
	}
	sess, err := svc.SignIn(ctx, auth.SignInRequest{Login: "probe@example.org", Password: secrets[0], Address: addr})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	_, err = svc.Authenticate(ctx, secrets[2])
	svc.CheckCSRF(sess, secrets[2])
	errs = append(errs, err,
		svc.ChangePassword(ctx, sess, auth.PasswordChange{Current: secrets[1], New: secrets[2], Address: addr}),
		svc.ChangePassword(ctx, sess, auth.PasswordChange{Current: secrets[0], New: secrets[2], Address: addr}))

	as.blocked = true
	_, err = svc.SignIn(ctx, auth.SignInRequest{Login: "probe@example.org", Password: secrets[1], Address: addr})
	errs = append(errs, err)
	as.blocked = false

	h, err := api.New(api.Config{Sessions: svc, Users: users.NewService(1, us, w, business), Log: logger,
		Real: clock.Real{}})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	var answers []string
	send := func(method, path, body, cookie, csrf string) error {
		r, err := http.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		if err != nil {
			return err
		}
		r.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			r.Header.Set("Cookie", auth.CookieName+"="+cookie)
		}
		if csrf != "" {
			r.Header.Set(auth.CSRFHeader, csrf)
		}
		rec := &probeRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		answers = append(answers, rec.body.String())
		return nil
	}
	for _, req := range [][5]string{
		{http.MethodGet, "/api/v1/me", "", secrets[2], ""},
		{http.MethodPut, "/api/v1/me", `{"name":"probe"}`, sess.Cookie(), secrets[2]},
		{http.MethodDelete, "/api/v1/me/sessions", "", sess.Cookie(), secrets[2]},
	} {
		if err := send(req[0], req[1], req[2], req[3], req[4]); err != nil {
			return err
		}
	}
	as.fail = errors.New("the database is unavailable")
	for _, body := range []string{
		`{"login":"probe@example.org","password":"` + secrets[1] + `"}`,
		`{"login":"probe@example.org","password":["` + secrets[1] + `"]}`,
	} {
		if err := send(http.MethodPost, "/api/v1/sessions", body, "", ""); err != nil {
			return err
		}
	}
	errs = append(errs, fmt.Errorf("answers: %s", strings.Join(answers, " | ")))
	return errors.Join(errs...)
}

// probeRecorder is an http.ResponseWriter that keeps the body.
type probeRecorder struct {
	header http.Header
	body   bytes.Buffer
}

func (r *probeRecorder) Header() http.Header         { return r.header }
func (r *probeRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *probeRecorder) WriteHeader(int)             {}

// probeUsers is the users of probeSignIn in memory.
type probeUsers struct {
	users.Store
	admins  int64
	created []udb.CreateUserParams
}

func (s *probeUsers) InTx(_ context.Context, f func(users.Queries) error) error { return f(s) }

func (s *probeUsers) CountAdmins(context.Context, int64) (int64, error) { return s.admins, nil }

func (s *probeUsers) CreateUser(_ context.Context, arg udb.CreateUserParams) (int64, error) {
	s.created = append(s.created, arg)
	return 1, nil
}

func (s *probeUsers) GetUser(context.Context, udb.GetUserParams) (udb.GetUserRow, error) {
	return udb.GetUserRow{ID: 1, PublicID: "SR0000000000P1", Login: "probe@example.org", Name: "probe",
		Role: auth.RoleAdmin, Source: "bootstrap", Status: "active", HasPassword: true}, nil
}

func (s *probeUsers) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error { return nil }

// probeAuth is the sign-in tables of probeSignIn in memory; fail makes the account lookup fail.
type probeAuth struct {
	auth.Store
	hash     string
	totp     bool
	fail     error
	blocked  bool
	sessions []authdb.CreateSessionParams
}

func (s *probeAuth) InTx(_ context.Context, f func(auth.Queries) error) error { return f(s) }

func (s *probeAuth) TryLockSignInSubject(context.Context, authdb.TryLockSignInSubjectParams) (bool, error) {
	return true, nil
}

func (s *probeAuth) BlockSignIn(context.Context, authdb.BlockSignInParams) error { return nil }

func (s *probeAuth) CompleteSecondFactor(context.Context, authdb.CompleteSecondFactorParams) (int64, error) {
	return 1, nil
}

func (s *probeAuth) GetThrottles(context.Context, authdb.GetThrottlesParams) ([]authdb.GetThrottlesRow, error) {
	if !s.blocked {
		return nil, nil
	}
	return []authdb.GetThrottlesRow{{SubjectKind: "account", ConsecutiveFailures: 9,
		BlockedUntil: pgtype.Timestamptz{Time: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true}}}, nil
}

func (s *probeAuth) GetSignInUser(_ context.Context, arg authdb.GetSignInUserParams) (authdb.GetSignInUserRow, error) {
	if s.fail != nil {
		return authdb.GetSignInUserRow{}, s.fail
	}
	if arg.Login != "probe@example.org" {
		return authdb.GetSignInUserRow{}, pgx.ErrNoRows
	}
	return authdb.GetSignInUserRow{ID: 1, PublicID: "SR0000000000P1", Name: "probe", Role: auth.RoleAdmin,
		Status: "active", PasswordHash: pgtype.Text{String: s.hash, Valid: true}, TotpEnrolled: s.totp,
		TotpRequired: "nobody"}, nil
}

func (s *probeAuth) RecordSignInFailure(context.Context, authdb.RecordSignInFailureParams) (int64, error) {
	return 1, nil
}

func (s *probeAuth) ResetSignInThrottles(context.Context, authdb.ResetSignInThrottlesParams) error {
	return nil
}

func (s *probeAuth) CreateSession(_ context.Context, arg authdb.CreateSessionParams) (int64, error) {
	s.sessions = append(s.sessions, arg)
	return int64(len(s.sessions)), nil
}

func (s *probeAuth) MarkSignedIn(context.Context, authdb.MarkSignedInParams) error { return nil }

func (s *probeAuth) GetSessionByToken(_ context.Context, arg authdb.GetSessionByTokenParams) (
	authdb.GetSessionByTokenRow, error) {
	for i, ss := range s.sessions {
		if bytes.Equal(ss.TokenHash, arg.TokenHash) {
			return authdb.GetSessionByTokenRow{ID: int64(i + 1), PublicID: ss.PublicID, UserID: 1, State: ss.State,
				Method: ss.Method, CreatedAt: ss.CreatedAt, LastUsedAt: ss.CreatedAt, IdleExpiresAt: ss.IdleExpiresAt,
				ExpiresAt: ss.ExpiresAt, UserPublicID: "SR0000000000P1", UserName: "probe", UserRole: auth.RoleAdmin,
				UserStatus: "active"}, nil
		}
	}
	return authdb.GetSessionByTokenRow{}, pgx.ErrNoRows
}

func (s *probeAuth) GetUserPassword(context.Context, authdb.GetUserPasswordParams) (authdb.GetUserPasswordRow,
	error) {
	return authdb.GetUserPasswordRow{PasswordHash: pgtype.Text{String: s.hash, Valid: true}}, nil
}

func (s *probeAuth) SetPassword(context.Context, authdb.SetPasswordParams) (int64, error) {
	return 1, nil
}

func (s *probeAuth) EndOtherUserSessions(context.Context, authdb.EndOtherUserSessionsParams) (int64, error) {
	return 1, nil
}

func (s *probeAuth) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error { return nil }

// probeTOTP gives the secrets to the second factor — as the password, TOTP codes and recovery codes of sign-in, of
// the second step, of the enrolment and of the removal, directly and through the API — and checks that the seed of an
// enrolment reaches neither the log nor an error.
func probeTOTP(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	business := clock.NewManual(now)
	w := audit.NewWriter(logger, business)
	k, err := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(masterKey("probe")), Source: keyring.SecretKeysVar},
		false)
	if err != nil {
		return err
	}
	st, err := k.Establish(ctx, &probeStore{}, now)
	if err != nil {
		return err
	}
	if err := k.Open(ctx, logger, st); err != nil {
		return err
	}
	hash, err := auth.HashPassword(ctx, secrets[0])
	if err != nil {
		return err
	}
	as := &probeAuth{hash: hash}
	roles := auth.Roles{auth.RoleAdmin: {"users:write"}, auth.RoleResponder: {"alerts:read"},
		auth.RoleViewer: {"alerts:read"}}
	sessions := auth.NewService(1, as, k, w, business, roles)
	ts := &probeTOTPStore{codeHash: hash}
	factors := totp.New(1, ts, k, w, clock.Clocks{Business: business, Real: clock.NewManual(now)}, sessions)
	sessions.UseSecondFactor(factors)
	addr := netip.MustParseAddr("192.0.2.1")
	sess, err := sessions.SignIn(ctx, auth.SignInRequest{Login: "probe@example.org", Password: secrets[0],
		Address: addr})
	if err != nil {
		return err
	}
	e, err := factors.Begin(ctx, sess)
	if err != nil {
		return err
	}
	_, err1 := factors.Confirm(ctx, sess, secrets[1], addr)
	as.totp, ts.enrolled = true, true
	_, err2 := sessions.SignIn(ctx, auth.SignInRequest{Login: "probe@example.org", Password: secrets[0],
		Proof: auth.Proof{RecoveryCode: secrets[1]}, Address: addr})
	limited, err3 := sessions.SignIn(ctx, auth.SignInRequest{Login: "probe@example.org", Password: secrets[0],
		Address: addr})
	_, err4 := sessions.SubmitSecondFactor(ctx, limited, auth.Proof{TOTPCode: secrets[2]}, addr)
	errs := []error{err1, err2, err3, err4,
		factors.Remove(ctx, sess, totp.Removal{Password: secrets[1]}, addr),
		factors.Remove(ctx, sess, totp.Removal{RecoveryCode: secrets[2]}, addr),
	}
	_, err = factors.RegenerateRecoveryCodes(ctx, sess, secrets[2], addr)
	errs = append(errs, err)
	h, err := api.New(api.Config{Sessions: sessions, Users: users.NewService(1, &probeUsers{}, w, business),
		TOTP: factors, Log: logger, Real: clock.Real{}})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	csrf, err := sessions.CSRFToken(sess)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	var answers []string
	for _, req := range [][3]string{
		{"/api/v1/me/totp/removal", `{"password":"` + secrets[1] + `"}`, sess.Cookie()},
		{"/api/v1/me/totp/confirmation", `{"code":"` + secrets[2] + `"}`, sess.Cookie()},
		{"/api/v1/sessions/current/totp", `{"recovery_code":"` + secrets[2] + `"}`, limited.Cookie()},
		{"/api/v1/sessions", `{"login":"probe@example.org","password":"` + secrets[0] + `","totp_code":"` +
			secrets[1] + `"}`, ""},
	} {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, req[0], strings.NewReader(req[1]))
		if err != nil {
			return err
		}
		r.Header.Set("Content-Type", "application/json")
		if req[2] != "" {
			r.Header.Set("Cookie", auth.CookieName+"="+req[2])
			r.Header.Set(auth.CSRFHeader, csrf)
		}
		rec := &probeRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		answers = append(answers, rec.body.String())
	}
	errs = append(errs, fmt.Errorf("answers: %s", strings.Join(answers, " | ")))
	if out := errors.Join(errs...); out != nil && strings.Contains(out.Error(), e.Secret) {
		return errors.New("the seed of the enrolment reached a returned error")
	}
	if lw, ok := log.(interface{ String() string }); ok && strings.Contains(lw.String(), e.Secret) {
		return errors.New("the seed of the enrolment reached the log")
	}
	return errors.Join(errs...)
}

// probeTOTPStore is the TOTP of probeTOTP in memory: one user, whose recovery code hash is codeHash.
type probeTOTPStore struct {
	totp.Store
	codeHash string
	row      tdb.GetTOTPRow
	enrolled bool
}

func (s *probeTOTPStore) InTx(_ context.Context, f func(totp.Queries) error) error { return f(s) }

func (s *probeTOTPStore) GetTOTPUser(context.Context, tdb.GetTOTPUserParams) (tdb.GetTOTPUserRow, error) {
	return tdb.GetTOTPUserRow{ID: 1, PublicID: "SR0000000000P1", Login: "probe@example.org", Name: "probe",
		Status: "active"}, nil
}

func (s *probeTOTPStore) SetPendingSeed(_ context.Context, arg tdb.SetPendingSeedParams) (int64, error) {
	s.row.PendingSeedCiphertext = arg.Ciphertext
	s.row.PendingSeedKeyID = pgtype.Text{String: arg.KeyID, Valid: true}
	return 1, nil
}

// GetTOTP answers the pending seed until enrolled is set, then the same ciphertext as the seed, which does not open
// for that field: the code paths of a TOTP code then fail on the decryption, and those of a recovery code go on.
func (s *probeTOTPStore) GetTOTP(context.Context, tdb.GetTOTPParams) (tdb.GetTOTPRow, error) {
	if !s.enrolled {
		return s.row, nil
	}
	return tdb.GetTOTPRow{SeedCiphertext: s.row.PendingSeedCiphertext, SeedKeyID: s.row.PendingSeedKeyID}, nil
}

func (s *probeTOTPStore) ListUnusedRecoveryCodes(context.Context, tdb.ListUnusedRecoveryCodesParams) (
	[]tdb.ListUnusedRecoveryCodesRow, error) {
	return []tdb.ListUnusedRecoveryCodesRow{{ID: 1, CodeHash: s.codeHash}}, nil
}

func (s *probeTOTPStore) UseStep(context.Context, tdb.UseStepParams) (int64, error) { return 1, nil }

func (s *probeTOTPStore) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error {
	return nil
}

func Probes() []Probe {
	return slices.Clone(registry)
}

// CheckProbe runs p with fresh random secrets and reports every secret found, verbatim or encoded (see leakForms), in
// its log output or in the returned error, its %+v form and every error it wraps.
func CheckProbe(ctx context.Context, p Probe) []Diagnostic {
	secrets := make([]string, probeSecrets)
	for i := range secrets {
		secrets[i] = rand.Text() + secretSuffix
	}
	var log bytes.Buffer
	err := p.Run(ctx, slices.Clone(secrets), &log)
	sinks := []struct{ name, text string }{
		{"the log output", log.String()},
		{"the returned error", strings.Join(errorTexts(err, 0), "\n")},
	}
	var diags []Diagnostic
	for i, secret := range secrets {
		forms := leakForms(secret)
		for _, sink := range sinks {
			j := slices.IndexFunc(forms, func(f leakForm) bool { return strings.Contains(sink.text, f.text) })
			if j < 0 {
				continue
			}
			msg := fmt.Sprintf("secret %d of %d reached %s", i+1, len(secrets), sink.name)
			if enc := forms[j].encoding; enc != "" {
				msg += " (" + enc + ")"
			}
			diags = append(diags, Diagnostic{Rule: 5, Path: p.Name, Message: msg})
		}
	}
	return diags
}

type leakForm struct {
	encoding string // empty for the secret itself
	text     string
}

// leakForms returns the secret as it may reach a log line or an error: verbatim, in hex, URL-escaped and in base64
// with the standard and the URL alphabet. The base64 forms are the characters that do not depend on the bytes around
// the secret, for each of its three possible alignments inside a longer encoded value, such as "user:secret" in a
// Basic authorization header.
func leakForms(secret string) []leakForm {
	h := hex.EncodeToString([]byte(secret))
	forms := []leakForm{
		{"", secret},
		{"hex", h},
		{"hex", strings.ToUpper(h)},
		{"URL-escaped", url.QueryEscape(secret)},
		{"URL-escaped", url.PathEscape(secret)},
	}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for shift := range 3 {
			encoded := enc.EncodeToString(append(make([]byte, shift), secret...))
			end := len(encoded)
			if (shift+len(secret))%3 != 0 {
				end-- // the last character also takes bits of the byte after the secret
			}
			forms = append(forms, leakForm{"base64", encoded[(8*shift+5)/6 : end]})
		}
	}
	return forms
}

func errorTexts(err error, depth int) []string {
	if err == nil || depth > maxErrorDepth {
		return nil
	}
	texts := []string{err.Error(), fmt.Sprintf("%+v", err)}
	if inner := errors.Unwrap(err); inner != nil {
		texts = append(texts, errorTexts(inner, depth+1)...)
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range multi.Unwrap() {
			texts = append(texts, errorTexts(inner, depth+1)...)
		}
	}
	return texts
}
