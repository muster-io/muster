// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/muster-io/muster/internal/logging"
)

// The webhook endpoint of Telegram Connections on the ingest listener, telegramWebhook (C-14.AC-11): Telegram posts
// the updates of a Connection in the webhook mode to WebhookPath followed by its public_id.
const (
	WebhookPath    = "/api/v1/callbacks/telegram/"
	WebhookPattern = "POST " + WebhookPath + "{connection_id}"
	// SecretTokenHeader carries the secret token Muster registered with setWebhook.
	SecretTokenHeader = "X-Telegram-Bot-Api-Secret-Token" //nolint:gosec // G101: a header name, not a credential
)

// secretBytes is the randomness of a webhook's secret token: 256 bits, 43 characters of base64url, which the Bot API
// accepts (1 to 256 characters of A-Z, a-z, 0-9, _ and -).
const secretBytes = 32

// NewSecret is a new random secret token for a webhook.
func NewSecret() (logging.Secret, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return logging.Secret(base64.RawURLEncoding.EncodeToString(b)), nil
}

// ErrNoWebhook is a webhook request for a Connection that does not exist, is deleted, is not a Telegram one or is not
// in the webhook mode.
var ErrNoWebhook = errors.New("no telegram connection in the webhook mode")

// Hooks find the Connection of a webhook request and its secret token; *connections.Service implements it.
type Hooks interface {
	Hooked(ctx context.Context, publicID string) (Conn, logging.Secret, error)
}

// WebhookConfig is what the webhook endpoint needs.
type WebhookConfig struct {
	Connections Hooks
	Router      *Router
	// BodyLimit bounds the body of an update.
	BodyLimit int64
	Log       *logging.Logger
}

// NewWebhook is the webhook endpoint (C-14.FR-1, AC-11): it answers 401 and changes nothing unless the Connection
// exists, is in the webhook mode and the header X-Telegram-Bot-Api-Secret-Token equals its secret token, compared in
// constant time; otherwise it hands the update to the router and answers 200. A body that is not an update is 400; a
// router that failed is 500, and Telegram sends the update again.
func NewWebhook(cfg WebhookConfig) http.Handler {
	return &webhook{cfg: cfg}
}

type webhook struct {
	cfg WebhookConfig
}

func (h *webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	publicID := r.PathValue("connection_id")
	if publicID == "" {
		publicID = strings.TrimPrefix(r.URL.Path, WebhookPath)
	}
	conn, secret, err := h.cfg.Connections.Hooked(ctx, publicID)
	switch {
	case errors.Is(err, ErrNoWebhook):
		unauthorized(w)
		return
	case err != nil:
		h.failed(w, r, publicID, err)
		return
	}
	given := r.Header.Get(SecretTokenHeader)
	if secret == "" || subtle.ConstantTimeCompare([]byte(given), []byte(secret)) != 1 {
		unauthorized(w)
		return
	}
	limit := h.cfg.BodyLimit
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		writeProblem(w, http.StatusBadRequest, "validation-failed", "Bad request", "The body is not a Telegram update.")
		return
	}
	u, err := ParseUpdate(body)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "validation-failed", "Bad request", "The body is not a Telegram update.")
		return
	}
	if _, err := h.cfg.Router.Route(ctx, conn, u); err != nil {
		h.failed(w, r, conn.PublicID, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// failed answers 500 and logs telegram_update_failed; Telegram sends the update again.
func (h *webhook) failed(w http.ResponseWriter, r *http.Request, publicID string, err error) {
	writeProblem(w, http.StatusInternalServerError, "internal", "Internal error",
		"The update could not be processed; Telegram sends it again.")
	h.cfg.Log.Log(r.Context(), logging.TelegramUpdateFailed, logging.F("connection", publicID),
		logging.F("error", err.Error()))
}

func unauthorized(w http.ResponseWriter) {
	writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Unauthenticated",
		"The secret token header is missing or wrong, or the Connection takes no webhook.")
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
