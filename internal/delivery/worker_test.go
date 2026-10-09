// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	idb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

const orgID = 1

var (
	business0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	real0     = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	errBoom   = errors.New("boom")
)

// The tables of the fake database.
type (
	fakeDest struct {
		id                        int64
		publicID                  string
		name, typ                 string
		connection                *int64
		health                    string
		limit, per                int64
		deleted                   bool
		brokenSince, nextProbe    *time.Time
		brokenCause, brokenReason *string
		// probeOnNext is next_probe_at 'infinity': the next due delivery is the probe.
		probeOnNext bool
		// secrets are the encrypted secret columns that are set, and named its destination_secrets rows.
		secrets, named int
		// channel is the Mattermost channel.
		channel string
		// mode is the mode of an outgoing webhook, empty for none.
		mode string
		// tgChannel and tgGroup are the channel and the discussion group of a Telegram Destination, nil until known.
		tgChannel, tgGroup *int64
	}
	fakeRoute struct {
		language  string
		window    int64
		publicID  string
		name      string
		threshold int64
		snooze    []int64
	}
	fakeGroup struct {
		id         int64
		publicID   string
		number     int64
		title      string
		status     string
		urgent     bool
		route      int64
		created    time.Time
		resolvedAt *time.Time
	}
	fakeDelivery struct {
		id, dest, group                     int64
		state                               string
		urgent                              bool
		version                             int64
		text, buttonKeyID                   string
		payload, hash                       []byte
		receivedAt                          *time.Time
		loud                                pgtype.Bool
		actualVersion                       int64
		actualHash                          []byte
		messageID, messageURL               *string
		publishedAt, lastDelivered, started *time.Time
		publications, attempts              int64
		batchUntil                          *time.Time
		next                                time.Time
		owner                               string
		until                               time.Time
		errorClass, lastError               *string
		threadState                         string
		possibleDuplicate                   bool
		updated                             time.Time
		anchorID, chainLastID               *string
		firstFailed                         *time.Time
		lateNote, republished               bool
		// heldBy is held_by_storm_id and storm the storm_id of a Storm summary, 0 for none.
		heldBy, storm int64
		desiredRetire bool
	}
	fakeReply struct {
		id, delivery, group, dest int64
		event                     string
		seqs                      []int64
		loudness                  string
		mentions, fingerprints    []string
		state                     string
		next                      time.Time
		owner                     string
		until                     time.Time
		errorClass, lastError     *string
		sentAt                    *time.Time
		messageID                 *string
		created                   time.Time
		attempts                  int64
		firstFailed               *time.Time
	}
	fakeBucket struct {
		tokens   float64
		refilled time.Time
	}
	bucketKey struct {
		kind string
		id   int64
	}
)

// fakeDB is the database of the package in memory, with the semantics of its queries: due on the business clock,
// leased on the real clock, the token buckets of the limiters and the head-of-line order of Thread replies.
type fakeDB struct {
	mu         sync.Mutex
	dests      map[int64]*fakeDest
	routes     map[int64]fakeRoute
	routeDests map[int64][]int64
	groups     map[int64]*fakeGroup
	deliveries []*fakeDelivery
	replies    []*fakeReply
	buckets    map[bucketKey]*fakeBucket
	events     []dbgen.InsertDeliveryEventParams
	notified   int
	nextID     int64
	fail       map[string]error
	calls      map[string]int
	details    int64
	// before runs once, before a query of that name, for changes that race the worker.
	before map[string]func()
	// connLimits are the limiters of Connections, limit and per_seconds; connLimit per connPer otherwise.
	connLimits map[int64][2]int64
	// internal are the synthetic Snapshots of Internal alerts, and hints the live-update hints.
	internal []fakeInternal
	hints    []db.Hint
	// storms are the Storms, timers the deadlines of their calm checks, timersNotified the wakes of the timer workers,
	// and connDeleted the Connections whose deletion abandoned deliveries.
	storms         map[int64]*fakeStorm
	timers         map[int64]time.Time
	timersNotified int
	// membershipLocks are the Routes whose membership lock Enqueue took shared, in order.
	membershipLocks []int64
	// webhookEvents are the events of outgoing webhooks.
	webhookEvents []*fakeEvent
	// copies is the Telegram copy buffer, and postLocks the posts whose copy lock was taken, in order.
	copies    map[copyKey]*fakeCopy
	postLocks []copyKey
}

// sqlBool is a boolean of SQL — true, false or NULL — with its three-valued logic, so that the fake meets a NULL where
// a query does: a NULL from a nullable column in a boolean written to a NOT NULL column fails the statement.
type sqlBool struct{ v, null bool }

func sqlIs(b bool) sqlBool { return sqlBool{v: b} }

// sqlIn is s IN (values): NULL when s is NULL.
func sqlIn(s *string, values ...string) sqlBool {
	if s == nil {
		return sqlBool{null: true}
	}
	return sqlIs(slices.Contains(values, *s))
}

func (a sqlBool) and(b sqlBool) sqlBool {
	switch {
	case (!a.null && !a.v) || (!b.null && !b.v):
		return sqlIs(false)
	case a.null || b.null:
		return sqlBool{null: true}
	}
	return sqlIs(true)
}

func (a sqlBool) or(b sqlBool) sqlBool {
	switch {
	case (!a.null && a.v) || (!b.null && b.v):
		return sqlIs(true)
	case a.null || b.null:
		return sqlBool{null: true}
	}
	return sqlIs(false)
}

func (a sqlBool) coalesce(d bool) sqlBool {
	if a.null {
		return sqlIs(d)
	}
	return a
}

func (a sqlBool) isTrue() bool { return !a.null && a.v }

// notNull is the value of a NOT NULL column, or the error PostgreSQL gives for a NULL.
func notNull(column string, b sqlBool) (bool, error) {
	if b.null {
		return false, fmt.Errorf("null value in column %q of relation \"deliveries\" violates not-null constraint", column)
	}
	return b.v, nil
}

// fakeStorm is a row of storms.
type fakeStorm struct {
	id, route                    int64
	started                      time.Time
	calmSince, ended             *time.Time
	alertGroupCount, urgentCount int64
}

// fakeInternal is a raise or a resolve of an Internal alert.
type fakeInternal struct {
	status, name string
	labels       map[string]string
}

func newFakeDB() *fakeDB {
	return &fakeDB{dests: map[int64]*fakeDest{}, routes: map[int64]fakeRoute{}, routeDests: map[int64][]int64{},
		groups: map[int64]*fakeGroup{}, buckets: map[bucketKey]*fakeBucket{}, fail: map[string]error{},
		calls: map[string]int{}, before: map[string]func(){}, details: 90, nextID: 100,
		connLimits: map[int64][2]int64{}, storms: map[int64]*fakeStorm{}, timers: map[int64]time.Time{},
		copies: map[copyKey]*fakeCopy{}}
}

func (f *fakeDB) call(name string) error {
	f.calls[name]++
	if b := f.before[name]; b != nil {
		delete(f.before, name)
		f.mu.Unlock()
		b()
		f.mu.Lock()
	}
	return f.fail[name]
}

func (f *fakeDB) id() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeDB) delivery(id int64) *fakeDelivery {
	for _, d := range f.deliveries {
		if d.id == id {
			return d
		}
	}
	return nil
}

func (f *fakeDB) reply(id int64) *fakeReply {
	for _, r := range f.replies {
		if r.id == id {
			return r
		}
	}
	return nil
}

func tz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func txt(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func strOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func at(t time.Time) *time.Time { return &t }

func nullInt(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func (f *fakeDB) ListRouteDestinations(_ context.Context, arg dbgen.ListRouteDestinationsParams) (
	[]dbgen.ListRouteDestinationsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListRouteDestinations"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRouteDestinationsRow
	for _, id := range f.routeDests[arg.RouteID] {
		if d := f.dests[id]; d != nil && !d.deleted {
			out = append(out, dbgen.ListRouteDestinationsRow{ID: d.id, PublicID: d.publicID, Name: d.name,
				Type: d.typ, ConnectionID: nullInt(d.connection), Health: d.health,
				WebhookMode: pgtype.Text{String: d.mode, Valid: d.mode != ""}})
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListRouteDestinationsRow) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (f *fakeDB) GetRouteDelivery(_ context.Context, arg dbgen.GetRouteDeliveryParams) (dbgen.GetRouteDeliveryRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetRouteDelivery"); err != nil {
		return dbgen.GetRouteDeliveryRow{}, err
	}
	r, ok := f.routes[arg.ID]
	if !ok {
		return dbgen.GetRouteDeliveryRow{}, pgx.ErrNoRows
	}
	return dbgen.GetRouteDeliveryRow{PublicID: r.publicID, Name: r.name, Language: r.language,
		ThreadBatchingWindowSeconds: r.window, StormThreshold: r.threshold}, nil
}

func (f *fakeDB) ShareRouteMembership(_ context.Context, arg dbgen.ShareRouteMembershipParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if arg.LockClass != db.RouteMembershipLockClass {
		return errors.New("not the membership lock")
	}
	f.membershipLocks = append(f.membershipLocks, arg.RouteID)
	return f.call("ShareRouteMembership")
}

func (f *fakeDB) EnsureDelivery(_ context.Context, arg dbgen.EnsureDeliveryParams) (dbgen.EnsureDeliveryRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("EnsureDelivery"); err != nil {
		return dbgen.EnsureDeliveryRow{}, err
	}
	for _, d := range f.deliveries {
		if d.group == arg.AlertGroupID.Int64 && d.dest == arg.DestinationID {
			return dbgen.EnsureDeliveryRow{ID: d.id, DesiredVersion: d.version, DesiredHash: d.hash,
				ThreadBatchUntil: tz(d.batchUntil), State: d.state, HeldByStormID: heldOf(d)}, nil
		}
	}
	d := &fakeDelivery{id: f.id(), dest: arg.DestinationID, group: arg.AlertGroupID.Int64, state: "pending",
		urgent: arg.Urgent, loud: arg.PublicationLoud, next: arg.Now, threadState: "none", updated: arg.Now,
		heldBy: arg.HeldByStormID.Int64}
	f.deliveries = append(f.deliveries, d)
	return dbgen.EnsureDeliveryRow{ID: d.id, State: d.state, HeldByStormID: heldOf(d), Inserted: true}, nil
}

// heldOf is held_by_storm_id of a delivery.
func heldOf(d *fakeDelivery) pgtype.Int8 {
	if d.heldBy == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: d.heldBy, Valid: true}
}

func (f *fakeDB) SetDesired(_ context.Context, arg dbgen.SetDesiredParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SetDesired"); err != nil {
		return "", err
	}
	d := f.delivery(arg.ID)
	d.version++
	d.text, d.payload, d.hash, d.buttonKeyID = arg.DesiredText, arg.DesiredPayload, arg.DesiredHash,
		arg.ButtonKeyID.String
	if d.receivedAt == nil && arg.ReceivedAt.Valid {
		d.receivedAt = at(arg.ReceivedAt.Time)
	}
	if arg.Open && (d.state == "withheld" || (d.lateNote && d.messageID == nil)) {
		d.loud = pgtype.Bool{Bool: arg.Firing, Valid: true}
	}
	if arg.Open {
		d.lateNote = false
	}
	switch {
	case d.state == "deleted_in_messenger" || d.state == "retired":
	case d.state == "withheld" && !arg.Open:
	default:
		d.state = "pending"
	}
	d.urgent, d.next, d.updated = arg.Urgent, arg.Now, arg.Now
	return d.state, nil
}

func (f *fakeDB) SetDeliveryUrgent(_ context.Context, arg dbgen.SetDeliveryUrgentParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SetDeliveryUrgent"); err != nil {
		return err
	}
	f.delivery(arg.ID).urgent = arg.Urgent
	return nil
}

func (f *fakeDB) SetThreadBatchUntil(_ context.Context, arg dbgen.SetThreadBatchUntilParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SetThreadBatchUntil"); err != nil {
		return err
	}
	f.delivery(arg.ID).batchUntil = at(arg.Until)
	return nil
}

func (f *fakeDB) InsertThreadReply(_ context.Context, arg dbgen.InsertThreadReplyParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertThreadReply"); err != nil {
		return err
	}
	f.replies = append(f.replies, &fakeReply{id: f.id(), delivery: arg.DeliveryID, group: arg.AlertGroupID,
		dest: arg.DestinationID, event: arg.Event, seqs: arg.EventSeqs, loudness: arg.Loudness, mentions: arg.Mentions,
		fingerprints: arg.Fingerprints, state: arg.State, next: arg.Due, created: arg.Now})
	return nil
}

func (f *fakeDB) CollectAlerts(_ context.Context, arg dbgen.CollectAlertsParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CollectAlerts"); err != nil {
		return false, err
	}
	for _, r := range f.replies {
		if r.delivery == arg.DeliveryID && r.state == "collecting" {
			r.seqs = append(r.seqs, arg.EventSeqs...)
			for _, fp := range arg.Fingerprints {
				if !slices.Contains(r.fingerprints, fp) {
					r.fingerprints = append(r.fingerprints, fp)
				}
			}
			switch arg.Loudness {
			case r.loudness:
				r.mentions = slices.Compact(slices.Sorted(slices.Values(append(r.mentions, arg.Mentions...))))
			case "loud":
				r.mentions = arg.Mentions
			}
			if arg.Loudness == "loud" {
				r.loudness = "loud"
			}
			return false, nil
		}
	}
	f.replies = append(f.replies, &fakeReply{id: f.id(), delivery: arg.DeliveryID, group: arg.AlertGroupID,
		dest: arg.DestinationID, event: "alerts_added", seqs: arg.EventSeqs, loudness: arg.Loudness,
		mentions: arg.Mentions, fingerprints: arg.Fingerprints, state: "collecting", next: arg.Due, created: arg.Now})
	return true, nil
}

func (f *fakeDB) NotifyDelivery(_ context.Context, channel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch channel {
	case delivery.Channel:
		f.notified++
	case groups.TimersChannel:
		f.timersNotified++
	default:
		return errors.New("wrong channel")
	}
	return f.call("NotifyDelivery")
}

func (f *fakeDB) healthy(dest int64) bool {
	d := f.dests[dest]
	return d != nil && d.health == "healthy"
}

// work says whether a pending delivery is work for the worker: no Storm holds it, and its Destination is not deleted
// unless it is its final edit.
func (f *fakeDB) work(d *fakeDelivery) bool {
	return d.heldBy == 0 && (!f.dests[d.dest].deleted || d.desiredRetire)
}

func (f *fakeDB) ClaimDueDeliveries(_ context.Context, arg dbgen.ClaimDueDeliveriesParams) (
	[]dbgen.ClaimDueDeliveriesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ClaimDueDeliveries"); err != nil {
		return nil, err
	}
	var due []*fakeDelivery
	for _, d := range f.deliveries {
		if d.state == "pending" && !d.next.After(arg.Due) && (d.owner == "" || !d.until.After(arg.Now)) &&
			f.healthy(d.dest) && f.work(d) {
			due = append(due, d)
		}
	}
	slices.SortFunc(due, func(a, b *fakeDelivery) int {
		if a.urgent != b.urgent {
			if a.urgent {
				return -1
			}
			return 1
		}
		if (a.lastDelivered == nil) != (b.lastDelivered == nil) {
			if a.lastDelivered == nil {
				return -1
			}
			return 1
		}
		if a.lastDelivered != nil {
			if c := a.lastDelivered.Compare(*b.lastDelivered); c != 0 {
				return c
			}
		}
		return cmp.Or(a.next.Compare(b.next), cmp.Compare(a.id, b.id))
	})
	var out []dbgen.ClaimDueDeliveriesRow
	for _, d := range due {
		if len(out) == int(arg.Lim) {
			break
		}
		d.owner, d.until = arg.Owner, arg.LeaseUntil
		out = append(out, dbgen.ClaimDueDeliveriesRow{ID: d.id, Urgent: d.urgent, NextAttemptAt: d.next,
			LastDeliveredAt: tz(d.lastDelivered)})
	}
	// The order of RETURNING is not guaranteed; the worker sorts.
	slices.Reverse(out)
	return out, nil
}

func (f *fakeDB) GetLeasedDelivery(_ context.Context, arg dbgen.GetLeasedDeliveryParams) (dbgen.GetLeasedDeliveryRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetLeasedDelivery"); err != nil {
		return dbgen.GetLeasedDeliveryRow{}, err
	}
	d := f.delivery(arg.ID)
	if d == nil || d.owner != arg.Owner || !d.until.After(arg.Now) {
		return dbgen.GetLeasedDeliveryRow{}, pgx.ErrNoRows
	}
	ds := f.dests[d.dest]
	out := dbgen.GetLeasedDeliveryRow{ID: d.id, AlertGroupID: d.group, DesiredVersion: d.version,
		DesiredPayload: d.payload, DesiredHash: d.hash, DesiredReceivedAt: tz(d.receivedAt), PublicationLoud: d.loud,
		LateNote: d.lateNote, RepublishedAfterDelete: d.republished, DesiredRetire: d.desiredRetire,
		ActualHash: d.actualHash, MessageID: txt(d.messageID), MessageUrl: txt(d.messageURL),
		PublicationStartedAt: tz(d.started), Attempts: d.attempts, DestinationID: ds.id,
		DestinationPublicID: ds.publicID, DestinationName: ds.name, DestinationType: ds.typ,
		ConnectionID: nullInt(ds.connection), DestinationHealth: ds.health, GroupCreatedAt: d.updated,
		TelegramChannelChatID: nullInt(ds.tgChannel), TelegramDiscussionChatID: nullInt(ds.tgGroup)}
	if d.storm != 0 {
		out.StormID = pgtype.Int8{Int64: d.storm, Valid: true}
		return out, nil
	}
	g := f.groups[d.group]
	out.AlertGroupPublicID, out.Number, out.GroupStatus, out.GroupCreatedAt, out.GroupResolvedAt = g.publicID,
		g.number, g.status, g.created, tz(g.resolvedAt)
	return out, nil
}

func (f *fakeDB) RenewDeliveryLease(_ context.Context, arg dbgen.RenewDeliveryLeaseParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RenewDeliveryLease"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d.owner == arg.Owner {
		d.until = arg.LeaseUntil
	}
	return nil
}

func (f *fakeDB) RenewReplyLease(_ context.Context, arg dbgen.RenewReplyLeaseParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RenewReplyLease"); err != nil {
		return err
	}
	if r := f.reply(arg.ID); r.owner == arg.Owner {
		r.until = arg.LeaseUntil
	}
	return nil
}

func (f *fakeDB) MarkDelivered(_ context.Context, arg dbgen.MarkDeliveredParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("MarkDelivered"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d.owner == arg.Owner && d.version == arg.Version {
		d.state, d.receivedAt, d.owner, d.until = "delivered", nil, "", time.Time{}
	}
	return nil
}

func (f *fakeDB) RescheduleDelivery(_ context.Context, arg dbgen.RescheduleDeliveryParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RescheduleDelivery"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d.owner == arg.Owner {
		d.next, d.owner, d.until = arg.At, "", time.Time{}
	}
	return nil
}

func (f *fakeDB) StartPublication(_ context.Context, arg dbgen.StartPublicationParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("StartPublication"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d.owner == arg.Owner {
		d.started = at(arg.Now)
	}
	return nil
}

func (f *fakeDB) RecordDelivered(_ context.Context, arg dbgen.RecordDeliveredParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordDelivered"); err != nil {
		return "", err
	}
	d := f.delivery(arg.ID)
	if d.owner != arg.Owner {
		return "", pgx.ErrNoRows
	}
	ended := endedRow(d)
	raced := ended && d.messageID == nil && arg.MessageID.Valid
	g := f.groups[d.group]
	left := f.dests[d.dest].deleted || g == nil || !slices.Contains(f.routeDests[g.route], d.dest)
	// desired_retire = d.desired_retire OR (f.raced AND d.storm_id IS NULL AND f.left_destination)
	retire, err := notNull("desired_retire", sqlIs(d.desiredRetire).or(sqlIs(raced).and(sqlIs(d.storm == 0)).and(
		sqlIs(left))))
	if err != nil {
		return "", err
	}
	d.actualVersion, d.actualHash = arg.Version, arg.Hash
	if arg.MessageID.Valid {
		d.messageID = strOf(arg.MessageID)
	}
	if arg.MessageUrl.Valid {
		d.messageURL = strOf(arg.MessageUrl)
	}
	if arg.Published {
		if d.publishedAt == nil {
			d.publishedAt = at(arg.Now)
		}
		d.publications++
	}
	d.lastDelivered = at(arg.Now)
	d.receivedAt = nil
	switch { // the old desired_retire, as every SET expression reads the row before the update
	case ended && !raced:
	case raced && d.storm != 0:
		d.state = "retired"
	case raced && left:
		d.state = "pending"
	case d.version == arg.Version && !d.desiredRetire:
		d.state = "delivered"
	default:
		d.state = "pending"
	}
	d.desiredRetire = retire
	d.next, d.attempts, d.errorClass, d.lastError, d.owner, d.until = arg.Now, 0, nil, nil, "", time.Time{}
	d.firstFailed = nil
	return d.state, nil
}

func (f *fakeDB) RecordDeliveryRetry(_ context.Context, arg dbgen.RecordDeliveryRetryParams) (
	dbgen.RecordDeliveryRetryRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordDeliveryRetry"); err != nil {
		return dbgen.RecordDeliveryRetryRow{}, err
	}
	d := f.delivery(arg.ID)
	if d.owner != arg.Owner {
		return dbgen.RecordDeliveryRetryRow{}, pgx.ErrNoRows
	}
	if arg.Counted {
		d.attempts++
		if d.firstFailed == nil {
			d.firstFailed = at(arg.Now)
		}
	}
	if d.messageID == nil {
		d.started = nil
	}
	d.next, d.errorClass, d.lastError, d.owner, d.until = arg.At, strOf(arg.ErrorClass), strOf(arg.Error), "",
		time.Time{}
	return dbgen.RecordDeliveryRetryRow{Attempts: d.attempts, FirstFailedAt: tz(d.firstFailed)}, nil
}

// limiterOf is the capacity and the refill per second of a bucket.
func (f *fakeDB) limiterOf(k bucketKey) (float64, float64) {
	if k.kind == "destination" {
		d := f.dests[k.id]
		return float64(d.limit), float64(d.limit) / float64(d.per)
	}
	if l, ok := f.connLimits[k.id]; ok {
		return float64(l[0]), float64(l[0]) / float64(l[1])
	}
	return connLimit, connLimit / connPer
}

// The limiter of every Connection of the fake.
const (
	connLimit = 1000.0
	connPer   = 1.0
)

func keysOf(dest, conn pgtype.Int8) []bucketKey {
	var out []bucketKey
	if dest.Valid {
		out = append(out, bucketKey{"destination", dest.Int64})
	}
	if conn.Valid {
		out = append(out, bucketKey{"connection", conn.Int64})
	}
	return out
}

func (f *fakeDB) EnsureBuckets(_ context.Context, arg dbgen.EnsureBucketsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("EnsureBuckets"); err != nil {
		return err
	}
	for _, k := range keysOf(arg.DestinationID, arg.ConnectionID) {
		if f.buckets[k] == nil {
			c, _ := f.limiterOf(k)
			f.buckets[k] = &fakeBucket{tokens: c, refilled: arg.Now}
		}
	}
	return nil
}

func (f *fakeDB) TakeTokens(_ context.Context, arg dbgen.TakeTokensParams) (dbgen.TakeTokensRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("TakeTokens"); err != nil {
		return dbgen.TakeTokensRow{}, err
	}
	keys := keysOf(arg.DestinationID, arg.ConnectionID)
	avail := map[bucketKey]float64{}
	ok := len(keys) > 0
	var due time.Time
	for _, k := range keys {
		c, rate := f.limiterOf(k)
		b := f.buckets[k]
		a := math.Min(c, b.tokens+arg.Now.Sub(b.refilled).Seconds()*rate)
		avail[k] = a
		if a < 1 {
			ok = false
			if d := arg.Now.Add(time.Duration((1 - a) / rate * float64(time.Second))); d.After(due) {
				due = d
			}
		}
	}
	if !ok {
		return dbgen.TakeTokensRow{Due: cmp.Or(due, time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC))}, nil
	}
	for _, k := range keys {
		f.buckets[k].tokens, f.buckets[k].refilled = avail[k]-1, arg.Now
	}
	return dbgen.TakeTokensRow{Taken: true, Due: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeDB) HoldBucket(_ context.Context, arg dbgen.HoldBucketParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("HoldBucket"); err != nil {
		return err
	}
	if b := f.buckets[bucketKey{arg.SubjectKind, arg.SubjectID}]; b != nil && b.refilled.Before(arg.Until) {
		b.tokens, b.refilled = 1, arg.Until
	}
	return nil
}

func (f *fakeDB) ClaimDueReplies(_ context.Context, arg dbgen.ClaimDueRepliesParams) ([]dbgen.ClaimDueRepliesRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ClaimDueReplies"); err != nil {
		return nil, err
	}
	var due []*fakeReply
	for _, r := range f.replies {
		d := f.delivery(r.delivery)
		if (r.state != "collecting" && r.state != "pending") || r.next.After(arg.Due) ||
			(r.owner != "" && r.until.After(arg.Now)) || d.messageID == nil || !f.healthy(r.dest) {
			continue
		}
		if slices.ContainsFunc(f.replies, func(p *fakeReply) bool {
			return p.delivery == r.delivery && p.state == "pending" && p.id < r.id
		}) {
			continue
		}
		due = append(due, r)
	}
	slices.SortFunc(due, func(a, b *fakeReply) int { return cmp.Or(a.next.Compare(b.next), cmp.Compare(a.id, b.id)) })
	var out []dbgen.ClaimDueRepliesRow
	for _, r := range due {
		if len(out) == int(arg.Lim) {
			break
		}
		r.owner, r.until, r.state = arg.Owner, arg.LeaseUntil, "pending"
		out = append(out, dbgen.ClaimDueRepliesRow{ID: r.id, NextAttemptAt: r.next})
	}
	return out, nil
}

func (f *fakeDB) GetLeasedReply(_ context.Context, arg dbgen.GetLeasedReplyParams) (dbgen.GetLeasedReplyRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetLeasedReply"); err != nil {
		return dbgen.GetLeasedReplyRow{}, err
	}
	r := f.reply(arg.ID)
	if r == nil || r.owner != arg.Owner || !r.until.After(arg.Now) {
		return dbgen.GetLeasedReplyRow{}, pgx.ErrNoRows
	}
	d, ds, g := f.delivery(r.delivery), f.dests[r.dest], f.groups[r.group]
	return dbgen.GetLeasedReplyRow{ID: r.id, DeliveryID: r.delivery, AlertGroupID: r.group, Event: r.event,
		EventSeqs: r.seqs, Loudness: r.loudness, Mentions: r.mentions, Fingerprints: r.fingerprints,
		Attempts: r.attempts, MessageID: txt(d.messageID), ThreadState: d.threadState, ThreadAnchorID: txt(d.anchorID),
		ThreadChainLastID: txt(d.chainLastID), RepublishedAfterDelete: d.republished,
		PublicationStartedAt: tz(d.started), DestinationID: ds.id,
		DestinationPublicID: ds.publicID, DestinationName: ds.name, DestinationType: ds.typ,
		ConnectionID: nullInt(ds.connection), TelegramChannelChatID: nullInt(ds.tgChannel),
		TelegramDiscussionChatID: nullInt(ds.tgGroup), AlertGroupPublicID: g.publicID, Number: g.number, Title: g.title,
		Status: g.status, Urgent: g.urgent, Language: f.routes[g.route].language}, nil
}

func (f *fakeDB) RescheduleReply(_ context.Context, arg dbgen.RescheduleReplyParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RescheduleReply"); err != nil {
		return err
	}
	if r := f.reply(arg.ID); r.owner == arg.Owner {
		r.next, r.owner, r.until = arg.At, "", time.Time{}
	}
	return nil
}

func (f *fakeDB) RecordReplySent(_ context.Context, arg dbgen.RecordReplySentParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordReplySent"); err != nil {
		return err
	}
	if r := f.reply(arg.ID); r.owner == arg.Owner {
		r.state, r.sentAt, r.messageID, r.owner, r.until = "sent", at(arg.Now), strOf(arg.MessageID), "", time.Time{}
	}
	return nil
}

func (f *fakeDB) RecordReplyRetry(_ context.Context, arg dbgen.RecordReplyRetryParams) (dbgen.RecordReplyRetryRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordReplyRetry"); err != nil {
		return dbgen.RecordReplyRetryRow{}, err
	}
	r := f.reply(arg.ID)
	if r.owner != arg.Owner {
		return dbgen.RecordReplyRetryRow{}, pgx.ErrNoRows
	}
	if arg.Counted {
		r.attempts++
		if r.firstFailed == nil {
			r.firstFailed = at(arg.Now)
		}
	}
	r.next, r.errorClass, r.lastError, r.owner, r.until = arg.At, strOf(arg.ErrorClass), strOf(arg.Error), "",
		time.Time{}
	return dbgen.RecordReplyRetryRow{Attempts: r.attempts, FirstFailedAt: tz(r.firstFailed)}, nil
}

func (f *fakeDB) NextDeliveryWork(_ context.Context, arg dbgen.NextDeliveryWorkParams) (dbgen.NextDeliveryWorkRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("NextDeliveryWork"); err != nil {
		return dbgen.NextDeliveryWorkRow{}, err
	}
	var out dbgen.NextDeliveryWorkRow
	add := func(next time.Time, owner string, until time.Time) {
		if owner == "" || !until.After(arg.Now) {
			if out.FreeAt.IsZero() || next.Before(out.FreeAt) {
				out.FreeAt = next
			}
		} else if out.LeaseEnd.IsZero() || until.Before(out.LeaseEnd) {
			out.LeaseEnd = until
		}
	}
	for _, d := range f.deliveries {
		if d.state == "pending" && (f.healthy(d.dest) || f.dests[d.dest].probeOnNext) && f.work(d) {
			add(d.next, d.owner, d.until)
		}
	}
	for _, ds := range f.dests {
		if ds.health == "broken" && (!ds.deleted || f.pendingOf(ds.id)) && !ds.probeOnNext {
			add(*ds.nextProbe, "", time.Time{})
		}
	}
	for _, r := range f.replies {
		if (r.state == "collecting" || r.state == "pending") && f.delivery(r.delivery).messageID != nil &&
			f.healthy(r.dest) && !slices.ContainsFunc(f.replies, func(p *fakeReply) bool {
			return p.delivery == r.delivery && p.state == "pending" && p.id < r.id
		}) {
			add(r.next, r.owner, r.until)
		}
	}
	f.eventWork(add)
	return out, nil
}

func (f *fakeDB) InsertDeliveryEvent(_ context.Context, arg dbgen.InsertDeliveryEventParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertDeliveryEvent"); err != nil {
		return err
	}
	f.events = append(f.events, arg)
	return nil
}

func (f *fakeDB) GetGroupForDeliveries(_ context.Context, arg dbgen.GetGroupForDeliveriesParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetGroupForDeliveries"); err != nil {
		return 0, err
	}
	for _, g := range f.groups {
		if g.publicID == arg.PublicID {
			return g.id, nil
		}
	}
	return 0, pgx.ErrNoRows
}

func (f *fakeDB) ListGroupDeliveries(_ context.Context, arg dbgen.ListGroupDeliveriesParams) (
	[]dbgen.ListGroupDeliveriesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupDeliveries"); err != nil {
		return nil, err
	}
	var out []dbgen.ListGroupDeliveriesRow
	for _, d := range f.deliveries {
		if d.group != arg.AlertGroupID {
			continue
		}
		ds := f.dests[d.dest]
		out = append(out, dbgen.ListGroupDeliveriesRow{State: d.state, ThreadState: d.threadState,
			PossibleDuplicate: d.possibleDuplicate, MessageUrl: txt(d.messageURL), LastError: txt(d.lastError),
			UpdatedAt: d.updated, DestinationPublicID: ds.publicID, DestinationName: ds.name, DestinationType: ds.typ,
			Health: ds.health, BrokenSince: tz(ds.brokenSince), BrokenReason: txt(ds.brokenReason)})
	}
	slices.SortFunc(out, func(a, b dbgen.ListGroupDeliveriesRow) int {
		return cmp.Compare(a.DestinationName, b.DestinationName)
	})
	return out, nil
}

func (f *fakeDB) CountDeliveryQueues(_ context.Context, arg dbgen.CountDeliveryQueuesParams) (
	[]dbgen.CountDeliveryQueuesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CountDeliveryQueues"); err != nil {
		return nil, err
	}
	var out []dbgen.CountDeliveryQueuesRow
	for _, ds := range f.dests {
		if ds.deleted {
			continue
		}
		var n int64
		for _, d := range f.deliveries {
			if d.dest == ds.id && d.state == "pending" && d.heldBy == 0 {
				n++
			}
		}
		for _, r := range f.replies {
			if r.dest == ds.id && (r.state == "pending" || r.state == "collecting") && !r.next.After(arg.Now) {
				n++
			}
		}
		n += f.pendingEvents(ds.id)
		out = append(out, dbgen.CountDeliveryQueuesRow{PublicID: ds.publicID, Queued: n})
	}
	return out, nil
}

func (f *fakeDB) GetRetentionDetailsDays(context.Context, int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.details, f.call("GetRetentionDetailsDays")
}

func (f *fakeDB) DeleteExpiredReplies(_ context.Context, arg dbgen.DeleteExpiredRepliesParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DeleteExpiredReplies"); err != nil {
		return 0, err
	}
	var n int64
	f.replies = slices.DeleteFunc(f.replies, func(r *fakeReply) bool {
		if n < int64(arg.BatchSize) && slices.Contains([]string{"sent", "dropped", "not_delivered"}, r.state) &&
			r.created.Before(arg.Cutoff) {
			n++
			return true
		}
		return false
	})
	return n, nil
}

func (f *fakeDB) RecordNotDelivered(_ context.Context, arg dbgen.RecordNotDeliveredParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordNotDelivered"); err != nil {
		return 0, err
	}
	d := f.delivery(arg.ID)
	if d.owner != arg.Owner {
		return 0, pgx.ErrNoRows
	}
	if d.messageID == nil {
		d.started = nil
	}
	if !endedRow(d) {
		d.state = "not_delivered"
	}
	d.desiredRetire, d.attempts, d.firstFailed, d.errorClass, d.lastError = false, 0, nil, &arg.ErrorClass, &arg.Error
	d.owner, d.until, d.updated = "", time.Time{}, arg.Now
	return d.id, nil
}

func (f *fakeDB) RecordReplyNotDelivered(_ context.Context, arg dbgen.RecordReplyNotDeliveredParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordReplyNotDelivered"); err != nil {
		return 0, err
	}
	r := f.reply(arg.ID)
	if r.owner != arg.Owner {
		return 0, pgx.ErrNoRows
	}
	r.state, r.errorClass, r.lastError, r.owner, r.until = "not_delivered", &arg.ErrorClass, &arg.Error, "",
		time.Time{}
	return r.id, nil
}

func (f *fakeDB) MarkPossibleDuplicate(_ context.Context, arg dbgen.MarkPossibleDuplicateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("MarkPossibleDuplicate"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d.owner == arg.Owner {
		d.possibleDuplicate, d.updated = true, arg.Now
	}
	return nil
}

func (f *fakeDB) GetLatestRepublishedAt(_ context.Context, arg dbgen.GetLatestRepublishedAtParams) (time.Time,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetLatestRepublishedAt"); err != nil {
		return time.Time{}, err
	}
	latest := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range f.events {
		if e.Kind == "republished" && e.AlertGroupID.Int64 == arg.AlertGroupID && e.DestinationID ==
			arg.DestinationID && e.OccurredAt.After(latest) {
			latest = e.OccurredAt
		}
	}
	return latest, nil
}

// endedRow reports whether a delivery is in a state that nothing revives.
func endedRow(d *fakeDelivery) bool {
	return slices.Contains([]string{"withheld", "deleted_in_messenger", "retired"}, d.state)
}

func (f *fakeDB) ResetForRepublish(_ context.Context, arg dbgen.ResetForRepublishParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ResetForRepublish"); err != nil {
		return 0, err
	}
	d := f.delivery(arg.ID)
	if d.republished || endedRow(d) {
		return 0, nil
	}
	d.messageID, d.messageURL, d.actualVersion, d.actualHash, d.started = nil, nil, 0, nil, nil
	d.threadState, d.anchorID, d.chainLastID, d.republished = "none", nil, nil, true
	d.loud, d.lateNote, d.state, d.attempts, d.firstFailed = pgtype.Bool{Bool: false, Valid: true}, false, "pending",
		0, nil
	d.errorClass, d.lastError, d.next, d.owner, d.until, d.updated = nil, nil, arg.Now, "", time.Time{}, arg.Now
	return 1, nil
}

func (f *fakeDB) MarkDeletedInMessenger(_ context.Context, arg dbgen.MarkDeletedInMessengerParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("MarkDeletedInMessenger"); err != nil {
		return 0, err
	}
	d := f.delivery(arg.ID)
	if endedRow(d) {
		return 0, nil
	}
	d.state, d.owner, d.until, d.updated = "deleted_in_messenger", "", time.Time{}, arg.Now
	return 1, nil
}

func (f *fakeDB) DropPendingReplies(_ context.Context, arg dbgen.DropPendingRepliesParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DropPendingReplies"); err != nil {
		return err
	}
	for _, r := range f.replies {
		if r.delivery == arg.DeliveryID && (r.state == "pending" || r.state == "collecting") {
			r.state, r.owner, r.until = "dropped", "", time.Time{}
		}
	}
	return nil
}

func (f *fakeDB) SettleResolvedPublication(_ context.Context, arg dbgen.SettleResolvedPublicationParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SettleResolvedPublication"); err != nil {
		return err
	}
	d := f.delivery(arg.ID)
	if d.state != "pending" || d.messageID != nil || d.heldBy != 0 || d.storm != 0 {
		return nil
	}
	health := f.dests[d.dest].health
	// late_note = d.late_note OR coalesce(ds.health = 'healthy' AND d.last_error_class IN (...), false)
	waited := sqlIs(health == "healthy").and(sqlIn(d.errorClass, "retry_after", "transient"))
	late, err := notNull("late_note", sqlIs(d.lateNote).or(waited.coalesce(false)))
	if err != nil {
		return err
	}
	if health == "broken" {
		d.state = "withheld"
	}
	if waited.isTrue() {
		d.loud = pgtype.Bool{Bool: false, Valid: true}
	}
	d.lateNote, d.updated = late, arg.Now
	return nil
}

func (f *fakeDB) ClaimBrokenProbes(_ context.Context, arg dbgen.ClaimBrokenProbesParams) (
	[]dbgen.ClaimBrokenProbesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ClaimBrokenProbes"); err != nil {
		return nil, err
	}
	var due []*fakeDest
	for _, ds := range f.dests {
		if ds.health != "broken" || (ds.deleted && !f.pendingOf(ds.id)) {
			continue
		}
		waiting := ds.probeOnNext && (slices.ContainsFunc(f.deliveries, func(d *fakeDelivery) bool {
			return d.dest == ds.id && d.state == "pending" && d.heldBy == 0 && !d.next.After(arg.Due) &&
				(d.owner == "" || !d.until.After(arg.Now))
		}) || f.eventDue(ds.id, arg.Due, arg.Now))
		if waiting || (!ds.probeOnNext && !ds.nextProbe.After(arg.Due)) {
			due = append(due, ds)
		}
	}
	slices.SortFunc(due, func(a, b *fakeDest) int {
		if a.probeOnNext != b.probeOnNext {
			if a.probeOnNext {
				return 1
			}
			return -1
		}
		if !a.probeOnNext {
			if c := a.nextProbe.Compare(*b.nextProbe); c != 0 {
				return c
			}
		}
		return cmp.Compare(a.id, b.id)
	})
	var out []dbgen.ClaimBrokenProbesRow
	for _, ds := range due {
		if len(out) == int(arg.Lim) {
			break
		}
		ds.nextProbe, ds.probeOnNext = at(arg.NextProbe), false
		out = append(out, dbgen.ClaimBrokenProbesRow{ID: ds.id, PublicID: ds.publicID, Name: ds.name, Type: ds.typ,
			ConnectionID: nullInt(ds.connection)})
	}
	return out, nil
}

func (f *fakeDB) LeaseOldestWaiting(_ context.Context, arg dbgen.LeaseOldestWaitingParams) (
	dbgen.LeaseOldestWaitingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LeaseOldestWaiting"); err != nil {
		return dbgen.LeaseOldestWaitingRow{}, err
	}
	var oldest *fakeDelivery
	for _, d := range f.deliveries {
		if d.dest == arg.DestinationID && d.state == "pending" && d.heldBy == 0 && (oldest == nil ||
			cmp.Or(d.next.Compare(oldest.next), cmp.Compare(d.id, oldest.id)) < 0) {
			oldest = d
		}
	}
	if oldest == nil {
		return dbgen.LeaseOldestWaitingRow{}, pgx.ErrNoRows
	}
	free := oldest.owner == "" || !oldest.until.After(arg.Now)
	if free {
		oldest.owner, oldest.until = arg.Owner, arg.LeaseUntil
		if g := f.groups[oldest.group]; oldest.messageID == nil && oldest.storm == 0 && g != nil {
			oldest.loud, oldest.lateNote = pgtype.Bool{Bool: g.status == "firing", Valid: true}, false
		}
	}
	return dbgen.LeaseOldestWaitingRow{ID: oldest.id, Free: free}, nil
}

func (f *fakeDB) MarkProbeOnNextDelivery(_ context.Context, arg dbgen.MarkProbeOnNextDeliveryParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("MarkProbeOnNextDelivery"); err != nil {
		return err
	}
	if ds := f.dests[arg.ID]; ds.health == "broken" {
		ds.probeOnNext, ds.nextProbe = true, nil
	}
	return nil
}

func (f *fakeDB) BreakDestination(_ context.Context, arg dbgen.BreakDestinationParams) (dbgen.BreakDestinationRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("BreakDestination"); err != nil {
		return dbgen.BreakDestinationRow{}, err
	}
	ds := f.dests[arg.ID]
	if ds.health != "healthy" {
		return dbgen.BreakDestinationRow{}, pgx.ErrNoRows
	}
	ds.health, ds.brokenSince, ds.brokenCause, ds.brokenReason = "broken", at(arg.Now), &arg.Cause, &arg.Reason
	ds.nextProbe, ds.probeOnNext = at(arg.NextProbe), false
	return dbgen.BreakDestinationRow{PublicID: ds.publicID, Name: ds.name}, nil
}

func (f *fakeDB) UpdateBrokenReason(_ context.Context, arg dbgen.UpdateBrokenReasonParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("UpdateBrokenReason"); err != nil {
		return err
	}
	if ds := f.dests[arg.ID]; ds.health == "broken" {
		ds.brokenReason = &arg.Reason
		if arg.Cause.Valid {
			ds.brokenCause = &arg.Cause.String
		}
	}
	return nil
}

func (f *fakeDB) ResetDestinationBudgets(_ context.Context, arg dbgen.ResetDestinationBudgetsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ResetDestinationBudgets"); err != nil {
		return err
	}
	for _, d := range f.deliveries {
		if d.dest == arg.DestinationID && d.state == "pending" {
			d.attempts, d.firstFailed = 0, nil
		}
	}
	for _, r := range f.replies {
		if r.dest == arg.DestinationID && (r.state == "pending" || r.state == "collecting") {
			r.attempts, r.firstFailed = 0, nil
		}
	}
	return nil
}

func (f *fakeDB) MarkDestinationHealthy(_ context.Context, arg dbgen.MarkDestinationHealthyParams) (
	dbgen.MarkDestinationHealthyRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("MarkDestinationHealthy"); err != nil {
		return dbgen.MarkDestinationHealthyRow{}, err
	}
	ds := f.dests[arg.ID]
	if ds.health != "broken" {
		return dbgen.MarkDestinationHealthyRow{}, pgx.ErrNoRows
	}
	since := *ds.brokenSince
	ds.health, ds.brokenSince, ds.brokenCause, ds.brokenReason, ds.nextProbe, ds.probeOnNext = "healthy", nil, nil,
		nil, nil, false
	return dbgen.MarkDestinationHealthyRow{PublicID: ds.publicID, Name: ds.name, WasBrokenSince: since,
		Deleted: ds.deleted}, nil
}

func (f *fakeDB) DropDueReplies(_ context.Context, arg dbgen.DropDueRepliesParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DropDueReplies"); err != nil {
		return err
	}
	for _, r := range f.replies {
		if r.dest == arg.DestinationID && (r.state == "pending" || r.state == "collecting") && !r.next.After(arg.Now) {
			r.state, r.owner, r.until = "dropped", "", time.Time{}
		}
	}
	return nil
}

// unpublished are the pending deliveries of a Destination never published there that no Storm holds, with whether
// their Alert Group is resolved.
func (f *fakeDB) unpublished(dest int64, f2 func(d *fakeDelivery, resolved bool)) {
	for _, d := range f.deliveries {
		if g := f.groups[d.group]; d.dest == dest && d.state == "pending" && d.messageID == nil && d.heldBy == 0 &&
			d.storm == 0 && g != nil {
			f2(d, g.status == "resolved")
		}
	}
}

func (f *fakeDB) WithholdResolvedUnpublished(_ context.Context, arg dbgen.WithholdResolvedUnpublishedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WithholdResolvedUnpublished"); err != nil {
		return err
	}
	f.unpublished(arg.DestinationID, func(d *fakeDelivery, resolved bool) {
		if resolved {
			d.state, d.owner, d.until, d.updated = "withheld", "", time.Time{}, arg.Now
		}
	})
	return nil
}

func (f *fakeDB) RecoverUnpublished(_ context.Context, arg dbgen.RecoverUnpublishedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecoverUnpublished"); err != nil {
		return err
	}
	f.unpublished(arg.DestinationID, func(d *fakeDelivery, resolved bool) {
		if !resolved {
			d.loud = pgtype.Bool{Bool: f.groups[d.group].status == "firing", Valid: true}
			d.lateNote, d.next, d.updated = false, arg.Now, arg.Now
		}
	})
	return nil
}

func (f *fakeDB) RecoverPublished(_ context.Context, arg dbgen.RecoverPublishedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecoverPublished"); err != nil {
		return err
	}
	for _, d := range f.deliveries {
		if d.dest == arg.DestinationID && d.state == "pending" && d.messageID != nil && d.heldBy == 0 {
			d.next, d.updated = arg.Now, arg.Now
		}
	}
	return nil
}

func (f *fakeDB) ListDestinationHealth(_ context.Context, _ int64) ([]dbgen.ListDestinationHealthRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListDestinationHealth"); err != nil {
		return nil, err
	}
	var out []dbgen.ListDestinationHealthRow
	for _, ds := range f.dests {
		if !ds.deleted {
			out = append(out, dbgen.ListDestinationHealthRow{PublicID: ds.publicID, Health: ds.health})
		}
	}
	return out, nil
}

func (f *fakeDB) ListPendingGroups(_ context.Context, arg dbgen.ListPendingGroupsParams) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListPendingGroups"); err != nil {
		return nil, err
	}
	var out []string
	for _, d := range f.deliveries {
		if g := f.groups[d.group]; g != nil && d.dest == arg.DestinationID && d.state == "pending" &&
			!slices.Contains(out, g.publicID) {
			out = append(out, g.publicID)
		}
	}
	return out, nil
}

// The built-in Integration of the fake, which the synthetic Snapshots of Internal alerts belong to.
const builtinID = 100

func (f *fakeDB) FindBuiltinIntegration(context.Context, int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return builtinID, f.call("FindBuiltinIntegration")
}

func (f *fakeDB) InsertInternalBody(_ context.Context, arg idb.InsertInternalBodyParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertInternalBody"); err != nil {
		return err
	}
	var w struct {
		Status string `json:"status"`
		Alerts []struct {
			Labels map[string]string `json:"labels"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(arg.Body, &w); err != nil {
		return err
	}
	labels := w.Alerts[0].Labels
	f.internal = append(f.internal, fakeInternal{status: w.Status, name: labels["alertname"], labels: labels})
	return nil
}

func (f *fakeDB) InsertInternalSnapshot(_ context.Context, arg idb.InsertInternalSnapshotParams) error {
	if arg.IntegrationID != builtinID {
		return errors.New("not the built-in integration")
	}
	return nil
}

func (f *fakeDB) NotifyInternalSnapshot(context.Context, idb.NotifyInternalSnapshotParams) error {
	return nil
}

func (f *fakeDB) ListOpenInternalAlerts(context.Context, idb.ListOpenInternalAlertsParams) (
	[]idb.ListOpenInternalAlertsRow, error) {
	return nil, nil
}

func (f *fakeDB) ListPendingInternalRaises(context.Context, int64) ([][]byte, error) { return nil, nil }

func (f *fakeDB) Notify(_ context.Context, h db.Hint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("Notify"); err != nil {
		return err
	}
	f.hints = append(f.hints, h)
	return nil
}

// fakeTx is a transaction of the fake database, which is not transactional.
type fakeTx struct{ pgx.Tx }

func (fakeTx) Commit(context.Context) error   { return nil }
func (fakeTx) Rollback(context.Context) error { return nil }

type fakeBeginner struct{ err error }

func (b *fakeBeginner) Begin(context.Context) (pgx.Tx, error) {
	if b.err != nil {
		return nil, b.err
	}
	return fakeTx{}, nil
}

// env is delivery over the fake database: a Destination of each kind, a Route, the recording adapter and the worker,
// with manual clocks.
type env struct {
	db       *fakeDB
	begin    *fakeBeginner
	business *clock.Manual
	real     *clock.Manual
	store    *delivery.Store
	svc      *delivery.Service
	w        *delivery.Worker
	rec      *deliverytest.Recorder
	log      *bytes.Buffer
}

// The fixtures: Route 1 with Destinations 11 (mattermost through Connection 5) and 12 (webhook); Alert Group 21.
const (
	routeID = 1
	destMM  = 11
	destWH  = 12
	connID  = 5
	groupID = 21
)

func newEnv(t *testing.T) *env {
	t.Helper()
	f := newFakeDB()
	conn := int64(connID)
	f.dests[destMM] = &fakeDest{id: destMM, publicID: "DSAAAAAAAAAA11", name: "ops", typ: delivery.TypeMattermost,
		connection: &conn, health: "healthy", limit: 6, per: 60}
	f.dests[destWH] = &fakeDest{id: destWH, publicID: "DSAAAAAAAAAA12", name: "hook", typ: delivery.TypeWebhook,
		health: "healthy", limit: 600, per: 60}
	f.routes[routeID] = fakeRoute{language: "en", window: 60, publicID: "RTAAAAAAAAAAA1", name: "payments",
		threshold: 20}
	f.routeDests[routeID] = []int64{destMM}
	f.groups[groupID] = &fakeGroup{id: groupID, publicID: "AGAAAAAAAAAA21", number: 7, title: "disk full",
		status: "firing", route: routeID, created: business0}
	e := &env{db: f, begin: &fakeBeginner{}, business: clock.NewManual(business0), real: clock.NewManual(real0),
		rec: &deliverytest.Recorder{}, log: &bytes.Buffer{}}
	e.rec.Clock = e.business
	e.store = delivery.NewTestStore(e.begin, f)
	logger := logging.New(e.log, logging.LevelInfo)
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Log: logger,
		Renderer: stubRenderer{}})
	e.w = &delivery.Worker{Store: e.store, Lease: db.Lease{Owner: "r1", Duration: delivery.Lease,
		Clocks: clock.Clocks{Business: e.business, Real: e.real}},
		Organizations: func(context.Context) ([]int64, error) { return []int64{orgID}, nil },
		Adapters:      delivery.Adapters{delivery.TypeMattermost: e.rec, delivery.TypeWebhook: e.rec}, Log: logger,
		Renderer: stubRenderer{}}
	return e
}

// breakDest makes a Destination Broken as unavailable since now, with its probe due after
// delivery.broken_probe_interval, as the outcome rules do.
func (e *env) breakDest(id int64, reason string) {
	ds := e.db.dests[id]
	cause := "unavailable"
	ds.health, ds.brokenSince, ds.brokenCause, ds.brokenReason = "broken", at(e.business.Now()), &cause, &reason
	ds.nextProbe = at(e.business.Now().Add(delivery.BrokenProbeInterval))
}

// group is the Alert Group of the fixtures in a status, as the dispatcher hands it over.
func (e *env) group(status groups.Status, title string) *groups.Group {
	g := e.db.groups[groupID]
	g.status, g.title = string(status), title
	return &groups.Group{ID: groupID, PublicID: g.publicID, Number: g.number, RouteID: routeID, Title: title,
		Status: status, Urgent: g.urgent}
}

// enqueue renders the Alert Group with the events, as the dispatcher's re-render step does.
func (e *env) enqueue(t *testing.T, g *groups.Group, actor groups.Actor, events ...groups.Recorded) {
	t.Helper()
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: actor, Events: events}); err != nil {
		t.Fatal(err)
	}
}

// round runs one round of the worker.
func (e *env) round(t *testing.T) time.Duration {
	t.Helper()
	next, err := e.w.Round(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func (e *env) only(t *testing.T) *fakeDelivery {
	t.Helper()
	if len(e.db.deliveries) != 1 {
		t.Fatalf("deliveries %d", len(e.db.deliveries))
	}
	return e.db.deliveries[0]
}

func created() groups.Recorded {
	return groups.Recorded{Seq: 1, Event: groups.EventCreated, Loudness: groups.Loud,
		Mentions: []groups.Mention{groups.MentionNewAlertGroup}, Fingerprints: []string{"fp1"}}
}

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(func() bool { return true }).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// TestPublishAndCollapse is C-11.FR-1 and AC-1: a new Alert Group is one Loud Publication with new_alert_group, in
// the delivery client class, recorded as a publication delivery event; an Acknowledge while the call is pending
// collapses into the next call, an edit carrying the newest Desired state; then the delivery is delivered and the
// latency from the Snapshot's receipt is observed.
func TestPublishAndCollapse(t *testing.T) {
	e := newEnv(t)
	received := business0.Add(-2 * time.Second)
	g := e.group(groups.StatusFiring, "disk full")
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
		Events: []groups.Recorded{created()}, ReceivedAt: &received}); err != nil {
		t.Fatal(err)
	}
	d := e.only(t)
	if d.version != 1 || !d.loud.Bool || d.state != "pending" || !d.receivedAt.Equal(received) || e.db.notified != 1 {
		t.Fatalf("delivery %+v", d)
	}
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "p1",
		MessageURL: "https://chat.example.org/p1"}, Then: func() {
		e.enqueue(t, e.group(groups.StatusAcknowledged, "disk full"), groups.System,
			groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	}})
	e.business.Advance(time.Second)
	e.round(t)
	if len(e.rec.Calls()) != 1 || e.only(t).state != "pending" {
		t.Fatalf("after the publication %+v", e.rec.Calls())
	}
	e.round(t)
	calls := e.rec.Calls()
	if len(calls) != 2 || calls[0].Method != deliverytest.MethodPublish || calls[1].Method != deliverytest.MethodUpdate {
		t.Fatalf("calls %+v", calls)
	}
	if c := calls[0]; c.Loudness != groups.Loud || !slices.Equal(c.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) ||
		c.Class != "delivery" || !strings.Contains(c.Message.Text(), "#7 disk full") || len(c.Message.Buttons) != 3 {
		t.Errorf("publication %+v", c)
	}
	if c := calls[1]; c.MessageID != "p1" || c.Loudness != groups.Quiet || len(c.Mentions) != 0 ||
		!strings.Contains(c.Message.Text(), "Acknowledged") || c.Message.Buttons[0].Command != "unacknowledge" {
		t.Errorf("update %+v", c)
	}
	if d.state != "delivered" || d.actualVersion != 2 || *d.messageID != "p1" || *d.messageURL !=
		"https://chat.example.org/p1" || d.publications != 1 || d.publishedAt == nil || d.started == nil ||
		d.receivedAt != nil {
		t.Errorf("delivered %+v", d)
	}
	if len(e.db.events) != 1 || e.db.events[0].Kind != "publication" || e.db.events[0].Loudness != "loud" ||
		!slices.Equal(e.db.events[0].Mentions, []string{"new_alert_group"}) || e.db.events[0].AlertGroupID.Int64 !=
		groupID || !strings.HasPrefix(e.db.events[0].PublicID, "DE") {
		t.Errorf("events %+v", e.db.events)
	}
	out := scrape(t)
	for _, want := range []string{
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAA11",kind="publication",outcome="delivered"}`,
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAA11",kind="update",outcome="delivered"}`,
		`muster_delivery_latency_seconds_bucket{destination="DSAAAAAAAAAA11",le="3"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	if !strings.Contains(e.log.String(), `"event":"delivery_attempt"`) || !strings.Contains(e.log.String(),
		`"group":"#7"`) || !strings.Contains(e.log.String(), `"outcome":"delivered"`) {
		t.Errorf("log %s", e.log)
	}
	// Nothing left: the next round calls nothing.
	e.round(t)
	if len(e.rec.Calls()) != 2 {
		t.Errorf("calls after %d", len(e.rec.Calls()))
	}
}

// TestNotModified is C-11.FR-1: a delivery whose actual hash equals the desired hash makes no call; an answer "not
// modified" is delivered.
func TestNotModified(t *testing.T) {
	e := newEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	d := e.only(t)
	// Same message: no new version, no call.
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAlertResolved, Loudness: groups.Quiet})
	if d.version != 1 {
		t.Errorf("version %d", d.version)
	}
	// A pending row whose actual message already matches: marked delivered without a call.
	d.state = "pending"
	e.round(t)
	if d.state != "delivered" || len(e.rec.Calls()) != 1 {
		t.Errorf("matching = %s, %d calls", d.state, len(e.rec.Calls()))
	}
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.NotModified())
	e.enqueue(t, e.group(groups.StatusFiring, "b"), groups.System,
		groups.Recorded{Seq: 3, Event: groups.EventAnnotationsChanged, Loudness: groups.Quiet})
	e.round(t)
	if d.state != "delivered" || d.attempts != 0 || e.rec.Count(deliverytest.MethodUpdate) != 1 {
		t.Errorf("not modified = %+v", d)
	}
}

// TestUrgentFirst is C-11.FR-3: Urgent deliveries are claimed first, but never pass a limiter.
func TestUrgentFirst(t *testing.T) {
	e := newEnv(t)
	for i := range int64(5) {
		gid := groupID + i
		e.db.groups[gid] = &fakeGroup{id: gid, publicID: "AGAAAAAAAAAA" + string(rune('A'+i)) + "A", number: 10 + i,
			title: "g", status: "firing", route: routeID, urgent: i == 3}
		e.enqueue(t, &groups.Group{ID: gid, Number: 10 + i, RouteID: routeID, Title: "g", Status: groups.StatusFiring,
			Urgent: i == 3}, groups.System, created())
		e.business.Advance(time.Millisecond)
	}
	e.db.dests[destMM].limit = 2
	e.db.buckets = map[bucketKey]*fakeBucket{}
	e.round(t)
	calls := e.rec.Calls()
	if len(calls) != 2 || !strings.HasPrefix(calls[0].Message.Text(), "#13 ") {
		t.Fatalf("calls %+v", calls)
	}
	waiting := 0
	for _, d := range e.db.deliveries {
		if d.state == "pending" {
			waiting++
			if !d.next.After(e.business.Now()) {
				t.Errorf("a waiting delivery is due at %v", d.next)
			}
		}
	}
	if waiting != 3 {
		t.Errorf("waiting %d", waiting)
	}
}

// TestRecordOutcomes: a RetryAfter waits exactly as asked, leaves attempts untouched and, since the messenger did not
// take the Publication, clears its start; the other outcomes have their rules in outcomes_test.go.
func TestRecordOutcomes(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	d := e.only(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(7*time.Second, delivery.ScopeDestination))
	e.round(t)
	if d.state != "pending" || !d.next.Equal(business0.Add(7*time.Second+delivery.TokenMargin)) || d.attempts != 0 ||
		*d.errorClass != "retry_after" || d.messageID != nil || d.started != nil || len(e.db.events) != 0 {
		t.Errorf("retry after %+v", d)
	}
	if b := e.db.buckets[bucketKey{"destination", destMM}]; b.tokens != 1 || !b.refilled.Equal(business0.Add(7*time.Second)) {
		t.Errorf("bucket %+v", b)
	}
	if !strings.Contains(e.log.String(), `"retry_after_ms":7000`) {
		t.Errorf("log %s", e.log)
	}
}

// TestLeaseLost is C-11.FR-15: a row whose lease went to another replica is not worked, and an outcome recorded
// after its lease was lost changes nothing; a row whose lease ran out is claimed again.
func TestLeaseLost(t *testing.T) {
	e := newEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	d := e.only(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}, Then: func() {
		d.owner = "r2"
	}})
	e.round(t)
	if d.messageID != nil || d.state != "pending" || len(e.db.events) != 0 {
		t.Errorf("recorded without the lease %+v", d)
	}
	// r2 holds it until its lease runs out on the real clock.
	d.until = real0.Add(delivery.Lease)
	e.round(t)
	if len(e.rec.Calls()) != 1 {
		t.Errorf("claimed a held row: %d calls", len(e.rec.Calls()))
	}
	e.real.Advance(delivery.Lease)
	e.round(t)
	if len(e.rec.Calls()) != 2 || d.state != "delivered" || d.publications != 1 {
		t.Errorf("after the lease ran out %+v", d)
	}
	// A claimed row whose lease runs out before the attempt is not called: any replica claims it again.
	d.state, d.version = "pending", 2
	d.hash = []byte("v2")
	e.db.before["GetLeasedDelivery"] = func() { e.db.deliveries[0].until = e.real.Now() }
	e.round(t)
	if len(e.rec.Calls()) != 2 || d.owner == "" {
		t.Errorf("called after the lease ran out: %d calls", len(e.rec.Calls()))
	}
	e.round(t)
	if len(e.rec.Calls()) != 3 || d.state != "delivered" {
		t.Errorf("claimed again: %d calls, %s", len(e.rec.Calls()), d.state)
	}
	// A claimed row that another replica took before the attempt is skipped.
	e.db.before["GetLeasedDelivery"] = func() { e.db.deliveries[0].owner = "r3" }
	d.state, d.version, d.hash = "pending", 3, []byte("v3")
	e.round(t)
	if len(e.rec.Calls()) != 3 {
		t.Errorf("worked a row of another replica")
	}
}

// TestWorkerFailures: a failed query is logged as delivery_work_failed and the row waits for its lease; a failed claim
// or wait fails the round.
func TestWorkerFailures(t *testing.T) {
	for _, q := range []string{"GetLeasedDelivery", "MarkDelivered", "TakeTokens", "EnsureBuckets", "RenewDeliveryLease",
		"RescheduleDelivery", "StartPublication", "RecordDelivered", "InsertDeliveryEvent", "RecordDeliveryRetry",
		"HoldBucket"} {
		t.Run(q, func(t *testing.T) {
			e := newEnv(t)
			e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
			d := e.only(t)
			switch q {
			case "MarkDelivered":
				d.actualHash = d.hash
			case "RescheduleDelivery":
				e.db.dests[destMM].limit = 1
				e.db.buckets[bucketKey{"destination", destMM}] = &fakeBucket{tokens: 0, refilled: business0}
			case "RecordDeliveryRetry":
				e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "x"))
			case "HoldBucket":
				e.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(time.Second, delivery.ScopeDestination))
			}
			e.db.fail[q] = errBoom
			e.round(t)
			if !strings.Contains(e.log.String(), `"event":"delivery_work_failed"`) ||
				!strings.Contains(e.log.String(), `"work":"delivery"`) {
				t.Errorf("log %s", e.log)
			}
		})
	}
	e := newEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.only(t).payload = []byte("not json")
	e.round(t)
	if !strings.Contains(e.log.String(), "read the desired message") {
		t.Errorf("an unreadable payload %s", e.log)
	}
	e.begin.err = errBoom
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("a failed claim = %v", err)
	}
	e.begin.err = nil
	e.db.fail["NextDeliveryWork"] = errBoom
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("a failed wait = %v", err)
	}
	e.w.Organizations = func(context.Context) ([]int64, error) { return nil, errBoom }
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("no organizations = %v", err)
	}
}

// TestNextAndRun: the worker waits until the earliest due time on the business clock or the end of a held lease on the
// real clock, at most delivery.MaxWait; it runs rounds until its context ends, woken at once by Wake, and backs off after a
// failed round.
func TestNextAndRun(t *testing.T) {
	e := newEnv(t)
	if n := e.round(t); n != delivery.MaxWait {
		t.Errorf("idle wait %v", n)
	}
	e.w.MaxWait, e.w.Batch = time.Minute, 1
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(9*time.Second, delivery.ScopeDestination))
	if n := e.round(t); n != 9*time.Second+delivery.TokenMargin {
		t.Errorf("wait for the retry %v", n)
	}
	d := e.only(t)
	d.owner, d.until = "r2", real0.Add(4*time.Second)
	if n := e.round(t); n != 4*time.Second {
		t.Errorf("wait for the lease %v", n)
	}
	var waits []time.Duration
	ctx, cancel := context.WithCancel(t.Context())
	e.w.Wait = func(_ context.Context, d time.Duration, wake <-chan struct{}) {
		waits = append(waits, d)
		if len(waits) == 1 {
			e.db.fail["NextDeliveryWork"] = errBoom
		}
		if len(waits) == 2 {
			delete(e.db.fail, "NextDeliveryWork")
			e.w.Wake()
			e.w.Wake()
			<-wake
		}
		if len(waits) == 3 {
			cancel()
		}
	}
	e.w.Run(ctx)
	if len(waits) != 3 || waits[0] != 4*time.Second || waits[1] < delivery.FailureBackoff/2 || waits[2] != 4*time.Second {
		t.Errorf("waits %v", waits)
	}
	// The real wait ends with its context, a wake or the time.
	w := &delivery.Worker{}
	w.Wake()
	done := make(chan struct{})
	go func() {
		delivery.WaitReal(t.Context(), time.Hour, w.WakeChannel())
		delivery.WaitReal(t.Context(), time.Millisecond, make(chan struct{}))
		cctx, ccancel := context.WithCancel(t.Context())
		ccancel()
		delivery.WaitReal(cctx, time.Hour, make(chan struct{}))
		close(done)
	}()
	<-done
	if wait, batch := w.Defaults(); wait != delivery.MaxWait || batch != delivery.Batch {
		t.Error("defaults")
	}
}

// TestDrainBatches: the worker claims delivery.Batch rows at a time until a claim comes back short.
func TestDrainBatches(t *testing.T) {
	e := newEnv(t)
	e.w.Batch = 2
	e.db.dests[destMM].limit = 100
	for i := range int64(5) {
		gid := groupID + i
		e.db.groups[gid] = &fakeGroup{id: gid, publicID: "AGAAAAAAAAAB" + string(rune('A'+i)) + "A", number: i,
			title: "g", status: "firing", route: routeID}
		e.enqueue(t, &groups.Group{ID: gid, Number: i, RouteID: routeID, Title: "g", Status: groups.StatusFiring},
			groups.System, created())
	}
	e.round(t)
	if e.rec.Count(deliverytest.MethodPublish) != 5 || e.db.calls["ClaimDueDeliveries"] != 3 {
		t.Errorf("%d publications in %d claims", e.rec.Count(deliverytest.MethodPublish),
			e.db.calls["ClaimDueDeliveries"])
	}
}

// TestStatesAndLeaderTasks are listAlertGroupDeliveries (C-11.FR-16), muster_delivery_queue and the retention of
// Thread replies.
func TestStatesAndLeaderTasks(t *testing.T) {
	e := newEnv(t)
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.breakDest(destWH, "HTTP 503")
	e.round(t)
	states, err := e.svc.States(t.Context(), "agaaaaaaaaaa21")
	if err != nil || len(states) != 2 || states[0].Destination.Name != "hook" ||
		states[0].State != delivery.StateWaitingForBroken || *states[0].Destination.BrokenReason != "HTTP 503" ||
		states[1].State != "delivered" || states[1].MessageURL == nil || states[1].Error != nil {
		t.Fatalf("states %+v, %v", states, err)
	}
	wh := e.db.deliveries[1]
	wh.state, wh.lastError, wh.threadState = "not_delivered", at2("boom"), "unattached"
	states, _ = e.svc.States(t.Context(), "AGAAAAAAAAAA21")
	if *states[0].Error != "boom" || !states[0].ThreadNotAttached {
		t.Errorf("not delivered %+v", states[0])
	}
	for _, id := range []string{"AG000000000000", "RTAAAAAAAAAAAA"} {
		if _, err := e.svc.States(t.Context(), id); !errors.Is(err, delivery.ErrNotFound) {
			t.Errorf("%s = %v", id, err)
		}
	}
	e.db.fail["ListGroupDeliveries"] = errBoom
	if _, err := e.svc.States(t.Context(), "AGAAAAAAAAAA21"); !errors.Is(err, errBoom) {
		t.Errorf("list failed = %v", err)
	}
	e.db.fail["GetGroupForDeliveries"] = errBoom
	if _, err := e.svc.States(t.Context(), "AGAAAAAAAAAA21"); !errors.Is(err, errBoom) {
		t.Errorf("group failed = %v", err)
	}

	if err := e.svc.ExportQueue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if out := scrape(t); !strings.Contains(out, `muster_delivery_queue{destination="DSAAAAAAAAAA12"} 0`) ||
		!strings.Contains(out, `muster_delivery_queue{destination="DSAAAAAAAAAA11"} 0`) {
		t.Errorf("queue %s", out)
	}
	e.db.dests[destWH].deleted = true
	if err := e.svc.ExportQueue(t.Context()); err != nil || strings.Contains(scrape(t), `DSAAAAAAAAAA12"} `) {
		t.Errorf("a deleted destination keeps its series: %v", err)
	}
	e.db.fail["CountDeliveryQueues"] = errBoom
	if err := e.svc.ExportQueue(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("count failed = %v", err)
	}

	old := business0.Add(-91 * 24 * time.Hour)
	for i := range 3 {
		e.db.replies = append(e.db.replies, &fakeReply{id: int64(500 + i), state: "sent", created: old},
			&fakeReply{id: int64(600 + i), state: "pending", created: old})
	}
	e.db.replies = append(e.db.replies, &fakeReply{id: 700, state: "sent", created: business0})
	n, err := e.svc.PruneReplies(t.Context(), business0)
	if err != nil || n != 3 || len(e.db.replies) != 4 {
		t.Errorf("pruned %d, %v, left %d", n, err, len(e.db.replies))
	}
	e.db.fail["DeleteExpiredReplies"] = errBoom
	if _, err := e.svc.PruneReplies(t.Context(), business0); !errors.Is(err, errBoom) {
		t.Errorf("delete failed = %v", err)
	}
	e.db.fail["GetRetentionDetailsDays"] = errBoom
	if _, err := e.svc.PruneReplies(t.Context(), business0); !errors.Is(err, errBoom) {
		t.Errorf("days failed = %v", err)
	}
}

func at2(s string) *string { return &s }

// TestRecordEvent is C-11.FR-21: a delivery event with its Destination, Alert Group, loudness (Quiet unless said),
// Mentions, error and details; a first Publication that does not come from `created` is Quiet until S-035 decides.
func TestRecordEvent(t *testing.T) {
	e := newEnv(t)
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System)
	e.round(t)
	if c := e.rec.Calls(); len(c) != 1 || c[0].Loudness != groups.Quiet || len(c[0].Mentions) != 0 ||
		e.db.events[0].Loudness != "quiet" {
		t.Errorf("a publication without created %+v", c)
	}
	gid := int64(groupID)
	err := delivery.RecordEvent(t.Context(), e.db, orgID, delivery.Event{At: business0, DestinationID: destMM,
		AlertGroupID: &gid, Kind: delivery.EventNotDelivered, ErrorClass: "unknown", Error: "HTTP 418",
		Detail: map[string]any{"status": 418}})
	last := e.db.events[len(e.db.events)-1]
	if err != nil || last.Kind != "not_delivered" || last.Loudness != "quiet" || last.Error.String != "HTTP 418" ||
		string(last.Detail) != `{"status":418}` || last.ErrorClass.String != "unknown" {
		t.Errorf("event %+v, %v", last, err)
	}
	if err := delivery.RecordEvent(t.Context(), e.db, orgID, delivery.Event{Kind: delivery.EventFinalEdit,
		Detail: map[string]any{"bad": func() {}}}); err == nil {
		t.Error("an unencodable detail")
	}
	e.db.fail["InsertDeliveryEvent"] = errBoom
	if err := delivery.RecordEvent(t.Context(), e.db, orgID, delivery.Event{Kind: delivery.EventFinalEdit}); !errors.Is(
		err, errBoom) {
		t.Errorf("a failed insert = %v", err)
	}
	if delivery.NewStore(e.begin, nil) == nil {
		t.Error("no store")
	}
}
