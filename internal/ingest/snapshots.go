// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// ErrNotFound is a Stored Snapshot that does not exist in the Organization or is older than
// retention.stored_snapshots.
var ErrNotFound = errors.New("no such stored snapshot")

// Ref names an Integration: its public_id and its name.
type Ref struct {
	PublicID string
	Name     string
}

// Summary is a Stored Snapshot as the list shows it.
type Summary struct {
	ID              int64
	PublicID        string
	Integration     Ref
	ReceivedAt      time.Time
	ProcessedAt     *time.Time
	SizeBytes       int64
	State           string
	ProcessingError *string
	GroupKey        *string
	AlertCount      *int64
	TruncatedAlerts *int64
}

// Snapshot is a Stored Snapshot with its body exactly as received and the request's Content-Type, if any.
type Snapshot struct {
	Summary
	Body        []byte
	ContentType *string
}

// Position is the sort key of a Stored Snapshot in the list: newest first by receipt time, then by id.
type Position struct {
	ReceivedAt time.Time
	ID         int64
}

// ListFilter selects a page of the Stored Snapshots of the Integration whose public_id is Integration, received in
// [From, To) and in States when they are set, after the position After when it is set.
type ListFilter struct {
	Integration string
	From, To    *time.Time
	States      []string
	After       *Position
	Limit       int
}

// Page is a page of Stored Snapshots, newest first; Next, the position to continue after, is nil on the last page.
type Page struct {
	Snapshots []Summary
	Next      *Position
}

// notBefore is the oldest receipt time the API shows: retention.stored_snapshots before now on the business clock.
// Partitions are dropped by day, so the API hides what is past the period while its partition still exists.
func (s *Service) notBefore(ctx context.Context) (time.Time, error) {
	days, err := s.store.GetRetention(ctx, s.orgID)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the retention of stored snapshots: %w", err)
	}
	return s.clock.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour), nil
}

// List lists the Stored Snapshots of an Integration, a deleted one included, newest first; an unknown Integration has
// none.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	pid, err := publicid.Parse(publicid.Integration, f.Integration)
	if err != nil {
		return Page{}, nil //nolint:nilerr // an id of no Integration has no Stored Snapshots; it is no error
	}
	in, err := s.store.FindSnapshotIntegration(ctx, dbgen.FindSnapshotIntegrationParams{OrgID: s.orgID, PublicID: pid})
	if errors.Is(err, pgx.ErrNoRows) {
		return Page{}, nil
	}
	if err != nil {
		return Page{}, fmt.Errorf("find the integration %s: %w", pid, err)
	}
	notBefore, err := s.notBefore(ctx)
	if err != nil {
		return Page{}, err
	}
	limit := min(max(f.Limit, 1), 1000)
	p := dbgen.ListStoredSnapshotsParams{OrgID: s.orgID, IntegrationID: in.ID, NotBefore: notBefore,
		From: timestamptz(f.From), To: timestamptz(f.To), States: f.States, PageSize: int32(limit) + 1}
	if p.States == nil {
		p.States = []string{}
	}
	if f.After != nil {
		p.AfterAt = pgtype.Timestamptz{Time: f.After.ReceivedAt, Valid: true}
		p.AfterID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	rows, err := s.store.ListStoredSnapshots(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the stored snapshots of %s: %w", in.PublicID, err)
	}
	ref := Ref{PublicID: in.PublicID, Name: in.Name}
	var page Page
	for i, r := range rows {
		if i == limit {
			last := page.Snapshots[limit-1]
			page.Next = &Position{ReceivedAt: last.ReceivedAt, ID: last.ID}
			break
		}
		page.Snapshots = append(page.Snapshots, Summary{
			ID: r.ID, PublicID: r.PublicID, Integration: ref, ReceivedAt: r.ReceivedAt.UTC(),
			ProcessedAt: timeOf(r.ProcessedAt), SizeBytes: r.SizeBytes, State: r.State,
			ProcessingError: textOf(r.ProcessingError), GroupKey: textOf(r.GroupKey), AlertCount: int8Of(r.AlertCount),
			TruncatedAlerts: int8Of(r.TruncatedAlerts),
		})
	}
	return page, nil
}

// Get reads the Stored Snapshot publicID with its body; one older than retention.stored_snapshots is ErrNotFound.
func (s *Service) Get(ctx context.Context, publicID string) (Snapshot, error) {
	pid, err := publicid.Parse(publicid.StoredSnapshot, publicID)
	if err != nil {
		return Snapshot{}, ErrNotFound
	}
	notBefore, err := s.notBefore(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	day := time.Date(notBefore.Year(), notBefore.Month(), notBefore.Day(), 0, 0, 0, 0, time.UTC)
	r, err := s.store.GetStoredSnapshot(ctx, dbgen.GetStoredSnapshotParams{OrgID: s.orgID, PublicID: pid,
		NotBefore: notBefore, NotBeforeDay: pgtype.Date{Time: day, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read the stored snapshot %s: %w", pid, err)
	}
	return Snapshot{
		Summary: Summary{
			ID: r.ID, PublicID: r.PublicID, Integration: Ref{PublicID: r.IntegrationPublicID, Name: r.IntegrationName},
			ReceivedAt: r.ReceivedAt.UTC(), ProcessedAt: timeOf(r.ProcessedAt), SizeBytes: r.SizeBytes, State: r.State,
			ProcessingError: textOf(r.ProcessingError), GroupKey: textOf(r.GroupKey), AlertCount: int8Of(r.AlertCount),
			TruncatedAlerts: int8Of(r.TruncatedAlerts),
		},
		Body: r.Body, ContentType: textOf(r.ContentType),
	}, nil
}

func timestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func int8Of(n pgtype.Int8) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

// Retention is retention.stored_snapshots: how long the Organization keeps its Stored Snapshots.
func (s *Service) Retention(ctx context.Context) (time.Duration, error) {
	days, err := s.store.GetRetention(ctx, s.orgID)
	if err != nil {
		return 0, fmt.Errorf("read the retention of stored snapshots: %w", err)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// FiringAlert is an Alert reported firing in a Stored Snapshot, with the current Static labels of its Integration
// applied as processing applies them.
type FiringAlert struct {
	IntegrationID int64
	Fingerprint   string
	Labels        map[string]string
}

// FiringRead is how a read of the firing Alerts ended: Bodies is how many distinct bodies it read, and Unread whether
// it stopped with Stored Snapshots of the period left unread.
type FiringRead struct {
	Bodies int
	Unread bool
}

// The bounds of a read of firing Alerts: the bodies, by count and by size, one query reads.
const (
	firingBodyBatch  = 64
	firingBatchBytes = 8 << 20
)

// firingPage is how many distinct bodies one query lists; a variable for the tests.
var firingPage int32 = 2000

// Firing reads the webhook Stored Snapshots received since since that did not fail, newest first and each distinct
// body of an Integration once, and calls visit with the Alerts each reports firing, the Integration's current Static
// labels applied; a body that does not parse, or that has none firing, is skipped. visit returns false to stop the
// read. It reads a bounded number of rows and bodies at a time, whatever the period holds.
func (s *Service) Firing(ctx context.Context, since time.Time, visit func([]FiringAlert) bool) (FiringRead, error) {
	rows, err := s.store.ListPreviewIntegrations(ctx, s.orgID)
	if err != nil {
		return FiringRead{}, fmt.Errorf("list the static labels of the integrations: %w", err)
	}
	static := make(map[int64]map[string]string, len(rows))
	for _, r := range rows {
		m := map[string]string{}
		if err := json.Unmarshal(r.StaticLabels, &m); err != nil {
			return FiringRead{}, fmt.Errorf("read the static labels of integration %d: %w", r.ID, err)
		}
		static[r.ID] = m
	}
	since, now := since.UTC(), s.clock.Now().UTC()
	// Stored Snapshots received while it reads are left out, so that the newest receipt of a body never moves above
	// the cursor.
	c := &bodyCursor{q: s.store, orgID: s.orgID, since: since, until: now.Add(time.Microsecond), day: dayOf(now),
		first: dayOf(since), seen: map[bodyKey]bool{}}
	var read FiringRead
	for {
		batch, err := c.batch(ctx)
		if err != nil || len(batch) == 0 {
			return read, err
		}
		bodies, err := s.bodiesOf(ctx, batch)
		if err != nil {
			return read, err
		}
		for i, b := range batch {
			body, ok := bodies[bodyAt{sha: string(b.row.BodySha256), day: b.day}]
			if !ok {
				continue // dropped by retention since it was listed
			}
			read.Bodies++
			alerts := firingOf(body, b.row.IntegrationID, static[b.row.IntegrationID])
			if len(alerts) == 0 || visit(alerts) {
				continue
			}
			if i < len(batch)-1 {
				read.Unread = true
				return read, nil
			}
			_, more, err := c.next(ctx)
			read.Unread = more
			return read, err
		}
	}
}

// firingOf is the Alerts a body reports firing, with the Static labels applied; none when it does not parse.
func firingOf(body []byte, integrationID int64, static map[string]string) []FiringAlert {
	p, err := ParsePayload(body)
	if err != nil {
		return nil
	}
	var out []FiringAlert
	for _, a := range p.Alerts {
		if a.Status != StatusFiring {
			continue
		}
		labels, _ := withStaticLabels(a.Labels, static)
		out = append(out, FiringAlert{IntegrationID: integrationID, Fingerprint: a.Fingerprint, Labels: labels})
	}
	return out
}

// bodyAt is a stored body: its hash and its UTC day.
type bodyAt struct {
	sha string
	day time.Time
}

// bodiesOf reads the bodies of a batch, each hash and day once.
func (s *Service) bodiesOf(ctx context.Context, batch []listedBody) (map[bodyAt][]byte, error) {
	p := dbgen.ListPreviewSnapshotBodiesParams{OrgID: s.orgID}
	asked := make(map[bodyAt]bool, len(batch))
	for _, b := range batch {
		k := bodyAt{sha: string(b.row.BodySha256), day: b.day}
		if asked[k] {
			continue
		}
		asked[k] = true
		p.BodySha256s = append(p.BodySha256s, b.row.BodySha256)
		p.BodyDays = append(p.BodyDays, pgtype.Date{Time: b.day, Valid: true})
	}
	rows, err := s.store.ListPreviewSnapshotBodies(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("read the bodies of stored snapshots: %w", err)
	}
	out := make(map[bodyAt][]byte, len(rows))
	for _, r := range rows {
		out[bodyAt{sha: string(r.BodySha256), day: dayOf(r.BodyDay.Time)}] = r.Body
	}
	return out, nil
}

// bodyKey is a distinct body of an Integration.
type bodyKey struct {
	integrationID int64
	sha           string
}

// listedBody is a distinct body as listed, with its UTC day.
type listedBody struct {
	row dbgen.ListPreviewBodiesRow
	day time.Time
}

// bodyCursor walks the distinct bodies of the webhook Stored Snapshots from the newest back, a page of one UTC day
// at a time, and yields each body of an Integration once.
type bodyCursor struct {
	q     Queries
	orgID int64
	// since and until bound the receipts read.
	since, until time.Time
	// day is the UTC day read now, and first the day of since, the last one to read.
	day, first time.Time
	page       []dbgen.ListPreviewBodiesRow
	pos        int
	// last is the cursor within day; dayDone says that day has no page after the current one.
	last    *dbgen.ListPreviewBodiesRow
	dayDone bool
	seen    map[bodyKey]bool
}

// next is the next distinct body not yielded yet; false when the period has no more.
func (c *bodyCursor) next(ctx context.Context) (listedBody, bool, error) {
	for {
		for c.pos < len(c.page) {
			r := c.page[c.pos]
			c.pos++
			k := bodyKey{integrationID: r.IntegrationID, sha: string(r.BodySha256)}
			if c.seen[k] {
				continue
			}
			c.seen[k] = true
			return listedBody{row: r, day: c.day}, true, nil
		}
		if c.dayDone {
			c.day, c.last, c.dayDone = c.day.AddDate(0, 0, -1), nil, false
		}
		if c.day.Before(c.first) {
			return listedBody{}, false, nil
		}
		from := c.day
		if c.since.After(from) {
			from = c.since
		}
		to := c.day.AddDate(0, 0, 1)
		if c.until.Before(to) {
			to = c.until
		}
		p := dbgen.ListPreviewBodiesParams{OrgID: c.orgID, BodyDay: pgtype.Date{Time: c.day, Valid: true},
			ReceivedFrom: from, ReceivedTo: to, PageSize: firingPage}
		if c.last != nil {
			p.AfterAt = pgtype.Timestamptz{Time: c.last.ReceivedAt, Valid: true}
			p.AfterIntegrationID = pgtype.Int8{Int64: c.last.IntegrationID, Valid: true}
			p.AfterSha256 = c.last.BodySha256
		}
		rows, err := c.q.ListPreviewBodies(ctx, p)
		if err != nil {
			return listedBody{}, false, fmt.Errorf("list the stored snapshots of %s: %w", c.day.Format(time.DateOnly), err)
		}
		c.page, c.pos, c.dayDone = rows, 0, len(rows) < int(firingPage)
		if len(rows) > 0 {
			c.last = &rows[len(rows)-1]
		}
	}
}

// batch is the next distinct bodies to read together: at most firingBodyBatch of them, and none more once they reach
// firingBatchBytes, so a batch holds at most that and one body.
func (c *bodyCursor) batch(ctx context.Context) ([]listedBody, error) {
	var out []listedBody
	size := int64(0)
	for len(out) < firingBodyBatch && size < firingBatchBytes {
		b, ok, err := c.next(ctx)
		if err != nil || !ok {
			return out, err
		}
		out = append(out, b)
		size += b.row.SizeBytes
	}
	return out, nil
}

// dayOf is the start of the UTC day of t.
func dayOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
