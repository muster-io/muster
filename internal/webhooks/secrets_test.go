// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
	keyringdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/templates"
	"github.com/muster-io/muster/internal/webhooks/dbgen"
)

var (
	t0      = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	errBoom = errors.New("boom")
)

// fakeDest is a row of destinations as the package reads and writes it.
type fakeDest struct {
	id                       int64
	publicID, name, typ      string
	version                  int64
	mode                     string
	events                   string
	proxy                    string
	password                 keyring.StoredSecret
	signing, previous        keyring.StoredSecret
	signingAt, previousSince *time.Time
	secrets                  map[string]keyring.StoredSecret
}

// fakeDB is the database of the package in memory: the Destinations, the Audit log and the hints.
type fakeDB struct {
	dests []*fakeDest
	audit []auditdb.InsertAuditEntryParams
	hints []db.Hint
	fail  map[string]error
	users map[int64]dbgen.GetBodyUserRow
	sas   map[int64]dbgen.GetBodyServiceAccountRow
	rows  []dbgen.ListBodyAlertsRow
}

func newFakeDB() *fakeDB {
	return &fakeDB{fail: map[string]error{}, dests: []*fakeDest{
		{id: 1, publicID: "DSAAAAAAAAAAA1", name: "hook", typ: TypeWebhook, version: 3, mode: ModeEvents,
			events: `{"url":"http://127.0.0.1/hook","headers":[]}`, proxy: `{"enabled":false}`,
			secrets: map[string]keyring.StoredSecret{}},
		{id: 2, publicID: "DSAAAAAAAAAAA2", name: "ops", typ: "mattermost", version: 1,
			secrets: map[string]keyring.StoredSecret{}},
	}}
}

func (f *fakeDB) byPublicID(id string) *fakeDest {
	for _, d := range f.dests {
		if d.publicID == id {
			return d
		}
	}
	return nil
}

func (f *fakeDB) byID(id int64) *fakeDest {
	for _, d := range f.dests {
		if d.id == id {
			return d
		}
	}
	return nil
}

func ts(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func (f *fakeDB) GetWebhookDestination(_ context.Context, arg dbgen.GetWebhookDestinationParams) (
	dbgen.GetWebhookDestinationRow, error) {
	if err := f.fail["GetWebhookDestination"]; err != nil {
		return dbgen.GetWebhookDestinationRow{}, err
	}
	d := f.byPublicID(arg.PublicID)
	if d == nil {
		return dbgen.GetWebhookDestinationRow{}, pgx.ErrNoRows
	}
	return dbgen.GetWebhookDestinationRow{ID: d.id, PublicID: d.publicID, Name: d.name, Type: d.typ,
		Version: d.version, SigningSecretSet: d.signing.Set(), SigningSecretUpdatedAt: ts(d.signingAt),
		PreviousSigningSecretSince: ts(d.previousSince)}, nil
}

func (f *fakeDB) ListSecrets(_ context.Context, arg dbgen.ListSecretsParams) ([]dbgen.ListSecretsRow, error) {
	if err := f.fail["ListSecrets"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListSecretsRow
	for name, s := range f.byID(arg.DestinationID).secrets {
		out = append(out, dbgen.ListSecretsRow{Name: name, ValueUpdatedAt: *s.UpdatedAt})
	}
	slices.SortFunc(out, func(a, b dbgen.ListSecretsRow) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (f *fakeDB) GetTarget(_ context.Context, arg dbgen.GetTargetParams) (dbgen.GetTargetRow, error) {
	if err := f.fail["GetTarget"]; err != nil {
		return dbgen.GetTargetRow{}, err
	}
	d := f.byID(arg.ID)
	if d == nil || d.typ != TypeWebhook {
		return dbgen.GetTargetRow{}, pgx.ErrNoRows
	}
	row := dbgen.GetTargetRow{ID: d.id, PublicID: d.publicID, WebhookMode: pgtype.Text{String: d.mode, Valid: true},
		Proxy: []byte(d.proxy), ProxyPasswordCiphertext: d.password.Ciphertext,
		ProxyPasswordKeyID: text(d.password.KeyID), SigningSecretCiphertext: d.signing.Ciphertext,
		SigningSecretKeyID: text(d.signing.KeyID), PreviousSigningSecretCiphertext: d.previous.Ciphertext,
		PreviousSigningSecretKeyID: text(d.previous.KeyID)}
	if d.events != "" {
		row.WebhookEventsConfig = []byte(d.events)
	}
	return row, nil
}

func (f *fakeDB) ListSecretValues(_ context.Context, arg dbgen.ListSecretValuesParams) (
	[]dbgen.ListSecretValuesRow, error) {
	if err := f.fail["ListSecretValues"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListSecretValuesRow
	for name, s := range f.byID(arg.DestinationID).secrets {
		out = append(out, dbgen.ListSecretValuesRow{Name: name, ValueCiphertext: s.Ciphertext, ValueKeyID: s.KeyID})
	}
	return out, nil
}

func (f *fakeDB) InTx(_ context.Context, fn func(TxQueries) error) error { return fn(f) }

func (f *fakeDB) LockWebhookDestination(_ context.Context, arg dbgen.LockWebhookDestinationParams) (
	dbgen.LockWebhookDestinationRow, error) {
	if err := f.fail["LockWebhookDestination"]; err != nil {
		return dbgen.LockWebhookDestinationRow{}, err
	}
	d := f.byPublicID(arg.PublicID)
	if d == nil {
		return dbgen.LockWebhookDestinationRow{}, pgx.ErrNoRows
	}
	return dbgen.LockWebhookDestinationRow{ID: d.id, PublicID: d.publicID, Name: d.name, Type: d.typ,
		Version: d.version, SigningSecretCiphertext: d.signing.Ciphertext, SigningSecretKeyID: text(d.signing.KeyID),
		SigningSecretUpdatedAt: ts(d.signingAt), PreviousSigningSecretSince: ts(d.previousSince)}, nil
}

func (f *fakeDB) BumpDestinationVersion(_ context.Context, arg dbgen.BumpDestinationVersionParams) (int64, error) {
	if err := f.fail["BumpDestinationVersion"]; err != nil {
		return 0, err
	}
	d := f.byID(arg.ID)
	d.version++
	return d.version, nil
}

func (f *fakeDB) UpsertSecret(_ context.Context, arg dbgen.UpsertSecretParams) error {
	if err := f.fail["UpsertSecret"]; err != nil {
		return err
	}
	at := arg.Now
	f.byID(arg.DestinationID).secrets[arg.Name] = keyring.StoredSecret{Ciphertext: arg.ValueCiphertext,
		KeyID: arg.ValueKeyID, UpdatedAt: &at}
	return nil
}

func (f *fakeDB) DeleteSecret(_ context.Context, arg dbgen.DeleteSecretParams) (int64, error) {
	if err := f.fail["DeleteSecret"]; err != nil {
		return 0, err
	}
	d := f.byID(arg.DestinationID)
	if _, ok := d.secrets[arg.Name]; !ok {
		return 0, nil
	}
	delete(d.secrets, arg.Name)
	return 1, nil
}

func (f *fakeDB) RotateSigningSecret(_ context.Context, arg dbgen.RotateSigningSecretParams) (int64, error) {
	if err := f.fail["RotateSigningSecret"]; err != nil {
		return 0, err
	}
	d := f.byID(arg.ID)
	at := arg.Now
	d.previous, d.previousSince = keyring.StoredSecret{Ciphertext: arg.PreviousCiphertext,
		KeyID: arg.PreviousKeyID.String}, nil
	if arg.PreviousCiphertext != nil {
		d.previousSince = &at
	}
	d.signing, d.signingAt = keyring.StoredSecret{Ciphertext: arg.Ciphertext, KeyID: arg.KeyID.String}, &at
	d.version++
	return d.version, nil
}

func (f *fakeDB) RetirePreviousSigningSecret(_ context.Context, arg dbgen.RetirePreviousSigningSecretParams) (int64,
	error) {
	if err := f.fail["RetirePreviousSigningSecret"]; err != nil {
		return 0, err
	}
	d := f.byID(arg.ID)
	if !d.previous.Set() {
		return 0, pgx.ErrNoRows
	}
	d.previous, d.previousSince = keyring.StoredSecret{}, nil
	d.version++
	return d.version, nil
}

func (f *fakeDB) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	f.audit = append(f.audit, arg)
	return nil
}

func (f *fakeDB) Notify(_ context.Context, h db.Hint) error {
	f.hints = append(f.hints, h)
	return f.fail["Notify"]
}

func (f *fakeDB) GetBodyUser(_ context.Context, arg dbgen.GetBodyUserParams) (dbgen.GetBodyUserRow, error) {
	u, ok := f.users[arg.ID]
	if !ok {
		return u, pgx.ErrNoRows
	}
	return u, nil
}

func (f *fakeDB) GetBodyServiceAccount(_ context.Context, arg dbgen.GetBodyServiceAccountParams) (
	dbgen.GetBodyServiceAccountRow, error) {
	sa, ok := f.sas[arg.ID]
	if !ok {
		return sa, pgx.ErrNoRows
	}
	return sa, nil
}

func (f *fakeDB) ListBodyAlerts(context.Context, dbgen.ListBodyAlertsParams) ([]dbgen.ListBodyAlertsRow, error) {
	return f.rows, f.fail["ListBodyAlerts"]
}

// keyState is keyring_state in memory.
type keyState struct {
	keyring.Store
	row *keyringdb.GetKeyringStateRow
}

func (s *keyState) GetKeyringState(context.Context) (keyringdb.GetKeyringStateRow, error) {
	if s.row == nil {
		return keyringdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *keyState) CreateKeyringState(_ context.Context, p keyringdb.CreateKeyringStateParams) (int64, error) {
	s.row = &keyringdb.GetKeyringStateRow{ActiveKeyID: p.ActiveKeyID, CanaryKeyID: p.ActiveKeyID,
		CanaryCiphertext: p.CanaryCiphertext}
	return 1, nil
}

// activeKeyring is a Keyring of the materials whose first key is active.
func activeKeyring(t *testing.T, materials ...byte) *keyring.Keyring {
	t.Helper()
	var keys [][]byte
	for _, m := range materials {
		keys = append(keys, bytes.Repeat([]byte{m}, keyring.KeySize))
	}
	k, err := keyring.New(keys, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), &keyState{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	return k
}

func newService(t *testing.T) (*Service, *fakeDB) {
	t.Helper()
	f := newFakeDB()
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	return New(1, Config{Store: f, Writer: f, Keyring: activeKeyring(t, 'k'),
		Audit: audit.NewWriter(logger, clock.NewManual(t0)), Business: clock.NewManual(t0),
		PublicURL: "https://muster.example.org/"}), f
}

var requester = Requester{Actor: audit.User(1, "SRAAAAAAAAAAA1"), Transport: audit.TransportAPI}

// TestSecrets is C-15.FR-10: a Secret is set write-only, encrypted, recorded as destination.updated with the Secret
// marked changed and never its value; the list shows names with their status and the ETag of the version; If-Match
// guards the changes; a missing Secret, another type or Destination are refused.
func TestSecrets(t *testing.T) {
	s, f := newService(t)
	ctx := t.Context()
	sec, next, err := s.SetSecret(ctx, requester, "dsaaaaaaaaaaa1", new(int64(3)), "token", "s3cr3t-token-value")
	if err != nil || sec.Name != "token" || !sec.UpdatedAt.Equal(t0) || next != 4 {
		t.Fatalf("set %+v %d (%v)", sec, next, err)
	}
	stored := f.dests[0].secrets["token"]
	if bytes.Contains(stored.Ciphertext, []byte("s3cr3t")) {
		t.Fatal("the secret is stored in clear")
	}
	if plain, err := s.cfg.Keyring.OpenSecret(FieldSecret, stored); err != nil || plain != "s3cr3t-token-value" {
		t.Fatalf("open %v", err)
	}
	if len(f.audit) != 1 || f.audit[0].Action != ActionUpdated || bytes.Contains(f.audit[0].Diff, []byte("s3cr3t")) ||
		!bytes.Contains(f.audit[0].Diff, []byte("/secrets/token")) || len(f.hints) != 1 {
		t.Fatalf("audit %+v hints %+v", f.audit, f.hints)
	}
	list, err := s.ListSecrets(ctx, "DSAAAAAAAAAAA1")
	if err != nil || len(list.Items) != 1 || list.Items[0].Name != "token" || list.Version != 4 {
		t.Fatalf("list %+v (%v)", list, err)
	}
	if _, _, err := s.SetSecret(ctx, requester, "DSAAAAAAAAAAA1", new(int64(3)), "token", "x"); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("stale set = %v", err)
	}
	if err := s.DeleteSecret(ctx, requester, "DSAAAAAAAAAAA1", new(int64(3)), "token"); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("stale delete = %v", err)
	}
	if err := s.DeleteSecret(ctx, requester, "DSAAAAAAAAAAA1", nil, "token"); err != nil || len(f.dests[0].secrets) != 0 {
		t.Fatalf("delete %v", err)
	}
	if err := s.DeleteSecret(ctx, requester, "DSAAAAAAAAAAA1", nil, "token"); !errors.Is(err, ErrNoSecret) {
		t.Errorf("missing = %v", err)
	}
	for name, c := range map[string]struct {
		name  string
		value logging.Secret
		code  string
	}{
		"name":  {"1bad", "v", CodeInvalidFormat},
		"empty": {"ok", "", CodeRequired},
		"long":  {"ok", logging.Secret(strings.Repeat("x", maxSecretLength+1)), CodeTooLong},
	} {
		_, _, err := s.SetSecret(ctx, requester, "DSAAAAAAAAAAA1", nil, c.name, c.value)
		if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Code != c.code || !strings.Contains(fe.Error(), fe.Pointer) {
			t.Errorf("%s = %v", name, err)
		}
	}
	for id, want := range map[string]error{"DSAAAAAAAAAAA2": ErrNotWebhook, "DSAAAAAAAAAAA9": ErrNotFound,
		"bad": ErrNotFound} {
		if _, err := s.ListSecrets(ctx, id); !errors.Is(err, want) {
			t.Errorf("list %s = %v", id, err)
		}
		if _, _, err := s.SetSecret(ctx, requester, id, nil, "a", "b"); !errors.Is(err, want) {
			t.Errorf("set %s = %v", id, err)
		}
	}
	for _, name := range []string{"GetWebhookDestination", "ListSecrets", "LockWebhookDestination", "UpsertSecret",
		"BumpDestinationVersion", "DeleteSecret", "Notify"} {
		f.fail[name] = errBoom
		_, err1 := s.ListSecrets(ctx, "DSAAAAAAAAAAA1")
		_, _, err2 := s.SetSecret(ctx, requester, "DSAAAAAAAAAAA1", nil, "a", "b")
		f.dests[0].secrets["a"] = keyring.StoredSecret{UpdatedAt: &t0}
		err3 := s.DeleteSecret(ctx, requester, "DSAAAAAAAAAAA1", nil, "a")
		if !errors.Is(err1, errBoom) && !errors.Is(err2, errBoom) && !errors.Is(err3, errBoom) {
			t.Errorf("%s did not fail", name)
		}
		delete(f.fail, name)
	}
}

// TestValidate is the check of the request of the events mode on save (C-15.FR-1): the URL and the headers parse and
// run on a dry run with a placeholder for every Secret they read, whether it is set or not; errors name the field and,
// for a template, its line and column.
func TestValidate(t *testing.T) {
	sb := templates.New(clock.NewManual(t0), clock.Real{})
	c := EventsConfig{URL: " https://example.org/hook?key={{ .Secrets.key }} ", Headers: []Header{
		{Name: " Authorization ", Value: "Bearer {{ .Secrets.token }}"}, {Name: "X-Team", Value: "ops"}}}
	if err := Validate(sb, "/events", &c); err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://example.org/hook?key={{ .Secrets.key }}" || c.Headers[0].Name != "Authorization" {
		t.Errorf("not trimmed: %+v", c)
	}
	for name, cc := range map[string]struct {
		c       EventsConfig
		pointer string
		code    string
	}{
		"no url":      {EventsConfig{URL: " "}, "/events/url", CodeRequired},
		"long":        {EventsConfig{URL: "https://x/" + strings.Repeat("a", maxTemplateLength)}, "/events/url", CodeTooLong},
		"syntax":      {EventsConfig{URL: "https://x/{{ "}, "/events/url", templates.CodeSyntax},
		"function":    {EventsConfig{URL: "https://x/{{ nope }}"}, "/events/url", templates.CodeUnknownFunction},
		"runtime":     {EventsConfig{URL: "https://x/{{ len 3 }}"}, "/events/url", templates.CodeSyntax},
		"scheme":      {EventsConfig{URL: "file:///etc/passwd"}, "/events/url", CodeInvalidFormat},
		"many":        {EventsConfig{URL: "https://x", Headers: make([]Header, maxHeaders+1)}, "/events/headers", CodeTooLong},
		"empty name":  {EventsConfig{URL: "https://x", Headers: []Header{{Name: " "}}}, "/events/headers/0/name", CodeRequired},
		"bad name":    {EventsConfig{URL: "https://x", Headers: []Header{{Name: "a b"}}}, "/events/headers/0/name", CodeInvalidFormat},
		"reserved":    {EventsConfig{URL: "https://x", Headers: []Header{{Name: "Webhook-Signature"}}}, "/events/headers/0/name", CodeReserved},
		"duplicate":   {EventsConfig{URL: "https://x", Headers: []Header{{Name: "A", Value: "1"}, {Name: "a", Value: "2"}}}, "/events/headers/1/name", CodeInvalidFormat},
		"line break":  {EventsConfig{URL: "https://x", Headers: []Header{{Name: "A", Value: "1\n2"}}}, "/events/headers/0/value", CodeInvalidFormat},
		"bad value":   {EventsConfig{URL: "https://x", Headers: []Header{{Name: "A", Value: "{{ end }}"}}}, "/events/headers/0/value", templates.CodeSyntax},
		"non ascii":   {EventsConfig{URL: "https://x", Headers: []Header{{Name: "Ä"}}}, "/events/headers/0/name", CodeInvalidFormat},
		"separator":   {EventsConfig{URL: "https://x", Headers: []Header{{Name: "a:b"}}}, "/events/headers/0/name", CodeInvalidFormat},
		"no host url": {EventsConfig{URL: "https://"}, "/events/url", CodeInvalidFormat},
	} {
		err := Validate(sb, "/events", &cc.c)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != cc.pointer || fe.Code != cc.code {
			t.Errorf("%s = %v", name, err)
		}
	}
	err := Validate(sb, "/events", &EventsConfig{URL: "https://x/\n {{ nope }}"})
	if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Line != 2 || fe.Column != 5 {
		t.Errorf("position = %+v", err)
	}
}

// TestWarnings is the literal-credential warning (C-15.FR-10): a header named Authorization, or a URL whose user
// information or query holds a literal value, that reads no Secret, with the JSON Pointer of the field.
func TestWarnings(t *testing.T) {
	c := EventsConfig{URL: "https://user:pass@example.org/hook?key=abc", Headers: []Header{
		{Name: "authorization", Value: "Bearer abc"},
		{Name: "Authorization", Value: "Bearer {{ .Secrets.token }}"},
		{Name: "X-Key", Value: "abc"},
		{Name: "Authorization", Value: " "},
	}}
	got := Warnings("/events", c)
	want := []Warning{{Kind: WarningLiteralCredential, Field: "/events/url"},
		{Kind: WarningLiteralCredential, Field: "/events/headers/0/value"}}
	if !slices.Equal(got, want) {
		t.Fatalf("warnings %+v", got)
	}
	for _, url := range []string{
		"https://example.org/hook",
		"https://example.org/hook?key={{ .Secrets.key }}",
		"https://{{ .Secrets.user }}:{{ .Secrets.pass }}@example.org/",
		"https://example.org/{{ end }}",
		"https://example.org/?{{ .Secrets.q }}",
		"%zz{{ .Secrets.a }}",
		"",
	} {
		if w := Warnings("/events", EventsConfig{URL: url}); len(w) != 0 {
			t.Errorf("%q: %+v", url, w)
		}
	}
	for _, url := range []string{"https://u@example.org/", "https://example.org/?a={{ .Other }}",
		"https://example.org/?a={{ .Secrets.a }}&b=x"} {
		if w := Warnings("/events", EventsConfig{URL: url}); len(w) != 1 {
			t.Errorf("%q: no warning", url)
		}
	}
	if refs := SecretRefs(`{{ .Secrets.b }}{{ if .Secrets.a }}{{ range .X }}{{ .Secrets.c }}{{ else }}{{ with .Secrets.b }}{{ end }}{{ end }}{{ end }}{{ define "t" }}{{ template "t" .Secrets.d }}{{ end }}`); !slices.Equal(refs,
		[]string{"a", "b", "c", "d"}) {
		t.Errorf("refs %v", refs)
	}
	if refs := SecretRefs("{{"); refs != nil {
		t.Errorf("refs of a broken template %v", refs)
	}
	if !ValidSecretName("_a1") || ValidSecretName("a-b") {
		t.Error("secret names")
	}
	if c, err := ParseEventsConfig([]byte(`{"url":"u"}`)); err != nil || c.Headers == nil ||
		string(c.JSON()) != `{"url":"u","headers":[]}` {
		t.Errorf("parse %+v %v", c, err)
	}
	if _, err := ParseEventsConfig([]byte(`[`)); err == nil {
		t.Error("parsed a broken request")
	}
	if string((EventsConfig{URL: "u"}).JSON()) != `{"url":"u","headers":[]}` {
		t.Error("json")
	}
}
