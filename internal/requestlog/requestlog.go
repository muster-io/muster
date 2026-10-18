// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package requestlog is the Request log (C-23): the recent HTTP requests Muster sent for a Connection or an outgoing
// webhook Destination, and the requests that reached a Connection's callbacks, each with its answer. The outbound
// package hands each attempt to the Writer already masked of the Secrets of its client and request, and Inbound each
// request of a callback masked of the values of outbound.MaskedHeaders; the Writer applies the shape masking — signed
// action ids, `/start` tokens, the token of an account_link event — and the truncation of mask.go, and inserts one row
// with the business time on the main pool, outside any transaction of the caller, never failing or retrying the
// request it records. A record's owner is its Connection, else its outgoing
// webhook Destination, else nobody. The Reader shows only the records newer than request_log.max_age among the newest
// request_log.max_records of their owner, and Prune deletes the rest.
package requestlog

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/requestlog/dbgen"
)

// The bounds of the Request log (defaults.md, P-55 to P-57).
const (
	// MaxRecords is request_log.max_records: the records kept of each owner.
	MaxRecords = 500
	// MaxAge is request_log.max_age, on the business clock.
	MaxAge = 7 * 24 * time.Hour
	// BodyMax is request_log.body_max: the bytes kept of a body.
	BodyMax = 16384
)

// WriteTimeout bounds the write of one record.
const WriteTimeout = 2 * time.Second

// PruneBatch is the most records one statement of the pruning deletes.
const PruneBatch = 5000

// The directions of a record.
const (
	DirectionOutbound = "outbound"
	DirectionInbound  = "inbound"
)

// Queries are the queries of the package; *dbgen.Queries implements them.
type Queries interface {
	InsertRecord(ctx context.Context, arg dbgen.InsertRecordParams) error
	FindConnection(ctx context.Context, arg dbgen.FindConnectionParams) (int64, error)
	FindDestination(ctx context.Context, arg dbgen.FindDestinationParams) (dbgen.FindDestinationRow, error)
	ListConnectionRecords(ctx context.Context, arg dbgen.ListConnectionRecordsParams) (
		[]dbgen.ListConnectionRecordsRow, error)
	ListDestinationRecords(ctx context.Context, arg dbgen.ListDestinationRecordsParams) (
		[]dbgen.ListDestinationRecordsRow, error)
	GetRecord(ctx context.Context, arg dbgen.GetRecordParams) (dbgen.GetRecordRow, error)
	CountNewerOfConnection(ctx context.Context, arg dbgen.CountNewerOfConnectionParams) (int64, error)
	CountNewerOfDestination(ctx context.Context, arg dbgen.CountNewerOfDestinationParams) (int64, error)
	CountNewerUnowned(ctx context.Context, arg dbgen.CountNewerUnownedParams) (int64, error)
	PruneOlder(ctx context.Context, arg dbgen.PruneOlderParams) (int64, error)
	ListConnectionIDs(ctx context.Context, orgID int64) ([]int64, error)
	ListWebhookDestinationIDs(ctx context.Context, orgID int64) ([]int64, error)
	PruneConnectionExcess(ctx context.Context, arg dbgen.PruneConnectionExcessParams) (int64, error)
	PruneDestinationExcess(ctx context.Context, arg dbgen.PruneDestinationExcessParams) (int64, error)
	PruneUnownedExcess(ctx context.Context, arg dbgen.PruneUnownedExcessParams) (int64, error)
}

var _ Queries = (*dbgen.Queries)(nil)

// Config is what a Writer needs.
type Config struct {
	OrgID int64
	// DB is the main pool, never a transaction of the caller: a record is written whatever becomes of the caller's.
	DB dbgen.DBTX
	// Business dates the records.
	Business clock.Clock
	Log      *logging.Logger
	// Timeout bounds each write; 0 is WriteTimeout.
	Timeout time.Duration
}

// Writer writes the records of the Request log of one Organization; it is the outbound package's Recorder and the
// InboundRecorder of the callbacks.
type Writer struct {
	orgID    int64
	q        Queries
	business clock.Clock
	log      *logging.Logger
	timeout  time.Duration
}

var _ outbound.Recorder = (*Writer)(nil)

// NewWriter returns the Writer of cfg.
func NewWriter(cfg Config) *Writer { return newWriter(cfg, dbgen.New(cfg.DB)) }

func newWriter(cfg Config, q Queries) *Writer {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = WriteTimeout
	}
	return &Writer{orgID: cfg.OrgID, q: q, business: cfg.Business, log: cfg.Log, timeout: timeout}
}

// Record writes the outbound record r (C-23.FR-1, FR-2, FR-4): shape masking, truncation and one INSERT with the
// business time, under a context that does not end with the caller's and ends after the timeout. A record that cannot
// be written is logged without its content and counted, never returned: the request it records is unchanged.
func (w *Writer) Record(ctx context.Context, r outbound.Record) {
	w.write(ctx, DirectionOutbound, r)
}

func (w *Writer) write(ctx context.Context, direction string, r outbound.Record) {
	w.insert(ctx, direction, r.Operation, r.Owner.PublicID, w.paramsOf(direction, r))
}

// insert writes the row p of the record of operation whose owner is owner, by public_id.
func (w *Writer) insert(ctx context.Context, direction, operation, owner string, p dbgen.InsertRecordParams) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.timeout)
	defer cancel()
	if err := w.q.InsertRecord(wctx, p); err != nil {
		metrics.RequestLogWriteFailures.With(direction).Inc()
		w.log.Log(ctx, logging.RequestLogWriteFailed, logging.F("direction", direction),
			logging.F("operation", operation), logging.F("owner", owner), logging.F("error", err.Error()))
		return
	}
	metrics.RequestLogRecords.With(direction).Inc()
}

var _ InboundRecorder = (*Writer)(nil)

// maxMethod is the longest method a record stores.
const maxMethod = 16

// RecordInbound writes the record of a request that reached a callback (C-23.FR-1 to FR-4): the direction inbound, the
// path direct, the attempt 1, no error, Muster's answer as its status, shape-masked and cut as an outbound record; a
// body that Inbound did not keep whole is stored empty, marked truncated, with its size, and a method longer than a
// record stores is cut. Like Record it fails nothing; a nil Writer records nothing.
func (w *Writer) RecordInbound(ctx context.Context, r InboundRecord) {
	if w == nil {
		return
	}
	p := w.paramsOf(DirectionInbound, outbound.Record{
		Owner:     outbound.Owner{Connection: r.Links.Connection, PublicID: r.Links.ConnectionID},
		Links:     outbound.Links{Destination: r.Links.Destination, AlertGroup: r.Links.AlertGroup},
		Operation: r.Operation, Method: r.Method[:min(len(r.Method), maxMethod)], URL: r.URL,
		RequestHeader: r.RequestHeader, RequestBody: r.Request.Data, Status: cmp.Or(r.Status, http.StatusOK),
		ResponseHeader: r.ResponseHeader, ResponseBody: r.Response.Data, Latency: r.Latency, Via: outbound.ViaDirect,
		Attempt: 1})
	p.RequestBodyBytes = r.Request.Size
	if !r.Request.Whole {
		p.RequestBody, p.RequestBodyTruncated = "", true
	}
	p.ResponseBodyBytes = r.Response.Size
	if !r.Response.Whole {
		p.ResponseBody, p.ResponseBodyTruncated = pgtype.Text{Valid: true}, true
	}
	w.insert(ctx, DirectionInbound, r.Operation, r.Links.ConnectionID, p)
}

// paramsOf is the row of r: masked, cut, and linked to its owner, its Destination and its Alert Group.
func (w *Writer) paramsOf(direction string, r outbound.Record) dbgen.InsertRecordParams {
	body, size, cut := textOf(r.RequestBody)
	p := dbgen.InsertRecordParams{
		OrgID: w.orgID, PublicID: publicid.New(publicid.RequestRecord), At: w.business.Now(), Direction: direction,
		Operation: r.Operation, Method: cmp.Or(r.Method, http.MethodGet), Url: clean(r.URL),
		RequestHeaders: headersOf(r.RequestHeader), RequestBody: body, RequestBodyBytes: size,
		RequestBodyTruncated: cut, LatencyMs: clamp32(r.Latency.Milliseconds()), Via: cmp.Or(r.Via, outbound.ViaDirect),
		Attempt: clamp32(int64(max(r.Attempt, 1))), ConnectionID: id(r.Owner.Connection),
		DestinationID: id(cmp.Or(r.Owner.Destination, r.Links.Destination)), AlertGroupID: id(r.Links.AlertGroup),
	}
	if r.Status != 0 {
		body, size, cut := textOf(r.ResponseBody)
		p.Status = pgtype.Int4{Int32: clamp32(int64(r.Status)), Valid: true}
		p.ResponseHeaders = headersOf(r.ResponseHeader)
		p.ResponseBody = pgtype.Text{String: body, Valid: true}
		p.ResponseBodyBytes, p.ResponseBodyTruncated = size, cut
	}
	if r.Outcome != "" && r.Outcome != outbound.OutcomeOK {
		p.ErrorClass = pgtype.Text{String: string(r.Outcome), Valid: true}
		if r.Status == 0 {
			p.ErrorCode = pgtype.Text{String: delivery.FailureCode(r.Outcome == outbound.OutcomeBlocked, r.Network),
				Valid: true}
		}
	}
	if r.Error != "" {
		text, _, _ := textOf([]byte(r.Error))
		p.Error = pgtype.Text{String: text, Valid: true}
	}
	return p
}

// headersOf is h as a JSON object of string arrays, every name and value masked; nil is an empty object.
func headersOf(h map[string][]string) []byte {
	out := make(map[string][]string, len(h))
	for name, values := range h {
		name = clean(name)
		for _, v := range values {
			out[name] = append(out[name], clean(v))
		}
		if out[name] == nil {
			out[name] = []string{}
		}
	}
	b, _ := json.Marshal(out) // a map of strings always encodes
	return b
}

func id(v int64) pgtype.Int8 { return pgtype.Int8{Int64: v, Valid: v != 0} }

func clamp32(v int64) int32 { return int32(min(max(v, 0), math.MaxInt32)) }

// ErrNotFound is a record, a Connection or a Destination that does not exist, is deleted or is past a bound.
var ErrNotFound = errors.New("no such request record, connection or destination")

// ErrOneOfRequired is a list that names neither a Connection nor a Destination, or both.
var ErrOneOfRequired = errors.New("exactly one of a connection and a destination is needed")

// The status classes of Filter.StatusClass.
const (
	Status2xx      = "2xx"
	Status3xx      = "3xx"
	Status4xx      = "4xx"
	Status5xx      = "5xx"
	StatusNoAnswer = "none"
)

// Filter is what a list shows: the records of exactly one owner, a Connection or a Destination by public_id — all
// those of an outgoing webhook, and those of its Connection that name a messenger Destination — of a direction,
// status class, operation and Alert Group when set, after a position, at most Limit of them (1 to MaxRecords).
type Filter struct {
	Connection  string
	Destination string
	Direction   string
	StatusClass string
	Operation   string
	AlertGroup  string
	After       *Position
	Limit       int
}

// Position is where a page ends: the time and the internal id of its last record.
type Position struct {
	At time.Time
	ID int64
}

// DestinationRef names the Destination a record concerns, with its health.
type DestinationRef struct {
	PublicID     string
	Name         string
	Type         string
	Health       string
	BrokenSince  *time.Time
	BrokenReason *string
}

// AlertGroupRef names the Alert Group a record concerns.
type AlertGroupRef struct {
	PublicID string
	Number   int64
}

// Summary is a record as a list shows it.
type Summary struct {
	ID          int64
	PublicID    string
	At          time.Time
	Direction   string
	Operation   string
	Method      string
	URL         string
	Status      *int
	LatencyMS   int64
	Via         string
	Attempt     int
	ErrorClass  *string
	ErrorCode   *string
	CheckKind   *string
	CheckRun    *string
	Connection  *string
	Destination *DestinationRef
	AlertGroup  *AlertGroupRef

	RequestBodyTruncated  bool
	ResponseBodyTruncated bool
}

// OwnerKind is whose Request log a record is in, which decides the Permission that reads it.
type OwnerKind string

// The owners of a record.
const (
	OwnerConnection  OwnerKind = "connection"
	OwnerDestination OwnerKind = "destination"
	OwnerNone        OwnerKind = "none"
)

// Record is one record with its headers and bodies; the answer's are nil without an answer.
type Record struct {
	Summary
	Owner             OwnerKind
	RequestHeaders    map[string][]string
	RequestBody       string
	RequestBodyBytes  int64
	ResponseHeaders   map[string][]string
	ResponseBody      *string
	ResponseBodyBytes int64
	Error             *string
}

// Page is one page of a list, newest first; Next is where the next one starts, nil after the last.
type Page struct {
	Records []Summary
	Next    *Position
}

// Reader reads the Request log of one Organization within its bounds (C-23.FR-4, FR-5).
type Reader struct {
	orgID    int64
	q        Queries
	business clock.Clock
}

// NewReader returns the Reader of the Organization orgID over db; business tells the age bound.
func NewReader(orgID int64, db dbgen.DBTX, business clock.Clock) *Reader {
	return &Reader{orgID: orgID, q: dbgen.New(db), business: business}
}

// List lists the records f names, newest first, within the bounds (C-23.FR-5): ErrOneOfRequired unless it names
// exactly one owner, ErrNotFound for an owner that does not exist or is deleted. An Alert Group that is no public_id
// matches nothing.
func (r *Reader) List(ctx context.Context, f Filter) (Page, error) {
	if (f.Connection == "") == (f.Destination == "") {
		return Page{}, ErrOneOfRequired
	}
	limit := min(max(f.Limit, 1), MaxRecords)
	p := dbgen.ListConnectionRecordsParams{OrgID: r.orgID, NotBefore: r.business.Now().Add(-MaxAge),
		MaxRecords: MaxRecords, PageSize: clamp32(int64(limit) + 1), Direction: text(f.Direction),
		Operation: text(f.Operation)}
	switch f.StatusClass {
	case "":
	case StatusNoAnswer:
		p.NoAnswer = true
	case Status2xx, Status3xx, Status4xx, Status5xx:
		from := int32(f.StatusClass[0]-'0') * 100
		p.StatusFrom, p.StatusTo = pgtype.Int4{Int32: from, Valid: true}, pgtype.Int4{Int32: from + 100, Valid: true}
	default:
		return Page{}, fmt.Errorf("list the request log: unknown status class %q", f.StatusClass)
	}
	if f.After != nil {
		p.AfterAt = pgtype.Timestamptz{Time: f.After.At, Valid: true}
		p.AfterID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	hook, err := r.owner(ctx, f, &p)
	if err != nil {
		return Page{}, err
	}
	page := Page{Records: []Summary{}}
	if f.AlertGroup != "" {
		g, err := publicid.Parse(publicid.AlertGroup, f.AlertGroup)
		if errors.Is(err, publicid.ErrInvalid) {
			return page, nil
		}
		p.AlertGroup = text(g)
	}
	rows, err := r.rows(ctx, hook, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the request log: %w", err)
	}
	for i, row := range rows {
		if i == limit {
			last := page.Records[limit-1]
			page.Next = &Position{At: last.At, ID: last.ID}
			break
		}
		page.Records = append(page.Records, summaryOf(row))
	}
	return page, nil
}

// owner finds the owner f names and sets it in p: a Connection, or a messenger Destination within its Connection's
// set; for an outgoing webhook it returns its id instead.
func (r *Reader) owner(ctx context.Context, f Filter, p *dbgen.ListConnectionRecordsParams) (hook int64, err error) {
	if f.Connection != "" {
		pub, err := publicid.Parse(publicid.Connection, f.Connection)
		if err != nil {
			return 0, ErrNotFound
		}
		p.ConnectionID, err = r.q.FindConnection(ctx, dbgen.FindConnectionParams{OrgID: r.orgID, PublicID: pub})
		return 0, notFound("find the connection", err)
	}
	pub, err := publicid.Parse(publicid.Destination, f.Destination)
	if err != nil {
		return 0, ErrNotFound
	}
	d, err := r.q.FindDestination(ctx, dbgen.FindDestinationParams{OrgID: r.orgID, PublicID: pub})
	switch {
	case err != nil:
		return 0, notFound("find the destination", err)
	case d.ConnectionID.Valid:
		p.ConnectionID, p.DestinationID = d.ConnectionID.Int64, pgtype.Int8{Int64: d.ID, Valid: true}
		return 0, nil
	}
	return d.ID, nil
}

// rows reads the bounded set p names, or that of the outgoing webhook hook.
func (r *Reader) rows(ctx context.Context, hook int64, p dbgen.ListConnectionRecordsParams) (
	[]dbgen.ListConnectionRecordsRow, error) {
	if hook == 0 {
		return r.q.ListConnectionRecords(ctx, p)
	}
	rows, err := r.q.ListDestinationRecords(ctx, dbgen.ListDestinationRecordsParams{OrgID: r.orgID,
		DestinationID: hook, NotBefore: p.NotBefore, MaxRecords: p.MaxRecords, Direction: p.Direction,
		NoAnswer: p.NoAnswer, StatusFrom: p.StatusFrom, StatusTo: p.StatusTo, Operation: p.Operation,
		AlertGroup: p.AlertGroup, AfterAt: p.AfterAt, AfterID: p.AfterID, PageSize: p.PageSize})
	out := make([]dbgen.ListConnectionRecordsRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, dbgen.ListConnectionRecordsRow(row))
	}
	return out, err
}

// notFound is err of what, ErrNotFound for no row, nil for nil.
func notFound(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Get reads the record publicID with its headers and bodies, and whose Request log it is in; ErrNotFound when it does
// not exist, its owner is deleted, or it is past either bound (C-23.FR-4, FR-5).
func (r *Reader) Get(ctx context.Context, publicID string) (Record, error) {
	pub, err := publicid.Parse(publicid.RequestRecord, publicID)
	if err != nil {
		return Record{}, ErrNotFound
	}
	row, err := r.q.GetRecord(ctx, dbgen.GetRecordParams{OrgID: r.orgID, PublicID: pub,
		NotBefore: r.business.Now().Add(-MaxAge)})
	if err != nil {
		return Record{}, notFound("read the request record", err)
	}
	rec := Record{Summary: Summary{ID: row.ID, PublicID: row.PublicID, At: row.At, Direction: row.Direction,
		Operation: row.Operation, Method: row.Method, URL: row.Url, Status: int4(row.Status),
		LatencyMS: int64(row.LatencyMs), Via: row.Via, Attempt: int(row.Attempt), ErrorClass: str(row.ErrorClass),
		ErrorCode: str(row.ErrorCode), CheckKind: str(row.CheckKind), CheckRun: str(row.CheckRun),
		Connection: str(row.ConnectionPublicID),
		Destination: destinationRef(row.DestinationPublicID, row.DestinationName, row.DestinationType,
			row.DestinationHealth, row.DestinationBrokenSince, row.DestinationBrokenReason),
		AlertGroup:           alertGroupRef(row.AlertGroupPublicID, row.AlertGroupNumber),
		RequestBodyTruncated: row.RequestBodyTruncated, ResponseBodyTruncated: row.ResponseBodyTruncated},
		RequestBody: row.RequestBody, RequestBodyBytes: row.RequestBodyBytes, ResponseBody: str(row.ResponseBody),
		ResponseBodyBytes: row.ResponseBodyBytes, Error: str(row.Error)}
	newer := func() (int64, error) {
		return r.q.CountNewerUnowned(ctx, dbgen.CountNewerUnownedParams{OrgID: r.orgID, At: row.At, ID: row.ID,
			MaxRecords: MaxRecords})
	}
	switch {
	case row.ConnectionID.Valid:
		rec.Owner = OwnerConnection
		if row.ConnectionDeletedAt.Valid {
			return Record{}, ErrNotFound
		}
		newer = func() (int64, error) {
			return r.q.CountNewerOfConnection(ctx, dbgen.CountNewerOfConnectionParams{OrgID: r.orgID,
				ConnectionID: row.ConnectionID.Int64, At: row.At, ID: row.ID, MaxRecords: MaxRecords})
		}
	case row.DestinationID.Valid:
		rec.Owner = OwnerDestination
		if row.DestinationDeletedAt.Valid {
			return Record{}, ErrNotFound
		}
		newer = func() (int64, error) {
			return r.q.CountNewerOfDestination(ctx, dbgen.CountNewerOfDestinationParams{OrgID: r.orgID,
				DestinationID: row.DestinationID.Int64, At: row.At, ID: row.ID, MaxRecords: MaxRecords})
		}
	default:
		rec.Owner = OwnerNone
	}
	n, err := newer()
	if err != nil {
		return Record{}, fmt.Errorf("read the request record: %w", err)
	}
	if n >= MaxRecords {
		return Record{}, ErrNotFound
	}
	if err := json.Unmarshal(row.RequestHeaders, &rec.RequestHeaders); err != nil {
		return Record{}, fmt.Errorf("read the request headers of %s: %w", row.PublicID, err)
	}
	if row.ResponseHeaders != nil {
		if err := json.Unmarshal(row.ResponseHeaders, &rec.ResponseHeaders); err != nil {
			return Record{}, fmt.Errorf("read the answer headers of %s: %w", row.PublicID, err)
		}
	}
	return rec, nil
}

func summaryOf(row dbgen.ListConnectionRecordsRow) Summary {
	return Summary{ID: row.ID, PublicID: row.PublicID, At: row.At, Direction: row.Direction,
		Operation: row.Operation, Method: row.Method, URL: row.Url, Status: int4(row.Status),
		LatencyMS: int64(row.LatencyMs), Via: row.Via, Attempt: int(row.Attempt), ErrorClass: str(row.ErrorClass),
		ErrorCode: str(row.ErrorCode), CheckKind: str(row.CheckKind), CheckRun: str(row.CheckRun),
		Connection: str(row.ConnectionPublicID),
		Destination: destinationRef(row.DestinationPublicID, row.DestinationName, row.DestinationType,
			row.DestinationHealth, row.DestinationBrokenSince, row.DestinationBrokenReason),
		AlertGroup:           alertGroupRef(row.AlertGroupPublicID, row.AlertGroupNumber),
		RequestBodyTruncated: row.RequestBodyTruncated, ResponseBodyTruncated: row.ResponseBodyTruncated}
}

func destinationRef(pub, name, typ, health pgtype.Text, since pgtype.Timestamptz, reason pgtype.Text) *DestinationRef {
	if !pub.Valid {
		return nil
	}
	ref := &DestinationRef{PublicID: pub.String, Name: name.String, Type: typ.String, Health: health.String,
		BrokenReason: str(reason)}
	if since.Valid {
		ref.BrokenSince = &since.Time
	}
	return ref
}

func alertGroupRef(pub pgtype.Text, number pgtype.Int8) *AlertGroupRef {
	if !pub.Valid {
		return nil
	}
	return &AlertGroupRef{PublicID: pub.String, Number: number.Int64}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func str(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func int4(v pgtype.Int4) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}

// Prune deletes the records of the Organization orgID older than request_log.max_age at now and then, for each
// Connection, each outgoing webhook Destination and the records without an owner, those past the newest
// request_log.max_records, PruneBatch rows per statement until one comes back short (C-23.FR-4). It deletes only what
// is past a bound, so two overlapping runs are harmless. It returns how many it deleted; an owner that fails does not
// stop the others.
func Prune(ctx context.Context, db dbgen.DBTX, orgID int64, now time.Time) (int64, error) {
	return prune(ctx, dbgen.New(db), orgID, now, PruneBatch)
}

func prune(ctx context.Context, q Queries, orgID int64, now time.Time, batch int32) (int64, error) {
	var total int64
	var errs []error
	drain := func(what string, del func() (int64, error)) {
		for ctx.Err() == nil {
			n, err := del()
			total += n
			if err != nil {
				errs = append(errs, fmt.Errorf("prune %s: %w", what, err))
				return
			}
			if n < int64(batch) {
				return
			}
		}
	}
	drain("the records older than request_log.max_age", func() (int64, error) {
		return q.PruneOlder(ctx, dbgen.PruneOlderParams{OrgID: orgID, Before: now.Add(-MaxAge), BatchSize: batch})
	})
	conns, err := q.ListConnectionIDs(ctx, orgID)
	if err != nil {
		errs = append(errs, fmt.Errorf("list the connections: %w", err))
	}
	for _, c := range conns {
		drain(fmt.Sprintf("the records of connection %d", c), func() (int64, error) {
			return q.PruneConnectionExcess(ctx, dbgen.PruneConnectionExcessParams{OrgID: orgID, ConnectionID: c,
				MaxRecords: MaxRecords, BatchSize: batch})
		})
	}
	hooks, err := q.ListWebhookDestinationIDs(ctx, orgID)
	if err != nil {
		errs = append(errs, fmt.Errorf("list the outgoing webhooks: %w", err))
	}
	for _, d := range hooks {
		drain(fmt.Sprintf("the records of destination %d", d), func() (int64, error) {
			return q.PruneDestinationExcess(ctx, dbgen.PruneDestinationExcessParams{OrgID: orgID, DestinationID: d,
				MaxRecords: MaxRecords, BatchSize: batch})
		})
	}
	drain("the records without an owner", func() (int64, error) {
		return q.PruneUnownedExcess(ctx, dbgen.PruneUnownedExcessParams{OrgID: orgID, MaxRecords: MaxRecords,
			BatchSize: batch})
	})
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	return total, errors.Join(errs...)
}
