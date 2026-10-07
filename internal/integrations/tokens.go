// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package integrations

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/integrations/dbgen"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/tokens"
)

// Unknown is the integration label of an ingestion request whose token matches no Integration that is not deleted
// (C-05.FR-4).
const Unknown = "unknown"

const (
	// valueLength is the length of a token after its prefix: 32 random bytes in base32 without padding.
	valueLength = 52
	// touchInterval is how often at most last_used_at is written, and touchQueue how many writes may wait.
	touchInterval = time.Minute
	touchQueue    = 256
)

// ErrInvalidToken is an ingestion request whose token is missing, malformed, unknown or revoked, or belongs to a
// deleted Integration; it never says which.
var ErrInvalidToken = errors.New("the integration token is not valid")

// Token is an Integration token as the API lists it; its value is never kept.
type Token struct {
	ID         int64
	PublicID   string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// CreatedToken is a token that was just issued, with its value and the Alertmanager snippet that carries it, which
// are shown this once. Integration is the name of its Integration, and Heartbeat whether that Integration's Heartbeat
// is on, so that the Heartbeat snippet can carry the token too.
type CreatedToken struct {
	Token       Token
	Value       string
	Snippet     string
	Integration string
	Heartbeat   bool
}

// Caller is who sends an ingestion request: the token and its Integration.
type Caller struct {
	TokenID       int64
	IntegrationID int64
	// Integration is the public_id of the token's Integration, the integration label of the request's metrics.
	Integration string
}

// wellFormed reports whether value has the shape of an Integration token: the prefix and 52 characters of lowercase
// base32.
func wellFormed(value string) bool {
	rest, ok := strings.CutPrefix(value, tokens.PrefixIntegration)
	if !ok || len(rest) != valueLength {
		return false
	}
	for _, c := range rest {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// hash is the SHA-256 of the whole token, prefix included, as integration_tokens.token_hash stores it.
func hash(value string) []byte {
	return tokens.Hash(value)
}

// Authenticate finds who sends an ingestion request with the token value (C-05.FR-3): a token of an Integration that
// is not deleted, not revoked. Anything else is ErrInvalidToken, returned with the Caller whose Integration names the
// metric label: the token's Integration when the token is known — a revoked one included, so that an old token still
// in use is seen — and Unknown when it matches no Integration or only a deleted one (C-05.FR-4, P-10). The use of a
// token is recorded at most once a minute, off the request path, so that ingestion stays insert-only.
func (s *Service) Authenticate(ctx context.Context, value string) (Caller, error) {
	unknown := Caller{Integration: Unknown}
	if !wellFormed(value) {
		return unknown, ErrInvalidToken
	}
	h := hash(value)
	row, err := s.store.FindIngestToken(ctx, dbgen.FindIngestTokenParams{OrgID: s.orgID, TokenHash: h})
	if errors.Is(err, pgx.ErrNoRows) {
		return unknown, ErrInvalidToken
	}
	if err != nil {
		return unknown, fmt.Errorf("find the integration token: %w", err)
	}
	if subtle.ConstantTimeCompare(row.TokenHash, h) != 1 || row.IntegrationDeletedAt.Valid {
		return unknown, ErrInvalidToken
	}
	c := Caller{TokenID: row.ID, IntegrationID: row.IntegrationID, Integration: row.IntegrationPublicID}
	if row.RevokedAt.Valid {
		return c, ErrInvalidToken
	}
	now := s.clock.Now().UTC()
	if !row.LastUsedAt.Valid || now.Sub(row.LastUsedAt.Time) >= touchInterval {
		s.touches.use(row.ID, now)
	}
	return c, nil
}

// touches are the uses of tokens waiting to be written to last_used_at by RunTouches, at most one a minute per token.
type touches struct {
	mu    sync.Mutex
	sent  map[int64]time.Time
	queue chan int64
}

func newTouches() touches {
	return touches{sent: map[int64]time.Time{}, queue: make(chan int64, touchQueue)}
}

// use queues the write of a use of the token id unless one was queued within the last minute; a full queue drops it,
// and a later use queues it again.
func (t *touches) use(id int64, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.sent[id]; ok && now.Sub(last) < touchInterval && !now.Before(last) {
		return
	}
	// Uses older than a minute decide nothing any more; dropping them bounds the map by the tokens used lately.
	for other, last := range t.sent {
		if now.Sub(last) >= touchInterval || now.Before(last) {
			delete(t.sent, other)
		}
	}
	select {
	case t.queue <- id:
		t.sent[id] = now
	default:
	}
}

// RunTouches writes the queued uses of tokens to last_used_at until ctx ends. A failed write is dropped: the next use
// after a minute writes it again.
func (s *Service) RunTouches(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.touches.queue:
			now := s.clock.Now().UTC()
			_ = s.store.TouchIntegrationToken(ctx, dbgen.TouchIntegrationTokenParams{
				OrgID: s.orgID, ID: id, Now: now, StaleBefore: now.Add(-touchInterval),
			})
		}
	}
}

// ListTokens lists the tokens of the Integration publicID that are not revoked, the newest first.
func (s *Service) ListTokens(ctx context.Context, publicID string) ([]Token, error) {
	in, err := s.get(ctx, s.store, publicID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListIntegrationTokens(ctx, dbgen.ListIntegrationTokensParams{OrgID: s.orgID,
		IntegrationID: in.ID})
	if err != nil {
		return nil, fmt.Errorf("list the tokens of the integration %s: %w", in.PublicID, err)
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, Token{ID: r.ID, PublicID: r.PublicID, Name: r.Name.String, CreatedAt: r.CreatedAt.UTC(),
			LastUsedAt: timeOf(r.LastUsedAt)})
	}
	return out, nil
}

// CreateToken issues a token of the Integration publicID with an optional name; the value and the Alertmanager
// snippet are returned once and only the hash is stored.
func (s *Service) CreateToken(ctx context.Context, r Requester, publicID, name string) (CreatedToken, error) {
	name = strings.TrimSpace(name)
	if name != "" {
		if err := checkName("/name", name); err != nil {
			return CreatedToken{}, err
		}
	}
	value, h, err := tokens.Generate(tokens.PrefixIntegration)
	if err != nil {
		return CreatedToken{}, err
	}
	now := s.clock.Now().UTC()
	t := Token{PublicID: publicid.New(publicid.IntegrationToken), Name: name, CreatedAt: now}
	var in Integration
	err = s.store.InTx(ctx, func(q Queries) error {
		var err error
		if in, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if in.Builtin {
			return ErrBuiltinImmutable
		}
		if t.ID, err = q.InsertIntegrationToken(ctx, dbgen.InsertIntegrationTokenParams{
			OrgID: s.orgID, PublicID: t.PublicID, IntegrationID: in.ID, Name: text(name), TokenHash: h, Now: now,
		}); err != nil {
			return fmt.Errorf("store the token of the integration %s: %w", in.PublicID, err)
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionTokenCreated,
			Resource: audit.Resource{Type: ResourceToken, PublicID: t.PublicID, Name: t.Name},
			Details:  map[string]any{"integration": in.PublicID}, SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: in.PublicID})
	})
	if err != nil {
		return CreatedToken{}, err
	}
	return CreatedToken{Token: t, Value: value, Snippet: Snippet(in.Name, s.IngestURL(), value), Integration: in.Name,
		Heartbeat: in.Heartbeat.Enabled}, nil
}

// RevokeToken revokes the token tokenID of the Integration publicID: it answers 401 from the next request. A token of
// another Integration, an unknown or an already revoked one is ErrNotFound.
func (s *Service) RevokeToken(ctx context.Context, r Requester, publicID, tokenID string) error {
	tid, err := publicid.Parse(publicid.IntegrationToken, tokenID)
	if err != nil {
		return ErrNotFound
	}
	return s.store.InTx(ctx, func(q Queries) error {
		in, err := s.lock(ctx, q, publicID)
		if err != nil {
			return err
		}
		row, err := q.RevokeIntegrationToken(ctx, dbgen.RevokeIntegrationTokenParams{
			OrgID: s.orgID, IntegrationID: in.ID, PublicID: tid, Now: s.clock.Now().UTC(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("revoke the token %s: %w", tid, err)
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionTokenRevoked,
			Resource: audit.Resource{Type: ResourceToken, PublicID: tid, Name: row.Name.String},
			Details:  map[string]any{"integration": in.PublicID}, SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: in.PublicID})
	})
}

// encodeLabels is the jsonb of Static labels: an object, empty when there are none.
func encodeLabels(labels map[string]string) ([]byte, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("encode the static labels: %w", err)
	}
	return b, nil
}

func decodeLabels(b []byte) (map[string]string, error) {
	labels := map[string]string{}
	if err := json.Unmarshal(b, &labels); err != nil {
		return nil, err
	}
	return labels, nil
}
