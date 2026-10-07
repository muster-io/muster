// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package live is the stream of live-update hints (ADR-0009; C-09.FR-25, C-03.FR-18): each replica runs one Hub that
// takes the hints of LISTEN/NOTIFY — writers send them in the transaction of their change — and of its own notice
// watcher, and fans them out to its server-sent events streams. A hint carries no data, so a client re-reads through
// the API and authorization stays in one place. A stream ends when its session ends, so that the reconnect gets 401.
package live

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/db"
)

// The hint types of this release; later capabilities add theirs and list them in resyncTypes.
const (
	HintOrganization  = "organization"
	HintSystemNotices = "system-notices"
	// HintAlertGroup names an Alert Group that changed; HintAlertGroups, without an id, says that new Alert Groups
	// may match a list (C-09.FR-25).
	HintAlertGroup  = "alert-group"
	HintAlertGroups = "alert-groups"
)

// resyncTypes are the hints of everything a client may hold, sent to every stream when the replica listens again
// after a loss, because the hints sent meanwhile are gone.
var resyncTypes = []string{HintOrganization, HintSystemNotices, HintAlertGroups}

// alertGroupHints are the hint types about Alert Groups, which only streams whose identity reads Alert Groups get.
var alertGroupHints = map[string]bool{HintAlertGroup: true, HintAlertGroups: true}

const (
	// MaxStreams is live.max_streams: the streams one replica serves at once.
	MaxStreams = 1000
	// MaxStreamsPerSession is live.max_streams_per_session: the streams of one session, one per open tab.
	MaxStreamsPerSession = 10
	// CheckInterval is how often a replica checks that the sessions of its streams are still usable, and evaluates
	// the Organization-wide notices.
	CheckInterval = 5 * time.Second
	// bufferSize is how many hints wait for a slow stream; a stream that falls further behind is closed, and its
	// client reconnects and reads everything again.
	bufferSize = 32
)

// ErrTooManyStreams refuses a stream beyond MaxStreams or MaxStreamsPerSession.
var ErrTooManyStreams = errors.New("too many live-updates streams")

// Hint is one live-update hint: its type and the public_id it names, empty for a whole collection.
type Hint struct {
	Type string
	ID   string
}

// Subscriber is who reads a stream: the session, whether its identity sees the notices for Admins
// (system-status:read) and whether it reads Alert Groups (alert-groups:read).
type Subscriber struct {
	SessionID   int64
	Admin       bool
	AlertGroups bool
}

// accepts reports whether the subscriber may get a hint of the type: the hints about Alert Groups go only to those
// who read them.
func (s Subscriber) accepts(hintType string) bool {
	return !alertGroupHints[hintType] || s.AlertGroups
}

// Subscription is one stream's share of the Hub.
type Subscription struct {
	Subscriber
	hints chan Hint
	done  chan struct{}
	once  sync.Once
}

func (s *Subscription) close() {
	s.once.Do(func() { close(s.done) })
}

// Hub fans the hints of one Organization out to the streams of this replica.
type Hub struct {
	orgID int64
	// live returns which of the sessions are still usable.
	live func(ctx context.Context, ids []int64) ([]int64, error)
	// every makes the ticks of the keepalive comments of the streams.
	every func(time.Duration) (<-chan time.Time, func())

	mu         sync.Mutex
	subs       map[*Subscription]struct{}
	perSession map[int64]int
	closed     bool
}

// NewHub returns the Hub of the Organization orgID; live tells which sessions are still usable.
func NewHub(orgID int64, live func(ctx context.Context, ids []int64) ([]int64, error)) *Hub {
	return &Hub{
		orgID: orgID, live: live, every: ticker,
		subs: map[*Subscription]struct{}{}, perSession: map[int64]int{},
	}
}

func ticker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Subscribe opens a stream's subscription; beyond MaxStreams on the replica or MaxStreamsPerSession for the session
// it is ErrTooManyStreams, and once the Hub is closed it is a subscription that is already done.
func (h *Hub) Subscribe(s Subscriber) (*Subscription, error) {
	sub := &Subscription{Subscriber: s, hints: make(chan Hint, bufferSize), done: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		sub.close()
		return sub, nil
	}
	if len(h.subs) >= MaxStreams || h.perSession[s.SessionID] >= MaxStreamsPerSession {
		return nil, ErrTooManyStreams
	}
	h.subs[sub] = struct{}{}
	h.perSession[s.SessionID]++
	return sub, nil
}

// Unsubscribe ends a subscription; a stream calls it when it stops.
func (h *Hub) Unsubscribe(sub *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(sub)
}

// remove ends sub; the caller holds mu.
func (h *Hub) remove(sub *Subscription) {
	if _, ok := h.subs[sub]; !ok {
		return
	}
	delete(h.subs, sub)
	if h.perSession[sub.SessionID]--; h.perSession[sub.SessionID] <= 0 {
		delete(h.perSession, sub.SessionID)
	}
	sub.close()
}

// Streams is how many streams the Hub serves.
func (h *Hub) Streams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Receive takes a hint of LISTEN/NOTIFY and sends it to every stream that may get it when it belongs to the Hub's
// Organization.
func (h *Hub) Receive(n db.Hint) {
	if n.OrgID != h.orgID {
		return
	}
	h.Send(Hint{Type: n.Type, ID: n.ID}, nil)
}

// Listening is called each time the replica listens for hints; after a loss it sends every stream the hints of
// everything, because the hints sent meanwhile are gone.
func (h *Hub) Listening(restored bool) {
	if !restored {
		return
	}
	for _, t := range resyncTypes {
		h.Send(Hint{Type: t}, nil)
	}
}

// Send sends hint to the streams whose subscriber to accepts, every stream when to is nil, the hints about Alert
// Groups only to those who read them. A stream whose hints are not read fast enough is closed instead.
func (h *Hub) Send(hint Hint, to func(Subscriber) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if (to != nil && !to(sub.Subscriber)) || !sub.accepts(hint.Type) {
			continue
		}
		select {
		case sub.hints <- hint:
		default:
			h.remove(sub)
		}
	}
}

// CheckSessions closes the streams whose session ended, expired or lost its user, in one query for the replica. When
// the check fails, as when the database is unavailable, every stream stays open until the next check.
func (h *Hub) CheckSessions(ctx context.Context) {
	h.mu.Lock()
	ids := make([]int64, 0, len(h.perSession))
	for id := range h.perSession {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	live, err := h.live(ctx, ids)
	if err != nil {
		return
	}
	usable := make(map[int64]bool, len(ids))
	for _, id := range ids {
		usable[id] = false
	}
	for _, id := range live {
		usable[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		// A stream of a session that was not checked opened during the check; the next one checks it.
		if ok, checked := usable[sub.SessionID]; checked && !ok {
			h.remove(sub)
		}
	}
}

// Run checks the sessions of the streams at every tick until ctx ends, then closes the Hub.
func (h *Hub) Run(ctx context.Context, ticks <-chan time.Time) {
	defer h.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			h.CheckSessions(ctx)
		}
	}
}

// Close ends every stream and refuses new ones; the server closes the Hub when it shuts down, so that the streams do
// not hold up the drain.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for sub := range h.subs {
		h.remove(sub)
	}
}
