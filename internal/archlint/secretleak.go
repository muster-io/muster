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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/accountlinks"
	alinkdb "github.com/muster-io/muster/internal/accountlinks/dbgen"
	"github.com/muster-io/muster/internal/api"
	"github.com/muster-io/muster/internal/audit"
	adb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/auth"
	authdb "github.com/muster-io/muster/internal/auth/dbgen"
	"github.com/muster-io/muster/internal/bridge"
	bgen "github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/cli"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/connections"
	cdb "github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/doctor"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
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
	"github.com/muster-io/muster/internal/organization"
	orgdb "github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/outgoingheartbeat"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/requestlog"
	"github.com/muster-io/muster/internal/runtime"
	"github.com/muster-io/muster/internal/slack"
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
	{Name: "bridge_connections", Run: probeBridge},
	{Name: "bridge_events", Run: probeBridgeEvents},
	{Name: "outgoing_webhooks", Run: probeWebhooks},
	{Name: "account_links", Run: probeAccountLinks},
	{Name: "bridge_links", Run: probeBridgeLinks},
	{Name: "bridge_check", Run: probeBridgeCheck},
	{Name: "slack_connections", Run: probeSlack},
	{Name: "slack_presses", Run: probeSlackPresses},
	{Name: "connection_drafts", Run: probeConnectionDrafts},
	{Name: "outgoing_heartbeat", Run: probeOutgoingHeartbeat},
	{Name: "key_rotation", Run: probeKeyRotation},
	{Name: "request_log", Run: probeRequestLog},
	{Name: "request_log_inbound", Run: probeRequestLogInbound},
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
	errs = append(errs, probeTester(ctx, &telegram.Tester{Adapter: a, Path: path, Renderer: probeRenderer(clocks),
		Real: clocks.Real}, dest))
	res, err := svc.CheckTelegramChannel(ctx, destinations.TelegramChannelCheck{Connection: publicID,
		Destination: &dest.ID, ChannelID: "@probe"})
	errs = append(errs, err, errors.New(string(res.Check.Outcome.Error)))
	for _, st := range res.Check.Steps {
		errs = append(errs, errors.New(st.Message))
	}
	return errors.Join(errs...)
}

// probeTelegramPresses routes Telegram button presses of conn, whose client answers through the stand-in server: one
// that cannot be verified, one from an account without an Account link and one whose Command is refused, on a Root
// message and on a Reminder, each answered with answerCallbackQuery, which the stand-in refuses with the bot token in
// its error, and each logged.
func probeTelegramPresses(ctx context.Context, router *telegram.Router, k *keyring.Keyring, clocks clock.Clocks,
	logger *logging.Logger, conn telegram.Conn) error {
	action, _, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return err
	}
	reminder, _, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectReply, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandStillOnIt})
	if err != nil {
		return err
	}
	router.Handle(telegram.KindCallbackQuery, (&telegram.Presses{Bindings: probeTGBindings{}, Links: probeTGLinks{},
		Commands: probeCommands{}, Keys: k, Path: deliverytest.Unlimited(1, clocks), Business: clocks.Business,
		PublicURL: "http://localhost:8080", Log: logger}).Handle)
	var errs []error
	for i, p := range []struct{ from, data string }{{"5001", "x" + action[1:]}, {"6001", action}, {"5001", action},
		{"6001", reminder}, {"5001", reminder}} {
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

func (probeTGBindings) TelegramReplyBinding(ctx context.Context, conn int64, group string, chat, message int64) (
	delivery.Binding, error) {
	return probeTGBindings{}.TelegramPressBinding(ctx, conn, group, chat, message)
}

// probePresses sends button presses to the callback of the Connection publicID: one that cannot be verified, one from
// an account without an Account link and one whose Command is refused, on a Root message and on a Reminder, whose
// answers go to the Thread of its Root message, each answered with an ephemeral post that the stand-in server refuses
// with the bot token in its error, each logged, and returns the answers, whose ephemeral_text must carry no secret.
func probePresses(ctx context.Context, svc *connections.Service, k *keyring.Keyring, clocks clock.Clocks,
	logger *logging.Logger, publicID string) error {
	action, keyID, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return err
	}
	reminder, _, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectReply, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandStillOnIt})
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
		{"u-linked", action}, {"u-unlinked", reminder}, {"u-linked", reminder}} {
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
	// A Destination test and a preview (C-16.FR-2): the token never shows in their requests, responses or errors.
	tester := &mattermost.Tester{Adapter: a, Path: path, Renderer: probeRenderer(clocks),
		Waits:  &mattermost.PressWaits{Notify: func(context.Context, string, string) error { return nil }},
		Budget: time.Second, Real: clocks.Real}
	errs = append(errs, probeTester(ctx, tester, dest))
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

func (probeBindings) ReplyBinding(ctx context.Context, conn int64, group, post string) (delivery.Binding, error) {
	b, err := probeBindings{}.PressBinding(ctx, conn, group, post)
	b.RootMessageID = "p-root"
	return b, err
}

func (probeBindings) PostDestination(context.Context, int64, string, string) (delivery.Destination, bool, error) {
	conn := int64(1)
	return delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: delivery.TypeMattermost,
		Connection: &conn}, true, nil
}

func (probeBindings) TestDestination(context.Context, int64, string, string) (delivery.Destination, bool, error) {
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

func (probeCommands) AnswerReminder(ctx context.Context, c groups.Caller, id string) (groups.Result, error) {
	return probeCommands{}.Acknowledge(ctx, c, id)
}

// The answers of the stand-in bridge of probeBridge.
const (
	bridgeProblem int32 = iota
	bridgeUnauthorized
	bridgeVersion
	bridgeVersionPushSecret
	bridgeInfo
	bridgeInfoPush
	bridgeForbidden
	bridgeChannel
)

// probeBridge pushes the secrets through a Bridge Connection (C-22.AC-3): the token, the push secret of push event
// delivery and the password of an HTTP proxy that refuses it, through the Connection's creation, its checks and the
// Destination check of a channel (checkChannel) — through the proxy; against a stand-in bridge whose problems, a 500,
// a 401 and a forbidden 403, echo the token, the push secret, which the bridge knows too, and the Authorization header
// in their detail, title and type, whose info names the token, and then the push secret, as its protocol version,
// whose info offers only poll, and whose channel is named after the token and the push secret; through the Bridge
// delivery — postMessage, editMessage, replyInThread, the Broken probe and a test message with its preview — whose
// messages the stand-in refuses with each of those problems, and through the proxy again; and against a listener
// that cuts every connection — through a creation refused for a malformed push secret, muster doctor, which checks
// the Connection with getInfo and its Destination with checkChannel, the Bridge delivery, its deletion, and a client
// with an unsupported proxy.
func probeBridge(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
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
	pushSecret := "whsec_" + base64.StdEncoding.EncodeToString([]byte(secrets[1]))
	var answer, channelAnswer, messageAnswer atomic.Int32
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			echo := "invalid token " + secrets[0] + " " + r.Header.Get("Authorization") + " push secret " + pushSecret
			a := answer.Load()
			switch {
			case strings.HasPrefix(r.URL.Path, "/v1/channels/"):
				a = channelAnswer.Load()
			case strings.HasPrefix(r.URL.Path, "/v1/messages"):
				a = messageAnswer.Load()
			}
			switch a {
			case bridgeChannel:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"channel_id": "probe",
					"name": secrets[0] + " " + pushSecret})
			case bridgeInfoPush:
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"protocol_version":"1","bridge":{"name":"probe","version":"1"},`+
					`"messenger":{"name":"probe"},"capabilities":{},"limits":{"max_body_length":4000,`+
					`"edit_max_age_seconds":null},"user_lookup":"none","event_delivery":["poll","push"]}`)
			case bridgeVersion:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"protocol_version": secrets[0]})
			case bridgeVersionPushSecret:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"protocol_version": pushSecret})
			case bridgeInfo:
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"protocol_version":"1","bridge":{"name":"probe","version":"1"},`+
					`"messenger":{"name":"probe"},"capabilities":{},"limits":{"max_body_length":4000,`+
					`"edit_max_age_seconds":null},"user_lookup":"none","event_delivery":["poll"]}`)
			default:
				status, class := http.StatusInternalServerError, "transient"
				switch a {
				case bridgeUnauthorized:
					status, class = http.StatusUnauthorized, "forbidden"
				case bridgeForbidden:
					status, class = http.StatusForbidden, "forbidden"
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "detail": echo, "title": echo,
					"type": "urn:" + secrets[0] + ":" + pushSecret, "class": class})
			}
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	cut, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	cutter := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			if c, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = c.Close()
			}
		})}
	go func() { _ = cutter.Serve(cut) }()
	defer func() { _ = cutter.Close() }()
	proxy, err := fakeproxy.Start(ctx, fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "other"})
	if err != nil {
		return err
	}
	defer func() { _ = proxy.Close() }()
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
	in := connections.Input{Type: connections.TypeBridge, Name: "probe", BridgeBaseURL: "http://" + ln.Addr().String(),
		EventDelivery: connections.EventDeliveryPush, BotToken: keyring.Replace(logging.Secret(secrets[0])),
		PushSecret: keyring.Replace(logging.Secret(pushSecret)), Limiter: connections.Limiter{Limit: 10, PerSeconds: 1},
		Proxy: proxyconf.Input{Enabled: true, Type: new("http"), Address: new(proxy.Addr()), UsernameSet: true,
			Username: new("muster"), Password: keyring.Replace(logging.Secret(secrets[2]))}}
	var errs []error
	check := func() {
		res, err := svc.Check(ctx, store.row.PublicID, nil)
		errs = append(errs, err, errors.New(string(res.Info)))
		for _, st := range res.Steps {
			errs = append(errs, errors.New(st.Message))
		}
	}
	checkChannel := func() {
		res, err := svc.CheckBridgeChannel(ctx, destinations.BridgeChannelCheck{Connection: store.row.PublicID,
			ChannelID: "probe"})
		errs = append(errs, err, errors.New(res.Check.Message), errors.New(res.Check.Name),
			errors.New(string(res.Check.Outcome.Error)))
	}
	doctorRun := func() {
		found, err := svc.Doctor(ctx, store, 2*time.Second)
		errs = append(errs, err)
		for _, f := range found {
			errs = append(errs, errors.New(f.Message))
		}
	}
	c, err := svc.Create(ctx, by, in)
	if err != nil {
		return err
	}
	check()
	checkChannel()
	in.Proxy, in.PushSecret, in.BotToken = proxyconf.Input{Enabled: false}, keyring.Keep, keyring.Keep
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	for _, a := range []int32{bridgeProblem, bridgeUnauthorized, bridgeVersion, bridgeVersionPushSecret, bridgeInfo,
		bridgeForbidden, bridgeChannel} {
		answer.Store(a)
		channelAnswer.Store(a)
		check()
		checkChannel()
	}
	answer.Store(bridgeInfoPush)
	for _, a := range []int32{bridgeProblem, bridgeUnauthorized, bridgeForbidden, bridgeChannel} {
		channelAnswer.Store(a)
		doctorRun()
	}
	check()
	for _, a := range []int32{bridgeProblem, bridgeUnauthorized, bridgeForbidden} {
		messageAnswer.Store(a)
		channelAnswer.Store(a)
		errs = append(errs, probeBridgeAdapter(ctx, svc, clocks))
	}
	proxied := in
	proxied.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new(proxy.Addr()), UsernameSet: true,
		Username: new("muster"), Password: keyring.Replace(logging.Secret(secrets[2]))}
	_, err = svc.Update(ctx, by, c.PublicID, nil, proxied)
	errs = append(errs, err, probeBridgeAdapter(ctx, svc, clocks))
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	malformed := in
	malformed.Name, malformed.BotToken = "malformed", keyring.Replace(logging.Secret(secrets[0]))
	malformed.PushSecret = keyring.Replace(logging.Secret("whsec_" + secrets[1]))
	_, err = svc.Create(ctx, by, malformed)
	errs = append(errs, err)
	in.BridgeBaseURL, in.BotToken = "http://"+cut.Addr().String(), keyring.Replace(logging.Secret(secrets[0]))
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	check()
	checkChannel()
	doctorRun()
	errs = append(errs, probeBridgeAdapter(ctx, svc, clocks))
	errs = append(errs, svc.Delete(ctx, by, c.PublicID, nil))
	_, err = bridge.NewClient(bridge.Network(network), bridge.Settings{BaseURL: "http://" + ln.Addr().String(),
		Token: logging.Secret(secrets[0]), Proxy: &outbound.Proxy{Type: "ftp", Address: "proxy:1", Username: "muster",
			Password: logging.Secret(secrets[2])}})
	return errors.Join(append(errs, err)...)
}

// probeBridgeAdapter posts, edits and replies through the Bridge delivery to the Destination 2 of the Connection on the
// interactive path, as lint 3 allows, checks it there and in the delivery class as the Broken probe does, and runs its
// test message and its preview; it returns the text of every outcome and step.
func probeBridgeAdapter(ctx context.Context, svc *connections.Service, clocks clock.Clocks) error {
	a := &bridge.Adapter{Targets: svc}
	conn := int64(1)
	dest := delivery.Destination{ID: 2, PublicID: "DSAAAAAAAAAAA2", Type: delivery.TypeBridge, Connection: &conn}
	m := delivery.Message{Kind: messages.KindRoot, Language: "en", Colour: "firing", Heading: &messages.Heading{
		Number: 1, Title: "probe", URL: "http://localhost:8080/alert-groups/AGAAAAAAAAAAA1"}}
	path := deliverytest.Unlimited(1, clocks)
	var errs []error
	for _, op := range []delivery.Op{delivery.PublishOp(a, m), delivery.UpdateOp(a, "m1", m),
		delivery.ReplyOp(a, delivery.Root{MessageID: "m1", ThreadAnchorID: "t-m1"}, m), delivery.CheckOp(a)} {
		o, err := path.Do(ctx, delivery.Subject{Destination: &dest}, op)
		errs = append(errs, err, errors.New(string(o.Error)))
	}
	o := a.Check(ctx, delivery.Call{Class: outbound.ClassDelivery, Destination: dest})
	errs = append(errs, errors.New(string(o.Error)))
	errs = append(errs, probeTester(ctx, &bridge.Tester{Adapter: a, Path: path, Renderer: probeRenderer(clocks),
		Real: clocks.Real}, dest))
	return errors.Join(errs...)
}

// probeBridgeEvents pushes the secrets through the event paths of a Bridge Connection (C-22.AC-3): the token, the push
// secret and a third secret as a forged signature. Pushed events with a wrong signature — the third secret — a stale
// timestamp, an unknown Connection, a body that is no event and carries the secrets, and a signed press from an
// account without an Account link, whose answer the stand-in bridge refuses with a 401 problem that echoes the token,
// the push secret and the Authorization header; then, in poll mode, the Leader's poller against the same stand-in,
// whose pollEvents fails with that problem, then answers a batch with such a press, then fails with a 403 echo, until
// the poll is stopped. Neither the log nor the push answers may carry a secret.
func probeBridgeEvents(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Now().UTC()
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
	data, _, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return err
	}
	pushSecret := "whsec_" + base64.StdEncoding.EncodeToString([]byte(secrets[1]))
	press := func(id string) []byte {
		return fmt.Appendf(nil, `{"id":%q,"type":"press","occurred_at":%q,"message_id":"m1",`+
			`"user":{"external_id":"u-unlinked","display_name":"probe"},"data":%q}`, id,
			time.Now().UTC().Format(time.RFC3339Nano), data)
	}
	var polls, answers atomic.Int32
	var polling atomic.Bool
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// In poll mode the Connection holds no push secret, which Muster therefore cannot recognize.
			echo := "invalid token " + secrets[0] + " " + r.Header.Get("Authorization")
			if !polling.Load() {
				echo += " push secret " + pushSecret
			}
			status, class := http.StatusUnauthorized, "forbidden"
			switch r.URL.Path {
			case "/v1/events":
				switch polls.Add(1) {
				case 1:
				case 2:
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"events":[%s],"next_cursor":"1"}`, press("ev-polled"))
					return
				default:
					status = http.StatusForbidden
				}
			case "/v1/answers":
				answers.Add(1)
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "detail": echo, "title": echo,
				"type": "urn:" + secrets[0], "class": class})
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
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
	in := connections.Input{Type: connections.TypeBridge, Name: "probe", BridgeBaseURL: "http://" + ln.Addr().String(),
		EventDelivery: connections.EventDeliveryPush, BotToken: keyring.Replace(logging.Secret(secrets[0])),
		PushSecret: keyring.Replace(logging.Secret(pushSecret)), Limiter: connections.Limiter{Limit: 10, PerSeconds: 1},
		Proxy: proxyconf.Input{Enabled: false}}
	c, err := svc.Create(ctx, by, in)
	if err != nil {
		return err
	}
	events := &bridge.Events{Store: svc, Bindings: probeBridgeBindings{}, Links: probeLinks{},
		Commands: probeCommands{}, Keys: k, Path: deliverytest.Unlimited(1, clocks), Budget: 5 * time.Second,
		PublicURL: "http://localhost:8080", Clocks: clocks, Log: logger}
	mux := http.NewServeMux()
	mux.Handle(bridge.PushPattern, bridge.NewPush(bridge.PushConfig{Connections: svc, Events: events,
		Real: clocks.Real, Log: logger}))
	var errs []error
	statuses := []int{}
	send := func(connection, id string, at time.Time, body []byte, signature string) {
		if signature == "" {
			var err error
			if signature, err = webhooks.Sign([]logging.Secret{logging.Secret(pushSecret)}, id, at, body); err != nil {
				errs = append(errs, err)
				return
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, bridge.CallbackPath+connection,
			bytes.NewReader(body))
		if err != nil {
			errs = append(errs, err)
			return
		}
		req.Header.Set(webhooks.HeaderID, id)
		req.Header.Set(webhooks.HeaderTimestamp, strconv.FormatInt(at.Unix(), 10))
		req.Header.Set(webhooks.HeaderSignature, signature)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		statuses = append(statuses, rec.Code)
		errs = append(errs, errors.New(rec.Body.String()))
	}
	forged := "v1," + base64.StdEncoding.EncodeToString([]byte(secrets[2]))
	notAnEvent := []byte(`{"id":"ev-2","token":"` + secrets[0] + `","secret":"` + pushSecret + `"}`)
	send(c.PublicID, "ev-1", time.Now(), press("ev-1"), forged)
	send(c.PublicID, "ev-1", time.Now().Add(-6*time.Minute), press("ev-1"), "")
	send("CNAAAAAAAAAAA9", "ev-1", time.Now(), press("ev-1"), "")
	send(c.PublicID, "ev-2", time.Now(), notAnEvent, "")
	send(c.PublicID, "ev-3", time.Now(), press("ev-3"), "")
	if want := []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusUnauthorized,
		http.StatusBadRequest, http.StatusNoContent}; !slices.Equal(statuses, want) || answers.Load() != 1 {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the pushes were answered %v with %d answers, want %v and 1",
			statuses, answers.Load(), want)})
	}

	polling.Store(true)
	in.EventDelivery, in.PushSecret, in.BotToken = connections.EventDeliveryPoll, keyring.Keep, keyring.Keep
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	pollCtx, stop := context.WithCancel(ctx)
	defer stop()
	poller := &bridge.Poller{Source: svc, Events: events, Log: logger, Wait: time.Second}
	ran := make(chan error, 1)
	go func() { ran <- poller.Run(pollCtx) }()
	deadline := time.After(10 * time.Second)
	for polls.Load() < 3 {
		select {
		case <-deadline:
			errs = append(errs, &probeIncomplete{fmt.Sprintf("the poller called pollEvents %d times", polls.Load())})
			polls.Store(3)
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	errs = append(errs, <-ran)
	if answers.Load() != 2 || store.cursor == nil {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the polled press was answered %d times, cursor stored %v",
			answers.Load()-1, store.cursor != nil)})
	}
	return errors.Join(errs...)
}

// probeBridgeBindings bind every Bridge press and Thread message to the Root message of the Alert Group
// AGAAAAAAAAAAA1, and find no Root message to delete.
type probeBridgeBindings struct{}

func (probeBridgeBindings) BridgePressBinding(context.Context, int64, string) (delivery.BridgeBinding, error) {
	return delivery.BridgeBinding{Kind: buttons.SubjectRoot, GroupPublicID: "AGAAAAAAAAAAA1",
		SnoozeSeconds: []int64{3600}}, nil
}

func (b probeBridgeBindings) BridgeThreadBinding(ctx context.Context, conn int64, message string) (
	delivery.BridgeBinding, error) {
	return b.BridgePressBinding(ctx, conn, message)
}

func (probeBridgeBindings) RootDeleted(context.Context, int64, string) error {
	return nil
}

// The answers of the stand-in Slack of probeSlack.
const (
	slackOK int32 = iota
	slackEcho
	slackUser
	slackScopesEcho
	slackMismatch
	slackInvalidAuth
	slackError
	slackMissingScope
	slackHTML500
	slackRateLimited
	slackNotMember
	slackLate
	slackGone
	slackMisplaced
)

// probeSlack pushes the secrets through a Slack Connection (C-24.AC-3): the bot token, the app-level token, the Slack
// signing secret and the password of its HTTP proxy. The Connection is created in http mode with all three secrets
// behind a proxy that refuses the password, and checked there; then, in socket mode without a proxy, it is checked
// against a stand-in Slack, where muster dev's route would send it, whose answers echo the three secrets and the
// Authorization header: as the workspace, the bot and a person, as the scopes of x-oauth-scopes, in ok:false answers
// of an unknown error, of invalid_auth and of missing_scope (needed), in an HTML 500 and in a 429; then behind a
// proxy that accepts the password, which every request of the check must cross, also with the stand-in answering only
// after the check stopped waiting; then against a listener that drops the connection; then in http mode. After each
// check the Destination check of a channel (conversations.info) and the channel list (conversations.list, whose
// cursors echo the secrets too) run against the same answers, the channel echoing them as its name and, once, without
// the bot; behind the accepting proxy every one of their requests must cross it (C-24.AC-3). Creations of a bot token
// and an app-level token without their prefixes, of http mode without the Slack signing secret and of socket mode
// without the app-level token are refused; muster doctor checks the Connection and its Destination; it is deleted;
// and a client is refused its proxy and its route. Between them the Slack adapter of its Destination publishes, edits,
// replies and checks against the same answers (probeSlackAdapter). Neither the log nor any error, step message, check
// item, channel or what the check found and the Connection shows may carry a secret.
func probeSlack(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
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
	botToken, appToken, signing := "xoxb-"+secrets[0], "xapp-"+secrets[1], secrets[2]
	proxyPassword := secrets[2] + "-proxy"
	var answer atomic.Int32
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			echo := auth + " " + botToken + " " + appToken + " " + signing + " " + proxyPassword
			a := answer.Load()
			if a == slackLate {
				// The answer comes once the check stopped waiting and closed the connection.
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
				a = slackEcho
			}
			slackAnswer(w, r, a, echo)
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	cut, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	cutter := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			if c, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = c.Close()
			}
		})}
	go func() { _ = cutter.Serve(cut) }()
	defer func() { _ = cutter.Close() }()
	refusing, err := fakeproxy.Start(ctx, fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "other"})
	if err != nil {
		return err
	}
	defer func() { _ = refusing.Close() }()
	accepting, err := fakeproxy.Start(ctx, fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: proxyPassword})
	if err != nil {
		return err
	}
	defer func() { _ = accepting.Close() }()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	network := mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}}
	var base atomic.Pointer[string]
	standIn := "http://" + ln.Addr().String() + "/api/"
	base.Store(&standIn)
	store := &probeConnections{}
	svc := connections.New(connections.Config{OrgID: 1, Store: store, Keyring: k,
		Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks, Network: network,
		SlackRoute:  func(logging.Secret) string { return *base.Load() },
		Interactive: deliverytest.Unlimited(1, clocks), IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081"},
		Log: logger})
	by := connections.Requester{Actor: audit.System, Transport: audit.TransportUI}
	proxied := func(p *fakeproxy.Server) proxyconf.Input {
		return proxyconf.Input{Enabled: true, Type: new("http"), Address: new(p.Addr()), UsernameSet: true,
			Username: new("muster"), Password: keyring.Replace(logging.Secret(proxyPassword))}
	}
	in := connections.Input{Type: connections.TypeSlack, Name: "probe", EventMode: connections.EventModeHTTP,
		BotToken: keyring.Replace(logging.Secret(botToken)), AppToken: keyring.Replace(logging.Secret(appToken)),
		SlackSigningSecret: keyring.Replace(logging.Secret(signing)), Proxy: proxied(refusing),
		Limiter: connections.Limiter{Limit: 50, PerSeconds: 60}}
	var errs []error
	shown := func(c connections.Connection, err error) {
		errs = append(errs, err)
		for _, s := range []*string{c.BotUsername, c.BotUserID, c.SlackTeamID, c.SlackTeamName, c.SlackAppID} {
			if s != nil {
				errs = append(errs, errors.New(*s))
			}
		}
	}
	check := func(ctx context.Context) connections.CheckResult {
		res, err := svc.Check(ctx, store.row.PublicID, nil)
		errs = append(errs, err, errors.New(res.BotName))
		for _, st := range res.Steps {
			errs = append(errs, errors.New(st.Message), errors.New(st.Code), errors.New(st.Via))
		}
		if id := res.Slack; id != nil {
			errs = append(errs, errors.New(id.TeamID+" "+id.TeamName+" "+id.BotUserID+" "+id.AppID))
		}
		shown(svc.Get(ctx, store.row.PublicID))
		return res
	}
	// dcheck is the Destination check of the stand-in's channel, and chans the channel list.
	dcheck := func(ctx context.Context) slack.ChannelCheck {
		res, err := svc.CheckSlackChannel(ctx, destinations.SlackChannelCheck{Connection: store.row.PublicID,
			ChannelID: "C0PROBE01"})
		c := res.Check
		errs = append(errs, err, errors.New(c.Name+" "+c.Message+" "+c.Code+" "+strings.Join(c.MissingScopes, ",")),
			errors.New(string(c.Outcome.Error)))
		return c
	}
	chans := func(ctx context.Context) slack.ChannelList {
		list, err := svc.SlackChannels(ctx, store.row.PublicID, "")
		errs = append(errs, err)
		for _, ch := range list.Channels {
			errs = append(errs, errors.New(ch.ID+" "+ch.Name))
		}
		return list
	}
	c, err := svc.Create(ctx, by, in)
	if err != nil {
		return err
	}
	if res := check(ctx); res.OK || refusing.Refused() == 0 {
		errs = append(errs, &probeIncomplete{what: "the check did not meet the proxy that refuses the password"})
	}
	in.EventMode, in.Proxy = connections.EventModeSocket, proxyconf.Input{Enabled: false}
	in.BotToken, in.AppToken, in.SlackSigningSecret = keyring.Keep, keyring.Keep, keyring.Keep
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	for _, a := range []int32{slackOK, slackEcho, slackUser, slackScopesEcho, slackMismatch, slackInvalidAuth,
		slackError, slackMissingScope, slackHTML500, slackRateLimited, slackNotMember} {
		answer.Store(a)
		res := check(ctx)
		if a == slackOK && (!res.OK || len(res.Steps) != 3 || !store.row.SlackTeamID.Valid) {
			errs = append(errs, &probeIncomplete{what: "the check against the stand-in Slack did not pass"})
		}
		dc, list := dcheck(ctx), chans(ctx)
		switch {
		case a == slackOK && (!dc.OK() || len(list.Channels) != 1):
			errs = append(errs, &probeIncomplete{what: "the channel check or list against the stand-in Slack failed"})
		case a == slackEcho && !list.Truncated:
			errs = append(errs, &probeIncomplete{what: "the channel list did not send the echoed cursor back"})
		case a == slackMissingScope && len(dc.MissingScopes) == 0:
			errs = append(errs, &probeIncomplete{what: "the channel check named no missing scope"})
		case a == slackNotMember && dc.Code != slack.CodeBotNotInChannel:
			errs = append(errs, &probeIncomplete{what: "the channel check did not miss the bot"})
		}
	}
	errs = append(errs, probeSlackAdapter(ctx, svc, clocks, &answer))
	answer.Store(slackOK)
	in.Proxy = proxied(accepting)
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	res := check(ctx)
	viaProxy := res.OK && len(accepting.Targets()) > 0
	for _, st := range res.Steps {
		viaProxy = viaProxy && (!st.Called() || st.Via == slack.ViaProxy)
	}
	if !viaProxy {
		errs = append(errs, &probeIncomplete{what: "the check did not reach the stand-in Slack through the proxy"})
	}
	crossed := len(accepting.Targets())
	if dc, list := dcheck(ctx), chans(ctx); !dc.OK() || len(list.Channels) != 1 ||
		len(accepting.Targets()) != crossed+2 {
		errs = append(errs, &probeIncomplete{
			what: "the channel check and list did not reach the stand-in Slack through the proxy"})
	}
	answer.Store(slackLate)
	late, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	if res := check(late); res.OK {
		errs = append(errs, &probeIncomplete{what: "the check waited for the late answer"})
	}
	if dc, list := dcheck(late), chans(late); dc.OK() || len(list.Channels) != 0 {
		errs = append(errs, &probeIncomplete{what: "the channel check or list waited for the late answer"})
	}
	cancel()
	answer.Store(slackEcho)
	cutBase := "http://" + cut.Addr().String() + "/api/"
	base.Store(&cutBase)
	check(ctx)
	dcheck(ctx)
	chans(ctx)
	base.Store(&standIn)
	in.EventMode, in.Proxy = connections.EventModeHTTP, proxyconf.Input{Enabled: false}
	_, err = svc.Update(ctx, by, c.PublicID, nil, in)
	errs = append(errs, err)
	check(ctx)
	dcheck(ctx)
	chans(ctx)
	refused := []connections.Input{in, in, in, in}
	refused[0].BotToken = keyring.Replace(logging.Secret("x" + secrets[0]))
	refused[1].EventMode, refused[1].BotToken = connections.EventModeSocket, keyring.Replace(logging.Secret(botToken))
	refused[1].AppToken = keyring.Replace(logging.Secret("bad-" + secrets[1]))
	refused[2].BotToken, refused[2].AppToken = keyring.Replace(logging.Secret(botToken)),
		keyring.Replace(logging.Secret(appToken))
	refused[3].EventMode, refused[3].BotToken = connections.EventModeSocket, keyring.Replace(logging.Secret(botToken))
	refused[3].SlackSigningSecret = keyring.Replace(logging.Secret(signing))
	for _, r := range refused {
		r.Name = "refused"
		_, err := svc.Create(ctx, by, r)
		if err == nil {
			errs = append(errs, &probeIncomplete{what: "a Slack Connection without a secret it needs was created"})
		}
		errs = append(errs, err)
	}
	for _, a := range []int32{slackOK, slackNotMember, slackEcho, slackMissingScope} {
		answer.Store(a)
		found, err := svc.Doctor(ctx, store, 2*time.Second)
		errs = append(errs, err)
		checked := 0
		for _, f := range found {
			errs = append(errs, errors.New(f.Name+" "+f.Message+" "+f.Warning))
			if f.OK() && (f.Kind == connections.FindingConnection || f.Name == "probe-slack") {
				checked++
			}
		}
		if a == slackOK && checked != 2 {
			errs = append(errs, &probeIncomplete{what: "muster doctor did not check the Connection and its Destination"})
		}
	}
	answer.Store(slackOK)
	errs = append(errs, svc.Delete(ctx, by, c.PublicID, nil))
	_, err = slack.NewClient(slack.Network(network), slack.Settings{BotToken: logging.Secret(botToken),
		AppToken: logging.Secret(appToken), SigningSecret: logging.Secret(signing), Proxy: &outbound.Proxy{Type: "ftp",
			Address: "proxy:1", Username: "muster", Password: logging.Secret(proxyPassword)}})
	errs = append(errs, err)
	_, err = slack.NewClient(slack.Network(network), slack.Settings{BotToken: logging.Secret(botToken),
		Route: func(token logging.Secret) string { return "http://muster:" + string(token) + "@127.0.0.1:1/api/" }})
	return errors.Join(append(errs, err)...)
}

// probeSlackAdapter publishes, edits and replies through the Slack adapter to the Destination 3 of probeSlack's
// Connection on the interactive path, as lint 3 allows, checks it there and in the delivery class as the Broken probe
// does, and runs its test message and its preview, against each answer of the stand-in Slack that answer selects: the
// echoes, refusals, a 500 and a 429 of probeSlack, an edit answered message_not_found whose read of the channel
// echoes the secrets (the deleted Root message), and a reply answered without thread_ts whose chat.delete is refused
// with the secrets (the misplaced reply). It returns the text of every outcome and step.
func probeSlackAdapter(ctx context.Context, svc *connections.Service, clocks clock.Clocks, answer *atomic.Int32) error {
	a := &slack.Adapter{Targets: svc, Version: "probe"}
	conn := int64(1)
	dest := delivery.Destination{ID: 3, PublicID: "DSAAAAAAAAAAA3", Type: delivery.TypeSlack, Connection: &conn}
	m := delivery.Message{Kind: messages.KindRoot, Language: "en", Colour: "firing", Heading: &messages.Heading{
		Number: 1, Title: "probe", URL: "http://localhost:8080/alert-groups/AGAAAAAAAAAAA1"}}
	path := deliverytest.Unlimited(1, clocks)
	var errs []error
	failed := map[string]bool{}
	for _, ans := range []int32{slackOK, slackEcho, slackInvalidAuth, slackError, slackHTML500, slackRateLimited,
		slackGone, slackMisplaced} {
		answer.Store(ans)
		outs := map[string]delivery.Outcome{}
		for name, op := range map[string]delivery.Op{"publish": delivery.PublishOp(a, m),
			"update": delivery.UpdateOp(a, "1.000001", m), "reply": delivery.ReplyOp(a, delivery.Root{
				MessageID: "1.000001"}, m), "check": delivery.CheckOp(a)} {
			o, err := path.Do(ctx, delivery.Subject{Destination: &dest}, op)
			errs = append(errs, err, errors.New(string(o.Error)+" "+o.MessageURL))
			outs[name] = o
			if o.Kind != delivery.OutcomeOK {
				failed[name] = true
			}
		}
		o := a.Check(ctx, delivery.Call{Class: outbound.ClassDelivery, Destination: dest})
		errs = append(errs, errors.New(string(o.Error)))
		errs = append(errs, probeTester(ctx, &slack.Tester{Adapter: a, Path: path, Renderer: probeRenderer(clocks),
			Real: clocks.Real}, dest))
		switch {
		case ans == slackOK && (outs["publish"].Kind != delivery.OutcomeOK || outs["update"].Kind != delivery.OutcomeOK ||
			outs["reply"].Kind != delivery.OutcomeOK):
			errs = append(errs, &probeIncomplete{what: "the Slack adapter did not deliver to the stand-in Slack"})
		case ans == slackGone && outs["update"].Kind != delivery.OutcomeGone:
			errs = append(errs, &probeIncomplete{what: "the Slack adapter did not find the deleted Root message"})
		case ans == slackMisplaced && (outs["reply"].Kind != delivery.OutcomeGone ||
			!strings.Contains(string(outs["reply"].Error), "could not be deleted")):
			errs = append(errs, &probeIncomplete{what: "the Slack adapter did not try to delete the misplaced reply"})
		}
	}
	if !failed["publish"] || !failed["update"] {
		errs = append(errs, &probeIncomplete{what: "no Publication and no edit through the Slack adapter failed"})
	}
	answer.Store(slackOK)
	return errors.Join(errs...)
}

// slackAnswer is the answer a of probeSlack's stand-in Slack to r, by its method and its token; echo, which carries the
// secrets, goes where the answer names something.
func slackAnswer(w http.ResponseWriter, r *http.Request, a int32, echo string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	scopes := strings.Join(slack.RequiredScopes, ",")
	if a == slackScopesEcho {
		scopes = echo
	}
	w.Header().Set("X-OAuth-Scopes", scopes)
	name := "probe"
	if a == slackEcho || a == slackUser || a == slackNotMember || a == slackGone || a == slackMisplaced {
		name = echo
	}
	var sent struct {
		ThreadTS string `json:"thread_ts"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&sent)
	}
	cursor := ""
	if a == slackEcho {
		cursor = echo
	}
	appID := "A0PROBE1"
	if a == slackMismatch {
		appID = "A0PROBE2"
	}
	var body map[string]any
	switch method := strings.TrimPrefix(r.URL.Path, "/api/"); {
	case a == slackInvalidAuth:
		body = map[string]any{"ok": false, "error": "invalid_auth", "warning": echo,
			"response_metadata": map[string]any{"warnings": []string{echo}, "messages": []string{echo}}}
	case a == slackError:
		body = map[string]any{"ok": false, "error": "invalid_auth " + echo, "needed": echo, "warning": echo}
	case a == slackMissingScope:
		body = map[string]any{"ok": false, "error": "missing_scope", "needed": echo, "provided": echo}
	case a == slackHTML500:
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "<html><body>"+echo+"</body></html>") //nolint:gosec // G705: a stand-in server that echoes the secrets on purpose
		return
	case a == slackRateLimited:
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		body = map[string]any{"ok": false, "error": "ratelimited", "warning": echo}
	case method == "auth.test" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer xapp-"):
		body = map[string]any{"ok": true, "app_id": appID}
	case method == "auth.test":
		body = map[string]any{"ok": true, "url": "https://probe.slack.com/", "team": name, "user": name,
			"team_id": "T0PROBE1", "user_id": "U0PROBE1"}
		if a != slackUser {
			body["bot_id"] = "B0PROBE1"
		}
	case method == "bots.info":
		body = map[string]any{"ok": true, "bot": map[string]any{"id": "B0PROBE1", "app_id": "A0PROBE1",
			"user_id": "U0PROBE1", "name": name}}
	case method == "conversations.info":
		body = map[string]any{"ok": true, "channel": map[string]any{"id": "C0PROBE01", "name": name,
			"is_channel": true, "is_private": false, "is_archived": false, "is_member": a != slackNotMember}}
	case method == "conversations.list":
		body = map[string]any{"ok": true, "channels": []map[string]any{{"id": "C0PROBE01", "name": name,
			"is_member": true, "is_archived": false, "is_private": false}},
			"response_metadata": map[string]any{"next_cursor": cursor}}
	case method == "chat.update" && a == slackGone:
		body = map[string]any{"ok": false, "error": "message_not_found", "warning": echo}
	case method == "chat.delete" && a == slackMisplaced:
		body = map[string]any{"ok": false, "error": "cant_delete_message " + echo, "warning": echo}
	case method == "chat.postMessage", method == "chat.update":
		message := map[string]any{"type": "message", "ts": "1.000002", "text": name, "user": "U0PROBE1"}
		if sent.ThreadTS != "" && a != slackMisplaced {
			message["thread_ts"] = sent.ThreadTS
		}
		body = map[string]any{"ok": true, "channel": "C0PROBE01", "ts": "1.000002", "message": message,
			"warning": echo}
	case method == "chat.delete":
		body = map[string]any{"ok": true, "channel": "C0PROBE01", "ts": "1.000002"}
	default:
		body = map[string]any{"ok": false, "error": "unknown_method", "warning": echo}
	}
	_ = json.NewEncoder(w).Encode(body)
}

// probeConnections is the one Connection of probeMattermost in memory, with the cursor of its Bridge events and the
// ids of the events it handled.
type probeConnections struct {
	connections.Queries
	row    *cdb.GetConnectionRow
	cursor *string
	seen   map[string]bool
}

func (s *probeConnections) InTx(_ context.Context, f func(connections.Queries) error) error {
	return f(s)
}

func (s *probeConnections) InsertConnection(_ context.Context, a cdb.InsertConnectionParams) (int64, error) {
	s.row = &cdb.GetConnectionRow{ID: 1, PublicID: a.PublicID, Type: a.Type, Name: a.Name,
		MattermostServerUrl: a.ServerUrl, TelegramBotApiBaseUrl: a.BotApiBaseUrl, TelegramUpdateMode: a.UpdateMode,
		TelegramWebhookSecretCiphertext: a.WebhookSecretCiphertext, TelegramWebhookSecretKeyID: a.WebhookSecretKeyID,
		BridgeBaseUrl: a.BridgeBaseUrl, BridgeEventDelivery: a.EventDelivery,
		BridgePushSecretCiphertext: a.PushSecretCiphertext, BridgePushSecretKeyID: a.PushSecretKeyID,
		BridgePushSecretUpdatedAt: a.PushSecretUpdatedAt, SlackEventMode: a.SlackEventMode,
		SlackAppTokenCiphertext: a.SlackAppTokenCiphertext, SlackAppTokenKeyID: a.SlackAppTokenKeyID,
		SlackAppTokenUpdatedAt: a.SlackAppTokenUpdatedAt, SlackSigningSecretCiphertext: a.SlackSigningSecretCiphertext,
		SlackSigningSecretKeyID: a.SlackSigningSecretKeyID, SlackSigningSecretUpdatedAt: a.SlackSigningSecretUpdatedAt,
		BotTokenCiphertext: a.BotTokenCiphertext, BotTokenKeyID: a.BotTokenKeyID, BotTokenUpdatedAt: a.BotTokenUpdatedAt,
		Proxy: a.Proxy, ProxyPasswordCiphertext: a.ProxyPasswordCiphertext, ProxyPasswordKeyID: a.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: a.ProxyPasswordUpdatedAt, LimiterLimit: a.LimiterLimit,
		LimiterPerSeconds: a.LimiterPerSeconds, CreatedAt: a.Now, Version: 1}
	return 1, nil
}

// GetConnection is the Connection, read by its id or its public_id; another public_id is none.
func (s *probeConnections) GetConnection(_ context.Context, a cdb.GetConnectionParams) (cdb.GetConnectionRow,
	error) {
	if s.row == nil || (a.PublicID.Valid && a.PublicID.String != s.row.PublicID) {
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
	s.row.BridgeBaseUrl, s.row.BridgeEventDelivery = a.BridgeBaseUrl, a.EventDelivery
	s.row.BridgePushSecretCiphertext, s.row.BridgePushSecretKeyID, s.row.BridgePushSecretUpdatedAt =
		a.PushSecretCiphertext, a.PushSecretKeyID, a.PushSecretUpdatedAt
	s.row.SlackEventMode = a.SlackEventMode
	s.row.SlackAppTokenCiphertext, s.row.SlackAppTokenKeyID, s.row.SlackAppTokenUpdatedAt = a.SlackAppTokenCiphertext,
		a.SlackAppTokenKeyID, a.SlackAppTokenUpdatedAt
	s.row.SlackSigningSecretCiphertext, s.row.SlackSigningSecretKeyID, s.row.SlackSigningSecretUpdatedAt =
		a.SlackSigningSecretCiphertext, a.SlackSigningSecretKeyID, a.SlackSigningSecretUpdatedAt
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

// GetSlackDestinationTarget is the Destination 3 of the Connection, on the stand-in's channel C0PROBE01, with the
// workspace address and the bot's name its last passing check stored.
func (s *probeConnections) GetSlackDestinationTarget(context.Context, cdb.GetSlackDestinationTargetParams) (
	cdb.GetSlackDestinationTargetRow, error) {
	if s.row == nil {
		return cdb.GetSlackDestinationTargetRow{}, pgx.ErrNoRows
	}
	r := s.row
	return cdb.GetSlackDestinationTargetRow{SlackChannelID: pgtype.Text{String: "C0PROBE01", Valid: true}, ID: r.ID,
		PublicID: r.PublicID, Type: r.Type, Name: r.Name, BotTokenCiphertext: r.BotTokenCiphertext,
		BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt,
		SlackAppTokenCiphertext: r.SlackAppTokenCiphertext, SlackAppTokenKeyID: r.SlackAppTokenKeyID,
		SlackAppTokenUpdatedAt: r.SlackAppTokenUpdatedAt, SlackSigningSecretCiphertext: r.SlackSigningSecretCiphertext,
		SlackSigningSecretKeyID: r.SlackSigningSecretKeyID, SlackSigningSecretUpdatedAt: r.SlackSigningSecretUpdatedAt,
		Proxy: r.Proxy, ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version, SlackTeamUrl: r.SlackTeamUrl,
		BotUsername: r.BotUsername}, nil
}

// GetBridgeDestinationTarget is the Destination 2 of the Connection, on the channel probe.
func (s *probeConnections) GetBridgeDestinationTarget(context.Context, cdb.GetBridgeDestinationTargetParams) (
	cdb.GetBridgeDestinationTargetRow, error) {
	if s.row == nil {
		return cdb.GetBridgeDestinationTargetRow{}, pgx.ErrNoRows
	}
	r := s.row
	return cdb.GetBridgeDestinationTargetRow{BridgeChannelID: pgtype.Text{String: "probe", Valid: true}, ID: r.ID,
		PublicID: r.PublicID, Type: r.Type, Name: r.Name, BridgeBaseUrl: r.BridgeBaseUrl,
		BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt,
		BridgePushSecretCiphertext: r.BridgePushSecretCiphertext, BridgePushSecretKeyID: r.BridgePushSecretKeyID,
		BridgePushSecretUpdatedAt: r.BridgePushSecretUpdatedAt, Proxy: r.Proxy,
		ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version}, nil
}

// ListBridgeDestinations lists the Destination 2 of the Connection on the channel probe for muster doctor.
func (s *probeConnections) ListBridgeDestinations(context.Context, int64) ([]cdb.ListBridgeDestinationsRow, error) {
	return []cdb.ListBridgeDestinationsRow{{ID: 2, PublicID: "DSAAAAAAAAAAA2", Name: "probe-bridge",
		ConnectionID: pgtype.Int8{Int64: 1, Valid: true}, BridgeChannelID: pgtype.Text{String: "probe",
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

// ListSlackDestinations names the one Slack Destination of probeSlack, on the stand-in's channel, so that muster
// doctor runs its Destination check.
func (s *probeConnections) ListSlackDestinations(context.Context, int64) ([]cdb.ListSlackDestinationsRow, error) {
	return []cdb.ListSlackDestinationsRow{{ID: 3, PublicID: "DSAAAAAAAAAAA3", Name: "probe-slack",
		ConnectionID: pgtype.Int8{Int64: 1, Valid: true}, SlackChannelID: pgtype.Text{String: "C0PROBE01",
			Valid: true}}}, nil
}

func (s *probeConnections) SetBotIdentity(context.Context, cdb.SetBotIdentityParams) error {
	return nil
}

// SetSlackIdentity records what a passing check of the Slack Connection found.
func (s *probeConnections) SetSlackIdentity(_ context.Context, a cdb.SetSlackIdentityParams) (int64, error) {
	s.row.SlackTeamID, s.row.SlackTeamName, s.row.SlackTeamUrl, s.row.SlackBotID = a.TeamID, a.TeamName, a.TeamUrl,
		a.BotID
	s.row.SlackAppID, s.row.BotUserID, s.row.BotUsername = a.AppID, a.BotUserID, a.BotUsername
	return 1, nil
}

// SetBridgeInfo records the info that a check of the Bridge Connection read.
func (s *probeConnections) SetBridgeInfo(_ context.Context, a cdb.SetBridgeInfoParams) (int64, error) {
	s.row.BridgeInfo, s.row.BridgeInfoReadAt = a.Info, pgtype.Timestamptz{Time: a.ReadAt, Valid: true}
	return 1, nil
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

// ListBridgePollingConnections is the Connection when it is a Bridge one in poll mode.
func (s *probeConnections) ListBridgePollingConnections(context.Context, int64) (
	[]cdb.ListBridgePollingConnectionsRow, error) {
	r := s.row
	if r == nil || r.Type != connections.TypeBridge || r.BridgeEventDelivery.String != connections.EventDeliveryPoll {
		return nil, nil
	}
	row := cdb.ListBridgePollingConnectionsRow{ID: r.ID, PublicID: r.PublicID, BridgeBaseUrl: r.BridgeBaseUrl,
		BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt,
		Proxy: r.Proxy, ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version}
	if s.cursor != nil {
		row.BridgeEventsCursor = pgtype.Text{String: *s.cursor, Valid: true}
	}
	return []cdb.ListBridgePollingConnectionsRow{row}, nil
}

func (s *probeConnections) StoreBridgeEventsCursor(_ context.Context, a cdb.StoreBridgeEventsCursorParams) (int64,
	error) {
	s.cursor = nil
	if a.Cursor.Valid {
		s.cursor = &a.Cursor.String
	}
	return 1, nil
}

func (s *probeConnections) LockBridgeEvent(context.Context, cdb.LockBridgeEventParams) error {
	return nil
}

func (s *probeConnections) BridgeEventSeen(_ context.Context, a cdb.BridgeEventSeenParams) (bool, error) {
	return s.seen[a.EventID], nil
}

func (s *probeConnections) RecordBridgeEvent(_ context.Context, a cdb.RecordBridgeEventParams) error {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.seen[a.EventID] = true
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
	if incomplete, ok := errors.AsType[*probeIncomplete](err); ok {
		diags = append(diags, Diagnostic{Rule: 5, Path: p.Name, Message: "the probe did not run its code path: " +
			incomplete.what})
	}
	if leak, ok := errors.AsType[*probeLeak](err); ok {
		for _, what := range leak.what {
			diags = append(diags, Diagnostic{Rule: 5, Path: p.Name, Message: what})
		}
	}
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

// probeIncomplete is the error of a probe whose code path did not run as the probe needs it to, for example an
// activation that should have committed and did not; CheckProbe reports it, so a probe cannot pass by running nothing.
// what never carries a secret.
type probeIncomplete struct{ what string }

func (e *probeIncomplete) Error() string { return "probe incomplete: " + e.what }

// probeLeak is the error of a probe that found, itself, values that are not planted secrets — signed action ids,
// tokens and codes the code under test made, the values of credential headers — where they must not be; each what
// names one value and where it went, never the value, and CheckProbe reports each.
type probeLeak struct{ what []string }

func (e *probeLeak) Error() string { return "leak: " + strings.Join(e.what, "; ") }

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
	// The template mode (C-15.FR-3, FR-7): the requests read the Secrets in their URLs, headers and bodies; the
	// stand-in echoes them, a redirect carries them, and templates fail on them or on a value that is missing.
	body := func(s string) *string { return &s }
	store.row.WebhookMode, store.row.Proxy = pgtype.Text{String: webhooks.ModeBoth, Valid: true},
		[]byte(`{"enabled":false}`)
	auth := []webhooks.Header{{Name: "Authorization", Value: "Bearer {{ .Secrets.other }}"}}
	for _, c := range []webhooks.TemplateConfig{
		{Create: webhooks.RequestTemplate{Method: http.MethodPost, URL: base + "/hook?k={{ .Secrets.token }}",
			Headers: auth, Body: body(`{"k":"{{ .Secrets.token }}"}`)},
			Update: webhooks.RequestTemplate{Method: http.MethodPut, URL: base + "/redirect?k={{ .Secrets.token }}",
				Headers: auth},
			OpenThread: &webhooks.RequestTemplate{Method: http.MethodPost,
				URL: base + "/hook/{{ .Secrets.other }}{{ .Response.missing }}"},
			ReplyInThread: &webhooks.RequestTemplate{Method: http.MethodPost, URL: base + "/hook"}},
		{Create: webhooks.RequestTemplate{Method: http.MethodPost, URL: base + "/hook",
			Body: body(`{{ printf "%d" .Secrets.token }}{{ len 3 }}`)},
			Update: webhooks.RequestTemplate{Method: http.MethodPut, URL: base + "/hook",
				Headers: []webhooks.Header{{Name: "X", Value: "{{ .Secrets.other }}{{ index .Secrets .Secrets.token }}"}}},
			ReplyInThread: &webhooks.RequestTemplate{Method: http.MethodPost,
				URL: base + "/hook/{{ .Secrets.token }}/{{ .Secrets.nope }}"}},
	} {
		store.row.WebhookTemplateConfig = c.JSON()
		state, _ := json.Marshal(delivery.RequestState{Group: &templates.Data{AlertGroup: templates.AlertGroup{
			Number: 1}}})
		w := &delivery.WebhookCall{State: state, Response: map[string]string{"id": "m1"}}
		dest := delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: webhooks.TypeWebhook}
		path := deliverytest.Unlimited(1, clock.Clocks{Business: clock.NewManual(now), Real: clock.Real{}})
		for _, op := range []delivery.Op{delivery.PublishOp(a, delivery.Message{}),
			delivery.UpdateOp(a, "m1", delivery.Message{}), delivery.ReplyOp(a, delivery.Root{}, delivery.Message{})} {
			out, err := path.Do(ctx, delivery.Subject{Destination: &dest}, delivery.WebhookOp(op, w))
			errs = append(errs, err, errors.New(string(out.Kind)+": "+string(out.Error)))
		}
		// A Destination test and a preview in the mode both (C-16.FR-2, C-15.FR-10): the test event and "create"
		// against the stand-in that echoes the secrets, and every request rendered.
		cfg, _ := json.Marshal(webhooks.EventsConfig{URL: base + "/hook?k={{ .Secrets.token }}",
			Headers: []webhooks.Header{{Name: "Authorization", Value: "Bearer {{ .Secrets.other }}"}}})
		store.row.WebhookEventsConfig = cfg
		clocks := clock.Clocks{Business: clock.NewManual(now), Real: clock.Real{}}
		errs = append(errs, probeTester(ctx, &webhooks.Tester{Adapter: a, Path: path, Data: probeRenderer(clocks),
			Budget: time.Second}, dest))
	}
	return errors.Join(errs...)
}

// probeRenderer is the renderer of the messages of the probes, without a database.
func probeRenderer(clocks clock.Clocks) *messages.Renderer {
	return messages.New(messages.Config{OrgID: 1, PublicURL: "http://localhost:8080", Business: clocks.Business,
		Real: clocks.Real, Log: logging.New(io.Discard, logging.LevelInfo)})
}

// destinationTester is the test and the preview of a Destination type.
type destinationTester interface {
	Test(ctx context.Context, in delivery.TestInput) ([]delivery.TestStep, error)
	Preview(ctx context.Context, in delivery.TestInput) ([]delivery.PreviewItem, error)
}

// probeTester runs the test and the preview of dest with an Alert Group of one firing Alert, and returns everything
// they show, as JSON, and their errors.
func probeTester(ctx context.Context, t destinationTester, dest delivery.Destination) error {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	in := delivery.TestInput{Destination: dest, Actor: groups.System, Source: &messages.Source{Number: 1,
		Title: "probe", Status: messages.ColourFiring, TimeZone: "UTC", StartedAt: now,
		Route: messages.RouteRef{Name: "probe", Language: "en", SnoozeDurations: []int64{3600}}, TotalAlerts: 1,
		Alerts: []messages.SourceAlert{{Fingerprint: "f1", Labels: map[string]string{"alertname": "Probe"},
			StartsAt: now, Firing: true}}}}
	steps, err := t.Test(ctx, in)
	shown, _ := json.Marshal(steps)
	items, perr := t.Preview(ctx, in)
	previewed, _ := json.Marshal(items)
	return errors.Join(err, perr, errors.New(string(shown)), errors.New(string(previewed)))
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

// probeAccountLinks pushes the secrets through Account links (C-18, ADR-0013): the first as the random bytes of a
// Telegram deep-link token, which the deep link carries in base64url; the second as the token of a `/start` to the
// bot's handler of private messages, as are the deep link's own tokens — used, used again and expired; the third as the
// code that confirms a Mattermost link through the API, wrong until its request is void. The stand-in servers refuse
// the bot's replies and its first direct message with the request in their errors. Neither the log, the errors nor the
// API's answers may carry them; the deep-link tokens and codes that come from crypto/rand are not planted secrets, so
// the probe looks for them itself and reports a leak as the first secret.
func probeAccountLinks(ctx context.Context, secrets []string, log io.Writer) error {
	var seen bytes.Buffer
	logger := logging.New(io.MultiWriter(log, &seen), logging.LevelInfo)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	business := clock.NewManual(now)
	clocks := clock.Clocks{Business: business, Real: clock.NewManual(now)}
	dm := &probeDirect{}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: dm}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	base := "http://" + ln.Addr().String()
	tg, err := telegram.NewClient(telegram.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}},
		telegram.Settings{BaseURL: base, Token: "123456:probe"})
	if err != nil {
		return err
	}
	mm, err := mattermost.NewClient(mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger,
		Real: clock.Real{}}, mattermost.Settings{ServerURL: base, Token: "probe"})
	if err != nil {
		return err
	}
	path := deliverytest.Unlimited(1, clocks)
	tgConn := telegram.Conn{ID: 1, PublicID: "CNAAAAAAAAAAA1", Client: tg}
	random := append([]byte(secrets[0]), make([]byte, 32)...)[:32]
	links := accountlinks.New(accountlinks.Config{OrgID: 1, Store: &probeLinkStore{},
		Audit: audit.NewWriter(logger, business), Business: business, Log: logger,
		Telegram:   telegram.Bots{Conns: probeTGConn{tgConn}, Path: path},
		Mattermost: mattermost.LinkBots{Conns: &probeMMConn{conn: mattermost.Connection{ID: 2, PublicID: "CNAAAAAAAAAAA2", Client: mm}}, Path: path},
		Random:     io.MultiReader(bytes.NewReader(random), rand.Reader)})
	sess := auth.Session{ID: 1, PublicID: "SNAAAAAAAAAAA1", State: auth.StateActive,
		User: auth.Principal{ID: 1, PublicID: "SRAAAAAAAAAAA1", Name: "Bob", Role: auth.RoleResponder}}
	h, err := api.New(api.Config{Sessions: probeSessions{sess: sess}, AccountLinks: links, Log: logger,
		Real: clock.Real{}})
	if err != nil {
		return err
	}
	var answers []string
	send := func(path, body string) string {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		if err != nil {
			return err.Error()
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", auth.CookieName+"=probe")
		r.Header.Set(auth.CSRFHeader, "probe")
		rec := &probeRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		return rec.body.String()
	}
	// The deep link carries its token to the person, so its answer is left out of what must not carry a secret.
	var started []string
	for range 2 {
		var st struct {
			DeepLink string `json:"deep_link"`
		}
		_ = json.Unmarshal([]byte(send("/api/v1/me/account-links/telegram", `{"connection_id":"CNAAAAAAAAAAA1"}`)), &st)
		_, token, _ := strings.Cut(st.DeepLink, "?start=")
		started = append(started, token)
	}
	start := &telegram.Start{Links: links, Path: path}
	var errs []error
	for i, token := range []string{secrets[1], started[0], started[0], "", started[1]} {
		if token == "" {
			business.Advance(accountlinks.TelegramTokenTTL)
			continue
		}
		u, err := telegram.ParseUpdate(fmt.Appendf(nil, `{"update_id":%d,"message":{"message_id":1,"chat":{"id":7001,
			"type":"private"},"from":{"id":7001,"is_bot":false,"first_name":"A","username":"a"},"text":%q}}`, 200+i,
			"/start "+token))
		if err != nil {
			return err
		}
		errs = append(errs, start.Handle(ctx, tgConn, u))
	}
	const mmStart = "/api/v1/me/account-links/mattermost"
	answers = append(answers, send(mmStart, `{"connection_id":"CNAAAAAAAAAAA2","username":"bob"}`))
	dm.allow()
	var req struct {
		RequestID string `json:"request_id"`
	}
	for range 2 {
		a := send(mmStart, `{"connection_id":"CNAAAAAAAAAAA2","username":"bob"}`)
		answers = append(answers, a)
		_ = json.Unmarshal([]byte(a), &req)
		for _, code := range []string{secrets[2], secrets[2], secrets[2], secrets[2], secrets[2], dm.code()} {
			answers = append(answers, send(mmStart+"/confirmation", `{"request_id":"`+req.RequestID+`","code":"`+
				code+`"}`))
		}
	}
	a := send(mmStart, `{"connection_id":"CNAAAAAAAAAAA2","username":"bob"}`)
	_ = json.Unmarshal([]byte(a), &req)
	answers = append(answers, a, send(mmStart+"/confirmation", `{"request_id":"`+req.RequestID+`","code":"`+
		dm.code()+`"}`))
	errs = append(errs, fmt.Errorf("answers: %s", strings.Join(answers, " | ")))
	text := seen.String() + strings.Join(answers, "") + errors.Join(errs...).Error()
	refused := dm.refusedCodes()
	if len(refused) == 0 || slices.Contains(refused, "") {
		errs = append(errs, errors.New("the probe saw no code in a refused direct message: "+secrets[0]))
	}
	for _, s := range append(append(dm.codes(), refused...), started[1]) {
		if s != "" && strings.Contains(text, s) {
			errs = append(errs, errors.New("a link token or code leaked: "+secrets[0]))
		}
	}
	return errors.Join(errs...)
}

// probeDirect is the stand-in Telegram and Mattermost server of probeAccountLinks: the bots read their users, every
// reply to `/start` is refused with the request in its error, and so is every direct message until allow, its code
// kept among the refused ones; after allow a direct message is kept for its code.
type probeDirect struct {
	mu      sync.Mutex
	allowed bool
	sent    []string
	refused []string
}

// refusedCodes are the codes of the direct messages that were refused, which their errors echo.
func (p *probeDirect) refusedCodes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.refused)
}

func (p *probeDirect) allow() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowed = true
}

var probeCodeRe = regexp.MustCompile(`[2-9A-HJKMNP-Z]{8}`)

// codes are the codes of the direct messages, the last one last.
func (p *probeDirect) codes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, m := range p.sent {
		out = append(out, probeCodeRe.FindString(m))
	}
	return out
}

func (p *probeDirect) code() string {
	c := p.codes()
	if len(c) == 0 {
		return ""
	}
	return c[len(c)-1]
}

func (p *probeDirect) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	refuse := func(v map[string]any) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/getMe"):
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Probe","username":"probe_bot"}}`)
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		refuse(map[string]any{"ok": false, "error_code": 400, "description": "refused " + string(body)})
	case r.URL.Path == "/api/v4/users/username/bob":
		_, _ = io.WriteString(w, `{"id":"u-bob","username":"bob"}`)
	case r.URL.Path == "/api/v4/users/me":
		_, _ = io.WriteString(w, `{"id":"bot"}`)
	case r.URL.Path == "/api/v4/channels/direct":
		_, _ = io.WriteString(w, `{"id":"dm"}`)
	case r.URL.Path == "/api/v4/posts":
		p.mu.Lock()
		defer p.mu.Unlock()
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &m)
		if !p.allowed {
			p.refused = append(p.refused, probeCodeRe.FindString(m.Message))
			refuse(map[string]any{"id": "api.post.refused", "message": "refused " + string(body)})
			return
		}
		p.sent = append(p.sent, m.Message)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"p"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// probeSessions is the one web session of probeAccountLinks, whose cookie and CSRF token are "probe".
type probeSessions struct {
	api.Sessions
	sess auth.Session
}

func (s probeSessions) Authenticate(_ context.Context, cookie string) (auth.Session, error) {
	if cookie != "probe" {
		return auth.Session{}, auth.ErrUnauthenticated
	}
	return s.sess, nil
}

func (s probeSessions) Permissions(auth.Session) []auth.Permission { return []auth.Permission{} }

func (s probeSessions) CheckCSRF(_ auth.Session, header string) bool { return header == "probe" }

// probeTGConn is the Telegram Connection of probeAccountLinks.
type probeTGConn struct{ conn telegram.Conn }

func (p probeTGConn) TelegramConn(context.Context, string) (telegram.Conn, error) { return p.conn, nil }

// probeMMConn is the Mattermost Connection of probeAccountLinks.
type probeMMConn struct{ conn mattermost.Connection }

func (p *probeMMConn) Connection(context.Context, string) (mattermost.Connection, error) {
	return p.conn, nil
}

// probeLinkStore is the Account links of probeAccountLinks and probeBridgeLinks in memory: the User 1, the Telegram
// Connection 1 without a stored bot username and the Mattermost Connection 2, or the Bridge Connection bridge, the
// link requests and the links.
type probeLinkStore struct {
	accountlinks.Queries
	reqs   []probeLinkRequest
	bridge *alinkdb.GetLinkConnectionRow
}

type probeLinkRequest struct {
	row       alinkdb.LockLinkRequestRow
	publicID  string
	messenger string
	conn      int64
	method    pgtype.Text
	target    pgtype.Text
	username  pgtype.Text
	display   pgtype.Text
	created   time.Time
}

func (s *probeLinkStore) InTx(_ context.Context, f func(accountlinks.Queries) error) error {
	return f(s)
}

func (s *probeLinkStore) Savepoint(_ context.Context, f func(accountlinks.Queries) error) error {
	return f(s)
}

func (s *probeLinkStore) GetLinkConnection(_ context.Context, a alinkdb.GetLinkConnectionParams) (
	alinkdb.GetLinkConnectionRow, error) {
	if s.bridge != nil {
		if a.PublicID == s.bridge.PublicID {
			return *s.bridge, nil
		}
		return alinkdb.GetLinkConnectionRow{}, pgx.ErrNoRows
	}
	switch a.PublicID {
	case "CNAAAAAAAAAAA1":
		return alinkdb.GetLinkConnectionRow{ID: 1, PublicID: a.PublicID, Type: "telegram", Name: "tg"}, nil
	case "CNAAAAAAAAAAA2":
		return alinkdb.GetLinkConnectionRow{ID: 2, PublicID: a.PublicID, Type: "mattermost", Name: "mm"}, nil
	}
	return alinkdb.GetLinkConnectionRow{}, pgx.ErrNoRows
}

func (s *probeLinkStore) LockLinkUser(context.Context, alinkdb.LockLinkUserParams) (alinkdb.LockLinkUserRow, error) {
	return alinkdb.LockLinkUserRow{ID: 1, PublicID: "SRAAAAAAAAAAA1", Login: "bob", Name: "Bob", Status: "active"}, nil
}

func (s *probeLinkStore) InsertLinkRequest(_ context.Context, a alinkdb.InsertLinkRequestParams) (int64, error) {
	id := int64(len(s.reqs) + 1)
	s.reqs = append(s.reqs, probeLinkRequest{row: alinkdb.LockLinkRequestRow{ID: id, SecretHash: a.SecretHash,
		ExpiresAt: a.ExpiresAt}, publicID: a.PublicID, messenger: a.Messenger, conn: a.ConnectionID,
		method: a.Method, target: a.TargetExternalID, username: a.TargetUsername, display: a.TargetDisplayName,
		created: a.CreatedAt})
	return id, nil
}

func (s *probeLinkStore) req(id int64) *probeLinkRequest { return &s.reqs[id-1] }

func (s *probeLinkStore) GetTelegramRequestByHash(_ context.Context, a alinkdb.GetTelegramRequestByHashParams) (
	alinkdb.GetTelegramRequestByHashRow, error) {
	for _, r := range s.reqs {
		if r.messenger == "telegram" && bytes.Equal(r.row.SecretHash, a.SecretHash) {
			return alinkdb.GetTelegramRequestByHashRow{ID: r.row.ID, UserID: 1, ConnectionID: r.conn,
				UserPublicID: "SRAAAAAAAAAAA1", ConnectionPublicID: "CNAAAAAAAAAAA1"}, nil
		}
	}
	return alinkdb.GetTelegramRequestByHashRow{}, pgx.ErrNoRows
}

func (s *probeLinkStore) GetLinkRequest(_ context.Context, a alinkdb.GetLinkRequestParams) (alinkdb.GetLinkRequestRow,
	error) {
	for _, r := range s.reqs {
		if r.publicID != a.PublicID {
			continue
		}
		out := alinkdb.GetLinkRequestRow{ID: r.row.ID, UserID: 1, Messenger: r.messenger, ConnectionID: r.conn,
			Method: r.method, TargetExternalID: r.target, TargetUsername: r.username, TargetDisplayName: r.display,
			ConnectionPublicID: "CNAAAAAAAAAAA2", ConnectionName: "mm"}
		if s.bridge != nil {
			out.ConnectionPublicID, out.ConnectionName = s.bridge.PublicID, s.bridge.Name
		}
		return out, nil
	}
	return alinkdb.GetLinkRequestRow{}, pgx.ErrNoRows
}

func (s *probeLinkStore) GetBridgeTokenRequestByHash(_ context.Context, a alinkdb.GetBridgeTokenRequestByHashParams) (
	alinkdb.GetBridgeTokenRequestByHashRow, error) {
	for _, r := range s.reqs {
		if r.messenger == "bridge" && r.method.String == accountlinks.MethodToken &&
			bytes.Equal(r.row.SecretHash, a.SecretHash) {
			return alinkdb.GetBridgeTokenRequestByHashRow{ID: r.row.ID, UserID: 1, ConnectionID: r.conn,
				UserPublicID: "SRAAAAAAAAAAA1", ConnectionPublicID: s.bridge.PublicID}, nil
		}
	}
	return alinkdb.GetBridgeTokenRequestByHashRow{}, pgx.ErrNoRows
}

func (s *probeLinkStore) CountUserBridgeCodeRequests(context.Context, alinkdb.CountUserBridgeCodeRequestsParams) (
	alinkdb.CountUserBridgeCodeRequestsRow, error) {
	return alinkdb.CountUserBridgeCodeRequestsRow{}, nil
}

func (s *probeLinkStore) CountTargetBridgeCodeRequests(context.Context, alinkdb.CountTargetBridgeCodeRequestsParams) (
	alinkdb.CountTargetBridgeCodeRequestsRow, error) {
	return alinkdb.CountTargetBridgeCodeRequestsRow{}, nil
}

func (s *probeLinkStore) VoidPendingBridgeCodeRequests(_ context.Context,
	a alinkdb.VoidPendingBridgeCodeRequestsParams) (int64, error) {
	return s.void(func(r probeLinkRequest) bool {
		return r.messenger == "bridge" && r.method.String == accountlinks.MethodCode
	}, a.Now), nil
}

func (s *probeLinkStore) LockLinkRequest(_ context.Context, a alinkdb.LockLinkRequestParams) (
	alinkdb.LockLinkRequestRow, error) {
	return s.req(a.ID).row, nil
}

func (s *probeLinkStore) ConsumeLinkRequest(_ context.Context, a alinkdb.ConsumeLinkRequestParams) (int64, error) {
	s.req(a.ID).row.ConsumedAt = pgtype.Timestamptz{Time: a.Now, Valid: true}
	return 1, nil
}

func (s *probeLinkStore) RecordWrongCode(_ context.Context, a alinkdb.RecordWrongCodeParams) (int64, error) {
	r := s.req(a.ID)
	r.row.Attempts++
	if r.row.Attempts >= a.MaxAttempts {
		r.row.VoidedAt = pgtype.Timestamptz{Time: a.Now, Valid: true}
	}
	return r.row.Attempts, nil
}

func (s *probeLinkStore) void(match func(probeLinkRequest) bool, now time.Time) int64 {
	var n int64
	for i := range s.reqs {
		if r := &s.reqs[i]; match(*r) && !r.row.ConsumedAt.Valid && !r.row.VoidedAt.Valid {
			r.row.VoidedAt = pgtype.Timestamptz{Time: now, Valid: true}
			n++
		}
	}
	return n
}

func (s *probeLinkStore) VoidPendingMattermostRequests(_ context.Context,
	a alinkdb.VoidPendingMattermostRequestsParams) (int64, error) {
	return s.void(func(r probeLinkRequest) bool { return r.messenger == "mattermost" }, a.Now), nil
}

func (s *probeLinkStore) VoidLinkRequest(_ context.Context, a alinkdb.VoidLinkRequestParams) error {
	s.void(func(r probeLinkRequest) bool { return r.row.ID == a.ID }, a.Now)
	return nil
}

func (s *probeLinkStore) LockCodeTarget(context.Context, alinkdb.LockCodeTargetParams) error {
	return nil
}

func (s *probeLinkStore) UpdateAccountLink(context.Context, alinkdb.UpdateAccountLinkParams) error {
	return nil
}

func (s *probeLinkStore) CountUserCodeRequests(context.Context, alinkdb.CountUserCodeRequestsParams) (
	alinkdb.CountUserCodeRequestsRow, error) {
	return alinkdb.CountUserCodeRequestsRow{}, nil
}

func (s *probeLinkStore) CountTargetCodeRequests(context.Context, alinkdb.CountTargetCodeRequestsParams) (
	alinkdb.CountTargetCodeRequestsRow, error) {
	return alinkdb.CountTargetCodeRequestsRow{}, nil
}

func (s *probeLinkStore) GetAccountLinkBySpace(context.Context, alinkdb.GetAccountLinkBySpaceParams) (
	alinkdb.GetAccountLinkBySpaceRow, error) {
	return alinkdb.GetAccountLinkBySpaceRow{}, pgx.ErrNoRows
}

func (s *probeLinkStore) GetUserLinkInSpace(context.Context, alinkdb.GetUserLinkInSpaceParams) (
	alinkdb.GetUserLinkInSpaceRow, error) {
	return alinkdb.GetUserLinkInSpaceRow{}, pgx.ErrNoRows
}

func (s *probeLinkStore) InsertAccountLink(context.Context, alinkdb.InsertAccountLinkParams) (int64, error) {
	return 1, nil
}

func (s *probeLinkStore) InsertAuditEntry(context.Context, adb.InsertAuditEntryParams) error {
	return nil
}

func (s *probeLinkStore) Notify(context.Context, db.Hint) error { return nil }

// probeBridgeLinks pushes the secrets through Account links of a Bridge Connection (C-22.FR-15): the first as the
// Connection's bot token and the random bytes of the first link token, which the token carries in base64url; the second
// as the random bytes of a token no one started; the third as the code that confirms a link through the API, wrong
// until its request is void. Through the API two tokens are started and code requests made, then confirmed; through
// account_link events the first token links, is sent again, the second expires, and the unknown token is refused. The
// stand-in bridge refuses its first findUsers with the query and the Authorization header in its problem, its first
// two direct messages with their bodies, which hold the codes, and every answer to an event with the request, the
// Authorization header and the token of the event it answers, as a bridge may repeat what it was sent. Neither the log, the errors nor the API's answers, apart
// from those that start a token, may carry them; the tokens and codes that come from crypto/rand are not planted
// secrets, so the probe looks for them itself and reports a leak as the first secret.
func probeBridgeLinks(ctx context.Context, secrets []string, log io.Writer) error {
	var seen bytes.Buffer
	logger := logging.New(io.MultiWriter(log, &seen), logging.LevelInfo)
	now := time.Now().UTC()
	business := clock.NewManual(now)
	clocks := clock.Clocks{Business: business, Real: clock.Real{}}
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
	bot := &probeBridgeBot{}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: bot}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	store := &probeConnections{}
	svc := connections.New(connections.Config{OrgID: 1, Store: store, Keyring: k,
		Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks,
		Network:     mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}},
		Interactive: deliverytest.Unlimited(1, clocks), IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081"},
		Log: logger})
	c, err := svc.Create(ctx, connections.Requester{Actor: audit.System, Transport: audit.TransportUI},
		connections.Input{Type: connections.TypeBridge, Name: "probe", BridgeBaseURL: "http://" + ln.Addr().String(),
			EventDelivery: connections.EventDeliveryPoll, BotToken: keyring.Replace(logging.Secret(secrets[0])),
			Limiter: connections.Limiter{Limit: 10, PerSeconds: 1}, Proxy: proxyconf.Input{Enabled: false}})
	if err != nil {
		return err
	}
	conn, err := svc.BridgeConn(ctx, c.PublicID)
	if err != nil {
		return err
	}
	path := deliverytest.Unlimited(1, clocks)
	random := append([]byte(secrets[0]), make([]byte, 32)...)[:32]
	links := accountlinks.New(accountlinks.Config{OrgID: 1, Store: &probeLinkStore{bridge: &alinkdb.GetLinkConnectionRow{
		ID: conn.ID, PublicID: conn.PublicID, Type: "bridge", Name: "probe",
		LinkUrlTemplate: "http://127.0.0.1/bot?start={token}"}}, Audit: audit.NewWriter(logger, business),
		Business: business, Log: logger, Bridge: bridge.LinkBots{Conns: svc, Path: path},
		Random: io.MultiReader(bytes.NewReader(random), rand.Reader)})
	sess := auth.Session{ID: 1, PublicID: "SNAAAAAAAAAAA1", State: auth.StateActive,
		User: auth.Principal{ID: 1, PublicID: "SRAAAAAAAAAAA1", Name: "Bob", Role: auth.RoleResponder}}
	h, err := api.New(api.Config{Sessions: probeSessions{sess: sess}, AccountLinks: links, Log: logger,
		Real: clock.Real{}})
	if err != nil {
		return err
	}
	send := func(path, body string) string {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		if err != nil {
			return err.Error()
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", auth.CookieName+"=probe")
		r.Header.Set(auth.CSRFHeader, "probe")
		rec := &probeRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		return rec.body.String()
	}
	const start = "/api/v1/me/account-links/bridge"
	connBody := `{"connection_id":"` + conn.PublicID + `"}`
	// A started token goes to the person, so its answer is left out of what must not carry a secret.
	var started []string
	for range 2 {
		var st struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal([]byte(send(start, connBody)), &st)
		started = append(started, st.Token)
	}
	unknown := base64.RawURLEncoding.EncodeToString(append([]byte(secrets[1]), make([]byte, 32)...)[:32])
	events := &bridge.Events{Store: svc, Bindings: probeBridgeBindings{}, Links: links, Linker: links,
		Commands: probeCommands{}, Keys: k, Path: path, Budget: 5 * time.Second, PublicURL: "http://localhost:8080",
		Clocks: clocks, Log: logger}
	var errs []error
	for i, token := range []string{started[0], started[0], unknown, "", started[1]} {
		if token == "" {
			business.Advance(accountlinks.BridgeTokenTTL)
			continue
		}
		bot.carries(token)
		errs = append(errs, events.Handle(ctx, conn, bridge.Event{ID: "ev-" + strconv.Itoa(i),
			Type: bridge.TypeAccountLink, OccurredAt: time.Now().UTC(), Token: token,
			User: bgen.Person{ExternalId: "u-bob", DisplayName: "Bob Brown",
				Handle: nullable.NewNullableWithValue("bob@example.org")}}))
	}
	codes, confirm := start+"/codes", start+"/confirmation"
	handle := `{"connection_id":"` + conn.PublicID + `","handle":"bob@example.org"}`
	var answers []string
	var req struct {
		RequestID string `json:"request_id"`
	}
	for range 3 {
		answers = append(answers, send(codes, handle))
	}
	for range 2 {
		a := send(codes, handle)
		answers = append(answers, a)
		_ = json.Unmarshal([]byte(a), &req)
		for _, code := range []string{secrets[2], secrets[2], secrets[2], secrets[2], secrets[2], bot.code()} {
			answers = append(answers, send(confirm, `{"request_id":"`+req.RequestID+`","code":"`+code+`"}`))
		}
	}
	a := send(codes, handle)
	_ = json.Unmarshal([]byte(a), &req)
	linked := send(confirm, `{"request_id":"`+req.RequestID+`","code":"`+bot.code()+`"}`)
	answers = append(answers, a, linked)
	errs = append(errs, fmt.Errorf("answers: %s", strings.Join(answers, " | ")))
	refused, sent, answered, searched := bot.counts()
	if refused < 2 || sent < 3 || answered < 4 || searched < 2 || !strings.Contains(linked, `"messenger":"bridge"`) {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("%d refused and %d sent direct messages, %d answers, %d "+
			"lookups, the last confirmation linked: %v", refused, sent, answered, searched,
			strings.Contains(linked, `"messenger":"bridge"`))})
	}
	text := seen.String() + strings.Join(answers, "") + errors.Join(errs...).Error()
	for _, s := range append(bot.allCodes(), started...) {
		if s != "" && strings.Contains(text, s) {
			errs = append(errs, errors.New("a link token or code leaked: "+secrets[0]))
		}
	}
	return errors.Join(errs...)
}

// probeBridgeBot is the stand-in bridge of probeBridgeLinks: its info offers a code by email through direct messages
// the bot starts and a link_url_template; it refuses the first findUsers with the query and the Authorization header
// in its problem and then finds bob; it refuses the first direct message as forbidden and the second as transient,
// with the request in its problem, and keeps the codes of the others; and it refuses every answer to an event as
// forbidden with the request, the Authorization header and the token of the event in its problem.
type probeBridgeBot struct {
	mu       sync.Mutex
	token    string
	searched int
	refused  []string
	sent     []string
	answered int
}

// carries records the token of the event to be answered next, which the stand-in repeats in its refusals.
func (p *probeBridgeBot) carries(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = token
}

// code is the code of the last direct message sent.
func (p *probeBridgeBot) code() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sent) == 0 {
		return ""
	}
	return p.sent[len(p.sent)-1]
}

// allCodes are the codes of every direct message, refused or sent.
func (p *probeBridgeBot) allCodes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append(slices.Clone(p.refused), p.sent...)
}

func (p *probeBridgeBot) counts() (refused, sent, answered, searched int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.refused), len(p.sent), p.answered, p.searched
}

func (p *probeBridgeBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	problem := func(status int, class string) {
		echo := "refused " + r.URL.RawQuery + " " + string(body) + " " + r.Header.Get("Authorization") + " " +
			p.token
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "title": echo, "detail": echo, "class": class})
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/info":
		_, _ = io.WriteString(w, `{"protocol_version":"1","bridge":{"name":"probe","version":"1"},`+
			`"messenger":{"name":"probe"},"capabilities":{"direct_messages":{"bot_can_start":true}},`+
			`"limits":{"max_body_length":4000,"edit_max_age_seconds":null},"user_lookup":"email",`+
			`"event_delivery":["poll"],"link_url_template":"http://127.0.0.1/bot?start={token}"}`)
	case "/v1/users":
		if p.searched++; p.searched == 1 {
			problem(http.StatusInternalServerError, "transient")
			return
		}
		_, _ = io.WriteString(w, `{"items":[{"external_id":"u-bob","display_name":"Bob Brown",`+
			`"handle":"bob@example.org"}]}`)
	case "/v1/direct-messages":
		code := probeCodeRe.FindString(string(body))
		switch len(p.refused) {
		case 0:
			p.refused = append(p.refused, code)
			problem(http.StatusForbidden, "forbidden")
		case 1:
			p.refused = append(p.refused, code)
			problem(http.StatusInternalServerError, "transient")
		default:
			p.sent = append(p.sent, code)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"message_id":"dm","replayed":false}`)
		}
	case "/v1/answers":
		p.answered++
		problem(http.StatusForbidden, "forbidden")
	default:
		problem(http.StatusNotFound, "gone")
	}
}

// probeBridgeCheck runs `muster bridge check` (C-22.FR-21) with the first secret as the token and the second, in
// base64, as the push secret, both read from files, against a stand-in bridge whose problems echo them with the
// request's Authorization and body and whose message ids carry them: once with an info that fails; once interactively
// against a bridge that only pushes, whose push listener receives a push signed with the third secret, a stale one, one
// whose body carries the secrets and the press, sent through an outbound client; once with replays that answer a
// message id that carries the token URL-escaped and a second one that carries the push secret; and once with a push
// secret file that holds no push secret. Neither the lines the command prints, which the probe copies to its log, its
// log lines, the listener's answers nor the errors may carry them.
func probeBridgeCheck(ctx context.Context, secrets []string, log io.Writer) error {
	const (
		modeInfoFails = iota
		modePush
		modeReplays
	)
	logger := logging.New(log, logging.LevelInfo)
	pushSecret := "whsec_" + base64.StdEncoding.EncodeToString([]byte(secrets[1]))
	dir, err := os.MkdirTemp("", "muster-probe-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := func(name, content string) (string, error) {
		path := filepath.Join(dir, name)
		return path, os.WriteFile(path, []byte(content), 0o600)
	}
	tokenFile, err := file("token", secrets[0]+"\n")
	if err != nil {
		return err
	}
	pushFile, err := file("push", pushSecret)
	if err != nil {
		return err
	}
	malformedFile, err := file("malformed", "whsec_"+secrets[1])
	if err != nil {
		return err
	}

	var mode atomic.Int32
	var mu sync.Mutex
	var data []string
	posts := 0
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			answer := func(status int, v any) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(v)
			}
			problem := func(status int, class string) {
				echo := "token " + secrets[0] + " " + url.QueryEscape(secrets[0]) + " " + r.Header.Get("Authorization") +
					" push secret " + pushSecret + " body " + string(body)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "detail": echo, "title": echo,
					"type": "urn:" + secrets[0], "class": class})
			}
			delivery := []string{"push"}
			if mode.Load() == modeReplays {
				delivery = []string{"poll", "push"}
			}
			var in bgen.MessageInput
			switch {
			case r.URL.Path == "/v1/info" && mode.Load() == modeInfoFails:
				problem(http.StatusInternalServerError, "transient")
			case r.URL.Path == "/v1/info":
				answer(http.StatusOK, map[string]any{"protocol_version": "1",
					"bridge": map[string]any{"name": "probe", "version": "1"}, "messenger": map[string]any{"name": "probe"},
					"capabilities": map[string]any{"buttons": map[string]any{"max_per_message": 4, "max_per_row": 4,
						"data_max_bytes": 64, "styles": []string{}}, "direct_messages": map[string]any{"bot_can_start": true},
						"delete": map[string]any{"max_age_seconds": nil}},
					"limits":      map[string]any{"max_body_length": 4000, "edit_max_age_seconds": nil},
					"user_lookup": "login", "event_delivery": delivery})
			case r.URL.Path == "/v1/messages" && json.Unmarshal(body, &in) == nil && len(in.Buttons) > 0:
				mu.Lock()
				data = data[:0]
				for _, b := range in.Buttons {
					data = append(data, b.Data)
				}
				mu.Unlock()
				answer(http.StatusCreated, map[string]any{"message_id": "b-" + secrets[0], "truncated": false,
					"replayed": false})
			case r.URL.Path == "/v1/messages" && mode.Load() == modeReplays:
				mu.Lock()
				posts++
				id := "p-" + url.QueryEscape(secrets[0])
				if posts%2 == 0 {
					id = "p-" + pushSecret
				}
				mu.Unlock()
				answer(http.StatusCreated, map[string]any{"message_id": id, "truncated": false, "replayed": false})
			case strings.HasPrefix(r.URL.Path, "/v1/channels/"):
				problem(http.StatusInternalServerError, "transient")
			case r.URL.Path == "/v1/users":
				problem(http.StatusBadRequest, "invalid")
			default:
				problem(http.StatusForbidden, "forbidden")
			}
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	base := "http://" + ln.Addr().String()

	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	pusher, err := outbound.New(outbound.Config{Class: outbound.ClassInteractive, ConnectTimeout: 2 * time.Second,
		Timeout: 5 * time.Second, Policy: outbound.StaticPolicy(policy), Logger: logger, Clock: clock.Real{}})
	if err != nil {
		return err
	}
	var errs []error
	statuses := []int{}
	push := func(target, id string, at time.Time, secret logging.Secret, body []byte) {
		signature, err := webhooks.Sign([]logging.Secret{secret}, id, at, body)
		if err != nil {
			errs = append(errs, err)
			return
		}
		res, err := pusher.Do(ctx, outbound.Request{Method: http.MethodPost, URL: target, Body: body,
			Header: http.Header{webhooks.HeaderID: {id}, webhooks.HeaderTimestamp: {strconv.FormatInt(at.Unix(), 10)},
				webhooks.HeaderSignature: {signature}, "Content-Type": {"application/json"}}})
		statuses = append(statuses, res.Status)
		errs = append(errs, err, errors.New(string(res.Body)))
	}
	press := func(id, d string) []byte {
		return fmt.Appendf(nil, `{"id":%q,"type":"press","occurred_at":%q,"message_id":%q,`+
			`"user":{"external_id":"u-probe","display_name":"probe"},"data":%q}`, id,
			time.Now().UTC().Format(time.RFC3339Nano), "b-"+secrets[0], d)
	}

	var printed strings.Builder
	check := func(m int32, args ...string) int {
		mode.Store(m)
		printed.Reset()
		lines := &probeLines{log: log, all: &printed}
		lines.on = func(line string) {
			kind, rest, _ := strings.Cut(line, " ")
			switch {
			case kind == "PUSH":
				lines.push = strings.Fields(rest)[0]
			case kind == "WAIT" && lines.push != "":
				mu.Lock()
				d := slices.Clone(data)
				mu.Unlock()
				if len(d) == 0 {
					errs = append(errs, &probeIncomplete{"the buttons step posted no buttons"})
					return
				}
				now := time.Now()
				forged := logging.Secret("whsec_" + base64.StdEncoding.EncodeToString([]byte(secrets[2])))
				push(lines.push, "ev-1", now, forged, press("ev-1", d[0]))
				push(lines.push, "ev-1", now.Add(-6*time.Minute), logging.Secret(pushSecret), press("ev-1", d[0]))
				push(lines.push, "ev-2", now, logging.Secret(pushSecret), []byte(`{"id":"ev-2","token":"`+secrets[0]+
					`","secret":"`+pushSecret+`","x":"`+secrets[2]+`"}`))
				push(lines.push, "ev-3", now, logging.Secret(pushSecret), press("ev-3", d[0]))
			}
		}
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return cli.BridgeCheckCommand(runCtx, append([]string{base, "--token-file", tokenFile, "--channel", "c"},
			args...), lines, lines)
	}

	if code := check(modeInfoFails, "--no-interactive", "--push-secret-file", pushFile); code != 1 ||
		!strings.HasPrefix(printed.String(), "FAIL info: answered 500 transient: ") {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the check of a failing info exited %d", code)})
	}
	code := check(modePush, "--push-secret-file", pushFile, "--forbidden-channel", "x", "--handle", "alice")
	want := []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusBadRequest, http.StatusNoContent}
	if code != 1 || !slices.Equal(statuses, want) || !strings.Contains(printed.String(), "PASS press\n") ||
		!strings.Contains(printed.String(), "PASS push\n") {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the interactive check exited %d with the pushes answered %v, "+
			"want 1 and %v with press and push passed", code, statuses, want)})
	}
	if code := check(modeReplays, "--no-interactive", "--push-secret-file", pushFile); code != 1 ||
		!strings.Contains(printed.String(), "FAIL replayed_key: the second request with the same Idempotency-Key "+
			"created message p-[redacted] instead of returning p-[redacted]\n") {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the check with replays exited %d", code)})
	}
	if code := check(modePush, "--push-secret-file", malformedFile); code != 2 {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the check with a malformed push secret exited %d", code)})
	}
	return errors.Join(errs...)
}

// probeLines is the standard output and error of `muster bridge check` in probeBridgeCheck: it copies what is written
// to the probe's log and to all and hands each line to on, outside its lock; push is the URL of the PUSH line.
type probeLines struct {
	log  io.Writer
	all  *strings.Builder
	on   func(line string)
	push string

	mu      sync.Mutex
	pending string
}

func (p *probeLines) Write(b []byte) (int, error) {
	p.mu.Lock()
	_, _ = p.log.Write(b)
	p.all.Write(b)
	p.pending += string(b)
	var lines []string
	for {
		line, rest, ok := strings.Cut(p.pending, "\n")
		if !ok {
			break
		}
		lines, p.pending = append(lines, line), rest
	}
	p.mu.Unlock()
	for _, line := range lines {
		p.on(line)
	}
	return len(b), nil
}

// probeConnectionDrafts pushes the secrets through the check of a draft Connection (C-13.FR-14) of each type. A saved
// Connection holds the bot token (secret 0) and the password (secret 1) of its SOCKS5 proxy, which accepts it. Five
// drafts of it are checked, four without a token — another address without a proxy, the saved address through another
// proxy, an HTTP one, another address through the saved proxy and the saved address without a proxy — and one with a
// token typed (secret 2) for another address. The saved address, the other addresses and the HTTP proxy are stand-ins
// that record every request they get — its method, target, headers and body — and answer the type's dry probe as it
// passes and anything else with an error that echoes the request's Authorization header and target. The probe returns
// the records, the typed token masked in those of its own address, so that the stored token or the stored proxy
// password in a request to an address or through a route that is not the saved one fails rule 5 as a log line or an
// error does; it is incomplete when a draft reached no stand-in or the draft through the saved proxy did not pass it.
func probeConnectionDrafts(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
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
	socks, err := fakeproxy.Start(ctx, fakeproxy.SOCKS5, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: secrets[1]})
	if err != nil {
		return err
	}
	defer func() { _ = socks.Close() }()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	network := mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}}
	var errs []error
	for _, typ := range []string{connections.TypeMattermost, connections.TypeTelegram, connections.TypeBridge} {
		svc := connections.New(connections.Config{OrgID: 1, Store: &probeConnections{}, Keyring: k,
			Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks, Network: network,
			Interactive: deliverytest.Unlimited(1, clocks), IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081"},
			Log: logger})
		errs = append(errs, probeDraftsOf(ctx, svc, typ, secrets, socks))
	}
	return errors.Join(errs...)
}

// probeDraftsOf saves a Connection of typ for probeConnectionDrafts at a stand-in behind the SOCKS5 proxy socks and
// checks its five drafts; it returns their errors and step messages and the requests that the stand-ins recorded.
func probeDraftsOf(ctx context.Context, svc *connections.Service, typ string, secrets []string,
	socks *fakeproxy.Server) error {
	// the saved address, another address, another proxy, another address through the saved proxy, and the address of
	// the typed token
	var stand [5]*probeDraftStandIn
	defer func() {
		for _, s := range stand {
			if s != nil {
				_ = s.srv.Close()
			}
		}
	}()
	var err error
	for i := range stand {
		if stand[i], err = startDraftStandIn(ctx); err != nil {
			return err
		}
	}
	saved, other, viaProxy, throughSaved, typed := stand[0], stand[1], stand[2], stand[3], stand[4]
	in := connections.Input{Type: typ, Name: "probe", BotToken: keyring.Replace(logging.Secret(secrets[0])),
		Limiter: connections.Limiter{Limit: 5, PerSeconds: 1},
		Proxy: proxyconf.Input{Enabled: true, Type: new("socks5"), Address: new(socks.Addr()), UsernameSet: true,
			Username: new("muster"), Password: keyring.Replace(logging.Secret(secrets[1]))}}
	setAddress := func(addr string) {
		switch typ {
		case connections.TypeTelegram:
			in.BotAPIBaseURL, in.UpdateMode = addr, connections.ModeLongPolling
		case connections.TypeBridge:
			in.BridgeBaseURL, in.EventDelivery = addr, connections.EventDeliveryPoll
		default:
			in.ServerURL = addr
		}
	}
	setAddress(saved.url())
	c, err := svc.Create(ctx, connections.Requester{Actor: audit.System, Transport: audit.TransportUI}, in)
	if err != nil {
		return err
	}
	draft := func(addr string, token keyring.SecretInput, p *proxyconf.Input) connections.Draft {
		setAddress(addr)
		return connections.Draft{Type: typ, ServerURL: in.ServerURL, BotAPIBaseURL: in.BotAPIBaseURL,
			UpdateMode: in.UpdateMode, BridgeBaseURL: in.BridgeBaseURL, EventDelivery: in.EventDelivery, BotToken: token,
			Proxy: p, ConnectionID: &c.PublicID}
	}
	drafts := []connections.Draft{
		draft(other.url(), keyring.Keep, &proxyconf.Input{Enabled: false}),
		draft(saved.url(), keyring.Keep, &proxyconf.Input{Enabled: true, Type: new("http"),
			Address: new(viaProxy.addr())}),
		draft(throughSaved.url(), keyring.Keep, nil),
		draft(saved.url(), keyring.Keep, &proxyconf.Input{Enabled: false}),
		draft(typed.url(), keyring.Replace(logging.Secret(secrets[2])), &proxyconf.Input{Enabled: false}),
	}
	var errs []error
	for _, d := range drafts {
		res, err := svc.CheckDraft(ctx, d)
		errs = append(errs, err)
		for _, st := range res.Steps {
			errs = append(errs, errors.New(st.Message))
		}
	}
	for i, what := range []string{"the draft at the saved address without a proxy made no dry probe there",
		"the draft with another address made no dry probe there", "the draft with another proxy sent nothing through it",
		"the draft with another address through the saved proxy made no dry probe there",
		"the draft with a typed token sent nothing"} {
		records := stand[i].taken()
		if len(records) == 0 {
			errs = append(errs, &probeIncomplete{typ + ": " + what})
		}
		for _, r := range records {
			if stand[i] == typed {
				r = masked(r, secrets[2])
			}
			errs = append(errs, errors.New(r))
		}
	}
	if !slices.Contains(socks.Targets(), throughSaved.addr()) {
		errs = append(errs, &probeIncomplete{typ + ": the draft with another address did not pass the saved proxy"})
	}
	return errors.Join(errs...)
}

// probeDraftStandIn is an address or the HTTP proxy of probeConnectionDrafts. It records every request it gets
// and answers the dry probe of each type as it passes: the Mattermost system ping 200 with the status OK, the Telegram
// getMe of the token 0:x 401 with JSON and a Bridge's info without a token 401 with a problem. Anything else gets an
// error that echoes the request's Authorization header, target and body, so that a token sent there also reaches the
// step's message. As an HTTP proxy it gets the requests to an http address in absolute form, with their
// Proxy-Authorization header.
type probeDraftStandIn struct {
	srv *http.Server
	ln  net.Listener

	mu      sync.Mutex
	records []string
}

func startDraftStandIn(ctx context.Context) (*probeDraftStandIn, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &probeDraftStandIn{ln: ln}
	s.srv = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: s}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *probeDraftStandIn) addr() string { return s.ln.Addr().String() }

func (s *probeDraftStandIn) url() string { return "http://" + s.addr() }

// taken are the requests recorded so far.
func (s *probeDraftStandIn) taken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records)
}

func (s *probeDraftStandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.records = append(s.records, fmt.Sprintf("%s %s %v %s", r.Method, r.RequestURI, r.Header, body))
	s.mu.Unlock()
	auth := r.Header.Get("Authorization")
	echo := "invalid token " + auth + " at " + r.RequestURI + " for " + string(body)
	answer := func(status int, contentType string, v any) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch path := r.URL.Path; {
	case path == mattermost.DryProbePath:
		answer(http.StatusOK, "application/json", map[string]any{"status": "OK"})
	case strings.HasSuffix(path, "/bot0:x/getMe"):
		answer(http.StatusUnauthorized, "application/json", map[string]any{"ok": false, "error_code": 401,
			"description": "Unauthorized"})
	case path == "/v1/info" && auth == "":
		answer(http.StatusUnauthorized, "application/problem+json", map[string]any{"status": 401,
			"class": "forbidden", "detail": "The token is missing."})
	case strings.HasPrefix(path, "/v1/"):
		answer(http.StatusInternalServerError, "application/problem+json", map[string]any{"status": 500,
			"class": "transient", "detail": echo, "title": echo})
	case strings.HasPrefix(path, "/bot"):
		answer(http.StatusInternalServerError, "application/json", map[string]any{"ok": false, "error_code": 500,
			"description": echo})
	default:
		answer(http.StatusUnauthorized, "application/json", map[string]any{
			"id": "api.context.session_expired.app_error", "message": echo, "status_code": 401})
	}
}

// masked is record with every form of the typed token, which may go to the address it was typed for, replaced.
func masked(record, typed string) string {
	for _, f := range leakForms(typed) {
		record = strings.ReplaceAll(record, f.text, "[typed]")
	}
	return record
}

// probeOutgoingHeartbeat pushes the secrets through the outgoing heartbeat (C-19.FR-7): the first in the path and the
// second in the query of its URL, set through the organization resource, whose Audit log diff is returned; the third
// as the password of its HTTP proxy, which refuses it. The sender then meets an answer of 503, a redirect whose target
// carries them, a refused connection, the refused proxy, an answer of 200 and a URL the outbound address policy
// blocks. Neither the log, the errors nor the last error it records for the System status may carry them.
func probeOutgoingHeartbeat(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	business := clock.NewManual(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	k, err := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(masterKey("probe")), Source: keyring.SecretKeysVar},
		false)
	if err != nil {
		return err
	}
	st, err := k.Establish(ctx, &probeStore{}, business.Now())
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
	status := http.StatusServiceUnavailable
	var mu sync.Mutex
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			code := status
			mu.Unlock()
			if code == http.StatusFound {
				w.Header().Set("Location", "http://elsewhere.example.org/"+r.URL.RawPath+"?"+r.URL.RawQuery)
			}
			w.WriteHeader(code)
			_, _ = fmt.Fprintf(w, "refused %s %s", r.URL.RawQuery, r.URL.Path) //nolint:gosec // G705: a stand-in server that echoes the secrets on purpose
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	proxy, err := fakeproxy.Start(ctx, fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "other"})
	if err != nil {
		return err
	}
	defer func() { _ = proxy.Close() }()

	tail := "/hook/" + url.PathEscape(secrets[0]) + "?token=" + url.QueryEscape(secrets[1])
	settings := &probeOrgSettings{proxy: []byte(`{"enabled":false}`)}
	svc := organization.NewService(1, settings, audit.NewWriter(logger, business), business).WithKeyring(k)
	set := func(target string, p organization.ProxyInput) error {
		o, err := svc.Get(ctx)
		if err != nil {
			return err
		}
		_, err = svc.Update(ctx, audit.System, audit.TransportAPI, netip.Addr{}, nil, organization.Input{
			Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel, CriticalIsUrgent: o.CriticalIsUrgent,
			SeverityMapping: o.SeverityMapping, SeverityStyles: o.SeverityStyles, Retention: o.Retention,
			InstanceLabels: o.InstanceLabels, TOTPRequired: o.TOTPRequired,
			OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace / time.Second),
			HeartbeatURL:          keyring.Replace(logging.Secret(target)), HeartbeatProxy: p})
		return err
	}
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	sender := &outgoingheartbeat.Sender{Store: probeHeartbeatStore{settings}, Keyring: k, Business: business, Log: logger,
		Network: outgoingheartbeat.Network{Policy: func(int64) outbound.PolicySource {
			return outbound.StaticPolicy(policy)
		}, Log: logger, Real: clock.Real{}}}
	base := "http://" + ln.Addr().String()
	direct := organization.ProxyInput{Enabled: false}
	typ, addr, user := proxyconf.TypeHTTP, proxy.Addr(), "muster"
	viaProxy := organization.ProxyInput{Enabled: true, Type: &typ, Address: &addr, UsernameSet: true, Username: &user,
		Password: keyring.Replace(logging.Secret(secrets[2]))}
	var errs []error
	for _, c := range []struct {
		target string
		proxy  organization.ProxyInput
		status int
	}{
		{base + tail, direct, http.StatusServiceUnavailable},
		{base + tail, direct, http.StatusFound},
		{"http://127.0.0.1:1" + tail, direct, http.StatusOK},
		{base + tail, viaProxy, http.StatusOK},
		{base + tail, direct, http.StatusOK},
		{"http://169.254.169.254" + tail, direct, http.StatusOK},
	} {
		errs = append(errs, set(c.target, c.proxy))
		mu.Lock()
		status = c.status
		mu.Unlock()
		business.Advance(outgoingheartbeat.Interval)
		errs = append(errs, sender.Send(ctx, 1), errors.New("last error: "+settings.lastError.String))
	}
	for _, e := range settings.audit {
		errs = append(errs, errors.New("audit: "+string(e.Diff)))
	}
	return errors.Join(errs...)
}

// probeOrgSettings is the organization row of probeOutgoingHeartbeat in memory: the resource for the organization
// Service and the outgoing heartbeat columns for the sender.
type probeOrgSettings struct {
	url, password keyring.StoredSecret
	proxy         []byte
	version       int64
	attemptAt     time.Time
	lastOK        pgtype.Bool
	lastError     pgtype.Text
	audit         []adb.InsertAuditEntryParams
}

func (s *probeOrgSettings) InTx(_ context.Context, f func(organization.SettingsQueries) error) error {
	return f(s)
}

func (s *probeOrgSettings) GetSettings(context.Context, int64) (orgdb.GetSettingsRow, error) {
	d := organization.Defaults()
	styles, err := json.Marshal(d.SeverityStyles)
	if err != nil {
		return orgdb.GetSettingsRow{}, err
	}
	return orgdb.GetSettingsRow{ID: 1, PublicID: "RGAAAAAAAAAAA1", Name: d.Name, TimeZone: d.TimeZone,
		SeverityLabel: d.SeverityLabel, SeverityMapping: []byte("[]"), SeverityStyles: styles,
		InstanceLabels: []string{}, RetentionStoredSnapshotsDays: d.Retention.StoredSnapshotsDays,
		RetentionAlertDetailsDays:        d.Retention.AlertDetailsDays,
		RetentionAlertGroupSummariesDays: d.Retention.AlertGroupSummariesDays,
		RetentionAuditLogDays:            d.Retention.AuditLogDays, TotpRequired: "nobody",
		OidcTokenGraceSeconds: int64(d.OIDCTokenGrace / time.Second), OutgoingHeartbeatUrlSet: s.url.Set(),
		OutgoingHeartbeatProxy: s.proxy, OutgoingHeartbeatProxyPasswordSet: s.password.Set(), Version: s.version}, nil
}

func (s *probeOrgSettings) LockSettings(context.Context, int64) (int64, error) { return s.version, nil }

func (s *probeOrgSettings) GetOutgoingHeartbeatSecrets(context.Context, int64) (
	orgdb.GetOutgoingHeartbeatSecretsRow, error) {
	return orgdb.GetOutgoingHeartbeatSecretsRow{
		OutgoingHeartbeatUrlCiphertext:           s.url.Ciphertext,
		OutgoingHeartbeatUrlKeyID:                pgtype.Text{String: s.url.KeyID, Valid: s.url.Set()},
		OutgoingHeartbeatProxyPasswordCiphertext: s.password.Ciphertext,
		OutgoingHeartbeatProxyPasswordKeyID:      pgtype.Text{String: s.password.KeyID, Valid: s.password.Set()},
	}, nil
}

func (s *probeOrgSettings) UpdateSettings(_ context.Context, arg orgdb.UpdateSettingsParams) (int64, error) {
	s.url = keyring.StoredSecret{Ciphertext: arg.UrlCiphertext, KeyID: arg.UrlKeyID.String}
	s.password = keyring.StoredSecret{Ciphertext: arg.ProxyPasswordCiphertext, KeyID: arg.ProxyPasswordKeyID.String}
	s.proxy, s.version = arg.Proxy, s.version+1
	return 1, nil
}

func (s *probeOrgSettings) InsertAuditEntry(_ context.Context, arg adb.InsertAuditEntryParams) error {
	s.audit = append(s.audit, arg)
	return nil
}

func (s *probeOrgSettings) Notify(context.Context, db.Hint) error { return nil }

// The outbound address policy and the values seen are not part of the probe: no Secret passes through them.
func (s *probeOrgSettings) GetOutboundPolicySettings(context.Context, int64) (orgdb.GetOutboundPolicySettingsRow,
	error) {
	return orgdb.GetOutboundPolicySettingsRow{}, errors.New("not used by the probe")
}

func (s *probeOrgSettings) LockOutboundPolicy(context.Context, int64) (int64, error) {
	return 0, errors.New("not used by the probe")
}

func (s *probeOrgSettings) UpdateOutboundPolicy(context.Context, orgdb.UpdateOutboundPolicyParams) (int64, error) {
	return 0, errors.New("not used by the probe")
}

func (s *probeOrgSettings) ListSeverityValues(context.Context, orgdb.ListSeverityValuesParams) (
	[]orgdb.ListSeverityValuesRow, error) {
	return nil, errors.New("not used by the probe")
}

func (s *probeOrgSettings) NotifyOutboundPolicy(context.Context, int64) error { return nil }

func (s *probeOrgSettings) ClaimOutgoingHeartbeat(_ context.Context, arg orgdb.ClaimOutgoingHeartbeatParams) (
	orgdb.ClaimOutgoingHeartbeatRow, error) {
	if !s.url.Set() || s.attemptAt.After(arg.DueBefore) {
		return orgdb.ClaimOutgoingHeartbeatRow{}, pgx.ErrNoRows
	}
	s.attemptAt = arg.Now
	return orgdb.ClaimOutgoingHeartbeatRow{OutgoingHeartbeatUrlCiphertext: s.url.Ciphertext,
		OutgoingHeartbeatUrlKeyID: pgtype.Text{String: s.url.KeyID, Valid: true}, OutgoingHeartbeatProxy: s.proxy,
		OutgoingHeartbeatProxyPasswordCiphertext: s.password.Ciphertext,
		OutgoingHeartbeatProxyPasswordKeyID:      pgtype.Text{String: s.password.KeyID, Valid: s.password.Set()}}, nil
}

func (s *probeOrgSettings) RecordOutgoingHeartbeat(_ context.Context, arg orgdb.RecordOutgoingHeartbeatParams) (
	pgtype.Bool, error) {
	previous := s.lastOK
	s.lastOK, s.lastError = pgtype.Bool{Bool: arg.Ok, Valid: true}, arg.LastError
	return previous, nil
}

// probeHeartbeatStore is probeOrgSettings as the sender's Store, whose InTx takes the sender's queries.
type probeHeartbeatStore struct{ *probeOrgSettings }

func (s probeHeartbeatStore) InTx(_ context.Context, f func(outgoingheartbeat.Queries) error) error {
	return f(s)
}

// probeKeyRotation runs muster secrets rotate-key with the secrets as master keys and as encrypted field values: an
// activation of a key the process does not hold and one refused while a live replica lacks the key, both through the
// command, one rolled back by a value that does not decrypt — altered, and under a key nobody holds — and, through the
// command, the activation that re-encrypts the values; then the command against an address where nothing listens,
// with the secrets as the master keys and the database password. Neither the log, the command's output, the errors
// nor the Audit log may carry them, and the final activation must commit once.
func probeKeyRotation(ctx context.Context, secrets []string, log io.Writer) error {
	var logged bytes.Buffer
	log = io.MultiWriter(log, &logged)
	logger := logging.New(log, logging.LevelInfo)
	k, err := keyring.Load(ctx, keyring.Env{Keys: logging.Secret(masterKey(secrets[0]) + "," + masterKey(secrets[1])),
		Source: keyring.SecretKeysVar}, false)
	if err != nil {
		return err
	}
	st, err := k.Establish(ctx, &probeStore{}, time.Unix(0, 0))
	if err != nil {
		return err
	}
	if err := k.Open(ctx, logger, st); err != nil {
		return err
	}
	ids := k.KeyIDs()
	const field = "connections.bot_token"
	values := make([]keyring.EncryptedValue, 0, len(secrets)+2)
	for i, secret := range secrets {
		ct, id, err := k.Encrypt(field, []byte(secret))
		if err != nil {
			return err
		}
		values = append(values, keyring.EncryptedValue{Row: keyring.RowKey{ID: int64(i)}, Ciphertext: ct, KeyID: id})
	}
	altered := bytes.Clone(values[0].Ciphertext)
	altered[len(altered)-1] ^= 1
	store := &probeRotationStore{active: ids[0], values: slices.Clone(values),
		replicas: []dbgen.Replica{{ReplicaID: "probe-a", KeyIds: ids}, {ReplicaID: "probe-b", KeyIds: ids[:1]}}}
	rotation := keyring.Rotation{Store: store, Keys: k, Audit: audit.NewWriter(logger, clock.Real{}), Log: logger,
		Business: clock.Real{}, Real: clock.Real{}}
	var rotated keyring.Rotated
	var rotateErrs []error
	activate := func(ctx context.Context, _ runtime.Options, r runtime.KeyRotation) (keyring.Rotated, error) {
		out, err := rotation.Activate(ctx, r.KeyID, r.Actor)
		rotated = out
		rotateErrs = append(rotateErrs, err)
		return out, err
	}
	command := func(id string) int {
		return cli.RotateKeyCommand(ctx, []string{"--activate", id, "--actor", "ops"}, runtime.Options{Stdout: log},
			activate, log)
	}
	var errs []error
	notHeld := keyring.KeyID(bytes.Repeat([]byte(secrets[2]), keyring.KeySize)[:keyring.KeySize])
	if code := command(notHeld); code != 1 || !errors.Is(rotateErrs[len(rotateErrs)-1], keyring.ErrKeyNotHeld) {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the activation of a key not held exited %d", code)})
	}
	if code := command(ids[1]); code != 1 { // refused: probe-b lacks the key
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the refused activation exited %d", code)})
	} else if _, lacking := errors.AsType[*keyring.ReplicasLackKeyError](rotateErrs[len(rotateErrs)-1]); !lacking {
		errs = append(errs, &probeIncomplete{"the activation was not refused for the replica that lacks the key"})
	}
	store.replicas = store.replicas[:1]
	store.values = append(slices.Clone(values), keyring.EncryptedValue{Row: keyring.RowKey{ID: 100},
		Ciphertext: altered, KeyID: ids[0]})
	_, err = rotation.Activate(ctx, ids[1], "ops") // rolled back: the altered value
	errs = append(errs, err)
	store.values = append(slices.Clone(values), keyring.EncryptedValue{Row: keyring.RowKey{ID: 101},
		Ciphertext: values[1].Ciphertext, KeyID: "k-unknown"})
	_, err = rotation.Activate(ctx, ids[1], "ops") // rolled back: a key nobody holds
	errs = append(errs, err)
	if store.active != ids[0] || len(store.audit) != 0 {
		errs = append(errs, &probeIncomplete{"a failed activation was not rolled back"})
	}
	store.values = slices.Clone(values)
	if code := command(ids[1]); code != 0 || store.active != ids[1] || len(store.audit) != 1 ||
		rotated.Reencrypted == 0 || !strings.Contains(logged.String(), `"event":"key_activated"`) {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the activation exited %d with %d audit entries",
			code, len(store.audit))})
	}
	for _, a := range store.audit {
		errs = append(errs, fmt.Errorf("audit %s %s", a.Diff, a.Details))
	}

	unreachable := runtime.Options{Environ: []string{"MUSTER_PUBLIC_URL=http://localhost:8080",
		"MUSTER_SECRET_KEYS=" + masterKey(secrets[1]) + "," + masterKey(secrets[2]),
		"MUSTER_DATABASE_HOST=127.0.0.1", "MUSTER_DATABASE_PORT=1", "MUSTER_DATABASE_NAME=muster",
		"MUSTER_DATABASE_USER=muster", "MUSTER_DATABASE_PASSWORD=" + secrets[0], "MUSTER_DATABASE_SSLMODE=disable"},
		Stdout: log}
	_, err = runtime.RotateKey(ctx, unreachable, runtime.KeyRotation{KeyID: ids[1], Actor: "ops"})
	if code := cli.RotateKeyCommand(ctx, []string{"--activate", ids[1], "--actor", "ops"}, unreachable,
		runtime.RotateKey, log); code != 1 {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("the command against no database exited %d", code)})
	}
	return errors.Join(append(append(errs, rotateErrs...), err)...)
}

// probeRotationStore is the database of an activation in memory for probeKeyRotation: one connections.bot_token
// field, the live replicas and the Audit log. A transaction whose function fails rolls back the active key, the values
// and the Audit log.
type probeRotationStore struct {
	active   string
	values   []keyring.EncryptedValue
	replicas []dbgen.Replica
	audit    []adb.InsertAuditEntryParams
}

func (s *probeRotationStore) InTx(_ context.Context, f func(keyring.RotationQueries) error) error {
	active, values, entries := s.active, slices.Clone(s.values), len(s.audit)
	if err := f(s); err != nil {
		s.active, s.values, s.audit = active, values, s.audit[:entries]
		return err
	}
	return nil
}

func (s *probeRotationStore) LockKeyringState(context.Context) (string, error) { return s.active, nil }

func (s *probeRotationStore) ActivateKey(_ context.Context, arg dbgen.ActivateKeyParams) (int64, error) {
	s.active = arg.KeyID
	return 1, nil
}

func (s *probeRotationStore) ListLiveReplicas(context.Context, time.Time) ([]dbgen.Replica, error) {
	return s.replicas, nil
}

func (s *probeRotationStore) ListOrganizationIDs(context.Context) ([]int64, error) {
	return []int64{1}, nil
}

func (s *probeRotationStore) CountOpenDeliveriesSignedByOtherKey(context.Context,
	dbgen.CountOpenDeliveriesSignedByOtherKeyParams) (int64, error) {
	return 0, nil
}

func (s *probeRotationStore) NotifyKeyring(context.Context, dbgen.NotifyKeyringParams) error {
	return nil
}

func (s *probeRotationStore) Notify(context.Context, db.Hint) error { return nil }

func (s *probeRotationStore) InsertAuditEntry(_ context.Context, arg adb.InsertAuditEntryParams) error {
	s.audit = append(s.audit, arg)
	return nil
}

func (s *probeRotationStore) EncryptedValues(_ context.Context, field string, _ int64, keyID string) (
	[]keyring.EncryptedValue, error) {
	if field != "connections.bot_token" {
		return nil, nil
	}
	var out []keyring.EncryptedValue
	for _, v := range s.values {
		if v.KeyID != keyID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *probeRotationStore) Reencrypt(_ context.Context, _ string, _ int64, row keyring.RowKey, ct []byte, keyID,
	_ string) (int64, error) {
	for i := range s.values {
		if s.values[i].Row.ID == row.ID {
			s.values[i].Ciphertext, s.values[i].KeyID = ct, keyID
		}
	}
	return 1, nil
}

// probeRequestLog pushes the secrets through the Request log (C-23.FR-3, AC-3). Clients of every type record their
// requests with a requestlog.Writer whose INSERTs an in-memory stand-in keeps — the lint has no database — and the
// probe returns every value an INSERT would store, so that no planted secret may be among them. The first secret is
// the bot token of a Telegram, a Mattermost and a Bridge Connection, also in the Telegram URL's path, which the
// stand-in server echoes in its answers with the Authorization header; the second the secret token of setWebhook and
// an outgoing webhook's Secret, sent in the Authorization header, another header and the URL's query, and echoed; the
// third the push secret of the Bridge Connection and the outgoing webhook's other Secret. Through them: getMe,
// setWebhook and a getUpdates whose answer carries a `/start <token>` and a press of a signed action id; a Root
// message with signed buttons through the adapter of each messenger; a pollEvents whose batch carries the token of an
// account_link event and a press; the direct messages with the codes of a Mattermost and a Bridge Account link; a
// getTeam that the stand-in redirects to a Location below the Mattermost server URL's path prefix; and an outgoing
// webhook event with its signature, Cookie and Proxy-Authorization headers, whose answer sets a cookie; and the getMe
// of a check before saving of a new Connection, whose client has no Connection. The stand-in echoes the request's
// path, base URL's path prefix included, in its answers. The signed action id, the tokens, the codes, the path
// prefixes of the base URLs and the values of those headers are not planted secrets, so the probe looks for them in
// the stored values itself; it is incomplete when an operation it drives left no record.
func probeRequestLog(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
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
	signed, keyID, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return err
	}
	stand := &probeRLServer{tg: probeWord(), mm: probeWord(), br: probeWord(), token: secrets[0], signed: signed,
		start: probeToken(), link: probeToken(), cookie: probeWord(), proxyAuth: probeWord(), setCookie: probeWord()}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: stand}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	rows := &probeRLRows{}
	w := requestlog.NewWriter(requestlog.Config{OrgID: 1, DB: rows, Business: clocks.Business, Log: logger})
	network := mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}, Recorder: w}
	base := "http://" + ln.Addr().String()
	tg, err := telegram.NewClient(telegram.Network(network), telegram.Settings{BaseURL: base + "/" + stand.tg,
		Token: logging.Secret(secrets[0]), Owner: outbound.Owner{Connection: 1, PublicID: "CNAAAAAAAAAAA1"}})
	if err != nil {
		return err
	}
	mm, err := mattermost.NewClient(network, mattermost.Settings{ServerURL: base + "/" + stand.mm,
		Token: logging.Secret(secrets[0]), Owner: outbound.Owner{Connection: 2, PublicID: "CNAAAAAAAAAAA2"}})
	if err != nil {
		return err
	}
	br, err := bridge.NewClient(bridge.Network(network), bridge.Settings{BaseURL: base + "/" + stand.br,
		Token: logging.Secret(secrets[0]), PushSecret: logging.Secret(secrets[2]),
		Owner: outbound.Owner{Connection: 3, PublicID: "CNAAAAAAAAAAA3"}})
	if err != nil {
		return err
	}
	targets := probeRLTargets{tg: telegram.Target{Client: tg, ConnectionID: 1, Channel: "@probe"},
		mm: mattermost.Target{Client: mm, ConnectionID: 2, ConnectionPublicID: "CNAAAAAAAAAAA2", TeamID: "t",
			TeamName: "team", ChannelID: "c", BotUserID: "bot"},
		br: bridge.Conn{ID: 3, PublicID: "CNAAAAAAAAAAA3", Version: 1, Client: br}}
	path := deliverytest.Unlimited(1, clocks)

	// Telegram: the token in the URL's path, the secret token of setWebhook, a `/start <token>` and a press polled.
	_, _ = tg.GetMe(ctx, outbound.ClassInteractive)
	_ = tg.SetWebhook(ctx, outbound.ClassInteractive, "http://localhost:8081/api/v1/callbacks/telegram/CNAAAAAAAAAAA1",
		logging.Secret(secrets[1]))
	_, _ = tg.GetUpdates(ctx, nil, 0)
	// A Root message with signed buttons through each messenger's adapter, on the interactive path as lint 3 allows.
	m := delivery.Message{Kind: messages.KindRoot, Language: "en", Colour: "firing", Heading: &messages.Heading{
		Number: 1, Title: "probe", URL: "http://localhost:8080/alert-groups/AGAAAAAAAAAAA1"},
		Buttons: []messages.Button{{Command: buttons.CommandAcknowledge, Label: "Acknowledge", ActionID: signed,
			KeyID: keyID}}}
	for i, a := range []delivery.Adapter{&telegram.Adapter{Targets: targets},
		&mattermost.Adapter{Targets: targets, PublicURL: "http://localhost:8080", IngestURL: "http://localhost:8081",
			Version: "v0.0.0"}, &bridge.Adapter{Targets: targets}} {
		conn := int64(i + 1)
		typ := []string{delivery.TypeTelegram, delivery.TypeMattermost, delivery.TypeBridge}[i]
		dest := delivery.Destination{ID: conn, PublicID: fmt.Sprintf("DSAAAAAAAAAAA%d", conn), Type: typ,
			Connection: &conn}
		_, _ = path.Do(ctx, delivery.Subject{Destination: &dest}, delivery.PublishOp(a, m))
	}
	// Mattermost: an answer that redirects below the server URL's path prefix and echoes it in its body.
	_, _ = mm.Team(ctx, outbound.ClassInteractive, "t")
	// Bridge: a batch with the token of an account_link event and a press.
	_, _ = br.PollEvents(ctx, nil, 0)
	// The codes of Account links in direct messages, which only the request knows as Secrets.
	by := accountlinks.Requester{UserID: 1, UserPublicID: "SRAAAAAAAAAAA1", Actor: audit.System,
		Transport: audit.TransportUI}
	mmLinks := accountlinks.New(accountlinks.Config{OrgID: 1, Store: &probeLinkStore{},
		Audit: audit.NewWriter(logger, clocks.Business), Business: clocks.Business, Log: logger,
		Mattermost: mattermost.LinkBots{Conns: targets, Path: path}})
	_, _ = mmLinks.StartMattermost(ctx, by, "CNAAAAAAAAAAA2", "bob")
	brLinks := accountlinks.New(accountlinks.Config{OrgID: 1, Store: &probeLinkStore{
		bridge: &alinkdb.GetLinkConnectionRow{ID: 3, PublicID: "CNAAAAAAAAAAA3", Type: "bridge", Name: "probe"}},
		Audit: audit.NewWriter(logger, clocks.Business), Business: clocks.Business, Log: logger,
		Bridge: bridge.LinkBots{Conns: targets, Path: path}})
	_, _ = brLinks.StartBridgeCode(ctx, by, "CNAAAAAAAAAAA3", "bob")
	// An outgoing webhook: its Secrets in headers and the URL's query, its signature, a cookie and a proxy credential.
	if err := probeRLWebhook(ctx, k, stand, base, secrets, network, clocks); err != nil {
		return err
	}
	// A check before saving of a new Connection: its client has the draft owner and records under its run alone.
	draft, err := telegram.NewClient(telegram.Network(network), telegram.Settings{BaseURL: base + "/" + stand.tg,
		Token: logging.Secret(secrets[0]), Owner: outbound.Owner{Draft: true}})
	if err != nil {
		return err
	}
	draftCtx, _ := outbound.StartCheck(ctx, outbound.CheckDraft, 0)
	_, _ = draft.GetMe(draftCtx, outbound.ClassInteractive)
	return rows.verdict(stand, signed)
}

// probeRLWebhook sends one event of an outgoing webhook whose Secrets are the second and the third secret through a
// client that records it with the Recorder of network.
func probeRLWebhook(ctx context.Context, k *keyring.Keyring, stand *probeRLServer, base string, secrets []string,
	network mattermost.Network, clocks clock.Clocks) error {
	store := &probeWebhookStore{secrets: map[string]keyring.StoredSecret{}, row: whdb.GetTargetRow{ID: 4,
		PublicID: "DSAAAAAAAAAAA4", WebhookMode: pgtype.Text{String: webhooks.ModeEvents, Valid: true},
		Proxy: []byte(`{"enabled":false}`)}}
	svc := webhooks.New(1, webhooks.Config{Store: store, Writer: store, Keyring: k,
		Audit: audit.NewWriter(network.Log, clocks.Business), Business: clocks.Business,
		PublicURL: "http://localhost:8080"})
	by := webhooks.Requester{Actor: audit.System, Transport: audit.TransportAPI}
	if _, _, err := svc.GenerateSigningSecret(ctx, by, "DSAAAAAAAAAAA4"); err != nil {
		return err
	}
	for name, v := range map[string]string{"token": secrets[1], "other": secrets[2]} {
		if _, _, err := svc.SetSecret(ctx, by, "DSAAAAAAAAAAA4", nil, name, logging.Secret(v)); err != nil {
			return err
		}
	}
	cfg, err := json.Marshal(webhooks.EventsConfig{URL: base + "/hook?k={{ .Secrets.token }}", Headers: []webhooks.Header{
		{Name: "Authorization", Value: "Bearer {{ .Secrets.token }}"}, {Name: "X-Api-Key", Value: "{{ .Secrets.token }}"},
		{Name: "X-Other", Value: "{{ .Secrets.other }}"}, {Name: "Cookie", Value: "session=" + stand.cookie},
		{Name: "Proxy-Authorization", Value: "Basic " + stand.proxyAuth}}})
	if err != nil {
		return err
	}
	store.row.WebhookEventsConfig = cfg
	a := &webhooks.Adapter{Service: svc, Sandbox: templates.New(clocks.Business, clocks.Real),
		Network: webhooks.Network{Policy: network.Policy, Log: network.Log, Real: network.Real,
			Recorder: network.Recorder}}
	_ = a.SendEvent(ctx, delivery.EventCall{Class: outbound.ClassDelivery, Destination: delivery.Destination{ID: 4,
		PublicID: "DSAAAAAAAAAAA4", Type: webhooks.TypeWebhook}, WebhookID: "msg_probe", Body: []byte(`{"event":"probe"}`)})
	return nil
}

// probeWord is a random word of lower-case letters and digits.
func probeWord() string { return "p" + strings.ToLower(rand.Text()) }

// probeToken is a random token of an Account link: 32 bytes in base64url, 43 characters.
func probeToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// probeRLTargets are the Connections of probeRequestLog for the adapters and the Account links of each messenger.
type probeRLTargets struct {
	tg telegram.Target
	mm mattermost.Target
	br bridge.Conn
}

func (p probeRLTargets) TelegramTarget(context.Context, int64) (telegram.Target, error) {
	return p.tg, nil
}

func (p probeRLTargets) Target(context.Context, int64) (mattermost.Target, error) { return p.mm, nil }

func (p probeRLTargets) Connection(context.Context, string) (mattermost.Connection, error) {
	return mattermost.Connection{ID: p.mm.ConnectionID, PublicID: p.mm.ConnectionPublicID, Client: p.mm.Client}, nil
}

func (p probeRLTargets) BridgeTarget(context.Context, int64) (bridge.Target, error) {
	return bridge.Target{Conn: p.br, ChannelID: "ch"}, nil
}

func (p probeRLTargets) BridgeConn(context.Context, string) (bridge.Conn, error) { return p.br, nil }

func (p probeRLTargets) BridgeInfo(ctx context.Context, conn bridge.Conn, class outbound.Class) (bridge.Info,
	bridge.Result) {
	return conn.Client.GetInfo(ctx, class)
}

// probeRLServer is the stand-in Telegram, Mattermost, Bridge and outgoing webhook server of probeRequestLog, below the
// path prefixes tg, mm and br and at /hook. It echoes the requests, their paths, their Authorization header and, from a
// messenger or a bridge, the bot token where a remote side may; it answers getUpdates with a `/start` of the token
// start and a press of signed, pollEvents with an account_link event of the token link and a press of signed, and
// getTeam with a redirect below the prefix mm; it keeps the codes of the direct messages and the signature, Cookie and
// Proxy-Authorization values of the webhook event, and sets the cookie setCookie.
type probeRLServer struct {
	tg, mm, br                          string
	token, signed, start, link          string
	cookie, proxyAuth, setCookie        string
	mu                                  sync.Mutex
	mmCodes, brCodes, signatures, sentC []string
}

func (p *probeRLServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	user := map[string]any{"id": 7001, "is_bot": false, "first_name": "Bob", "username": "bob"}
	person := map[string]any{"external_id": "u-bob", "display_name": "Bob", "handle": "bob"}
	echo := "echo " + r.URL.Path + " " + string(body) + " " + r.Header.Get("Authorization") + " " + p.token
	switch rest := r.URL.Path; {
	case strings.HasPrefix(rest, "/"+p.tg+"/"):
		switch rest[strings.LastIndex(rest, "/")+1:] {
		case "getMe":
			answer(http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"id": 1, "is_bot": true,
				"first_name": p.token, "username": "probe_bot"}})
		case "getUpdates":
			answer(http.StatusOK, map[string]any{"ok": true, "result": []any{
				map[string]any{"update_id": 1, "message": map[string]any{"message_id": 1, "date": 0, "from": user,
					"chat": map[string]any{"id": 7001, "type": "private"}, "text": "/start " + p.start}},
				map[string]any{"update_id": 2, "callback_query": map[string]any{"id": "q1", "from": user,
					"chat_instance": "c1", "data": p.signed}}}})
		case "setWebhook":
			answer(http.StatusOK, map[string]any{"ok": true, "result": true, "description": echo})
		default:
			answer(http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"message_id": 5, "date": 0,
				"chat": map[string]any{"id": -100, "type": "channel"}, "text": echo}})
		}
	case strings.HasPrefix(rest, "/"+p.mm+"/api/v4/"):
		switch strings.TrimPrefix(rest, "/"+p.mm+"/api/v4/") {
		case "users/username/bob":
			answer(http.StatusOK, map[string]any{"id": "u-bob", "username": "bob"})
		case "users/me":
			answer(http.StatusOK, map[string]any{"id": "bot", "username": "probe", "is_bot": true})
		case "channels/direct":
			answer(http.StatusCreated, map[string]any{"id": "dm", "type": "D"})
		case "teams/t":
			w.Header().Set("Location", "/"+p.mm+"/api/v4/teams/moved")
			answer(http.StatusFound, map[string]any{"echo": r.URL.Path})
		default:
			var post struct {
				ChannelID string `json:"channel_id"`
				Message   string `json:"message"`
			}
			_ = json.Unmarshal(body, &post)
			if post.ChannelID == "dm" {
				p.mmCodes = append(p.mmCodes, probeCodeRe.FindString(post.Message))
			}
			answer(http.StatusCreated, map[string]any{"id": "p1", "channel_id": post.ChannelID, "message": echo})
		}
	case strings.HasPrefix(rest, "/"+p.br+"/v1/"):
		switch strings.TrimPrefix(rest, "/"+p.br+"/v1/") {
		case "info":
			answer(http.StatusOK, map[string]any{"protocol_version": "1",
				"bridge": map[string]any{"name": "probe", "version": "1"}, "messenger": map[string]any{"name": "probe"},
				"capabilities": map[string]any{"buttons": map[string]any{"max_per_message": 4, "max_per_row": 4,
					"data_max_bytes": 64, "styles": []string{}}, "direct_messages": map[string]any{"bot_can_start": true}},
				"limits":      map[string]any{"max_body_length": 4000, "edit_max_age_seconds": nil},
				"user_lookup": "login", "event_delivery": []string{"poll"}})
		case "messages":
			answer(http.StatusCreated, map[string]any{"message_id": "m1", "truncated": false, "replayed": false,
				"echo": r.URL.Path})
		case "events":
			at := "2026-10-18T12:00:00Z"
			answer(http.StatusOK, map[string]any{"next_cursor": "c1", "events": []any{
				map[string]any{"id": "e1", "type": "account_link", "occurred_at": at, "token": p.link, "user": person},
				map[string]any{"id": "e2", "type": "press", "occurred_at": at, "message_id": "m1", "user": person,
					"data": p.signed}}})
		case "users":
			answer(http.StatusOK, map[string]any{"items": []any{person}})
		case "direct-messages":
			p.brCodes = append(p.brCodes, probeCodeRe.FindString(string(body)))
			answer(http.StatusCreated, map[string]any{"message_id": "dm", "replayed": false})
		default:
			answer(http.StatusNotFound, map[string]any{"status": 404, "title": "gone", "class": "gone"})
		}
	case rest == "/hook":
		p.signatures = append(p.signatures, r.Header.Get(webhooks.HeaderSignature))
		p.sentC = append(p.sentC, r.Header.Get("Cookie")+" "+r.Header.Get("Proxy-Authorization"))
		http.SetCookie(w, &http.Cookie{Name: "probe", Value: p.setCookie, Secure: true, HttpOnly: true,
			SameSite: http.SameSiteStrictMode})
		answer(http.StatusOK, map[string]any{"echo": r.URL.RawQuery + " " + r.Header.Get("X-Api-Key") + " " +
			r.Header.Get("X-Other") + " " + r.Header.Get("Authorization") + " " + string(body)})
	default:
		answer(http.StatusNotFound, map[string]any{})
	}
}

// probeRLRows stands for the database of the Request log: it keeps the arguments of every INSERT, which are the values
// of the columns the Writer stores, and refuses every read.
type probeRLRows struct {
	mu   sync.Mutex
	rows []probeRLRow
}

// probeRLRow is one record: its operation, the fifth parameter of InsertRecord, and every text and jsonb value.
type probeRLRow struct {
	operation string
	values    []string
}

func (p *probeRLRows) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(sql, "INSERT INTO request_log") || len(args) < 5 {
		return pgconn.CommandTag{}, errors.New("the probe keeps only the records of the request log")
	}
	row := probeRLRow{}
	row.operation, _ = args[4].(string)
	for _, a := range args {
		switch v := a.(type) {
		case string:
			row.values = append(row.values, v)
		case []byte:
			row.values = append(row.values, string(v))
		case pgtype.Text:
			if v.Valid {
				row.values = append(row.values, v.String)
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows = append(p.rows, row)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (p *probeRLRows) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("the probe reads no records")
}

func (p *probeRLRows) QueryRow(context.Context, string, ...any) pgx.Row { return probeRLNoRow{} }

// probeRLNoRow is the answer to every read of probeRLRows.
type probeRLNoRow struct{}

func (probeRLNoRow) Scan(...any) error { return errors.New("the probe reads no records") }

// verdict is the error of probeRequestLog: every stored value, for CheckProbe to search for the planted secrets; a
// probeLeak for each value that is no planted secret but must not be stored either, which the probe searches for
// itself; and a probeIncomplete when an operation it drove left no record or the stand-in saw no code or signature.
func (p *probeRLRows) verdict(stand *probeRLServer, signed string) error {
	p.mu.Lock()
	rows := slices.Clone(p.rows)
	p.mu.Unlock()
	stand.mu.Lock()
	mmCodes, brCodes := slices.Clone(stand.mmCodes), slices.Clone(stand.brCodes)
	signatures, sent := slices.Clone(stand.signatures), slices.Clone(stand.sentC)
	stand.mu.Unlock()
	var errs []error
	ops := map[string]int{}
	var stored []string
	for _, r := range rows {
		ops[r.operation]++
		stored = append(stored, r.values...)
	}
	var missing []string
	for _, op := range []string{"getMe", "setWebhook", "getUpdates", "sendMessage", "getInfo", "postMessage",
		"pollEvents", "getUserByUsername", "createDirectChannel", "getTeam", "findUsers", "sendDirectMessage", "event"} {
		if ops[op] == 0 {
			missing = append(missing, op)
		}
	}
	if ops["createPost"] < 2 {
		missing = append(missing, "createPost of the Root message and of the direct message")
	}
	if ops[""] > 0 {
		missing = append(missing, "the operation of a record")
	}
	switch {
	case len(missing) > 0:
		errs = append(errs, &probeIncomplete{"no record of " + strings.Join(missing, ", ")})
	case len(mmCodes) == 0 || slices.Contains(mmCodes, "") || len(brCodes) == 0 || slices.Contains(brCodes, ""):
		errs = append(errs, &probeIncomplete{"the stand-in saw no code in a direct message"})
	case len(signatures) == 0 || slices.Contains(signatures, "") ||
		sent[0] != "session="+stand.cookie+" Basic "+stand.proxyAuth:
		errs = append(errs, &probeIncomplete{"the webhook event carried no signature, Cookie or Proxy-Authorization"})
	}
	if !slices.ContainsFunc(rows, func(r probeRLRow) bool { return slices.Contains(r.values, outbound.CheckDraft) }) {
		errs = append(errs, &probeIncomplete{"no record of the check before saving of a new Connection"})
	}
	values := []probeRLValue{
		{"a signed action id", signed}, {"the token of a /start", stand.start},
		{"the token of an account_link event", stand.link}, {"the path prefix of a Telegram base URL", stand.tg},
		{"the path prefix of a Mattermost server URL", stand.mm}, {"the path prefix of a bridge's base URL", stand.br},
		{"the value of Cookie", stand.cookie}, {"the value of Proxy-Authorization", stand.proxyAuth},
		{"the value of Set-Cookie", stand.setCookie},
	}
	for _, c := range mmCodes {
		values = append(values, probeRLValue{"the code of a Mattermost direct message", c})
	}
	for _, c := range brCodes {
		values = append(values, probeRLValue{"the code of a Bridge direct message", c})
	}
	for _, s := range signatures {
		values = append(values, probeRLValue{"the value of webhook-signature", s})
	}
	return errors.Join(append(errs, probeRLLeaks(rows, values),
		fmt.Errorf("request_log rows: %s", strings.Join(stored, "\n")))...)
}

// probeRLValue is a value that is no planted secret and must not be stored, and what it is.
type probeRLValue struct{ what, value string }

// probeRLLeaks is a probeLeak for each of values found in a stored value of rows, naming it and the record's
// operation, never the value; nil when none is.
func probeRLLeaks(rows []probeRLRow, values []probeRLValue) error {
	var leaks []string
	for _, v := range values {
		for _, r := range rows {
			if v.value != "" && slices.ContainsFunc(r.values, func(s string) bool { return strings.Contains(s, v.value) }) {
				leaks = append(leaks, v.what+" reached the record of "+r.operation)
			}
		}
	}
	if len(leaks) == 0 {
		return nil
	}
	slices.Sort(leaks)
	return &probeLeak{what: slices.Compact(leaks)}
}

// probeRequestLogInbound pushes secrets through the Request log of the callbacks (C-23.FR-3, AC-3). The three
// callbacks run behind runtime.Callbacks, as the runtime wraps them, with stand-in Connections and a requestlog.Writer
// whose INSERTs the in-memory probeRLRows keeps — the lint has no database. The first secret is the Authorization of
// a Mattermost press and of a Bridge push and the bot token of the Bridge Connection, the second the webhook secret
// token of the Telegram Connection, and the third the push secret of the Bridge Connection and the Slack signing secret
// of the Slack Connection. Through them: a Mattermost press of a signed action id; Telegram updates with the secret
// token, a `/start <token>` and a press of the signed action id, and the `/start` again with a wrong secret token,
// which is refused before its body is read; an account_link event signed with the push secret under another
// webhook-id, refused after the signature check, and the same event with a forged signature; a Slack press of the
// signed action id with its response_url and verification token in the form, signed with the Slack signing secret, and
// again with a forged signature; each with an Authorization or a Cookie header. The signed action id, the tokens, the
// response_url, the wrong secret token, the header values and the signatures are not planted secrets, so the probe
// looks for them in the stored values itself; it is incomplete when a callback answered otherwise or left no record.
func probeRequestLogInbound(ctx context.Context, secrets []string, log io.Writer) error {
	logger := logging.New(log, logging.LevelInfo)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
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
	signed, keyID, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return err
	}
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	pushSecret := "whsec_" + base64.StdEncoding.EncodeToString([]byte(secrets[2]))
	brc, err := bridge.NewClient(bridge.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clocks.Real},
		bridge.Settings{BaseURL: "http://127.0.0.1:1", Token: logging.Secret(secrets[0]),
			PushSecret: logging.Secret(pushSecret)})
	if err != nil {
		return err
	}
	stand := probeInbound{secret: logging.Secret(secrets[1]), bridge: brc, slackSecret: logging.Secret(secrets[2])}
	rows := &probeRLRows{}
	w := requestlog.NewWriter(requestlog.Config{OrgID: 1, DB: rows, Business: clocks.Business, Log: logger})
	h := runtime.Callbacks(w, clocks.Real, mattermost.CallbackConfig{Connections: stand, Bindings: stand,
		Links: probeLinks{}, Commands: probeCommands{}, Keys: k, Business: clocks.Business, Log: logger},
		telegram.WebhookConfig{Connections: stand, Router: &telegram.Router{Offsets: stand, Log: logger}, Log: logger},
		bridge.PushConfig{Connections: stand, Real: clocks.Real, Log: logger},
		slack.CallbackConfig{Connections: stand, Presses: stand, Real: clocks.Real, Base: ctx, Log: logger})
	var errs []error
	post := func(path, body string, want int, header ...string) {
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			errs = append(errs, &probeIncomplete{fmt.Sprintf("POST %s answered %d, want %d", path, rec.Code, want)})
		}
	}
	auth, cookie, wrong, start, link := probeWord(), probeWord(), probeWord(), probeToken(), probeToken()

	press, err := json.Marshal(map[string]any{"user_id": "u-bob", "channel_id": "c", "post_id": "p",
		"trigger_id": "t", "context": map[string]string{"action": signed, "key_id": keyID}})
	if err != nil {
		return err
	}
	post(mattermost.CallbackPath+"CNAAAAAAAAAAA2", string(press), http.StatusOK, "Authorization", "Bearer "+secrets[0],
		"Cookie", "MMAUTHTOKEN="+cookie)

	from := `"from":{"id":7,"is_bot":false,"first_name":"b"}`
	startUpdate := fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"date":0,"chat":{"id":7,"type":"private"},`+
		`%s,"text":"/start %s"}}`, from, start)
	pressUpdate := fmt.Sprintf(`{"update_id":2,"callback_query":{"id":"q",%s,"chat_instance":"c","data":%q}}`, from,
		signed)
	hook := telegram.WebhookPath + "CNAAAAAAAAAAA1"
	post(hook, startUpdate, http.StatusOK, telegram.SecretTokenHeader, secrets[1], "Authorization", "Basic "+auth)
	post(hook, pressUpdate, http.StatusOK, telegram.SecretTokenHeader, secrets[1])
	post(hook, startUpdate, http.StatusUnauthorized, telegram.SecretTokenHeader, wrong)

	event := fmt.Sprintf(`{"id":"e1","type":"account_link","occurred_at":"2026-10-18T12:00:00Z","token":%q,`+
		`"user":{"external_id":"u-bob","display_name":"Bob","handle":"bob"}}`, link)
	at := clocks.Real.Now()
	sigA, err := webhooks.Sign([]logging.Secret{logging.Secret(pushSecret)}, "e2", at, []byte(event))
	if err != nil {
		return err
	}
	forged := make([]byte, 32)
	_, _ = rand.Read(forged)
	sigB := "v1," + base64.StdEncoding.EncodeToString(forged)
	pushPath, ts := bridge.CallbackPath+"CNAAAAAAAAAAA3", strconv.FormatInt(at.Unix(), 10)
	post(pushPath, event, http.StatusBadRequest, webhooks.HeaderID, "e2", webhooks.HeaderTimestamp, ts,
		webhooks.HeaderSignature, sigA, "Authorization", "Bearer "+secrets[0])
	post(pushPath, event, http.StatusUnauthorized, webhooks.HeaderID, "e1", webhooks.HeaderTimestamp, ts,
		webhooks.HeaderSignature, sigB, "Cookie", "session="+cookie)

	hooks, verification := probeWord(), probeWord()
	payload, err := json.Marshal(map[string]any{"type": "block_actions", "token": verification, "api_app_id": "A0PROBE",
		"user": map[string]any{"id": "U0PROBE"}, "container": map[string]any{"type": "message_attachment",
			"message_ts": "1.000001", "channel_id": "C0PROBE"}, "response_url": "https://hooks.slack.com/actions/T0PROBE/1/" +
			hooks, "actions": []any{map[string]any{"action_id": "ack", "type": "button", "value": signed}}})
	if err != nil {
		return err
	}
	slackBody := "payload=" + url.QueryEscape(string(payload))
	slackSig := fakeslack.Sign(secrets[2], ts, []byte(slackBody))
	forgedSig := "v0=" + hex.EncodeToString(forged)
	slackPath := slack.CallbackPath + "CNAAAAAAAAAAA4"
	post(slackPath, slackBody, http.StatusOK, slack.HeaderTimestamp, ts, slack.HeaderSignature, slackSig,
		"Authorization", "Bearer "+secrets[0])
	post(slackPath, slackBody, http.StatusUnauthorized, slack.HeaderTimestamp, ts, slack.HeaderSignature, forgedSig,
		"Cookie", "session="+cookie)

	return rows.inboundVerdict(errs, []probeRLValue{{"a signed action id", signed}, {"the token of a /start", start},
		{"the token of an account_link event", link}, {"a wrong secret token", wrong}, {"the value of Authorization", auth},
		{"the value of Cookie", cookie}, {"the value of webhook-signature", sigA},
		{"the value of webhook-signature", sigB}, {"the response_url of a Slack press", hooks},
		{"the verification token of a Slack press", verification}, {"the value of X-Slack-Signature", slackSig},
		{"the value of X-Slack-Signature", forgedSig}})
}

// inboundVerdict is the error of probeRequestLogInbound: errs, the answers that were not as they should be; a
// probeIncomplete unless each callback left one record per request; a probeLeak for each of values that a record
// stores; and every stored value, for CheckProbe to search for the planted secrets.
func (p *probeRLRows) inboundVerdict(errs []error, values []probeRLValue) error {
	p.mu.Lock()
	rows := slices.Clone(p.rows)
	p.mu.Unlock()
	ops := map[string]int{}
	var stored []string
	for _, r := range rows {
		ops[r.operation]++
		stored = append(stored, r.values...)
	}
	press, update, push, interactive := ops[requestlog.OperationPress], ops[requestlog.OperationUpdate],
		ops[requestlog.OperationPush], ops[requestlog.OperationInteractive]
	if press != 1 || update != 3 || push != 2 || interactive != 2 || len(rows) != 8 {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("records of %d presses, %d updates, %d pushes and %d "+
			"interactions among %d, want 1, 3, 2 and 2", press, update, push, interactive, len(rows))})
	}
	return errors.Join(append(errs, probeRLLeaks(rows, values),
		fmt.Errorf("request_log rows: %s", strings.Join(stored, "\n")))...)
}

// probeInbound stands for the Connections of probeRequestLogInbound: the Mattermost CNAAAAAAAAAAA2, whose presses are
// bound to no post; the Telegram CNAAAAAAAAAAA1 in the webhook mode with the secret token secret, whose updates find
// no handler; the Bridge CNAAAAAAAAAAA3 in push mode with the client bridge, whose events are never handled; and the
// Slack CNAAAAAAAAAAA4 in http mode with the Slack signing secret slackSecret, whose presses are never handled.
type probeInbound struct {
	secret      logging.Secret
	bridge      *bridge.Client
	slackSecret logging.Secret
}

func (p probeInbound) SlackPressConnection(context.Context, string) (slack.Conn, logging.Secret, error) {
	return slack.Conn{ID: 4, PublicID: "CNAAAAAAAAAAA4", Mode: slack.EventModeHTTP}, p.slackSecret, nil
}

func (probeInbound) Go(context.Context, slack.Conn, []byte) {}

func (probeInbound) Connection(context.Context, string) (mattermost.Connection, error) {
	return mattermost.Connection{ID: 2, PublicID: "CNAAAAAAAAAAA2"}, nil
}

func (probeInbound) PressBinding(context.Context, int64, string, string) (delivery.Binding, error) {
	return delivery.Binding{}, delivery.ErrNotBound
}

func (probeInbound) ReplyBinding(context.Context, int64, string, string) (delivery.Binding, error) {
	return delivery.Binding{}, delivery.ErrNotBound
}

func (probeInbound) PostDestination(context.Context, int64, string, string) (delivery.Destination, bool, error) {
	return delivery.Destination{}, false, nil
}

func (probeInbound) TestDestination(context.Context, int64, string, string) (delivery.Destination, bool, error) {
	return delivery.Destination{}, false, nil
}

func (p probeInbound) Hooked(context.Context, string) (telegram.Conn, logging.Secret, error) {
	return telegram.Conn{ID: 1, PublicID: "CNAAAAAAAAAAA1"}, p.secret, nil
}

func (probeInbound) HandleOnce(ctx context.Context, _, _ int64, f func(context.Context) error) (bool, error) {
	return true, f(ctx)
}

func (p probeInbound) PushConnection(context.Context, string) (bridge.Conn, string, error) {
	return bridge.Conn{ID: 3, PublicID: "CNAAAAAAAAAAA3", Version: 1, Client: p.bridge}, bridge.EventDeliveryPush, nil
}

func (probeInbound) PollingConnections(context.Context) ([]bridge.Polled, error) { return nil, nil }

func (probeInbound) StoreEventsCursor(context.Context, bridge.Conn, *string) error { return nil }

func (probeInbound) HandleEventOnce(context.Context, int64, string, func(context.Context) error) (bool, error) {
	return false, nil
}

// probeSlackPresses is the probe of Slack presses in http mode (C-24.FR-9 to FR-11, FR-14): the presses of the Slack
// Connection CNAAAAAAAAAAA5 reach slackInteraction behind the Request log's inbound recorder, whose records, and those
// of the answers, a requestlog.Writer keeps in the in-memory probeRLRows. The bot token is the first secret, the last
// segment of the press's response_url the second without secretSuffix — a response_url's token has only letters and
// digits (F-093), so the probe looks for it, in every leak form, in the log output, the returned error and the records
// itself — and the Slack signing secret the third. A press with a signature
// that has not even its shape and a stale one are refused; one from an account without an Account link and one from a linked account
// whose Command is refused are answered, and both answers fail both ways: the stand-in Slack refuses
// chat.postEphemeral with user_not_in_channel and the request and the secrets echoed, and the response_url answers 500
// echoing its address, the request and the secrets, and then drops the connection. A press handled directly whose response_url is not Slack's is not answered
// there. No log line, error or record may carry a secret; the verification token, the signatures and the signed action
// id are not planted secrets, so the probe looks for them in the records itself. It is incomplete when a press was
// answered otherwise, a log line or a record is missing, or the stand-in was not called.
func probeSlackPresses(ctx context.Context, secrets []string, log io.Writer) error {
	var lines bytes.Buffer
	var linesMu sync.Mutex
	logger := logging.New(io.MultiWriter(log, &lockedWriter{w: &lines, mu: &linesMu}), logging.LevelInfo)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
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
	signed, _, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGAAAAAAAAAAA1",
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return err
	}
	botToken, hooksToken, signing := "xoxb-"+secrets[0], strings.TrimSuffix(secrets[1], secretSuffix), secrets[2]
	var ephemerals, responses atomic.Int32
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			echo := r.Header.Get("Authorization") + " " + r.URL.String() + " " + string(body) + " " + botToken + " " +
				signing
			switch {
			case r.URL.Path == "/api/chat.postEphemeral":
				ephemerals.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "user_not_in_channel",
					"warning": echo})
			case strings.HasPrefix(r.URL.Path, "/actions/") && responses.Add(1)%2 == 1:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": echo})
			case strings.HasPrefix(r.URL.Path, "/actions/"):
				if c, _, err := http.NewResponseController(w).Hijack(); err == nil {
					_ = c.Close()
				}
			default:
				http.NotFound(w, r)
			}
		})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	base := "http://" + ln.Addr().String()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		return err
	}
	rows := &probeRLRows{}
	writer := requestlog.NewWriter(requestlog.Config{OrgID: 1, DB: rows, Business: clocks.Business, Log: logger})
	client, err := slack.NewClient(slack.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clocks.Real,
		Recorder: writer}, slack.Settings{BotToken: logging.Secret(botToken), SigningSecret: logging.Secret(signing),
		Owner: outbound.Owner{Connection: 5, PublicID: "CNAAAAAAAAAAA5"},
		Route: func(logging.Secret) string { return base + "/api/" }})
	if err != nil {
		return err
	}
	conn := slack.Conn{ID: 5, PublicID: "CNAAAAAAAAAAA5", Mode: slack.EventModeHTTP, Client: client}
	presses := &slack.Presses{Bindings: probeSlackBindings{}, Links: probeLinks{}, Commands: probeCommands{}, Keys: k,
		Path: deliverytest.Unlimited(1, clocks), Budget: 2 * time.Second, Business: clocks.Business,
		PublicURL: "http://localhost:8080", Log: logger}
	mux := http.NewServeMux()
	mux.Handle(slack.CallbackPattern, requestlog.Inbound(writer, clocks.Real, requestlog.OperationInteractive,
		slack.NewCallback(slack.CallbackConfig{Connections: probeSlackConn{conn: conn, secret: logging.Secret(signing)},
			Presses: presses, Real: clocks.Real, Base: ctx, Log: logger})))
	verification := probeWord()
	payload := func(user, responseURL string) []byte {
		b, _ := json.Marshal(map[string]any{"type": "block_actions", "token": verification, "api_app_id": "A0PROBE",
			"user": map[string]any{"id": user}, "container": map[string]any{"type": "message_attachment",
				"message_ts": "1.000001", "channel_id": "C0PROBE"}, "channel": map[string]any{"id": "C0PROBE"},
			"response_url": responseURL, "actions": []any{map[string]any{"action_id": "ack", "type": "button",
				"value": signed}}}) // plain values always encode
		return b
	}
	var errs []error
	var sigs []string
	post := func(user string, age time.Duration, forge bool, want int) {
		body := "payload=" + url.QueryEscape(string(payload(user, base+"/actions/T0PROBE/1/"+hooksToken)))
		ts := strconv.FormatInt(clocks.Real.Now().Add(-age).Unix(), 10)
		sig := fakeslack.Sign(signing, ts, []byte(body))
		if forge {
			sig = "v0=" + probeWord()
		}
		sigs = append(sigs, sig)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, slack.CallbackPath+conn.PublicID,
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set(slack.HeaderTimestamp, ts)
		req.Header.Set(slack.HeaderSignature, sig)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		errs = append(errs, errors.New(rec.Body.String()))
		if rec.Code != want {
			errs = append(errs, &probeIncomplete{fmt.Sprintf("a Slack press answered %d, want %d", rec.Code, want)})
		}
	}
	post("u-other", 0, true, http.StatusUnauthorized)
	post("u-other", 10*time.Minute, false, http.StatusUnauthorized)
	post("u-other", 0, false, http.StatusOK)
	post("u-linked", 0, false, http.StatusOK)
	handled, stop := context.WithTimeout(ctx, 10*time.Second)
	if presses.Wait(handled) != nil {
		errs = append(errs, &probeIncomplete{"a confirmed Slack press was not handled"})
	}
	stop()
	presses.Handle(ctx, conn, payload("u-other", "https://evil.example.org/actions/T0PROBE/1/"+hooksToken))
	linesMu.Lock()
	logged := lines.String()
	linesMu.Unlock()
	rejected, pressLines := strings.Count(logged, `"event":"slack_press_rejected"`), strings.Count(logged,
		`"event":"slack_press"`)
	switch {
	case rejected != 2 || pressLines != 3:
		errs = append(errs, &probeIncomplete{fmt.Sprintf("%d slack_press_rejected and %d slack_press lines, want 2 and 3",
			rejected, pressLines)})
	case ephemerals.Load() != 3 || responses.Load() != 2:
		errs = append(errs, &probeIncomplete{fmt.Sprintf("%d calls of chat.postEphemeral and %d of the response_url, "+
			"want 3 and 2", ephemerals.Load(), responses.Load())})
	}
	rows.mu.Lock()
	stored := slices.Clone(rows.rows)
	rows.mu.Unlock()
	ops := map[string]int{}
	var values []string
	for _, r := range stored {
		ops[r.operation]++
		values = append(values, r.values...)
	}
	if ops[requestlog.OperationInteractive] != 4 || ops["chat.postEphemeral"] != 3 || ops["response_url"] != 2 {
		errs = append(errs, &probeIncomplete{fmt.Sprintf("records %v, want 4 interactions, 3 chat.postEphemeral and 2 "+
			"response_url", ops)})
	}
	leaks := []probeRLValue{{"the verification token of a Slack press", verification}, {"a signed action id", signed},
		{"the path of a response_url", "/actions/T0PROBE"}}
	for _, sig := range sigs {
		leaks = append(leaks, probeRLValue{"the value of X-Slack-Signature", sig})
	}
	returned := strings.Join(errorTexts(errors.Join(errs...), 0), "\n")
	var tokenLeaks []string
	for _, f := range leakForms(hooksToken) {
		leaks = append(leaks, probeRLValue{"the token of a response_url", f.text})
		if strings.Contains(logged, f.text) {
			tokenLeaks = append(tokenLeaks, "the token of a response_url reached the log output")
		}
		if strings.Contains(returned, f.text) {
			tokenLeaks = append(tokenLeaks, "the token of a response_url reached the returned error")
		}
	}
	if len(tokenLeaks) > 0 {
		slices.Sort(tokenLeaks)
		errs = append(errs, &probeLeak{what: slices.Compact(tokenLeaks)})
	}
	return errors.Join(append(errs, probeRLLeaks(stored, leaks),
		fmt.Errorf("request_log rows: %s", strings.Join(values, "\n")))...)
}

// lockedWriter is w written under mu, for log lines written by several goroutines and read by the probe.
type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// probeSlackConn is the Slack Connection of probeSlackPresses: conn, in http mode, with the Slack signing secret
// secret.
type probeSlackConn struct {
	conn   slack.Conn
	secret logging.Secret
}

func (p probeSlackConn) SlackPressConnection(_ context.Context, publicID string) (slack.Conn, logging.Secret, error) {
	if publicID != p.conn.PublicID {
		return slack.Conn{}, "", slack.ErrNoConnection
	}
	return p.conn, p.secret, nil
}

// probeSlackBindings bind every press to the Root message of the Alert Group AGAAAAAAAAAAA1 on the Slack Destination 3.
type probeSlackBindings struct{}

func (probeSlackBindings) SlackPressBinding(_ context.Context, conn int64, _, _ string) (delivery.SlackBinding, error) {
	return delivery.SlackBinding{Kind: buttons.SubjectRoot, GroupPublicID: "AGAAAAAAAAAAA1", AlertGroup: 1,
		Destination: delivery.Destination{ID: 3, PublicID: "DSAAAAAAAAAAA3", Type: delivery.TypeSlack,
			Connection: &conn}}, nil
}

func (probeSlackBindings) SlackTestDestination(context.Context, int64, string, string) (delivery.Destination, bool,
	error) {
	return delivery.Destination{}, false, nil
}
