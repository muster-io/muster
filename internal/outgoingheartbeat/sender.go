// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package outgoingheartbeat sends the outgoing heartbeat (C-19.FR-7, ADR-0014): a request from the Leader to a dead
// man's switch once per outgoing_heartbeat.interval, so that a Muster that is down, or has no Leader, is noticed
// outside it. The URL is a Secret of the organization resource; the request goes through internal/outbound in the
// heartbeat client class, with the Organization's own heartbeat proxy, and is never retried or queued: the next
// interval is the retry. The result of the last attempt is kept on the organization row for the System status.
package outgoingheartbeat

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	orgdb "github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
)

const (
	// Interval is outgoing_heartbeat.interval, measured on the business clock.
	Interval = time.Minute
	// Method is outgoing_heartbeat.method; the request has an empty body.
	Method = http.MethodGet
	// Timeout is outgoing_heartbeat.timeout: one request, from the dial to the end of the answer.
	Timeout = 10 * time.Second

	connectTimeout = 5 * time.Second
	// maxAnswer is how much of the dead man's switch's answer is read; nothing of it is kept.
	maxAnswer = 64 << 10
	// maxError bounds the stored error of the last attempt, in characters.
	maxError = 500
)

// Queries are what the sender reads and writes.
type Queries interface {
	ClaimOutgoingHeartbeat(ctx context.Context, arg orgdb.ClaimOutgoingHeartbeatParams) (
		orgdb.ClaimOutgoingHeartbeatRow, error)
	RecordOutgoingHeartbeat(ctx context.Context, arg orgdb.RecordOutgoingHeartbeatParams) (pgtype.Bool, error)
	// Notify sends a live-update hint, in the transaction of the result.
	Notify(ctx context.Context, h db.Hint) error
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: newQueries(pool), pool: pool}
}

type pgQueries struct {
	*orgdb.Queries
	exec db.Execer
}

func newQueries(d orgdb.DBTX) pgQueries {
	return pgQueries{Queries: orgdb.New(d), exec: d}
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(newQueries(tx)) })
}

// Network is what the outbound client of an Organization needs.
type Network struct {
	// Policy is the outbound address policy of the Organization orgID.
	Policy func(orgID int64) outbound.PolicySource
	Log    *logging.Logger
	// Real is the real clock of the outbound client.
	Real clock.Clock
	// Resolver resolves host names; nil is the system resolver.
	Resolver outbound.Resolver
}

// Sender is the Leader task outgoing_heartbeat. Each run sends at most one request per Organization: the claim of the
// attempt, a conditional update of the organization row on the business clock, lets one run per interval through,
// so a run twice, on two Leaders at once or after a handover, sends once (ADR-0007).
type Sender struct {
	Store   Store
	Keyring *keyring.Keyring
	Network Network
	// Business is the business clock, which outgoing_heartbeat.interval and the attempt times follow.
	Business clock.Clock
	Log      *logging.Logger

	mu      sync.Mutex
	clients map[int64]cachedClient
}

type cachedClient struct {
	digest [32]byte
	client *outbound.Client
}

// Send sends the outgoing heartbeat of the Organization orgID when it has a URL and the interval since the last
// attempt has passed, and records the result. A failure is counted by the outbound client and logged as
// outgoing_heartbeat_failed at every attempt; the first success after a failure is logged as
// outgoing_heartbeat_recovered. A change of the result between ok and failed sends a system-status hint.
func (s *Sender) Send(ctx context.Context, orgID int64) error {
	now := s.Business.Now().UTC().Truncate(time.Microsecond)
	row, err := s.Store.ClaimOutgoingHeartbeat(ctx, orgdb.ClaimOutgoingHeartbeatParams{OrgID: orgID, Now: now,
		DueBefore: now.Add(-Interval)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no URL, or not due yet
	}
	if err != nil {
		return fmt.Errorf("claim the outgoing heartbeat: %w", err)
	}
	failure := s.send(ctx, orgID, row)
	if failure != "" {
		s.Log.Log(ctx, logging.OutgoingHeartbeatFailed, logging.F("error", failure))
	}
	recovered, err := s.record(ctx, orgID, now, failure)
	if err != nil {
		return err
	}
	if recovered {
		s.Log.Log(ctx, logging.OutgoingHeartbeatRecovered)
	}
	return nil
}

// send makes the request and returns why it failed, without the URL, or "" when it succeeded.
func (s *Sender) send(ctx context.Context, orgID int64, row orgdb.ClaimOutgoingHeartbeatRow) string {
	target, err := s.Keyring.OpenSecret(organization.FieldHeartbeatURL, keyring.StoredSecret{
		Ciphertext: row.OutgoingHeartbeatUrlCiphertext, KeyID: row.OutgoingHeartbeatUrlKeyID.String})
	if err != nil {
		return failedURL
	}
	password, err := s.Keyring.OpenSecret(organization.FieldHeartbeatProxyPassword, keyring.StoredSecret{
		Ciphertext: row.OutgoingHeartbeatProxyPasswordCiphertext, KeyID: row.OutgoingHeartbeatProxyPasswordKeyID.String})
	if err != nil {
		return failedPassword
	}
	proxy, err := proxyconf.Parse(row.OutgoingHeartbeatProxy)
	if err != nil {
		return failedProxy
	}
	client, err := s.client(orgID, target, proxy, password)
	if err != nil {
		return capped(failedClient + err.Error())
	}
	if _, err := client.Do(ctx, outbound.Request{Method: Method, URL: string(target)}); err != nil {
		return masked(err)
	}
	return ""
}

// record stores the result of the attempt claimed at attemptAt and announces a change between ok and failed; it
// reports whether the attempt succeeded after a failed one. The result of an attempt that a newer claim or a changed
// URL replaced is dropped.
func (s *Sender) record(ctx context.Context, orgID int64, attemptAt time.Time, failure string) (bool, error) {
	ok := failure == ""
	var recovered bool
	err := s.Store.InTx(ctx, func(q Queries) error {
		recovered = false
		previous, err := q.RecordOutgoingHeartbeat(ctx, orgdb.RecordOutgoingHeartbeatParams{OrgID: orgID,
			AttemptAt: attemptAt, Ok: ok, LastError: pgtype.Text{String: failure, Valid: !ok}})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("record the outgoing heartbeat: %w", err)
		}
		recovered = ok && previous.Valid && !previous.Bool
		if previous.Valid && previous.Bool == ok {
			return nil
		}
		return q.Notify(ctx, db.Hint{OrgID: orgID, Type: live.HintSystemStatus})
	})
	if err != nil {
		return false, err
	}
	return recovered, nil
}

// client is the outbound client of the Organization, built again when its URL, proxy or proxy password changed.
func (s *Sender) client(orgID int64, target logging.Secret, proxy proxyconf.Config,
	password logging.Secret) (*outbound.Client, error) {
	h := sha256.New()
	h.Write(proxy.JSON())
	for _, v := range []logging.Secret{target, password} {
		h.Write([]byte(strconv.Itoa(len(v)) + ":" + string(v)))
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[orgID]; ok && c.digest == digest {
		return c.client, nil
	}
	n := s.Network
	c, err := outbound.New(outbound.Config{Class: outbound.ClassHeartbeat, ConnectTimeout: connectTimeout,
		Timeout: Timeout, Proxy: proxy.Outbound(password), Secrets: secretsOf(target), Policy: n.Policy(orgID),
		Logger: n.Log, Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxAnswer})
	if err != nil {
		return nil, err
	}
	if s.clients == nil {
		s.clients = map[int64]cachedClient{}
	}
	s.clients[orgID] = cachedClient{digest: digest, client: c}
	return c, nil
}

// secretsOf are the parts of the URL that are redacted from every log line and error of the client: the URL, its
// path, its query and the password of its userinfo, any of which may carry the dead man's switch's token.
func secretsOf(target logging.Secret) []logging.Secret {
	out := []logging.Secret{target}
	u, err := url.Parse(string(target))
	if err != nil {
		return out
	}
	for _, part := range []string{u.Path, u.EscapedPath(), u.RawQuery, u.User.String()} {
		if len(part) > 1 {
			out = append(out, logging.Secret(part))
		}
	}
	if pw, ok := u.User.Password(); ok && pw != "" {
		out = append(out, logging.Secret(pw))
	}
	return out
}

// masked is what failed in a request, built from the fields of the outbound error and never from its text, which
// names the URL: the status and outcome of an answer, the rule that blocked it, or what failed without an answer.
// FailureOf reads its class back.
func masked(err error) string {
	e, ok := errors.AsType[*outbound.Error](err)
	switch {
	case !ok:
		return failedRequest
	case e.Status != 0:
		return fmt.Sprintf(answeredFormat, e.Status, e.Outcome)
	case e.Outcome == outbound.OutcomeBlocked:
		return capped(blockedPrefix + e.Rule)
	case e.Network != "":
		return capped(e.Network + ": " + e.Detail)
	}
	return string(e.Outcome)
}

// capped cuts s to maxError characters.
func capped(s string) string {
	if utf8.RuneCountInString(s) <= maxError {
		return s
	}
	return string([]rune(s)[:maxError])
}
