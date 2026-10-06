// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// The routes of ingestion, as log lines name them: never the path, which may carry the token (C-05.FR-3).
const (
	RouteHeader = "/api/v1/ingest"
	RoutePath   = "/api/v1/ingest/{ingest_token}"
)

// The outcomes of muster_ingest_requests_total.
const (
	OutcomeAccepted     = "accepted"
	OutcomeUnauthorized = "unauthorized"
	OutcomeTooLarge     = "too_large"
)

// problemBase is where the problem type URIs live (x-problem-types).
const problemBase = "https://muster-io.github.io/muster/problems/"

// Authenticator finds who sends an ingestion request by its token: internal/integrations.
type Authenticator interface {
	Authenticate(ctx context.Context, value string) (integrations.Caller, error)
}

// Storer stores a received body as a Stored Snapshot: the Service.
type Storer interface {
	Store(ctx context.Context, r Received) (Stored, error)
}

// HandlerConfig is what the ingestion handler serves with.
type HandlerConfig struct {
	Auth      Authenticator
	Snapshots Storer
	Log       *logging.Logger
	// Real is the real clock, for the request durations.
	Real clock.Clock
	// TrustedProxies are MUSTER_TRUSTED_PROXIES, for the client address.
	TrustedProxies []netip.Prefix
	// BodyLimit is ingest.body_limit; zero takes BodyLimit.
	BodyLimit int64
}

type handler struct {
	HandlerConfig
}

// NewHandler is the handler of the ingest listener: the two ingestion routes with POST, and 404 for every other
// request, as the API answers a path or a method it has no operation for, until the Heartbeat and the messenger
// callbacks arrive.
func NewHandler(cfg HandlerConfig) http.Handler {
	if cfg.BodyLimit <= 0 {
		cfg.BodyLimit = BodyLimit
	}
	h := &handler{HandlerConfig: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+RouteHeader, func(w http.ResponseWriter, r *http.Request) {
		h.ingest(w, r, RouteHeader, bearer(r.Header.Get("Authorization")))
	})
	mux.HandleFunc("POST "+RoutePath, func(w http.ResponseWriter, r *http.Request) {
		h.ingest(w, r, RoutePath, r.PathValue("ingest_token"))
	})
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

// ingest checks the token, then the size of the body, stores the body and answers 202 once it committed
// (C-05.FR-3, C-05.FR-4). It never answers 400: the body is not parsed. Every request is counted with its outcome,
// and every line about it names its route, never its path.
func (h *handler) ingest(w http.ResponseWriter, r *http.Request, route, token string) {
	start := h.Real.Now()
	defer func() {
		metrics.IngestRequestDuration.With().Update(h.Real.Now().Sub(start).Seconds())
	}()
	ctx := r.Context()
	caller, err := h.Auth.Authenticate(ctx, token)
	if errors.Is(err, integrations.ErrInvalidToken) {
		h.reject(w, r, route, caller.Integration, OutcomeUnauthorized)
		return
	}
	if err != nil {
		h.fail(w, r, route, integrations.Unknown, err)
		return
	}
	if r.ContentLength > h.BodyLimit {
		h.reject(w, r, route, caller.Integration, OutcomeTooLarge)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.BodyLimit))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		h.reject(w, r, route, caller.Integration, OutcomeTooLarge)
		return
	}
	if err != nil {
		// The body did not arrive whole: nothing is stored, and the connection is dropped without an answer, so that
		// the sender never takes it as delivered and sends it again.
		panic(http.ErrAbortHandler)
	}
	stored, err := h.Snapshots.Store(ctx, Received{IntegrationID: caller.IntegrationID, Body: body,
		ContentType: contentType(r.Header.Get("Content-Type"))})
	if err != nil {
		h.fail(w, r, route, caller.Integration, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	metrics.IngestRequests.With(caller.Integration, OutcomeAccepted).Inc()
	h.Log.Log(ctx, logging.SnapshotAccepted, logging.F("integration", caller.Integration),
		logging.F("stored_snapshot", stored.PublicID), logging.F("size_bytes", len(body)),
		logging.F("route_pattern", route))
}

// maxContentType bounds the Content-Type that is stored.
const maxContentType = 255

// contentType is the Content-Type to store: the request's when it is valid UTF-8 and at most maxContentType bytes,
// none otherwise, so that an odd header never makes the write fail.
func contentType(v string) string {
	if len(v) > maxContentType || !utf8.ValidString(v) {
		return ""
	}
	return v
}

// reject answers 401 or 413 for outcome, counts it and logs it.
func (h *handler) reject(w http.ResponseWriter, r *http.Request, route, integration, outcome string) {
	if outcome == OutcomeTooLarge {
		writeProblem(w, http.StatusRequestEntityTooLarge, "payload-too-large", "Payload too large",
			"The body exceeds ingest.body_limit.")
	} else {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Unauthenticated",
			"The Integration token is missing, wrong or revoked.")
	}
	metrics.IngestRequests.With(integration, outcome).Inc()
	addr := auth.ClientAddress(r.RemoteAddr, r.Header.Values("X-Forwarded-For"), h.TrustedProxies)
	client := ""
	if addr.IsValid() {
		client = addr.String()
	}
	h.Log.Log(r.Context(), logging.IngestRejected, logging.F("integration", integration),
		logging.F("outcome", outcome), logging.F("route_pattern", route), logging.F("client_address", client))
}

// fail answers 500 for a failed lookup or write, which Alertmanager retries, and logs it.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, route, integration string, err error) {
	writeProblem(w, http.StatusInternalServerError, "internal", "Internal error",
		"The webhook could not be stored; send it again.")
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
