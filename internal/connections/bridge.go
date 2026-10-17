// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
)

// TypeBridge is the type of a Bridge Connection (C-22.FR-1): a bot of a messenger that a bridge speaks the Bridge
// protocol for.
const TypeBridge = "bridge"

// The event delivery modes of a Bridge Connection (C-22.FR-1); connection.bridge.event_delivery is poll.
const (
	EventDeliveryPoll = bridge.EventDeliveryPoll
	EventDeliveryPush = bridge.EventDeliveryPush
)

// The default limiter of a Bridge Connection (connection.bridge.limiter): 10 requests per second.
const (
	BridgeDefaultLimit      = 10
	BridgeDefaultPerSeconds = 1
)

// The steps of the check of a Bridge Connection (C-22.FR-2).
const (
	StepInfo          = "info"
	StepEventDelivery = "event_delivery"
)

const (
	fieldPushSecret = "connections.bridge_push_secret"
	// pushSecretPrefix starts a push secret in the Standard Webhooks form, followed by base64.
	pushSecretPrefix = "whsec_"
)

// bridgeEventLockClass is the first key of the transaction advisory lock of the claim of one Bridge event, whose second
// key is hashtext of the Connection's id and the event id: HandleEventOnce holds it while the event is handled, so
// that a second copy of the event waits for the first and then finds its id recorded. The classes 0x6d75_0001 to
// 0x6d75_0006 are taken (internal/auth, internal/db, internal/delivery).
const bridgeEventLockClass int32 = 0x6d75_0007

// BridgeCallbackURL is where a bridge in push mode posts the events of the Connection publicID: MUSTER_INGEST_URL
// followed by /api/v1/callbacks/bridge/<public_id> (C-22.FR-1).
func (s *Service) BridgeCallbackURL(publicID string) string {
	base := ""
	if s.cfg.IngestURL != nil {
		base = strings.TrimSuffix(s.cfg.IngestURL.String(), "/")
	}
	return base + bridge.CallbackPath + publicID
}

// checkBridgeAddress validates and normalizes where a Bridge Connection calls (C-22.FR-1): its base URL, stored
// without its trailing slashes, and its event delivery mode.
func checkBridgeAddress(in *Input) error {
	in.ServerURL, in.BotAPIBaseURL, in.UpdateMode = "", "", ""
	base, err := bridge.ParseBaseURL(in.BridgeBaseURL)
	if err != nil {
		return &FieldError{Pointer: "/base_url", Code: CodeInvalidFormat,
			Detail: "The base URL is not an absolute http or https URL without user information, query or fragment."}
	}
	in.BridgeBaseURL = base
	if in.EventDelivery != EventDeliveryPoll && in.EventDelivery != EventDeliveryPush {
		return &FieldError{Pointer: "/event_delivery", Code: CodeInvalidFormat,
			Detail: "The event delivery mode is not poll or push."}
	}
	return nil
}

// checkPushSecret validates the push secret of the Bridge Connection in, saved over stored (nil on a creation): push
// event delivery needs one, sent now or stored, in the Standard Webhooks form, whsec_ followed by base64; poll takes
// none, and its save clears the stored one. The detail never shows the value.
func checkPushSecret(in Input, stored *Connection) error {
	p := in.PushSecret
	if in.EventDelivery != EventDeliveryPush {
		if p.Given && !p.Null {
			return &FieldError{Pointer: "/push_secret", Code: CodeUnsupported,
				Detail: "Only push event delivery takes a push secret."}
		}
		return nil
	}
	switch {
	case p.Null, !p.Given && (stored == nil || !stored.PushSecret.Set):
		return &FieldError{Pointer: "/push_secret", Code: CodeRequired,
			Detail: "Push event delivery needs a push secret."}
	case p.Given && !validPushSecret(p.Value):
		return &FieldError{Pointer: "/push_secret", Code: CodeInvalidFormat,
			Detail: "The push secret is not whsec_ followed by base64."}
	}
	return nil
}

func validPushSecret(v logging.Secret) bool {
	rest, ok := strings.CutPrefix(string(v), pushSecretPrefix)
	if !ok {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(rest)
	return err == nil && len(key) > 0
}

// pushSecret applies the push secret of in to the stored one at now: the one sent in push event delivery, none
// otherwise. It returns the field to store and whether it changed.
func (s *Service) pushSecret(in Input, stored keyring.StoredSecret, now time.Time) (keyring.StoredSecret, bool,
	error) {
	if in.Type != TypeBridge || in.EventDelivery != EventDeliveryPush {
		return s.cfg.Keyring.ApplySecret(fieldPushSecret, stored, keyring.Clear, now)
	}
	return s.cfg.Keyring.ApplySecret(fieldPushSecret, stored, in.PushSecret, now)
}

func storedPushSecret(r dbgen.GetConnectionRow) keyring.StoredSecret {
	s := keyring.StoredSecret{Ciphertext: r.BridgePushSecretCiphertext, KeyID: r.BridgePushSecretKeyID.String}
	if r.BridgePushSecretUpdatedAt.Valid {
		at := r.BridgePushSecretUpdatedAt.Time.UTC()
		s.UpdatedAt = &at
	}
	return s
}

var (
	_ bridge.Targets     = (*Service)(nil)
	_ bridge.Connections = (*Service)(nil)
)

// bridgeEventQueries are the queries of the events of Bridge Connections, part of Queries: the Connections to poll,
// the cursor of their events, and the claim, the record and the pruning of event ids.
type bridgeEventQueries interface {
	ListBridgePollingConnections(ctx context.Context, orgID int64) ([]dbgen.ListBridgePollingConnectionsRow, error)
	StoreBridgeEventsCursor(ctx context.Context, arg dbgen.StoreBridgeEventsCursorParams) (int64, error)
	LockBridgeEvent(ctx context.Context, arg dbgen.LockBridgeEventParams) error
	BridgeEventSeen(ctx context.Context, arg dbgen.BridgeEventSeenParams) (bool, error)
	RecordBridgeEvent(ctx context.Context, arg dbgen.RecordBridgeEventParams) error
	PruneBridgeEventsSeen(ctx context.Context, arg dbgen.PruneBridgeEventsSeenParams) (int64, error)
}

// cachedBridge is the Bridge client of a version of a Connection.
type cachedBridge struct {
	version int64
	client  *bridge.Client
}

// bridgeClient builds the client of the stored Bridge Connection row, with its decrypted token, push secret and proxy
// password; the bridge knows the push secret too, so the client masks it like the token.
func (s *Service) bridgeClient(row dbgen.GetConnectionRow) (*bridge.Client, error) {
	token, err := s.cfg.Keyring.OpenSecret(fieldBotToken, storedToken(row))
	if err != nil {
		return nil, fmt.Errorf("open the token of the connection %s: %w", row.PublicID, err)
	}
	pushSecret, err := s.cfg.Keyring.OpenSecret(fieldPushSecret, storedPushSecret(row))
	if err != nil {
		return nil, fmt.Errorf("open the push secret of the connection %s: %w", row.PublicID, err)
	}
	password, err := s.cfg.Keyring.OpenSecret(fieldProxyPassword, storedPassword(row))
	if err != nil {
		return nil, fmt.Errorf("open the proxy password of the connection %s: %w", row.PublicID, err)
	}
	p, err := proxyconf.Parse(row.Proxy)
	if err != nil {
		return nil, err
	}
	return bridge.NewClient(bridge.Network(s.cfg.Network), bridge.Settings{BaseURL: row.BridgeBaseUrl.String,
		Token: token, PushSecret: pushSecret, Proxy: p.Outbound(password)})
}

// cachedBridgeClient is the Bridge client of the Connection id at version for delivery, built again when the version
// changed; the checks and muster doctor build their own.
func (s *Service) cachedBridgeClient(id, version int64, build func() (*bridge.Client, error)) (*bridge.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cc, ok := s.bridgeClients[id]; ok && cc.version == version {
		return cc.client, nil
	}
	c, err := build()
	if err != nil {
		return nil, err
	}
	s.bridgeClients[id] = cachedBridge{version: version, client: c}
	return c, nil
}

// forgetBridgeClient drops the cached Bridge client of the deleted Connection id.
func (s *Service) forgetBridgeClient(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bridgeClients, id)
}

// BridgeTarget is where the Bridge Destination destinationID posts, for the Bridge delivery (bridge.Targets): its
// channel id and its Connection with the protocol client, kept until the Connection changes. A deleted Destination
// still has one, for its final edit. A Destination that is unknown or not a Bridge one, or whose Connection is deleted
// or not a Bridge Connection, is bridge.ErrNoTarget.
func (s *Service) BridgeTarget(ctx context.Context, destinationID int64) (bridge.Target, error) {
	r, err := s.cfg.Store.GetBridgeDestinationTarget(ctx, dbgen.GetBridgeDestinationTargetParams{OrgID: s.cfg.OrgID,
		DestinationID: destinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return bridge.Target{}, bridge.ErrNoTarget
	}
	if err != nil {
		return bridge.Target{}, fmt.Errorf("read the connection of the destination %d: %w", destinationID, err)
	}
	if r.Type != TypeBridge {
		return bridge.Target{}, bridge.ErrNoTarget
	}
	row := dbgen.GetConnectionRow{ID: r.ID, PublicID: r.PublicID, Type: r.Type, Name: r.Name,
		BridgeBaseUrl: r.BridgeBaseUrl, BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID,
		BotTokenUpdatedAt: r.BotTokenUpdatedAt, BridgePushSecretCiphertext: r.BridgePushSecretCiphertext,
		BridgePushSecretKeyID: r.BridgePushSecretKeyID, BridgePushSecretUpdatedAt: r.BridgePushSecretUpdatedAt,
		Proxy: r.Proxy, ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version}
	c, err := s.cachedBridgeClient(r.ID, r.Version, func() (*bridge.Client, error) { return s.bridgeClient(row) })
	if err != nil {
		return bridge.Target{}, err
	}
	return bridge.Target{Conn: bridge.Conn{ID: r.ID, PublicID: r.PublicID, Version: r.Version, Client: c},
		ChannelID: r.BridgeChannelID.String}, nil
}

// BridgeInfo is the info of the Bridge Connection conn for the Bridge delivery (bridge.Targets): Muster's copy, through
// the InfoCache, read again through conn's client in class when it is older than bridge.info_max_age.
func (s *Service) BridgeInfo(ctx context.Context, conn bridge.Conn, class outbound.Class) (bridge.Info, bridge.Result) {
	return s.info.Info(ctx, conn, class)
}

// checkBridge runs the check of the Bridge Connection row (C-22.FR-2) on the interactive path, limited by the
// Connection: the step info reads getInfo again through the InfoCache, which stores, keeps and logs what it read; the
// step event_delivery then checks that the bridge offers the Connection's event delivery mode, and is skipped when
// info failed. The info read is stored also when event_delivery fails. A failure of info carries the code the
// protocol names or that of delivery.FailureCode, with the bridge's masked text.
func (s *Service) checkBridge(ctx context.Context, row dbgen.GetConnectionRow, baseURL *string) (CheckResult,
	error) {
	if baseURL != nil {
		return CheckResult{}, &FieldError{Pointer: "/base_url", Code: CodeUnsupported,
			Detail: "Only a Telegram Connection takes an unsaved base URL."}
	}
	c, err := s.bridgeClient(row)
	if err != nil {
		return CheckResult{}, err
	}
	conn := bridge.Conn{ID: row.ID, PublicID: row.PublicID, Version: row.Version, Client: c}
	var info bridge.Info
	var r bridge.Result
	var stored error
	var latency time.Duration
	id := row.ID
	if _, err := s.cfg.Interactive.Do(ctx, delivery.Subject{Connection: &id}, delivery.ReadOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			start := s.cfg.Clocks.Real.Now()
			info, r, stored = s.info.Refresh(ctx, conn, call.Class)
			latency = s.cfg.Clocks.Real.Now().Sub(start)
			return r.Outcome()
		})); err != nil {
		return CheckResult{}, err
	}
	if stored != nil {
		return CheckResult{}, stored
	}
	step := Step{Name: StepInfo, OK: r.OK(), Latency: latency, Via: c.Via()}
	if !r.OK() {
		step.Message, step.Code = string(r.Error), r.CheckCode()
		return CheckResult{Bridge: true, Steps: []Step{step, {Name: StepEventDelivery, Skipped: true,
			Via: c.Via()}}}, nil
	}
	ok, code, message := bridge.EventDeliveryStep(info, row.BridgeEventDelivery.String)
	return CheckResult{OK: ok, Bridge: true, Info: info.Raw, Steps: []Step{step, {Name: StepEventDelivery, OK: ok,
		Via: c.Via(), Message: message, Code: code}}}, nil
}

// CheckBridgeChannel runs the Destination check of a Bridge Destination being saved or checked, for the Destination
// write path (C-22.FR-3): checkChannel through the Bridge Connection that the check names, on the interactive path,
// limited by the Destination when it exists and by the Connection otherwise, with a client built for the check. A
// Connection that is missing, deleted or not a Bridge Connection is destinations.ErrUnknownConnection.
func (s *Service) CheckBridgeChannel(ctx context.Context, in destinations.BridgeChannelCheck) (
	destinations.BridgeChannelChecked, error) {
	row, err := s.row(ctx, s.cfg.Store, in.Connection)
	if errors.Is(err, ErrNotFound) || (err == nil && row.Type != TypeBridge) {
		return destinations.BridgeChannelChecked{}, destinations.ErrUnknownConnection
	}
	if err != nil {
		return destinations.BridgeChannelChecked{}, err
	}
	c, err := s.bridgeClient(row)
	if err != nil {
		return destinations.BridgeChannelChecked{}, err
	}
	conn := row.ID
	subject := delivery.Subject{Connection: &conn}
	if in.Destination != nil {
		subject = delivery.Subject{Destination: &delivery.Destination{ID: *in.Destination, Type: TypeBridge,
			Connection: &conn}}
	}
	res, err := bridge.CheckDestination(ctx, c, bridge.Interactive(s.cfg.Interactive, subject), in.ChannelID)
	if err != nil {
		return destinations.BridgeChannelChecked{}, err
	}
	return destinations.BridgeChannelChecked{ConnectionID: row.ID, Check: res}, nil
}

// StoredInfo is the info that a read of the Bridge Connection conn stored, with when it was read (bridge.InfoStore);
// there is none for a Connection that is deleted or no longer at conn's version, or that was never read.
func (s *Service) StoredInfo(ctx context.Context, conn bridge.Conn) (bridge.Stored, bool, error) {
	r, err := s.cfg.Store.GetConnection(ctx, dbgen.GetConnectionParams{OrgID: s.cfg.OrgID,
		ID: pgtype.Int8{Int64: conn.ID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return bridge.Stored{}, false, nil
	}
	if err != nil {
		return bridge.Stored{}, false, fmt.Errorf("read the info of the connection %s: %w", conn.PublicID, err)
	}
	if r.Type != TypeBridge || r.Version != conn.Version || len(r.BridgeInfo) == 0 || !r.BridgeInfoReadAt.Valid {
		return bridge.Stored{}, false, nil
	}
	// A copy that does not decode is none, so that it is read again.
	if i, err := bridge.DecodeInfo(r.BridgeInfo); err == nil {
		return bridge.Stored{Info: i, ReadAt: r.BridgeInfoReadAt.Time.UTC()}, true, nil
	}
	return bridge.Stored{}, false, nil
}

// StoreInfo stores the info i that a read of the Bridge Connection conn found at at, on the business clock, as the
// bridge sent it, and sends the Connection's live hint (bridge.InfoStore). It stores nothing for a Connection that
// changed since conn was read.
func (s *Service) StoreInfo(ctx context.Context, conn bridge.Conn, i bridge.Info, at time.Time) error {
	return s.cfg.Store.InTx(ctx, func(q Queries) error {
		n, err := q.SetBridgeInfo(ctx, dbgen.SetBridgeInfoParams{OrgID: s.cfg.OrgID, ID: conn.ID, Info: i.Raw,
			ReadAt: at.UTC(), Version: conn.Version})
		if err != nil {
			return fmt.Errorf("record the info of the connection %s: %w", conn.PublicID, err)
		}
		if n == 0 {
			return nil
		}
		return q.Notify(ctx, db.Hint{OrgID: s.cfg.OrgID, Type: Hint, ID: conn.PublicID})
	})
}

// PushConnection is the Bridge Connection publicID for the push callback (bridge.Connections): its client, kept until
// the Connection changes, whose settings hold the push secret, and its event delivery mode. A Connection that is
// unknown, deleted or not a Bridge Connection is bridge.ErrNoConnection.
func (s *Service) PushConnection(ctx context.Context, publicID string) (bridge.Conn, string, error) {
	row, err := s.row(ctx, s.cfg.Store, publicID)
	if errors.Is(err, ErrNotFound) || (err == nil && row.Type != TypeBridge) {
		return bridge.Conn{}, "", bridge.ErrNoConnection
	}
	if err != nil {
		return bridge.Conn{}, "", err
	}
	c, err := s.cachedBridgeClient(row.ID, row.Version, func() (*bridge.Client, error) { return s.bridgeClient(row) })
	if err != nil {
		return bridge.Conn{}, "", err
	}
	return bridge.Conn{ID: row.ID, PublicID: row.PublicID, Version: row.Version, Client: c},
		row.BridgeEventDelivery.String, nil
}

// PollingConnections lists the Bridge Connections in poll mode for the Leader's poller (bridge.Connections), each with
// its client, kept until the Connection changes, and its stored cursor. A Connection whose client cannot be built is
// left out, with bridge_poll_failed.
func (s *Service) PollingConnections(ctx context.Context) ([]bridge.Polled, error) {
	rows, err := s.cfg.Store.ListBridgePollingConnections(ctx, s.cfg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list the bridge connections to poll: %w", err)
	}
	out := make([]bridge.Polled, 0, len(rows))
	for _, r := range rows {
		row := dbgen.GetConnectionRow{ID: r.ID, PublicID: r.PublicID, Type: TypeBridge, BridgeBaseUrl: r.BridgeBaseUrl,
			BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID,
			BotTokenUpdatedAt: r.BotTokenUpdatedAt, Proxy: r.Proxy, ProxyPasswordCiphertext: r.ProxyPasswordCiphertext,
			ProxyPasswordKeyID: r.ProxyPasswordKeyID, ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt,
			Version: r.Version}
		c, err := s.cachedBridgeClient(r.ID, r.Version, func() (*bridge.Client, error) { return s.bridgeClient(row) })
		if err != nil {
			s.cfg.Log.Log(ctx, logging.BridgePollFailed, logging.F("connection", r.PublicID),
				logging.F("error", err.Error()))
			continue
		}
		p := bridge.Polled{Conn: bridge.Conn{ID: r.ID, PublicID: r.PublicID, Version: r.Version, Client: c}}
		if r.BridgeEventsCursor.Valid {
			p.Cursor = &r.BridgeEventsCursor.String
		}
		out = append(out, p)
	}
	return out, nil
}

// StoreEventsCursor stores the cursor of the events of the Bridge Connection c, or drops it when cursor is nil
// (bridge.Connections); it stores nothing once the Connection changed since c was read, or was deleted. It is not a
// change of the configuration.
func (s *Service) StoreEventsCursor(ctx context.Context, c bridge.Conn, cursor *string) error {
	arg := dbgen.StoreBridgeEventsCursorParams{OrgID: s.cfg.OrgID, ID: c.ID, Version: c.Version}
	if cursor != nil {
		arg.Cursor = pgtype.Text{String: *cursor, Valid: true}
	}
	if _, err := s.cfg.Store.StoreBridgeEventsCursor(ctx, arg); err != nil {
		return fmt.Errorf("store the events cursor of the connection %s: %w", c.PublicID, err)
	}
	return nil
}

// HandleEventOnce runs f for the event eventID of the Bridge Connection connectionID unless its id was recorded
// within bridge.event_dedup_window (bridge.Connections), in one transaction that holds the claim's advisory lock — of
// the class bridgeEventLockClass, keyed by the Connection and the event id — so that a second copy of the event waits
// for the first and then finds its id recorded; once f succeeded, the id is recorded at the business clock's now and
// the transaction commits. f runs its own transactions on other connections of the pool: the claim holds no row lock
// that the dispatcher takes. An error of f records nothing, so that the event comes again; a crash after f and before
// the commit repeats f once on redelivery, which the dispatcher's preconditions turn into a refusal or a no-op
// (ADR-0017).
func (s *Service) HandleEventOnce(ctx context.Context, connectionID int64, eventID string,
	f func(context.Context) error) (bool, error) {
	ran := false
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		if err := q.LockBridgeEvent(ctx, dbgen.LockBridgeEventParams{LockClass: bridgeEventLockClass,
			ConnectionID: connectionID, EventID: eventID}); err != nil {
			return fmt.Errorf("lock the event: %w", err)
		}
		seen, err := q.BridgeEventSeen(ctx, dbgen.BridgeEventSeenParams{OrgID: s.cfg.OrgID,
			ConnectionID: connectionID, EventID: eventID})
		if err != nil {
			return fmt.Errorf("read the record of the event: %w", err)
		}
		if seen {
			return nil
		}
		if err := f(ctx); err != nil {
			return err
		}
		if err := q.RecordBridgeEvent(ctx, dbgen.RecordBridgeEventParams{OrgID: s.cfg.OrgID,
			ConnectionID: connectionID, EventID: eventID, SeenAt: s.cfg.Clocks.Business.Now()}); err != nil {
			return fmt.Errorf("record the event: %w", err)
		}
		ran = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return ran, nil
}

// PruneEventsSeen deletes at most limit records of Bridge event ids seen bridge.event_dedup_window before now, on the
// business clock, for short-lived pruning, and returns how many it deleted.
func (s *Service) PruneEventsSeen(ctx context.Context, now time.Time, limit int32) (int64, error) {
	n, err := s.cfg.Store.PruneBridgeEventsSeen(ctx, dbgen.PruneBridgeEventsSeenParams{OrgID: s.cfg.OrgID,
		Before: now.Add(-bridge.EventDedupWindow), BatchSize: limit})
	if err != nil {
		return 0, fmt.Errorf("prune the bridge events seen: %w", err)
	}
	return n, nil
}
