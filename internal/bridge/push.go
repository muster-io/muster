// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/webhooks"
)

// PushPattern is the route of bridgeEvent on the callback mux of the ingest listener: a bridge in push mode posts the
// events of a Connection to CallbackPath followed by its public_id (C-22.FR-11).
const PushPattern = "POST " + CallbackPath + "{connection_id}"

// PushTolerance is bridge.push_tolerance: the longest a pushed event's webhook-timestamp may be from Muster's real
// clock, either way, as Standard Webhooks recommends.
const PushTolerance = 5 * time.Minute

// MaxEventBytes bounds the body of a pushed event: an event is a few kilobytes at most, a Thread message of 4,000
// characters included.
const MaxEventBytes = 64 << 10

// The reasons of bridge_event_rejected.
const (
	reasonUnknownConnection = "unknown_connection"
	reasonPollMode          = "poll_mode"
	reasonSignature         = "signature"
	reasonTimestamp         = "timestamp"
	reasonNotAnEvent        = "not_an_event"
	reasonIDMismatch        = "id_mismatch"
	reasonTooLarge          = "too_large"
)

var timestampRe = regexp.MustCompile(`^[0-9]{1,12}$`)

// PushConfig is what the push callback needs.
type PushConfig struct {
	Connections Connections
	Events      *Events
	// Real is the real clock that webhook-timestamp is checked against.
	Real clock.Clock
	// Tolerance is bridge.push_tolerance; zero is PushTolerance.
	Tolerance time.Duration
	// BodyLimit bounds the body of an event; zero is MaxEventBytes.
	BodyLimit int64
	Log       *logging.Logger
}

// NewPush is the push callback of Bridge Connections, bridgeEvent (C-22.FR-11, AC-14), which answers every request
// itself. Nothing changes before a request is authenticated: an unknown or deleted Connection, one in poll mode, a
// missing Standard Webhooks header, a webhook-signature without a v1 signature of webhook-id, webhook-timestamp and the
// body under the Connection's push secret, compared in constant time, and a webhook-timestamp further than Tolerance
// from the real clock are all answered 401 with one body, so that the endpoint reveals nothing about which Connections
// exist; the signature is checked before the timestamp, so that an unsigned request does not learn whether its
// timestamp was in range. A body larger than BodyLimit, one that is no event of the Bridge protocol and an event whose
// id is not webhook-id are answered 400, and the bridge drops them. Otherwise the event goes to Events, which waits for
// a free slot, handles it once by its id and answers the person before the 204; an event whose handling failed is
// answered 500 and is not recorded, so that the bridge sends it again. Each refusal logs bridge_event_rejected with its
// reason, never the signature, the secret, the headers or the body.
func NewPush(cfg PushConfig) http.Handler {
	if cfg.Tolerance <= 0 {
		cfg.Tolerance = PushTolerance
	}
	if cfg.BodyLimit <= 0 {
		cfg.BodyLimit = MaxEventBytes
	}
	return &push{cfg: cfg}
}

type push struct {
	cfg PushConfig
}

func (h *push) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := r.PathValue("connection_id")
	if path == "" {
		path = strings.TrimPrefix(r.URL.Path, CallbackPath)
	}
	// The log names the Connection only by an id that could be one, never by whatever the path holds.
	named, err := publicid.Parse(publicid.Connection, path)
	if err != nil {
		named = ""
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, h.cfg.BodyLimit+1))
	switch {
	case err != nil:
		h.badRequest(ctx, w, named, reasonNotAnEvent, "The body is not an event of the Bridge protocol.")
		return
	case int64(len(body)) > h.cfg.BodyLimit:
		h.badRequest(ctx, w, named, reasonTooLarge, "The body is larger than an event of the Bridge protocol.")
		return
	}
	conn, mode, err := h.cfg.Connections.PushConnection(ctx, path)
	switch {
	case errors.Is(err, ErrNoConnection):
		h.unauthorized(ctx, w, named, reasonUnknownConnection)
		return
	case err != nil:
		h.failed(w)
		h.cfg.Log.Log(ctx, logging.BridgeEventFailed, logging.F("connection", named), logging.F("error", err.Error()))
		return
	case mode != EventDeliveryPush:
		h.unauthorized(ctx, w, conn.PublicID, reasonPollMode)
		return
	}
	id := r.Header.Get(webhooks.HeaderID)
	timestamp := r.Header.Get(webhooks.HeaderTimestamp)
	signature := r.Header.Get(webhooks.HeaderSignature)
	if id == "" || timestamp == "" || signature == "" || conn.Client == nil ||
		!webhooks.Verify(conn.Client.settings.PushSecret, id, timestamp, body, signature) {
		h.unauthorized(ctx, w, conn.PublicID, reasonSignature)
		return
	}
	if !h.fresh(timestamp) {
		h.unauthorized(ctx, w, conn.PublicID, reasonTimestamp)
		return
	}
	ev, err := DecodeEvent(body)
	if err != nil {
		h.badRequest(ctx, w, conn.PublicID, reasonNotAnEvent, "The body is not an event of the Bridge protocol.")
		return
	}
	if ev.ID != id {
		h.badRequest(ctx, w, conn.PublicID, reasonIDMismatch, "The id of the event is not webhook-id.")
		return
	}
	if err := h.cfg.Events.Handle(ctx, conn, ev); err != nil {
		// Events logged bridge_event_failed.
		h.failed(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// fresh reports whether webhook-timestamp is Unix seconds within Tolerance of the real clock, either way.
func (h *push) fresh(timestamp string) bool {
	if !timestampRe.MatchString(timestamp) {
		return false
	}
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	d := h.cfg.Real.Now().Sub(time.Unix(sec, 0))
	return d <= h.cfg.Tolerance && d >= -h.cfg.Tolerance
}

func (h *push) reject(ctx context.Context, connection, reason string) {
	h.cfg.Log.Log(ctx, logging.BridgeEventRejected, logging.F("connection", connection), logging.F("reason", reason))
}

// unauthorized answers 401 with the one body of every refusal before authentication.
func (h *push) unauthorized(ctx context.Context, w http.ResponseWriter, connection, reason string) {
	h.reject(ctx, connection, reason)
	writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Unauthenticated",
		"The signature, the Connection or its event delivery mode is wrong.")
}

// badRequest answers 400: the bridge drops the event.
func (h *push) badRequest(ctx context.Context, w http.ResponseWriter, connection, reason, detail string) {
	h.reject(ctx, connection, reason)
	writeProblem(w, http.StatusBadRequest, "validation-failed", "Bad request", detail)
}

// failed answers 500: the bridge sends the event again.
func (h *push) failed(w http.ResponseWriter) {
	writeProblem(w, http.StatusInternalServerError, "internal", "Internal error",
		"The event could not be handled; the bridge sends it again.")
}

// problemBase is where the problem type URIs live (x-problem-types).
const problemBase = "https://muster-io.github.io/muster/problems/"

// problem is an RFC 9457 problem. It has no instance, like the other answers of the ingest listener.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func writeProblem(w http.ResponseWriter, status int, typ, title, detail string) {
	w.Header().Set("Content-Type", problemType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: problemBase + typ, Title: title, Status: status, Detail: detail})
}
