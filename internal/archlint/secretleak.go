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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/api"
	"github.com/muster-io/muster/internal/audit"
	adb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/auth"
	authdb "github.com/muster-io/muster/internal/auth/dbgen"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/connections"
	cdb "github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/doctor"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/heartbeat"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	intdb "github.com/muster-io/muster/internal/integrations/dbgen"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/oidc"
	odb "github.com/muster-io/muster/internal/oidc/dbgen"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/runtime"
	"github.com/muster-io/muster/internal/telegram"
	"github.com/muster-io/muster/internal/templates"
	"github.com/muster-io/muster/internal/tokens"
	tokdb "github.com/muster-io/muster/internal/tokens/dbgen"
	"github.com/muster-io/muster/internal/totp"
	tdb "github.com/muster-io/muster/internal/totp/dbgen"
	"github.com/muster-io/muster/internal/users"
	udb "github.com/muster-io/muster/internal/users/dbgen"
	"github.com/muster-io/muster/internal/webhooks"
	whdb "github.com/muster-io/muster/internal/webhooks/dbgen"
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
	{Name: "oidc", Run: probeOIDC},
	{Name: "api_tokens", Run: probeTokens},
	{Name: "ingestion", Run: probeIngestion},
	{Name: "heartbeat", Run: probeHeartbeat},
	{Name: "mattermost_connections", Run: probeMattermost},
	{Name: "telegram_connections", Run: probeTelegram},
	{Name: "outgoing_webhooks", Run: probeWebhooks},
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

// probeOIDC sends the secrets through the OIDC settings and sign-in: the first as the client secret, which the token
// endpoint of a stand-in identity provider echoes in its error, the second as the password of a SOCKS5 proxy that
// refuses it, and the third as an authorization code, an ID token and a state. Neither the log — with the Audit log
// entries it copies — nor the returned errors may carry them.
func probeOIDC(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	business := clock.NewManual(now)
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
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	issuer := "http://" + ln.Addr().String()
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/.well-known/openid-configuration" {
				_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
					issuer, issuer+"/authorize", issuer+"/token", issuer+"/jwks")
				return
			}
			body, _ := io.ReadAll(r.Body)
			http.Error(w, `{"error":"`+secrets[0]+`","error_description":"`+string(body)+r.Header.Get("Authorization")+`"}`,
				http.StatusBadRequest)
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	socks, err := fakeproxy.Start(ctx, fakeproxy.SOCKS5, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "other"})
	if err != nil {
		return err
	}
	defer func() { _ = socks.Close() }()

	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	store := &probeOIDCStore{}
	svc := oidc.NewService(oidc.Config{OrgID: 1, Store: store, Keyring: k, Audit: audit.NewWriter(logger, business),
		Clocks: clock.Clocks{Business: business, Real: clock.Real{}}, Log: logger, PublicURL: &url.URL{Scheme: "http",
			Host: "localhost:8080"}, Network: oidc.Network{Policy: outbound.StaticPolicy(policy), Log: logger,
			Real: clock.Real{}}})
	scopes := []string{"profile"}
	in := oidc.Input{Enabled: true, IssuerURL: issuer, ClientID: "muster", ClientSecret: keyring.Replace(
		logging.Secret(secrets[0])), Scopes: &scopes, GroupsClaim: "groups", UnmatchedRole: oidc.UnmatchedNone,
		Proxy: proxyconf.Input{Enabled: true, Type: new("socks5"), Address: new(socks.Addr()), UsernameSet: true,
			Username: new("muster"), Password: keyring.Replace(logging.Secret(secrets[1]))}}
	var errs []error
	_, err = svc.Update(ctx, oidc.Requester{Actor: audit.System, Transport: audit.TransportUI}, nil, in)
	errs = append(errs, err)
	res, err := svc.Check(ctx)
	errs = append(errs, err, errors.New(res.Error))

	in.Proxy = proxyconf.Input{Enabled: false}
	_, err = svc.Update(ctx, oidc.Requester{Actor: audit.System, Transport: audit.TransportUI}, nil, in)
	errs = append(errs, err)
	start, err := svc.StartSignIn(ctx, "/"+secrets[2])
	errs = append(errs, err)
	out := svc.CompleteSignIn(ctx, oidc.Callback{Code: secrets[2], State: start.State, CookieState: start.State})
	out2 := svc.CompleteSignIn(ctx, oidc.Callback{Code: secrets[2], State: secrets[2], CookieState: secrets[2],
		Error: secrets[2]})
	p, err := oidc.NewProvider(oidc.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}},
		issuer, "muster", logging.Secret(secrets[0]), nil)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	d, err := p.Discover(ctx)
	errs = append(errs, err)
	_, err = p.Verify(ctx, d, logging.Secret(secrets[2]), secrets[2])
	errs = append(errs, err, fmt.Errorf("outcomes: %+v %+v", out, out2))
	_, err = p.Refresh(ctx, d, logging.Secret(secrets[2]))
	errs = append(errs, err)
	_, err = p.VerifyRefreshed(ctx, d, logging.Secret(secrets[2]))
	errs = append(errs, err)

	// A link: the code of the callback.
	sess := auth.Session{ID: 7, User: auth.Principal{ID: 1, PublicID: "SR0000000000P1", Name: "probe"}}
	link, err := svc.StartLink(ctx, sess)
	errs = append(errs, err)
	if u, perr := url.Parse(link.URL); perr == nil {
		out3 := svc.CompleteLink(ctx, sess, oidc.Callback{Code: secrets[2], State: u.Query().Get("state")})
		errs = append(errs, fmt.Errorf("link outcome: %+v", out3))
	}

	// A background re-check: the offline token, which the stand-in refuses with the secret in its answer.
	ct, keyID, err := k.Encrypt("users.oidc_offline_token", []byte(secrets[2]))
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	store.token, store.tokenKey = ct, keyID
	_, err = svc.Recheck(ctx, func(context.Context, int64, int32) ([]int64, error) { return []int64{1}, nil },
		"probe")
	errs = append(errs, err)
	return errors.Join(errs...)
}

// probeOIDCStore is the OIDC settings and requests of probeOIDC in memory, with one user who holds an offline token.
type probeOIDCStore struct {
	oidc.Store
	row      *odb.OidcSetting
	requests map[string]odb.InsertAuthRequestParams
	token    []byte
	tokenKey string
}

func (s *probeOIDCStore) LockLinkUser(context.Context, odb.LockLinkUserParams) (odb.LockLinkUserRow, error) {
	return odb.LockLinkUserRow{ID: 1, PublicID: "SR0000000000P1", Name: "probe", Status: "active",
		HasPassword: true}, nil
}

func (s *probeOIDCStore) TakeLinkRequest(_ context.Context, a odb.TakeLinkRequestParams) (odb.TakeLinkRequestRow,
	error) {
	r, ok := s.requests[string(a.StateHash)]
	if !ok {
		return odb.TakeLinkRequestRow{}, pgx.ErrNoRows
	}
	return odb.TakeLinkRequestRow{Nonce: r.Nonce, CodeVerifierCiphertext: r.CodeVerifierCiphertext,
		CodeVerifierKeyID: r.CodeVerifierKeyID, ExpiresAt: r.ExpiresAt}, nil
}

func (s *probeOIDCStore) GetCheckUser(context.Context, odb.GetCheckUserParams) (odb.GetCheckUserRow, error) {
	return odb.GetCheckUserRow{ID: 1, PublicID: "SR0000000000P1", Name: "probe", Role: "viewer", Status: "active",
		OidcSubject: pgtype.Text{String: "probe", Valid: true}, OidcOfflineTokenCiphertext: s.token,
		OidcOfflineTokenKeyID: pgtype.Text{String: s.tokenKey, Valid: true}, LiveSession: true}, nil
}

func (s *probeOIDCStore) LockCheckUser(context.Context, odb.LockCheckUserParams) (odb.LockCheckUserRow, error) {
	return odb.LockCheckUserRow{Role: "viewer", Status: "active"}, nil
}

func (s *probeOIDCStore) DropCheck(context.Context, odb.DropCheckParams) (int64, error) {
	return 1, nil
}

func (s *probeOIDCStore) FinishCheck(context.Context, odb.FinishCheckParams) (int64, error) {
	return 1, nil
}

func (s *probeOIDCStore) EndUserSessions(context.Context, odb.EndUserSessionsParams) (int64, error) {
	return 1, nil
}

func (s *probeOIDCStore) RefuseUser(context.Context, odb.RefuseUserParams) error { return nil }

func (s *probeOIDCStore) InTx(_ context.Context, f func(oidc.Queries) error) error { return f(s) }

func (s *probeOIDCStore) GetSettings(context.Context, int64) (odb.OidcSetting, error) {
	if s.row == nil {
		return odb.OidcSetting{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *probeOIDCStore) LockSettings(context.Context, int64) (int64, error) {
	if s.row == nil {
		return 0, pgx.ErrNoRows
	}
	return s.row.Version, nil
}

func (s *probeOIDCStore) InsertSettings(_ context.Context, a odb.InsertSettingsParams) (int64, error) {
	s.row = &odb.OidcSetting{OrgID: a.OrgID, Enabled: a.Enabled, IssuerUrl: a.IssuerUrl, ClientID: a.ClientID,
		ClientSecretCiphertext: a.ClientSecretCiphertext, ClientSecretKeyID: a.ClientSecretKeyID,
		ClientSecretUpdatedAt: a.ClientSecretUpdatedAt, Scopes: a.Scopes, GroupsClaim: a.GroupsClaim,
		GroupMappings: a.GroupMappings, UnmatchedRole: a.UnmatchedRole, Proxy: a.Proxy,
		ProxyPasswordCiphertext: a.ProxyPasswordCiphertext, ProxyPasswordKeyID: a.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: a.ProxyPasswordUpdatedAt, Version: 1, UpdatedAt: a.UpdatedAt}
	return 1, nil
}

func (s *probeOIDCStore) UpdateSettings(_ context.Context, a odb.UpdateSettingsParams) (int64, error) {
	s.row.Enabled, s.row.Proxy, s.row.Version = a.Enabled, a.Proxy, s.row.Version+1
	s.row.ProxyPasswordCiphertext, s.row.ProxyPasswordKeyID = a.ProxyPasswordCiphertext, a.ProxyPasswordKeyID
	return 1, nil
}

func (s *probeOIDCStore) SetGroupsClaimMissing(context.Context, odb.SetGroupsClaimMissingParams) error {
	return nil
}

func (s *probeOIDCStore) LatestKeptAdmin(context.Context, int64) (odb.LatestKeptAdminRow, error) {
	return odb.LatestKeptAdminRow{}, pgx.ErrNoRows
}

func (s *probeOIDCStore) InsertAuthRequest(_ context.Context, a odb.InsertAuthRequestParams) (int64, error) {
	if s.requests == nil {
		s.requests = map[string]odb.InsertAuthRequestParams{}
	}
	s.requests[string(a.StateHash)] = a
	return 1, nil
}

func (s *probeOIDCStore) TakeAuthRequest(_ context.Context, a odb.TakeAuthRequestParams) (odb.TakeAuthRequestRow,
	error) {
	r, ok := s.requests[string(a.StateHash)]
	if !ok {
		return odb.TakeAuthRequestRow{}, pgx.ErrNoRows
	}
	return odb.TakeAuthRequestRow{Purpose: r.Purpose, Nonce: r.Nonce, CodeVerifierCiphertext: r.CodeVerifierCiphertext,
		CodeVerifierKeyID: r.CodeVerifierKeyID, ReturnTo: r.ReturnTo, ExpiresAt: r.ExpiresAt}, nil
}

func (s *probeOIDCStore) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error {
	return nil
}

// probeTokens sends the secrets as bearer tokens to the API — as they are, and after the prefixes of a Personal access
// token and a Service account token — on a read and on a change, and issues tokens whose values then authenticate
// while the database fails. None of the answers, errors or log lines may carry a secret. The issued values are not
// among the planted secrets, so the probe looks for them itself and reports a leak as the first secret.
func probeTokens(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	business := clock.NewManual(now)
	store := &probeTokenStore{}
	svc := tokens.New(1, store, audit.NewWriter(logger, business), business,
		auth.Roles{auth.RoleAdmin: {"users:read", "users:write"}}, tokens.NewLimiter(clock.Real{}))
	by := tokens.Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportUI}
	pat, err1 := svc.CreatePersonal(ctx, by, tokens.Owner{ID: 1, PublicID: "SRAAAAAAAAAAAA"},
		[]auth.Permission{"users:read"}, tokens.NewPersonal{Name: "probe", Permissions: []auth.Permission{"users:read"}})
	sat, err2 := svc.CreateServiceAccountToken(ctx, by, "SAAAAAAAAAAAAA", tokens.NewToken{Name: "probe"})
	errs := []error{err1, err2}
	store.fail = errors.New("the database is unavailable")
	h, err := api.New(api.Config{Tokens: svc, Log: logger, Real: clock.Real{}})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	var values []string
	for _, secret := range secrets {
		values = append(values, secret, "mstr_pat_"+secret, "mstr_sat_"+secret)
	}
	var answers []string
	for _, value := range append(values, pat.Value, sat.Value) {
		_, err := svc.Authenticate(ctx, value, netip.MustParseAddr("192.0.2.1"))
		errs = append(errs, err)
		for _, req := range [][3]string{{http.MethodGet, "/api/v1/me", ""},
			{http.MethodPost, "/api/v1/users", `{"name":"x","login":"x","role":"viewer"}`}} {
			r, err := http.NewRequestWithContext(ctx, req[0], req[1], strings.NewReader(req[2]))
			if err != nil {
				return err
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+value)
			rec := &probeRecorder{header: http.Header{}}
			h.ServeHTTP(rec, r)
			answers = append(answers, rec.body.String())
		}
	}
	errs = append(errs, fmt.Errorf("answers: %s", strings.Join(answers, " | ")))
	out := errors.Join(errs...)
	for _, issued := range []string{pat.Value, sat.Value} {
		if issued == "" {
			return fmt.Errorf("no token was issued: %w", out)
		}
		lw, ok := log.(interface{ String() string })
		if (out != nil && strings.Contains(out.Error(), issued)) || (ok && strings.Contains(lw.String(), issued)) {
			return fmt.Errorf("an issued token value leaked; reported as %s", secrets[0])
		}
	}
	return out
}

// probeTokenStore is the database of probeTokens: one Service account, and every query failing once fail is set.
type probeTokenStore struct {
	tokens.Store
	fail error
}

func (s *probeTokenStore) InTx(_ context.Context, f func(tokens.Queries) error) error { return f(s) }

func (s *probeTokenStore) InsertToken(context.Context, tokdb.InsertTokenParams) (int64, error) {
	return 1, nil
}

func (s *probeTokenStore) InsertTokenPermissions(context.Context, tokdb.InsertTokenPermissionsParams) error {
	return nil
}

func (s *probeTokenStore) LockServiceAccount(context.Context, tokdb.LockServiceAccountParams) (int64, error) {
	return 1, nil
}

func (s *probeTokenStore) GetServiceAccount(context.Context, tokdb.GetServiceAccountParams) (
	tokdb.GetServiceAccountRow, error) {
	return tokdb.GetServiceAccountRow{ID: 1, PublicID: "SAAAAAAAAAAAAA", Name: "probe", Role: auth.RoleAdmin,
		Status: tokens.StatusActive, Version: 1}, nil
}

func (s *probeTokenStore) GetTokenByHash(context.Context, tokdb.GetTokenByHashParams) (tokdb.GetTokenByHashRow,
	error) {
	return tokdb.GetTokenByHashRow{}, s.fail
}

func (s *probeTokenStore) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error {
	return nil
}

// probeIngestion sends known secrets as Integration tokens to the ingestion endpoint (C-05.FR-3, C-05.AC-5): in the
// path, which only the route pattern may name, and as a bearer token, with and without the mstr_int_ prefix. It also
// issues a token and sends with it in the path while the write succeeds and while it fails, and while the token
// lookup fails. None of the answers, errors or log lines may carry a token. The issued value is not among the planted
// secrets, so the probe looks for it itself and reports a leak as the first secret.
func probeIngestion(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	business := clock.NewManual(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	store := &probeIntegrationStore{}
	svc := integrations.New(integrations.Config{OrgID: 1, Store: store, Audit: audit.NewWriter(logger, business),
		Business: business})
	by := integrations.Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportUI}
	issued, err := svc.CreateToken(ctx, by, "NTAAAAAAAAAAAA", "probe")
	errs := []error{err}
	store.hash = tokens.Hash(issued.Value)
	snapshots := &probeSnapshots{}
	h := ingest.NewHandler(ingest.HandlerConfig{Auth: svc, Snapshots: snapshots, Log: logger, Real: clock.Real{}})
	var answers []string
	send := func(path, bearer string) error {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader("{}"))
		if err != nil {
			return err
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := &probeRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		answers = append(answers, rec.body.String())
		return nil
	}
	var values []string
	for _, secret := range secrets {
		values = append(values, secret, integrations.Unknown, "mstr_int_"+secret)
	}
	values = append(values, issued.Value)
	for _, value := range values {
		errs = append(errs, send("/api/v1/ingest/"+url.PathEscape(value), ""), send("/api/v1/ingest", value))
	}
	snapshots.fail = errors.New("the database is unavailable")
	errs = append(errs, send("/api/v1/ingest/"+url.PathEscape(issued.Value), ""))
	store.fail = errors.New("the database is unavailable")
	errs = append(errs, send("/api/v1/ingest/"+url.PathEscape(issued.Value), ""))
	errs = append(errs, fmt.Errorf("answers: %s", strings.Join(answers, " | ")))
	out := errors.Join(errs...)
	if issued.Value == "" || snapshots.stored == 0 {
		return fmt.Errorf("no token was issued or accepted: %w", out)
	}
	lw, ok := log.(interface{ String() string })
	if strings.Contains(out.Error(), issued.Value) || (ok && strings.Contains(lw.String(), issued.Value)) {
		return fmt.Errorf("an issued token value leaked; reported as %s", secrets[0])
	}
	return out
}

// probeIntegrationStore is the database of probeIngestion: one Integration with the token whose hash is hash, and
// every token lookup failing once fail is set.
type probeIntegrationStore struct {
	integrations.Store
	hash []byte
	fail error
}

func (s *probeIntegrationStore) InTx(_ context.Context, f func(integrations.Queries) error) error {
	return f(s)
}

func (s *probeIntegrationStore) LockIntegration(context.Context, intdb.LockIntegrationParams) (int64, error) {
	return 1, nil
}

func (s *probeIntegrationStore) GetIntegration(context.Context, intdb.GetIntegrationParams) (intdb.GetIntegrationRow,
	error) {
	return intdb.GetIntegrationRow{ID: 1, PublicID: "NTAAAAAAAAAAAA", Name: "probe", ConnectionMode: "webhook_only",
		StaticLabels: []byte("{}"), Version: 1}, nil
}

func (s *probeIntegrationStore) LastSnapshotTimes(context.Context, intdb.LastSnapshotTimesParams) (
	[]intdb.LastSnapshotTimesRow, error) {
	return nil, nil
}

func (s *probeIntegrationStore) CountTruncatedGroupsOf(context.Context, intdb.CountTruncatedGroupsOfParams) (
	[]intdb.CountTruncatedGroupsOfRow, error) {
	return nil, nil
}

func (s *probeIntegrationStore) ListLongRepeatRoutes(context.Context, intdb.ListLongRepeatRoutesParams) (
	[]intdb.ListLongRepeatRoutesRow, error) {
	return nil, nil
}

func (s *probeIntegrationStore) InsertIntegrationToken(context.Context, intdb.InsertIntegrationTokenParams) (int64,
	error) {
	return 1, nil
}

func (s *probeIntegrationStore) Notify(context.Context, db.Hint) error { return nil }

func (s *probeIntegrationStore) FindIngestToken(_ context.Context, arg intdb.FindIngestTokenParams) (
	intdb.FindIngestTokenRow, error) {
	if s.fail != nil {
		return intdb.FindIngestTokenRow{}, s.fail
	}
	if !bytes.Equal(arg.TokenHash, s.hash) {
		return intdb.FindIngestTokenRow{}, pgx.ErrNoRows
	}
	return intdb.FindIngestTokenRow{ID: 1, TokenHash: s.hash, IntegrationID: 1,
		IntegrationPublicID: "NTAAAAAAAAAAAA"}, nil
}

func (s *probeIntegrationStore) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error {
	return nil
}

// probeHeartbeat sends known secrets as Integration tokens to the Heartbeat endpoint (C-07.FR-1, C-07.AC-3): in the
// path, which only the route pattern may name, and as a bearer token, with and without the mstr_int_ prefix, by GET
// and POST. It also issues a token and signals with it in the path while recording succeeds and while it fails, and
// while the token lookup fails. None of the answers, errors or log lines may carry a token; as in probeIngestion, the
// probe looks for the issued value itself.
func probeHeartbeat(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	business := clock.NewManual(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	store := &probeIntegrationStore{}
	svc := integrations.New(integrations.Config{OrgID: 1, Store: store, Audit: audit.NewWriter(logger, business),
		Business: business})
	by := integrations.Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportUI}
	issued, err := svc.CreateToken(ctx, by, "NTAAAAAAAAAAAA", "probe")
	errs := []error{err}
	store.hash = tokens.Hash(issued.Value)
	signals := &probeSignals{}
	h := heartbeat.NewHandler(heartbeat.HandlerConfig{Auth: svc, Signals: signals, Log: logger})
	var answers []string
	send := func(method, path, bearer string) error {
		r, err := http.NewRequestWithContext(ctx, method, path, strings.NewReader("anything"))
		if err != nil {
			return err
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := &probeRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		answers = append(answers, rec.body.String())
		return nil
	}
	var values []string
	for _, secret := range secrets {
		values = append(values, secret, integrations.Unknown, "mstr_int_"+secret)
	}
	values = append(values, issued.Value)
	for _, value := range values {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			errs = append(errs, send(method, "/api/v1/heartbeat/"+url.PathEscape(value), ""),
				send(method, "/api/v1/heartbeat", value))
		}
	}
	signals.fail = errors.New("the database is unavailable")
	errs = append(errs, send(http.MethodPost, "/api/v1/heartbeat/"+url.PathEscape(issued.Value), ""))
	store.fail = errors.New("the database is unavailable")
	errs = append(errs, send(http.MethodGet, "/api/v1/heartbeat/"+url.PathEscape(issued.Value), ""))
	errs = append(errs, fmt.Errorf("answers: %s", strings.Join(answers, " | ")))
	out := errors.Join(errs...)
	if issued.Value == "" || signals.recorded == 0 {
		return fmt.Errorf("no token was issued or accepted: %w", out)
	}
	lw, ok := log.(interface{ String() string })
	if strings.Contains(out.Error(), issued.Value) || (ok && strings.Contains(lw.String(), issued.Value)) {
		return fmt.Errorf("an issued token value leaked; reported as %s", secrets[0])
	}
	return out
}

// probeSignals records Heartbeat signals until fail is set.
type probeSignals struct {
	recorded int
	fail     error
}

func (s *probeSignals) Signal(context.Context, int64) error {
	if s.fail != nil {
		return s.fail
	}
	s.recorded++
	return nil
}

// probeSnapshots stores bodies until fail is set.
type probeSnapshots struct {
	stored int
	fail   error
}

func (s *probeSnapshots) Store(context.Context, ingest.Received) (ingest.Stored, error) {
	if s.fail != nil {
		return ingest.Stored{}, s.fail
	}
	s.stored++
	return ingest.Stored{PublicID: "SSAAAAAAAAAAAA"}, nil
}

// probeMattermost pushes the bot token (secret 0) and the proxy password (secret 1) of a Mattermost Connection through
// its creation, the Connection check, the channel list and the Destination check, against a stand-in server that
// answers every call with the token and the Authorization header in its error, directly and through a SOCKS5 proxy
// that refuses the password; sends button presses to the callback of the Connection, whose ephemeral posts the
// server refuses and whose answers carry their text instead; publishes, edits, replies and checks through the adapter
// and runs the checks of muster doctor against the same server; and builds a client whose proxy, with a password
// (secret 2), is refused.
func probeMattermost(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	clocks := clock.Clocks{Business: clock.NewManual(now), Real: clock.Real{}}
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
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"id":"api.context.session_expired.app_error","message":%q,"status_code":401}`, //nolint:gosec // G705: a stand-in server that echoes the token on purpose
				"invalid token "+secrets[0]+" "+r.Header.Get("Authorization"))
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	socks, err := fakeproxy.Start(ctx, fakeproxy.SOCKS5, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "other"})
	if err != nil {
		return err
	}
	defer func() { _ = socks.Close() }()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	network := mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}}
	store := &probeConnections{}
	svc := connections.New(connections.Config{OrgID: 1, Store: store, Keyring: k,
		Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks, Network: network,
		Interactive: deliverytest.Unlimited(1, clocks), IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081"},
		Log: logger})
	by := connections.Requester{Actor: audit.System, Transport: audit.TransportUI}
	in := connections.Input{Type: connections.TypeMattermost, Name: "probe", ServerURL: "http://" + ln.Addr().String(),
		BotToken: keyring.Replace(logging.Secret(secrets[0])), Limiter: connections.Limiter{Limit: 5, PerSeconds: 1},
		Proxy: proxyconf.Input{Enabled: true, Type: new("socks5"), Address: new(socks.Addr()), UsernameSet: true,
			Username: new("muster"), Password: keyring.Replace(logging.Secret(secrets[1]))}}
	var errs []error
	texts := func(c connections.CheckResult) error {
		var b strings.Builder
		for _, st := range c.Steps {
			b.WriteString(st.Message + "\n")
		}
		return errors.New(b.String())
	}
	c, err := svc.Create(ctx, by, in)
	errs = append(errs, err)
	res, err := svc.Check(ctx, c.PublicID, nil)
	errs = append(errs, err, texts(res))
	in.Proxy = proxyconf.Input{Enabled: false}
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	res, err = svc.Check(ctx, c.PublicID, nil)
	errs = append(errs, err, texts(res))
	_, err = svc.Channels(ctx, c.PublicID, "", "")
	errs = append(errs, err)
	errs = append(errs, probePresses(ctx, svc, k, clocks, logger, c.PublicID), probeAdapter(ctx, svc, store, clocks))
	checked, err := svc.CheckChannel(ctx, destinations.ChannelCheck{Connection: c.PublicID, TeamID: "t",
		ChannelID: "c"})
	errs = append(errs, err)
	for _, st := range checked.Check.Steps {
		errs = append(errs, errors.New(st.Message), errors.New(string(st.Outcome.Error)))
	}
	_, err = mattermost.NewClient(network, mattermost.Settings{ServerURL: "http://" + ln.Addr().String(),
		Token: logging.Secret(secrets[0]), Proxy: &outbound.Proxy{Type: "ftp", Address: "proxy:1", Username: "muster",
			Password: logging.Secret(secrets[2])}})
	return errors.Join(append(errs, err)...)
}

// probeTelegram pushes the secrets through a Telegram Connection (C-14.FR-12): the bot token, and the proxy password of
// a SOCKS5 proxy that refuses it, through its creation, its check, an unsaved base URL, a mode switch whose setWebhook
// the stand-in refuses, muster doctor with its Destination, the Leader's poller, the adapter's posts, edits, replies and
// Destination checks, the answers to button presses, the Destination check of a save and the deleteWebhook of a
// deletion; the webhook secret token through setWebhook and the webhook endpoint; and the token through a refused
// connection. The stand-in server echoes the token, the path and the body of each request in its descriptions.
func probeTelegram(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	clocks := clock.Clocks{Business: clock.NewManual(now), Real: clock.Real{}}
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
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			status := http.StatusInternalServerError
			if strings.HasSuffix(r.URL.Path, "/bot0:x/getMe") || strings.HasSuffix(r.URL.Path, "/getUpdates") {
				status = http.StatusUnauthorized
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": status,
				"description": "invalid token " + secrets[0] + " at " + r.URL.Path + " for " + string(body)})
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	socks, err := fakeproxy.Start(ctx, fakeproxy.SOCKS5, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "other"})
	if err != nil {
		return err
	}
	defer func() { _ = socks.Close() }()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	network := mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}}
	store := &probeConnections{}
	svc := connections.New(connections.Config{OrgID: 1, Store: store, Keyring: k,
		Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks, Network: network,
		Interactive: deliverytest.Unlimited(1, clocks), IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081"},
		Log: logger})
	by := connections.Requester{Actor: audit.System, Transport: audit.TransportUI}
	in := connections.Input{Type: connections.TypeTelegram, Name: "probe", BotAPIBaseURL: "http://" + ln.Addr().String(),
		UpdateMode: connections.ModeLongPolling, BotToken: keyring.Replace(logging.Secret(secrets[0])),
		Limiter: connections.Limiter{Limit: 15, PerSeconds: 1},
		Proxy: proxyconf.Input{Enabled: true, Type: new("socks5"), Address: new(socks.Addr()), UsernameSet: true,
			Username: new("muster"), Password: keyring.Replace(logging.Secret(secrets[1]))}}
	var errs []error
	texts := func(c connections.CheckResult) error {
		var b strings.Builder
		for _, st := range c.Steps {
			b.WriteString(st.Message + "\n")
		}
		return errors.New(b.String())
	}
	c, err := svc.Create(ctx, by, in)
	errs = append(errs, err)
	res, err := svc.Check(ctx, c.PublicID, nil)
	errs = append(errs, err, texts(res))
	in.Proxy = proxyconf.Input{Enabled: false}
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	res, err = svc.Check(ctx, c.PublicID, nil)
	errs = append(errs, err, texts(res))
	unsaved := "http://127.0.0.1:1/x"
	res, err = svc.Check(ctx, c.PublicID, &unsaved)
	errs = append(errs, err, texts(res))
	in.UpdateMode = connections.ModeWebhook
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	found, err := svc.Doctor(ctx, store, 2*time.Second)
	errs = append(errs, err)
	for _, f := range found {
		errs = append(errs, errors.New(f.Message))
	}
	tg := telegram.Network(network)
	client, err := telegram.NewClient(tg, telegram.Settings{BaseURL: "http://" + ln.Addr().String(),
		Token: logging.Secret(secrets[0])})
	if err != nil {
		return err
	}
	r := client.SetWebhook(ctx, outbound.ClassInteractive, "http://localhost:8081/hook", logging.Secret(secrets[2]))
	errs = append(errs, errors.New(string(r.Outcome.Error)), errors.New(r.Description), errors.New(r.Detail))
	refused, err := telegram.NewClient(tg, telegram.Settings{BaseURL: "http://127.0.0.1:1",
		Token: logging.Secret(secrets[0])})
	if err != nil {
		return err
	}
	_, r = refused.GetMe(ctx, outbound.ClassInteractive)
	errs = append(errs, errors.New(string(r.Outcome.Error)), errors.New(r.Detail))
	router := &telegram.Router{Offsets: svc, Log: logger}
	pollCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	errs = append(errs, (&telegram.Poller{Source: svc, Router: router, Log: logger}).Run(pollCtx))
	cancel()
	rec := &probeRecorder{header: http.Header{}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegram.WebhookPath+c.PublicID,
		strings.NewReader(`{"update_id":1}`))
	if err != nil {
		return err
	}
	req.Header.Set(telegram.SecretTokenHeader, secrets[2])
	telegram.NewWebhook(telegram.WebhookConfig{Connections: svc, Router: router, Log: logger}).ServeHTTP(rec, req)
	errs = append(errs, errors.New(rec.body.String()))
	errs = append(errs, probeTelegramAdapter(ctx, svc, clocks, c.PublicID))
	errs = append(errs, probeTelegramPresses(ctx, router, k, clocks, logger, telegram.Conn{ID: store.row.ID,
		PublicID: c.PublicID, Client: client}))
	store.row.TelegramUpdateMode = pgtype.Text{String: connections.ModeWebhook, Valid: true}
	errs = append(errs, svc.Delete(ctx, by, c.PublicID, nil))
	return errors.Join(errs...)
}

// probeTelegramAdapter publishes, edits and replies through the Telegram adapter to the Destination 1 of the
// Connection publicID on the interactive path, as lint 3 allows, checks it in the delivery class as the Broken probe
// does and through the Connection as a save does; it returns the text of every outcome and step.
func probeTelegramAdapter(ctx context.Context, svc *connections.Service, clocks clock.Clocks, publicID string) error {
	a := &telegram.Adapter{Targets: svc, Clock: clocks.Real}
	conn := int64(1)
	dest := delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: delivery.TypeTelegram, Connection: &conn}
	m := delivery.Message{Kind: messages.KindRoot, Language: "en", Colour: "firing", Heading: &messages.Heading{
		Number: 1, Title: "probe", URL: "http://localhost:8080/alert-groups/AGAAAAAAAAAAA1"}}
	path := deliverytest.Unlimited(1, clocks)
	var errs []error
	for _, op := range []delivery.Op{delivery.PublishOp(a, m), delivery.UpdateOp(a, "1", m),
		delivery.ReplyOp(a, delivery.Root{MessageID: "1"}, m), delivery.CheckOp(a)} {
		o, err := path.Do(ctx, delivery.Subject{Destination: &dest}, op)
		errs = append(errs, err, errors.New(string(o.Error)))
	}
	o := a.Check(ctx, delivery.Call{Class: outbound.ClassDelivery, Destination: dest})
	errs = append(errs, errors.New(string(o.Error)))
	res, err := svc.CheckTelegramChannel(ctx, destinations.TelegramChannelCheck{Connection: publicID,
		Destination: &dest.ID, ChannelID: "@probe"})
	errs = append(errs, err, errors.New(string(res.Check.Outcome.Error)))
	for _, st := range res.Check.Steps {
		errs = append(errs, errors.New(st.Message))
	}
	return errors.Join(errs...)
}

// probeTelegramPresses routes Telegram button presses of conn, whose client answers through the stand-in server: one
// that cannot be verified, one from an account without an Account link and one whose Command is refused, each
// answered with answerCallbackQuery, which the stand-in refuses with the bot token in its error, and each logged.
func probeTelegramPresses(ctx context.Context, router *telegram.Router, k *keyring.Keyring, clocks clock.Clocks,
	logger *logging.Logger, conn telegram.Conn) error {
	action, _, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return err
	}
	router.Handle(telegram.KindCallbackQuery, (&telegram.Presses{Bindings: probeTGBindings{}, Links: probeTGLinks{},
		Commands: probeCommands{}, Keys: k, Path: deliverytest.Unlimited(1, clocks), Business: clocks.Business,
		PublicURL: "http://localhost:8080", Log: logger}).Handle)
	var errs []error
	for i, p := range []struct{ from, data string }{{"5001", "x" + action[1:]}, {"6001", action}, {"5001", action}} {
		u, err := telegram.ParseUpdate(fmt.Appendf(nil, `{"update_id":%d,"callback_query":{"id":"q%d","from":{"id":%s},
			"message":{"message_id":1,"chat":{"id":-1001000000001,"type":"channel"}},"data":%q}}`, 100+i, i, p.from,
			p.data))
		if err != nil {
			return err
		}
		_, err = router.Route(ctx, conn, u)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// probeTGLinks link the Telegram account 5001 only.
type probeTGLinks struct{}

func (probeTGLinks) Lookup(ctx context.Context, space, external string) (accountlinks.User, error) {
	if external == "5001" {
		external = "u-linked"
	}
	return probeLinks{}.Lookup(ctx, space, external)
}

// probeTGBindings bind every Telegram press to Destination 1.
type probeTGBindings struct{}

func (probeTGBindings) TelegramPressBinding(context.Context, int64, string, int64, int64) (delivery.Binding, error) {
	conn := int64(1)
	return delivery.Binding{Destination: delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1",
		Type: delivery.TypeTelegram, Connection: &conn}, ChannelID: "-1001000000001", Language: "en"}, nil
}

// probePresses sends button presses to the callback of the Connection publicID: one that cannot be verified, one from
// an account without an Account link and one whose Command is refused, each answered with an ephemeral post that the
// stand-in server refuses with the bot token in its error, each logged, and returns the answers, whose ephemeral_text
// must carry no secret.
func probePresses(ctx context.Context, svc *connections.Service, k *keyring.Keyring, clocks clock.Clocks,
	logger *logging.Logger, publicID string) error {
	action, keyID, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(mattermost.CallbackPattern, mattermost.NewCallback(mattermost.CallbackConfig{Connections: svc,
		Bindings: probeBindings{}, Links: probeLinks{}, Commands: probeCommands{}, Keys: k,
		Path: deliverytest.Unlimited(1, clocks), Business: clocks.Business, PublicURL: "http://localhost:8080",
		Log: logger}))
	var errs []error
	for _, p := range []struct{ user, action string }{{"u-linked", "x" + action[1:]}, {"u-unlinked", action},
		{"u-linked", action}} {
		body := fmt.Sprintf(`{"user_id":%q,"channel_id":"c","post_id":"p","context":{"action":%q,"key_id":%q}}`,
			p.user, p.action, keyID)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mattermost.CallbackPath+publicID,
			strings.NewReader(body))
		if err != nil {
			return err
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		errs = append(errs, errors.New(rec.Body.String()))
	}
	return errors.Join(errs...)
}

// probeAdapter publishes, edits and replies through the adapter to the Destination 1 of the Connection on the
// interactive path, as lint 3 allows, checks it in the delivery class as the Broken probe does, and runs the checks of
// muster doctor in the background class; it returns the text of every outcome and finding.
func probeAdapter(ctx context.Context, svc *connections.Service, store *probeConnections, clocks clock.Clocks) error {
	a := &mattermost.Adapter{Targets: svc, PublicURL: "http://localhost:8080", IngestURL: "http://localhost:8081",
		Version: "v0.0.0"}
	conn := int64(1)
	dest := delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: delivery.TypeMattermost, Connection: &conn}
	m := delivery.Message{Kind: messages.KindRoot, Language: "en", Colour: "firing", Heading: &messages.Heading{
		Number: 1, Title: "probe", URL: "http://localhost:8080/alert-groups/AGAAAAAAAAAAA1"}}
	path := deliverytest.Unlimited(1, clocks)
	var errs []error
	for _, op := range []delivery.Op{delivery.PublishOp(a, m), delivery.UpdateOp(a, "post-1", m),
		delivery.ReplyOp(a, delivery.Root{MessageID: "post-1"}, m), delivery.CheckOp(a)} {
		o, err := path.Do(ctx, delivery.Subject{Destination: &dest}, op)
		errs = append(errs, err, errors.New(string(o.Error)))
	}
	o := a.Check(ctx, delivery.Call{Class: outbound.ClassDelivery, Destination: dest})
	errs = append(errs, errors.New(string(o.Error)))
	found, err := svc.Doctor(ctx, store, 5*time.Second)
	errs = append(errs, err)
	for _, f := range found {
		errs = append(errs, errors.New(f.Message))
	}
	return errors.Join(errs...)
}

// probeBindings bind every press to Destination 1 in the channel c.
type probeBindings struct{}

func (probeBindings) PressBinding(context.Context, int64, string, string) (delivery.Binding, error) {
	conn := int64(1)
	return delivery.Binding{Destination: delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1",
		Type: delivery.TypeMattermost, Connection: &conn}, ChannelID: "c", Language: "en"}, nil
}

func (probeBindings) PostDestination(context.Context, int64, string, string) (delivery.Destination, bool, error) {
	conn := int64(1)
	return delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: delivery.TypeMattermost,
		Connection: &conn}, true, nil
}

// probeLinks link the account u-linked only.
type probeLinks struct{}

func (probeLinks) Lookup(_ context.Context, _, external string) (accountlinks.User, error) {
	if external != "u-linked" {
		return accountlinks.User{}, accountlinks.ErrNotLinked
	}
	return accountlinks.User{ID: 1, PublicID: "SRAAAAAAAAAAA1", Login: "bob", Name: "Bob", Role: "responder",
		Status: accountlinks.StatusActive}, nil
}

// probeCommands refuse every Command.
type probeCommands struct{}

func (probeCommands) Acknowledge(context.Context, groups.Caller, string) (groups.Result, error) {
	return groups.Result{}, &groups.RefusedError{Code: groups.CodeAlreadyResolved, Message: "already resolved"}
}

func (probeCommands) Unacknowledge(ctx context.Context, c groups.Caller, id string) (groups.Result, error) {
	return probeCommands{}.Acknowledge(ctx, c, id)
}

func (probeCommands) Resolve(ctx context.Context, c groups.Caller, id string, _ *string) (groups.Result, error) {
	return probeCommands{}.Acknowledge(ctx, c, id)
}

func (probeCommands) Snooze(ctx context.Context, c groups.Caller, id string, _ groups.SnoozeEnd) (groups.Result,
	error) {
	return probeCommands{}.Acknowledge(ctx, c, id)
}

func (probeCommands) Unsnooze(ctx context.Context, c groups.Caller, id string) (groups.Result, error) {
	return probeCommands{}.Acknowledge(ctx, c, id)
}

// probeConnections is the one Connection of probeMattermost in memory.
type probeConnections struct {
	connections.Queries
	row *cdb.GetConnectionRow
}

func (s *probeConnections) InTx(_ context.Context, f func(connections.Queries) error) error {
	return f(s)
}

func (s *probeConnections) InsertConnection(_ context.Context, a cdb.InsertConnectionParams) (int64, error) {
	s.row = &cdb.GetConnectionRow{ID: 1, PublicID: a.PublicID, Type: a.Type, Name: a.Name,
		MattermostServerUrl: a.ServerUrl, TelegramBotApiBaseUrl: a.BotApiBaseUrl, TelegramUpdateMode: a.UpdateMode,
		TelegramWebhookSecretCiphertext: a.WebhookSecretCiphertext, TelegramWebhookSecretKeyID: a.WebhookSecretKeyID,
		BotTokenCiphertext: a.BotTokenCiphertext, BotTokenKeyID: a.BotTokenKeyID,
		BotTokenUpdatedAt: a.BotTokenUpdatedAt, Proxy: a.Proxy, ProxyPasswordCiphertext: a.ProxyPasswordCiphertext,
		ProxyPasswordKeyID: a.ProxyPasswordKeyID, ProxyPasswordUpdatedAt: a.ProxyPasswordUpdatedAt,
		LimiterLimit: a.LimiterLimit, LimiterPerSeconds: a.LimiterPerSeconds, CreatedAt: a.Now, Version: 1}
	return 1, nil
}

func (s *probeConnections) GetConnection(context.Context, cdb.GetConnectionParams) (cdb.GetConnectionRow, error) {
	if s.row == nil {
		return cdb.GetConnectionRow{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *probeConnections) LockConnection(context.Context, cdb.LockConnectionParams) (cdb.LockConnectionRow, error) {
	if s.row == nil {
		return cdb.LockConnectionRow{}, pgx.ErrNoRows
	}
	return cdb.LockConnectionRow{ID: s.row.ID, PublicID: s.row.PublicID, Type: s.row.Type, Version: s.row.Version},
		nil
}

func (s *probeConnections) UpdateConnection(_ context.Context, a cdb.UpdateConnectionParams) error {
	s.row.Name, s.row.MattermostServerUrl, s.row.Proxy = a.Name, a.ServerUrl, a.Proxy
	s.row.TelegramBotApiBaseUrl, s.row.TelegramUpdateMode = a.BotApiBaseUrl, a.UpdateMode
	s.row.TelegramWebhookSecretCiphertext, s.row.TelegramWebhookSecretKeyID = a.WebhookSecretCiphertext,
		a.WebhookSecretKeyID
	s.row.BotTokenCiphertext, s.row.BotTokenKeyID = a.BotTokenCiphertext, a.BotTokenKeyID
	s.row.ProxyPasswordCiphertext, s.row.ProxyPasswordKeyID = a.ProxyPasswordCiphertext, a.ProxyPasswordKeyID
	s.row.Version++
	return nil
}

// GetDestinationTarget is the Destination 1 of the Connection, in the team t and the channel c.
func (s *probeConnections) GetDestinationTarget(context.Context, cdb.GetDestinationTargetParams) (
	cdb.GetDestinationTargetRow, error) {
	if s.row == nil {
		return cdb.GetDestinationTargetRow{}, pgx.ErrNoRows
	}
	r := s.row
	return cdb.GetDestinationTargetRow{MattermostTeamID: pgtype.Text{String: "t", Valid: true},
		MattermostChannelID: pgtype.Text{String: "c", Valid: true}, ID: r.ID, PublicID: r.PublicID, Type: r.Type,
		Name: r.Name, MattermostServerUrl: r.MattermostServerUrl, BotTokenCiphertext: r.BotTokenCiphertext,
		BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt, Proxy: r.Proxy,
		ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version}, nil
}

// GetTelegramDestinationTarget is the Destination 1 of the Connection, on the channel @probe whose ids are known.
func (s *probeConnections) GetTelegramDestinationTarget(context.Context, cdb.GetTelegramDestinationTargetParams) (
	cdb.GetTelegramDestinationTargetRow, error) {
	if s.row == nil {
		return cdb.GetTelegramDestinationTargetRow{}, pgx.ErrNoRows
	}
	r := s.row
	return cdb.GetTelegramDestinationTargetRow{TelegramChannelID: pgtype.Text{String: "@probe", Valid: true},
		TelegramChannelChatID:    pgtype.Int8{Int64: -1001000000001, Valid: true},
		TelegramDiscussionChatID: pgtype.Int8{Int64: -1001000000002, Valid: true}, ID: r.ID, PublicID: r.PublicID,
		Type: r.Type, Name: r.Name, TelegramBotApiBaseUrl: r.TelegramBotApiBaseUrl,
		BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt,
		Proxy: r.Proxy, ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version}, nil
}

// ListTelegramDestinations lists the Destination 1 of the Connection on @probe for muster doctor.
func (s *probeConnections) ListTelegramDestinations(context.Context, int64) ([]cdb.ListTelegramDestinationsRow,
	error) {
	return []cdb.ListTelegramDestinationsRow{{ID: 1, PublicID: "DSAAAAAAAAAAA1", Name: "probe",
		ConnectionID: pgtype.Int8{Int64: 1, Valid: true}, TelegramChannelID: pgtype.Text{String: "@probe",
			Valid: true}}}, nil
}

// CountConnectionDestinations finds no Destination, so that the Connection can be deleted.
func (s *probeConnections) CountConnectionDestinations(context.Context, cdb.CountConnectionDestinationsParams) (int64,
	error) {
	return 0, nil
}

// MarkConnectionDeleted keeps the row: the probe's deletion only runs to its deleteWebhook.
func (s *probeConnections) MarkConnectionDeleted(context.Context, cdb.MarkConnectionDeletedParams) error {
	return nil
}

// ListConnections lists the Connection for muster doctor.
func (s *probeConnections) ListConnections(context.Context, cdb.ListConnectionsParams) ([]cdb.ListConnectionsRow,
	error) {
	if s.row == nil {
		return nil, nil
	}
	return []cdb.ListConnectionsRow{cdb.ListConnectionsRow(*s.row)}, nil
}

// ListMattermostDestinations lists the Destination 1 of the Connection for muster doctor.
func (s *probeConnections) ListMattermostDestinations(context.Context, int64) ([]cdb.ListMattermostDestinationsRow,
	error) {
	return []cdb.ListMattermostDestinationsRow{{ID: 1, PublicID: "DSAAAAAAAAAAA1", Name: "probe",
		ConnectionID: pgtype.Int8{Int64: 1, Valid: true}, MattermostTeamID: pgtype.Text{String: "t", Valid: true},
		MattermostChannelID: pgtype.Text{String: "c", Valid: true}}}, nil
}

func (s *probeConnections) SetBotIdentity(context.Context, cdb.SetBotIdentityParams) error {
	return nil
}

func (s *probeConnections) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error {
	return nil
}

func (s *probeConnections) Notify(context.Context, db.Hint) error { return nil }

// ListPollingConnections is the Connection when it is a Telegram one in the long-polling mode.
func (s *probeConnections) ListPollingConnections(context.Context, int64) ([]cdb.ListPollingConnectionsRow, error) {
	r := s.row
	if r == nil || r.Type != connections.TypeTelegram || r.TelegramUpdateMode.String != connections.ModeLongPolling {
		return nil, nil
	}
	return []cdb.ListPollingConnectionsRow{{ID: r.ID, PublicID: r.PublicID, TelegramBotApiBaseUrl: r.TelegramBotApiBaseUrl,
		BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt,
		Proxy: r.Proxy, ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version}}, nil
}

func (s *probeConnections) LockUpdates(context.Context, cdb.LockUpdatesParams) error { return nil }

func (s *probeConnections) AwaitUpdates(context.Context, cdb.AwaitUpdatesParams) error { return nil }

func (s *probeConnections) GetUpdateOffset(context.Context, cdb.GetUpdateOffsetParams) (cdb.GetUpdateOffsetRow,
	error) {
	return cdb.GetUpdateOffsetRow{}, nil
}

func (s *probeConnections) GetOutage(context.Context) (cdb.GetOutageRow, error) {
	return cdb.GetOutageRow{}, nil
}

func (s *probeConnections) StoreUpdateOffset(context.Context, cdb.StoreUpdateOffsetParams) error {
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

// probeWebhooks pushes the secrets through an outgoing webhook (C-15.FR-10, FR-5): two of them as named Secrets read
// by the URL and a header, the third as the password of a SOCKS5 proxy that refuses it; through their list, events
// that the stand-in answers with 400 echoing the request, a redirect whose target carries them, request templates
// that fail on them, and the refused proxy. Neither the log nor the outcomes, which delivery shows as the Timeline
// error, may carry them.
func probeWebhooks(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
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
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/redirect") {
				w.Header().Set("Location", "http://elsewhere.example.org/?k="+secrets[0]+"&o="+secrets[1])
				w.WriteHeader(http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "refused %s %s", r.URL.RawQuery, r.Header.Get("Authorization")) //nolint:gosec // G705: a stand-in server that echoes the secrets on purpose
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	socks, err := fakeproxy.Start(ctx, fakeproxy.SOCKS5, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "other"})
	if err != nil {
		return err
	}
	defer func() { _ = socks.Close() }()
	password, _, err := k.ApplySecret(webhooks.FieldProxyPassword, keyring.StoredSecret{},
		keyring.Replace(logging.Secret(secrets[2])), now)
	if err != nil {
		return err
	}
	store := &probeWebhookStore{secrets: map[string]keyring.StoredSecret{}, row: whdb.GetTargetRow{ID: 1,
		PublicID: "DSAAAAAAAAAAA1", WebhookMode: pgtype.Text{String: webhooks.ModeEvents, Valid: true},
		Proxy: []byte(`{"enabled":false}`), ProxyPasswordCiphertext: password.Ciphertext,
		ProxyPasswordKeyID: pgtype.Text{String: password.KeyID, Valid: true}}}
	svc := webhooks.New(1, webhooks.Config{Store: store, Writer: store, Keyring: k,
		Audit: audit.NewWriter(logger, clock.NewManual(now)), Business: clock.NewManual(now),
		PublicURL: "http://localhost:8080"})
	by := webhooks.Requester{Actor: audit.System, Transport: audit.TransportAPI}
	var errs []error
	_, _, err = svc.GenerateSigningSecret(ctx, by, "DSAAAAAAAAAAA1")
	errs = append(errs, err)
	for i, name := range []string{"token", "other"} {
		_, _, err := svc.SetSecret(ctx, by, "DSAAAAAAAAAAA1", nil, name, logging.Secret(secrets[i]))
		errs = append(errs, err)
	}
	list, err := svc.ListSecrets(ctx, "DSAAAAAAAAAAA1")
	errs = append(errs, err, fmt.Errorf("%+v", list))
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	a := &webhooks.Adapter{Service: svc, Sandbox: templates.New(clock.NewManual(now), clock.Real{}),
		Network: webhooks.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}}}
	base := "http://" + ln.Addr().String()
	for _, c := range []struct{ url, header, proxy string }{
		{base + "/hook?k={{ .Secrets.token }}", "Bearer {{ .Secrets.other }}", `{"enabled":false}`},
		{base + "/redirect?k={{ .Secrets.token }}", "x", `{"enabled":false}`},
		{base + "/hook/{{ printf \"%d\" .Secrets.token }}{{ len 3 }}", "x", `{"enabled":false}`},
		{base + "/hook", "{{ .Secrets.other }}{{ index .Secrets .Secrets.token }}{{ len 3 }}", `{"enabled":false}`},
		{base + "/hook?k={{ .Secrets.token }}", "{{ .Secrets.other }}",
			`{"enabled":true,"type":"socks5","address":"` + socks.Addr() + `","username":"muster"}`},
	} {
		cfg, _ := json.Marshal(webhooks.EventsConfig{URL: c.url, Headers: []webhooks.Header{{Name: "Authorization",
			Value: c.header}}})
		store.row.WebhookEventsConfig, store.row.Proxy = cfg, []byte(c.proxy)
		out := a.SendEvent(ctx, delivery.EventCall{Class: outbound.ClassDelivery,
			Destination: delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: webhooks.TypeWebhook},
			WebhookID:   "msg_probe", Body: []byte(`{}`)})
		errs = append(errs, errors.New(string(out.Kind)+": "+string(out.Error)))
	}
	return errors.Join(errs...)
}

// probeWebhookStore is the one outgoing webhook of probeWebhooks in memory.
type probeWebhookStore struct {
	webhooks.TxQueries
	row     whdb.GetTargetRow
	version int64
	secrets map[string]keyring.StoredSecret
}

func (s *probeWebhookStore) InTx(_ context.Context, f func(webhooks.TxQueries) error) error {
	return f(s)
}

func (s *probeWebhookStore) GetWebhookDestination(context.Context, whdb.GetWebhookDestinationParams) (
	whdb.GetWebhookDestinationRow, error) {
	return whdb.GetWebhookDestinationRow{ID: 1, PublicID: s.row.PublicID, Name: "probe", Type: webhooks.TypeWebhook,
		Version: s.version, SigningSecretSet: s.row.SigningSecretCiphertext != nil}, nil
}

func (s *probeWebhookStore) LockWebhookDestination(context.Context, whdb.LockWebhookDestinationParams) (
	whdb.LockWebhookDestinationRow, error) {
	return whdb.LockWebhookDestinationRow{ID: 1, PublicID: s.row.PublicID, Name: "probe", Type: webhooks.TypeWebhook,
		Version: s.version, SigningSecretCiphertext: s.row.SigningSecretCiphertext,
		SigningSecretKeyID: s.row.SigningSecretKeyID}, nil
}

func (s *probeWebhookStore) ListSecrets(context.Context, whdb.ListSecretsParams) ([]whdb.ListSecretsRow, error) {
	var out []whdb.ListSecretsRow
	for name, v := range s.secrets {
		out = append(out, whdb.ListSecretsRow{Name: name, ValueUpdatedAt: *v.UpdatedAt})
	}
	return out, nil
}

func (s *probeWebhookStore) GetTarget(context.Context, whdb.GetTargetParams) (whdb.GetTargetRow, error) {
	return s.row, nil
}

func (s *probeWebhookStore) ListSecretValues(context.Context, whdb.ListSecretValuesParams) (
	[]whdb.ListSecretValuesRow, error) {
	var out []whdb.ListSecretValuesRow
	for name, v := range s.secrets {
		out = append(out, whdb.ListSecretValuesRow{Name: name, ValueCiphertext: v.Ciphertext, ValueKeyID: v.KeyID})
	}
	return out, nil
}

func (s *probeWebhookStore) BumpDestinationVersion(context.Context, whdb.BumpDestinationVersionParams) (int64,
	error) {
	s.version++
	return s.version, nil
}

func (s *probeWebhookStore) UpsertSecret(_ context.Context, a whdb.UpsertSecretParams) error {
	at := a.Now
	s.secrets[a.Name] = keyring.StoredSecret{Ciphertext: a.ValueCiphertext, KeyID: a.ValueKeyID, UpdatedAt: &at}
	return nil
}

func (s *probeWebhookStore) RotateSigningSecret(_ context.Context, a whdb.RotateSigningSecretParams) (int64, error) {
	s.row.SigningSecretCiphertext, s.row.SigningSecretKeyID = a.Ciphertext, a.KeyID
	s.row.PreviousSigningSecretCiphertext, s.row.PreviousSigningSecretKeyID = a.PreviousCiphertext, a.PreviousKeyID
	s.version++
	return s.version, nil
}

func (s *probeWebhookStore) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error {
	return nil
}

func (s *probeWebhookStore) Notify(context.Context, db.Hint) error { return nil }
