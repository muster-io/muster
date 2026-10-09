// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
)

// The response mapping of sends and edits (C-14.FR-7, C-11.FR-8) on top of the mapping of every call (client.go),
// with an error inside a 200 body classified the same way: 429 waits exactly parameters.retry_after for the
// Destination, and for the whole Connection when a second Destination of it gets a 429 within ConnectionWindow; "message
// is not modified" is success (F-017); "message to edit not found" and "message can't be edited" mean the Root message
// is gone and is republished — the bot may edit its own messages at any age (F-065), so that answer means the message
// cannot be edited at all; "message to be replied not found" is a lost Thread (F-008); "can't parse entities" is sent
// again without markup; 401, a kicked bot and missing rights are Fatal; an answer that is not JSON, a 5xx and a timeout
// are Transient; anything else is unknown.

// ConnectionWindow is how close two 429s of different Destinations of a Connection are for the second to hold the
// whole Connection.
const ConnectionWindow = 60 * time.Second

// The descriptions that decide an outcome, lower case.
var (
	notModified    = []string{"message is not modified"}
	goneRoot       = []string{"message to edit not found", "message can't be edited"}
	lostThread     = []string{"message to be replied not found"}
	markupRejected = []string{"can't parse entities"}
	fatalRefusals  = []string{"bot was kicked", "not enough rights", "chat not found", "need administrator rights",
		"bot is not a member", "have no rights to send", "chat_write_forbidden", "bot was blocked"}
)

// says reports whether the description holds any of the phrases.
func says(description string, phrases []string) bool {
	d := strings.ToLower(description)
	for _, s := range phrases {
		if strings.Contains(d, s) {
			return true
		}
	}
	return false
}

// classify is the outcome of a send or an edit: the outcome of the call refined by the description of a refusal. A
// RetryAfter holds the Destination; the adapter widens it to the Connection.
func classify(r Result) delivery.Outcome {
	o := r.Outcome
	if r.OK() {
		return o
	}
	switch {
	case o.Kind == delivery.OutcomeRetryAfter:
		o.Scope = delivery.ScopeDestination
	case says(r.Description, notModified):
		return delivery.Outcome{Kind: delivery.OutcomeOK}
	case says(r.Description, goneRoot):
		o.Kind = delivery.OutcomeGone
	case says(r.Description, lostThread):
		o.Kind = delivery.OutcomeThreadLost
	case says(r.Description, markupRejected):
		o.Kind = delivery.OutcomeMarkupRejected
	case r.Status == http.StatusUnauthorized || r.Code == http.StatusUnauthorized,
		says(r.Description, fatalRefusals):
		o.Kind = delivery.OutcomeFatal
	}
	return o
}

// limits remembers the last 429 of each Connection, on the real clock, since Telegram counts its limits in real time.
// It is per replica: a 429 another replica got does not widen this one's.
type limits struct {
	mu   sync.Mutex
	last map[int64]rateLimited
}

// rateLimited is a 429 of a Destination at a time.
type rateLimited struct {
	destination int64
	at          time.Time
}

// scope is the scope of a 429 of the Destination of the Connection: the whole Connection when another Destination of
// it got one within ConnectionWindow, the Destination otherwise; rc is the real clock, nil for the system's.
func (l *limits) scope(rc clock.Clock, connection, destination int64) delivery.Scope {
	if rc == nil {
		rc = clock.Real{}
	}
	now := rc.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[int64]rateLimited{}
	}
	prev, ok := l.last[connection]
	l.last[connection] = rateLimited{destination: destination, at: now}
	if ok && prev.destination != destination && now.Sub(prev.at) <= ConnectionWindow {
		return delivery.ScopeConnection
	}
	return delivery.ScopeDestination
}
