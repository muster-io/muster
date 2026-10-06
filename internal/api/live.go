// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/live"
)

// StreamLiveUpdates is streamLiveUpdates: the server-sent events stream of change hints (C-09.FR-25, ADR-0009). It
// lasts until the client goes, the session ends or the server shuts down; the reconnect of an ended session gets 401.
func (s *Server) StreamLiveUpdates(ctx context.Context, _ gen.StreamLiveUpdatesRequestObject) (
	gen.StreamLiveUpdatesResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := s.live.Subscribe(live.Subscriber{SessionID: id.Session.ID, Admin: id.Can(permissionSystemStatus)})
	if err != nil {
		return nil, err
	}
	return liveStream{ctx: ctx, hub: s.live, sub: sub, real: s.real}, nil
}

// streamWriteTimeout bounds each write of a stream, so that a client that stopped reading is cut off instead of
// holding its handler and, at shutdown, the drain.
const streamWriteTimeout = 10 * time.Second

// liveStream writes the stream itself rather than through the generated response, so that each event is flushed
// through the middleware's writers and the subscription ends with the response.
type liveStream struct {
	ctx  context.Context //nolint:containedctx // the request's context, which the generated visitor does not pass on
	hub  Live
	sub  *live.Subscription
	real clock.Clock
}

func (l liveStream) VisitStreamLiveUpdatesResponse(w http.ResponseWriter) error {
	defer l.hub.Unsubscribe(l.sub)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// A reverse proxy such as nginx would otherwise buffer the events.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	// The connection may serve further requests after the stream: they write without the stream's deadline.
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	write := writerFunc(func(p []byte) (int, error) {
		// A writer without deadlines, as in tests, writes without one.
		_ = rc.SetWriteDeadline(l.real.Now().Add(streamWriteTimeout))
		return w.Write(p)
	})
	// Once the answer has begun, a failed write only means the client went away: there is nothing left to answer.
	_ = l.hub.Stream(l.ctx, write, rc.Flush, l.sub)
	return nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
