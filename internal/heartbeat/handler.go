// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/logging"
)

// The routes of the Heartbeat endpoint, as log lines name them: never the path, which may carry the token.
const (
	RouteHeader = integrations.HeartbeatPath
	RoutePath   = integrations.HeartbeatPath + "/{ingest_token}"
)

// problemBase is where the problem type URIs live (x-problem-types).
const problemBase = "https://muster-io.github.io/muster/problems/"

// Authenticator finds the Integration of a signal by its token: internal/integrations.
type Authenticator interface {
	Authenticate(ctx context.Context, value string) (integrations.Caller, error)
}

// Signaler records a signal of an Integration: the Service.
type Signaler interface {
	Signal(ctx context.Context, integrationID int64) error
}

// HandlerConfig is what the Heartbeat handler serves with.
type HandlerConfig struct {
	Auth    Authenticator
	Signals Signaler
	Log     *logging.Logger
	// TrustedProxies are MUSTER_TRUSTED_PROXIES, for the client address of a refused request.
	TrustedProxies []netip.Prefix
	// BodyLimit is ingest.body_limit, how much of a body is read and discarded; zero takes ingest.BodyLimit.
	BodyLimit int64
}

type handler struct {
	HandlerConfig
}

// NewHandler is the Heartbeat endpoint of the ingest listener (C-07.FR-1): GET and POST on /api/v1/heartbeat with the
// token as Authorization: Bearer, and on /api/v1/heartbeat/{ingest_token} with the token in the path; HEAD is served
// as GET, as net/http does. Any other request under it answers 404, as the API answers a path or a method it has no
// operation for.
func NewHandler(cfg HandlerConfig) http.Handler {
	if cfg.BodyLimit <= 0 {
		cfg.BodyLimit = ingest.BodyLimit
	}
	h := &handler{HandlerConfig: cfg}
	mux := http.NewServeMux()
	header := func(w http.ResponseWriter, r *http.Request) {
		h.signal(w, r, RouteHeader, bearer(r.Header.Get("Authorization")))
	}
	path := func(w http.ResponseWriter, r *http.Request) {
		h.signal(w, r, RoutePath, r.PathValue("ingest_token"))
	}
	mux.HandleFunc("GET "+RouteHeader, header)
	mux.HandleFunc("POST "+RouteHeader, header)
	mux.HandleFunc("GET "+RoutePath, path)
	mux.HandleFunc("POST "+RoutePath, path)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "not-found", "Not found", "No such endpoint.")
	})
	return mux
}

// bearer is the token of an Authorization header with the Bearer scheme, or empty.
func bearer(header string) string {
	scheme, value, _ := strings.Cut(header, " ")
	if !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

// signal checks the token of a request, discards its body unread up to the body limit and records the signal; it
// answers 204 whatever the body, also while the Integration's Heartbeat is off, and 401 for a missing, wrong or
// revoked token, an API token or a token of a deleted Integration. Every line about it names its route, never its
// path.
func (h *handler) signal(w http.ResponseWriter, r *http.Request, route, token string) {
	ctx := r.Context()
	caller, err := h.Auth.Authenticate(ctx, token)
	if errors.Is(err, integrations.ErrInvalidToken) {
		h.reject(w, r, route, caller.Integration)
		return
	}
	if err != nil {
		h.fail(w, r, route, integrations.Unknown, err)
		return
	}
	// The body is never parsed; reading it lets the connection serve the next request.
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, h.BodyLimit))
	if err := h.Signals.Signal(ctx, caller.IntegrationID); err != nil {
		h.fail(w, r, route, caller.Integration, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// reject answers 401 and logs it as a refused request of the ingest listener.
func (h *handler) reject(w http.ResponseWriter, r *http.Request, route, integration string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Unauthenticated",
		"The Integration token is missing, wrong or revoked.")
	addr := auth.ClientAddress(r.RemoteAddr, r.Header.Values("X-Forwarded-For"), h.TrustedProxies)
	client := ""
	if addr.IsValid() {
		client = addr.String()
	}
	h.Log.Log(r.Context(), logging.IngestRejected, logging.F("integration", integration),
		logging.F("outcome", ingest.OutcomeUnauthorized), logging.F("route_pattern", route),
		logging.F("client_address", client))
}

// fail answers 500 for a failed lookup or write; the next signal replaces it.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, route, integration string, err error) {
	writeProblem(w, http.StatusInternalServerError, "internal", "Internal error",
		"The Heartbeat signal could not be recorded; send it again.")
	h.Log.Log(r.Context(), logging.IngestFailed, logging.F("integration", integration),
		logging.F("route_pattern", route), logging.F("error", err.Error()))
}

// problem is an RFC 9457 problem. It has no instance: the path of a request may carry its token.
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
