// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outgoingheartbeat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/keyring"
	keyringdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	orgdb "github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// fakeStore is the outgoing heartbeat columns of one organization row, with the claim and the record as the queries
// apply them.
type fakeStore struct {
	mu        sync.Mutex
	orgID     int64
	url       keyring.StoredSecret
	password  keyring.StoredSecret
	proxy     []byte
	attemptAt *time.Time
	lastOK    pgtype.Bool
	lastError pgtype.Text
	hints     []db.Hint
	fail      map[string]error
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	if err := s.fail["InTx"]; err != nil {
		return err
	}
	return f(s)
}

func (s *fakeStore) ClaimOutgoingHeartbeat(_ context.Context, arg orgdb.ClaimOutgoingHeartbeatParams) (
	orgdb.ClaimOutgoingHeartbeatRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail["ClaimOutgoingHeartbeat"]; err != nil {
		return orgdb.ClaimOutgoingHeartbeatRow{}, err
	}
	if arg.OrgID != s.orgID || !s.url.Set() || (s.attemptAt != nil && s.attemptAt.After(arg.DueBefore)) {
		return orgdb.ClaimOutgoingHeartbeatRow{}, pgx.ErrNoRows
	}
	at := arg.Now
	s.attemptAt = &at
	return orgdb.ClaimOutgoingHeartbeatRow{
		OutgoingHeartbeatUrlCiphertext: s.url.Ciphertext, OutgoingHeartbeatUrlKeyID: text(s.url.KeyID),
		OutgoingHeartbeatProxy: s.proxy, OutgoingHeartbeatProxyPasswordCiphertext: s.password.Ciphertext,
		OutgoingHeartbeatProxyPasswordKeyID: text(s.password.KeyID),
	}, nil
}

func (s *fakeStore) RecordOutgoingHeartbeat(_ context.Context, arg orgdb.RecordOutgoingHeartbeatParams) (
	pgtype.Bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail["RecordOutgoingHeartbeat"]; err != nil {
		return pgtype.Bool{}, err
	}
	if arg.OrgID != s.orgID || s.attemptAt == nil || !s.attemptAt.Equal(arg.AttemptAt) {
		return pgtype.Bool{}, pgx.ErrNoRows
	}
	previous := s.lastOK
	s.lastOK, s.lastError = pgtype.Bool{Bool: arg.Ok, Valid: true}, arg.LastError
	return previous, nil
}

func (s *fakeStore) Notify(_ context.Context, h db.Hint) error {
	if err := s.fail["Notify"]; err != nil {
		return err
	}
	s.hints = append(s.hints, h)
	return nil
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

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

func activeKeyring(t *testing.T) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{'h'}, keyring.KeySize)}, false)
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

// dms is a dead man's switch: it counts the requests and answers status.
type dms struct {
	mu       sync.Mutex
	methods  []string
	bodies   []int
	status   int
	received chan struct{}
}

func newDMS(t *testing.T) (*dms, *httptest.Server) {
	t.Helper()
	d := &dms{status: http.StatusOK, received: make(chan struct{}, 16)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.methods = append(d.methods, r.Method)
		d.bodies = append(d.bodies, int(r.ContentLength))
		status := d.status
		d.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return d, srv
}

func (d *dms) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.methods)
}

func (d *dms) answer(status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status = status
}

type fixture struct {
	store  *fakeStore
	sender *Sender
	clock  *clock.Manual
	log    *bytes.Buffer
	keys   *keyring.Keyring
}

func loopbackPolicy(t *testing.T) outbound.PolicySource {
	t.Helper()
	p, err := outbound.ParsePolicy("standard", []string{"127.0.0.1/32"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return outbound.StaticPolicy(p)
}

func newFixture(t *testing.T, target string) *fixture {
	t.Helper()
	k := activeKeyring(t)
	f := &fixture{store: &fakeStore{orgID: 1, proxy: []byte(`{"enabled": false}`), fail: map[string]error{}},
		clock: clock.NewManual(t0), log: &bytes.Buffer{}, keys: k}
	if target != "" {
		f.setURL(t, target)
	}
	log := logging.New(f.log, logging.LevelInfo)
	f.sender = &Sender{Store: f.store, Keyring: k, Business: f.clock, Log: log,
		Network: Network{Policy: func(int64) outbound.PolicySource { return loopbackPolicy(t) }, Log: log,
			Real: clock.Real{}}}
	return f
}

func (f *fixture) setURL(t *testing.T, target string) {
	t.Helper()
	ct, id, err := f.keys.Encrypt(organization.FieldHeartbeatURL, []byte(target))
	if err != nil {
		t.Fatal(err)
	}
	f.store.url = keyring.StoredSecret{Ciphertext: ct, KeyID: id}
}

func (f *fixture) send(t *testing.T) {
	t.Helper()
	if err := f.sender.Send(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
}

func counter(outcome outbound.Outcome) uint64 {
	return metrics.ClientRequests.With(string(outbound.ClassHeartbeat), string(outcome)).Get()
}

// TestOnePerInterval covers C-19.AC-7 on the sender: one GET with an empty body per outgoing_heartbeat.interval of
// the business clock, counted under client="heartbeat"; a run within the interval sends nothing.
func TestOnePerInterval(t *testing.T) {
	d, srv := newDMS(t)
	f := newFixture(t, srv.URL+"/ping/token")
	ok := counter(outbound.OutcomeOK)
	f.send(t)
	f.clock.Advance(Interval - time.Second)
	f.send(t)
	if d.count() != 1 {
		t.Fatalf("within the interval: %d requests", d.count())
	}
	f.clock.Advance(time.Second)
	f.send(t)
	if d.count() != 2 || !slices.Equal(d.methods, []string{"GET", "GET"}) || d.bodies[0] > 0 {
		t.Errorf("requests %v, bodies %v", d.methods, d.bodies)
	}
	if got := counter(outbound.OutcomeOK) - ok; got != 2 {
		t.Errorf("ok counted %d times", got)
	}
	if !f.store.lastOK.Bool || f.store.lastError.Valid || !f.store.attemptAt.Equal(t0.Add(Interval)) {
		t.Errorf("result %+v %+v at %v", f.store.lastOK, f.store.lastError, f.store.attemptAt)
	}
	// The first result is a change of the result: the System status hears of it, once.
	if len(f.store.hints) != 1 || f.store.hints[0] != (db.Hint{OrgID: 1, Type: live.HintSystemStatus}) {
		t.Errorf("hints %v", f.store.hints)
	}
}

// TestRunTwiceSameInterval: two runs at the same time, as two Leaders that overlap, send once.
func TestRunTwiceSameInterval(t *testing.T) {
	d, srv := newDMS(t)
	f := newFixture(t, srv.URL+"/ping")
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _ = f.sender.Send(t.Context(), 1) })
	}
	wg.Wait()
	if d.count() != 1 {
		t.Errorf("%d requests", d.count())
	}
}

// TestNothingWithoutURL: without a URL nothing is sent, recorded or logged.
func TestNothingWithoutURL(t *testing.T) {
	f := newFixture(t, "")
	f.send(t)
	if f.store.attemptAt != nil || f.log.Len() != 0 || len(f.store.hints) != 0 {
		t.Errorf("attempt %v, log %s", f.store.attemptAt, f.log)
	}
	if err := f.sender.Send(t.Context(), 2); err != nil {
		t.Errorf("another organization: %v", err)
	}
}

// TestFailureCountedNoRetry covers C-19.FR-7: a 503 is one request, counted as transient under client="heartbeat",
// stored as the last error without the URL, logged as outgoing_heartbeat_failed and announced; the next interval is
// the retry, and a failure again is logged again but not announced.
func TestFailureCountedNoRetry(t *testing.T) {
	d, srv := newDMS(t)
	f := newFixture(t, srv.URL+"/ping/secret-token")
	f.send(t)
	d.answer(http.StatusServiceUnavailable)
	transient := counter(outbound.OutcomeTransient)
	f.clock.Advance(Interval)
	f.send(t)
	if d.count() != 2 || counter(outbound.OutcomeTransient)-transient != 1 {
		t.Fatalf("%d requests, %d transient", d.count(), counter(outbound.OutcomeTransient)-transient)
	}
	if f.store.lastOK.Bool || f.store.lastError.String != "answered 503 (transient)" {
		t.Errorf("result %+v %q", f.store.lastOK, f.store.lastError.String)
	}
	if n := strings.Count(f.log.String(), `"event":"outgoing_heartbeat_failed"`); n != 1 ||
		!strings.Contains(f.log.String(), `"level":"WARN"`) || strings.Contains(f.log.String(), "secret-token") {
		t.Errorf("log:\n%s", f.log)
	}
	if len(f.store.hints) != 2 {
		t.Errorf("hints %v", f.store.hints)
	}
	f.clock.Advance(Interval)
	f.send(t)
	if d.count() != 3 || strings.Count(f.log.String(), `"event":"outgoing_heartbeat_failed"`) != 2 ||
		len(f.store.hints) != 2 {
		t.Errorf("a failure again: %d requests, hints %v", d.count(), f.store.hints)
	}
}

// TestRecovered: the first success after a failure is logged as outgoing_heartbeat_recovered and announced.
func TestRecovered(t *testing.T) {
	d, srv := newDMS(t)
	d.answer(http.StatusInternalServerError)
	f := newFixture(t, srv.URL+"/ping")
	f.send(t)
	d.answer(http.StatusOK)
	f.clock.Advance(Interval)
	f.send(t)
	if !strings.Contains(f.log.String(), `"event":"outgoing_heartbeat_recovered"`) || !f.store.lastOK.Bool ||
		f.store.lastError.Valid || len(f.store.hints) != 2 {
		t.Errorf("log %s, result %+v, hints %v", f.log, f.store.lastOK, f.store.hints)
	}
}

// TestThroughProxy: with the Organization's heartbeat proxy, the request goes through it, with its password.
func TestThroughProxy(t *testing.T) {
	d, srv := newDMS(t)
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0",
		fakeproxy.Options{Username: "hb", Password: "proxy-pw"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	f := newFixture(t, srv.URL+"/ping")
	user := "hb"
	f.store.proxy = proxyconf.Config{Enabled: true, Type: proxyconf.TypeHTTP, Address: proxy.Addr(),
		Username: &user}.JSON()
	ct, id, err := f.keys.Encrypt(organization.FieldHeartbeatProxyPassword, []byte("proxy-pw"))
	if err != nil {
		t.Fatal(err)
	}
	f.store.password = keyring.StoredSecret{Ciphertext: ct, KeyID: id}
	f.send(t)
	if d.count() != 1 || !slices.Contains(proxy.Targets(), strings.TrimPrefix(srv.URL, "http://")) {
		t.Errorf("%d requests, proxy targets %v", d.count(), proxy.Targets())
	}
	// A changed password builds the client again; the proxy refuses it.
	ct, id, _ = f.keys.Encrypt(organization.FieldHeartbeatProxyPassword, []byte("wrong-pw"))
	f.store.password = keyring.StoredSecret{Ciphertext: ct, KeyID: id}
	f.clock.Advance(Interval)
	f.send(t)
	if d.count() != 1 || f.store.lastOK.Bool || strings.Contains(f.store.lastError.String, "wrong-pw") ||
		f.store.lastError.String == "" {
		t.Errorf("a wrong password: %d requests, %q", d.count(), f.store.lastError.String)
	}
}

// TestStaleResultNotRecorded: the result of an attempt that a newer claim replaced is not stored.
func TestStaleResultNotRecorded(t *testing.T) {
	f := newFixture(t, "http://127.0.0.1:1/ping")
	later := t0.Add(time.Hour)
	f.store.attemptAt = &later
	f.store.lastOK = pgtype.Bool{Bool: true, Valid: true}
	recovered, err := f.sender.record(t.Context(), 1, t0, "")
	if err != nil || recovered || len(f.store.hints) != 0 {
		t.Errorf("record = %v, %v, hints %v", recovered, err, f.store.hints)
	}
}

// TestUndecryptable: a URL or a proxy password the Keyring cannot open, or a stored proxy it cannot read, is a failed
// attempt with a fixed error.
func TestUndecryptable(t *testing.T) {
	for want, spoil := range map[string]func(f *fixture){
		"the URL cannot be decrypted": func(f *fixture) { f.store.url.Ciphertext[len(f.store.url.Ciphertext)-1] ^= 1 },
		"the proxy password cannot be decrypted": func(f *fixture) {
			f.store.password = keyring.StoredSecret{Ciphertext: []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"),
				KeyID: f.store.url.KeyID}
		},
		"the stored proxy cannot be read": func(f *fixture) { f.store.proxy = []byte("[") },
		"the outbound client cannot be built": func(f *fixture) {
			f.store.proxy = []byte(`{"enabled": true, "type": "gopher", "address": "x:1"}`)
		},
	} {
		f := newFixture(t, "http://127.0.0.1:1/ping")
		spoil(f)
		f.send(t)
		if f.store.lastOK.Bool || !strings.HasPrefix(f.store.lastError.String, want) {
			t.Errorf("%s: %q", want, f.store.lastError.String)
		}
	}
}

func TestStoreErrors(t *testing.T) {
	down := errors.New("down")
	for _, name := range []string{"ClaimOutgoingHeartbeat", "InTx", "RecordOutgoingHeartbeat", "Notify"} {
		_, srv := newDMS(t)
		f := newFixture(t, srv.URL+"/ping")
		f.store.fail[name] = down
		if err := f.sender.Send(t.Context(), 1); !errors.Is(err, down) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestMaskedNeverHasURL: the error of every kind of failure is built from the fields of the outbound error.
func TestMaskedNeverHasURL(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&outbound.Error{Outcome: outbound.OutcomeFatal, Status: 404}, "answered 404 (fatal)"},
		{&outbound.Error{Outcome: outbound.OutcomeRedirect, Status: 302}, "answered 302 (redirect)"},
		{&outbound.Error{Outcome: outbound.OutcomeBlocked, Rule: "the always-blocked range 169.254.0.0/16"},
			"blocked by the outbound address policy: the always-blocked range 169.254.0.0/16"},
		{&outbound.Error{Outcome: outbound.OutcomeTransient, Network: outbound.NetworkConnect,
			Detail: "dial tcp 127.0.0.1:1: connect: connection refused"},
			"connect: dial tcp 127.0.0.1:1: connect: connection refused"},
		{&outbound.Error{Outcome: outbound.OutcomeFatal}, "fatal"},
		{errors.New(`GET "https://hc.example.org/secret": boom`), "the request failed"},
	}
	for _, tt := range tests {
		if got := masked(tt.err); got != tt.want {
			t.Errorf("masked(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
	long := &outbound.Error{Outcome: outbound.OutcomeTransient, Network: "tls", Detail: strings.Repeat("é", 2*maxError)}
	if got := masked(long); len([]rune(got)) != maxError {
		t.Errorf("a long error has %d characters", len([]rune(got)))
	}
}

func TestSecretsOf(t *testing.T) {
	got := secretsOf("https://user:pw@hc.example.org/ping/tok%2Fen?key=abc")
	for _, want := range []logging.Secret{"https://user:pw@hc.example.org/ping/tok%2Fen?key=abc", "/ping/tok/en",
		"/ping/tok%2Fen", "key=abc", "user:pw", "pw"} {
		if !slices.Contains(got, want) {
			t.Errorf("secrets %v lack %q", got, want)
		}
	}
	if got := secretsOf("http://hc.example.org/"); len(got) != 1 {
		t.Errorf("a URL without a path: %v", got)
	}
	if got := secretsOf("http://[::1"); len(got) != 1 {
		t.Errorf("an unparsable URL: %v", got)
	}
}
