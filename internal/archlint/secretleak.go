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
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/doctor"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/runtime"
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
