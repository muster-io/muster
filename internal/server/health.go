// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package server

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
)

// Health answers the probes of the internal listener (C-02.FR-4): liveness without checks, readiness from the
// database, and not ready once the shutdown has begun.
type Health struct {
	ping         func(context.Context) error
	shuttingDown atomic.Bool
}

// NewHealth checks readiness with ping, which runs SELECT 1 on the main pool within a second.
func NewHealth(ping func(context.Context) error) *Health {
	return &Health{ping: ping}
}

// ShuttingDown makes readiness answer 503 from now on.
func (h *Health) ShuttingDown() {
	h.shuttingDown.Store(true)
}

// Live is getHealthLive: 200 without checks.
func (h *Health) Live(w http.ResponseWriter, _ *http.Request) {
	text(w, http.StatusOK, "ok")
}

// Ready is getHealthReady: 200 while the database answers, 503 otherwise and during the shutdown.
func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	switch {
	case h.shuttingDown.Load():
		text(w, http.StatusServiceUnavailable, "shutting down")
	case h.ping(r.Context()) != nil:
		text(w, http.StatusServiceUnavailable, "database unavailable")
	default:
		text(w, http.StatusOK, "ok")
	}
}

// Internal is the handler of the internal listener: /health/live, /health/ready and metrics at /metrics (getMetrics).
func Internal(h *Health, metrics http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", h.Live)
	mux.HandleFunc("GET /health/ready", h.Ready)
	mux.Handle("GET /metrics", metrics)
	return mux
}

func text(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body+"\n")
}
