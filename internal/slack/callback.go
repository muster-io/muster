// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/requestlog"
)

// CallbackPattern is the route of slackInteraction on the callback mux of the ingest listener: Slack posts the presses
// of a Connection in http mode to CallbackPath followed by its public_id (C-24.FR-9).
const CallbackPattern = "POST " + CallbackPath + "{connection_id}"

// RequestTolerance is slack.request_tolerance: the longest a press's X-Slack-Request-Timestamp may be from Muster's
// real clock, either way, as Slack recommends (F-104).
const RequestTolerance = 5 * time.Minute

// MaxRequestBytes bounds the raw body of a press, which is read whole before its form is parsed.
const MaxRequestBytes = 1 << 20

// The headers of a request that Slack signs (F-104).
const (
	HeaderSignature = "X-Slack-Signature"
	HeaderTimestamp = "X-Slack-Request-Timestamp"
)

// signatureVersion is the version of Slack's signature: the prefix of X-Slack-Signature and of the base string it
// signs.
const signatureVersion = "v0"

// The reasons of slack_press_rejected: answered 401 alike, or 400.
const (
	reasonBadSignature      = "bad_signature"
	reasonStale             = "stale"
	reasonUnknownConnection = "unknown_connection"
	reasonWrongMode         = "wrong_mode"
	reasonNoPayload         = "no_payload"
	reasonTooLarge          = "too_large"
)

var timestampRe = regexp.MustCompile(`^[0-9]{1,12}$`)

// ErrNoConnection is a press for a Connection that is unknown, deleted or not a Slack one.
var ErrNoConnection = errors.New("no such slack connection")

// Conn is a Slack Connection as its presses need it: its ids, its event mode and its Web API client, which answers
// the people who pressed.
type Conn struct {
	ID       int64
	PublicID string
	Mode     string
	Client   *Client
}

// Connections find the Connection a press names with its Slack signing secret: a Slack Connection in socket mode, or
// in http mode without a stored secret, comes with an empty one, which the callback refuses; one that is unknown,
// deleted or not a Slack one is ErrNoConnection. *connections.Service implements it.
type Connections interface {
	SlackPressConnection(ctx context.Context, publicID string) (Conn, logging.Secret, error)
}

// Handler takes the payload of each press the callback confirmed and handles it in the background, in ctx; *Presses
// implements it.
type Handler interface {
	Go(ctx context.Context, c Conn, payload []byte)
}

// CallbackConfig is what the callback of presses in http mode needs.
type CallbackConfig struct {
	Connections Connections
	// Presses handle each confirmed press after its answer.
	Presses Handler
	// Real is the real clock that X-Slack-Request-Timestamp is checked against (S-020).
	Real clock.Clock
	// Tolerance is slack.request_tolerance; zero is RequestTolerance.
	Tolerance time.Duration
	// Base is the context of the process that a press is handled in once it is answered: never the request's, which
	// ends with its answer, and not one that the start of a shutdown ends, since the shutdown drains the presses and
	// ends Base with its grace period; nil takes the request's without its end.
	Base context.Context //nolint:containedctx // the process context the presses outlive their requests in
	Log  *logging.Logger
}

// NewCallback is the callback of the presses of Slack Connections in http mode, slackInteraction (C-24.FR-9, AC-11),
// which answers every request itself. The raw body is read, up to MaxRequestBytes, before anything else; a larger one
// is answered 400. Nothing changes before a request is authenticated: an unknown or deleted Connection, one in socket
// mode, an X-Slack-Request-Timestamp further than the Tolerance from the real clock, either way, and an
// X-Slack-Signature that is not `v0=` and the hex HMAC-SHA256 of `v0:<timestamp>:<raw body>` under the Connection's
// Slack signing secret, compared in constant time, are all answered 401 with one body, so that the endpoint reveals
// nothing about which Connections exist. An authenticated body without a `payload` field that is a JSON object is
// answered 400. Any other request is answered 200 with an empty body at once, and its payload goes to Presses in the
// process's context, since Slack waits only 3 seconds (F-093). Each refusal logs slack_press_rejected with its reason,
// never the signature, the secret, the headers or the body; a Connection that cannot be read is answered 500 and logs
// slack_press as failed. Every request for a known Connection is in its Request log, refused ones included, through
// the annotation of requestlog.Inbound (C-23.FR-1).
func NewCallback(cfg CallbackConfig) http.Handler {
	if cfg.Tolerance <= 0 {
		cfg.Tolerance = RequestTolerance
	}
	return &callback{cfg: cfg}
}

type callback struct {
	cfg CallbackConfig
}

func (h *callback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, readErr := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	// The Connection is read before any refusal, so that every request for a known one is in its Request log
	// (C-23.FR-1); the answers keep their order, so that the endpoint still tells nothing about which Connections exist.
	conn, secret, err := h.cfg.Connections.SlackPressConnection(ctx, r.PathValue("connection_id"))
	known := ""
	if err == nil {
		known = conn.PublicID
		requestlog.Annotate(ctx, requestlog.Links{Connection: conn.ID, ConnectionID: conn.PublicID})
	}
	switch {
	case readErr != nil:
		h.badRequest(ctx, w, known, reasonNoPayload)
		return
	case len(body) > MaxRequestBytes:
		h.badRequest(ctx, w, known, reasonTooLarge)
		return
	}
	switch {
	case errors.Is(err, ErrNoConnection):
		h.unauthorized(ctx, w, "", reasonUnknownConnection)
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "internal", "Internal error", "The press could not be handled.")
		h.cfg.Log.Log(ctx, logging.SlackPress, logging.F("connection", ""), logging.F("group", ""),
			logging.F("command", ""), logging.F("outcome", outcomeFailed), logging.F("error", err.Error()))
		return
	case conn.Mode != EventModeHTTP || secret == "":
		h.unauthorized(ctx, w, known, reasonWrongMode)
		return
	}
	timestamp := r.Header.Get(HeaderTimestamp)
	if !h.fresh(timestamp) {
		h.unauthorized(ctx, w, known, reasonStale)
		return
	}
	if !verifySignature(secret, timestamp, body, r.Header.Get(HeaderSignature)) {
		h.unauthorized(ctx, w, known, reasonBadSignature)
		return
	}
	payload, ok := payloadOf(body)
	if !ok {
		h.badRequest(ctx, w, known, reasonNoPayload)
		return
	}
	base := h.cfg.Base
	if base == nil {
		base = context.WithoutCancel(ctx)
	}
	h.cfg.Presses.Go(base, conn, payload)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// fresh reports whether timestamp is Unix seconds within the Tolerance of the real clock, either way.
func (h *callback) fresh(timestamp string) bool {
	if !timestampRe.MatchString(timestamp) {
		return false
	}
	sec, _ := strconv.ParseInt(timestamp, 10, 64) // at most 12 digits, which always fit
	d := h.cfg.Real.Now().Sub(time.Unix(sec, 0))
	return d <= h.cfg.Tolerance && d >= -h.cfg.Tolerance
}

// verifySignature reports whether header is Slack's signature of body sent at timestamp under secret (F-104): `v0=`
// and the hex HMAC-SHA256 of `v0:<timestamp>:<body>`, compared in constant time.
func verifySignature(secret logging.Secret, timestamp string, body []byte, header string) bool {
	given, ok := strings.CutPrefix(header, signatureVersion+"=")
	if !ok || secret == "" {
		return false
	}
	sum, err := hex.DecodeString(given)
	if err != nil || len(sum) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signatureVersion + ":" + timestamp + ":"))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sum)
}

// payloadOf is the `payload` field of a form-encoded body when it is a JSON object, as a copy of its own.
func payloadOf(body []byte) ([]byte, bool) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, false
	}
	p := []byte(form.Get("payload"))
	if !json.Valid(p) || !bytes.HasPrefix(bytes.TrimLeft(p, " \t\r\n"), []byte("{")) {
		return nil, false
	}
	return p, true
}

func (h *callback) reject(ctx context.Context, connection, reason string) {
	h.cfg.Log.Log(ctx, logging.SlackPressRejected, logging.F("connection", connection), logging.F("reason", reason))
}

// unauthorized answers 401 with the one body of every refusal before authentication.
func (h *callback) unauthorized(ctx context.Context, w http.ResponseWriter, connection, reason string) {
	h.reject(ctx, connection, reason)
	writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Unauthenticated",
		"The request is not signed by Slack for this Connection.")
}

// badRequest answers 400: the body is no press Slack could have sent.
func (h *callback) badRequest(ctx context.Context, w http.ResponseWriter, connection, reason string) {
	h.reject(ctx, connection, reason)
	writeProblem(w, http.StatusBadRequest, "validation-failed", "Bad request", "The body is not a Slack interaction.")
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
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: problemBase + typ, Title: title, Status: status, Detail: detail})
}
