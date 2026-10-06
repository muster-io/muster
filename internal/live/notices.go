// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package live

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// Notices watches the Organization-wide notices of C-02.FR-24 for the streams of one replica. The notices change with
// time as well as with the writes of the Leader, so each replica evaluates them every CheckInterval on the business
// clock and sends a system-notices hint when the set a stream may see changed: the notices for everyone to every
// stream, the notices for Admins only to the streams of identities that hold system-status:read.
type Notices struct {
	read     func(ctx context.Context, now time.Time) ([]organization.Notice, error)
	business clock.Clock
	hub      *Hub
	log      *logging.Logger

	mu sync.Mutex
	// seen is false until a check succeeded; all and admins are the sets each audience saw at the last check.
	seen        bool
	all, admins string
	failing     bool
}

// NewNotices returns the watcher of the notices that read computes at a business time, sending hints through hub.
func NewNotices(read func(ctx context.Context, now time.Time) ([]organization.Notice, error), business clock.Clock,
	hub *Hub, log *logging.Logger) *Notices {
	return &Notices{read: read, business: business, hub: hub, log: log}
}

// Visible returns the active notices the caller may see: the notices for everyone, and those for Admins when admin
// (the caller holds system-status:read).
func (n *Notices) Visible(ctx context.Context, admin bool) ([]organization.Notice, error) {
	all, err := n.read(ctx, n.business.Now())
	if err != nil {
		return nil, err
	}
	return visible(all, admin), nil
}

func visible(all []organization.Notice, admin bool) []organization.Notice {
	out := []organization.Notice{}
	for _, x := range all {
		if x.Audience == organization.AudienceAll || admin {
			out = append(out, x)
		}
	}
	return out
}

// fingerprint names a set of notices with what a client shows of them.
func fingerprint(notices []organization.Notice) string {
	var b strings.Builder
	for _, x := range notices {
		b.WriteString(string(x.Kind) + "|" + x.Since.UTC().Format(time.RFC3339Nano) + "|" +
			x.Until.UTC().Format(time.RFC3339Nano) + ";")
	}
	return b.String()
}

// Check evaluates the notices once and sends the hints of a change. The first successful check only records the
// sets: a client reads the notices when it starts. A failed check is logged once, until a check succeeds again.
func (n *Notices) Check(ctx context.Context) {
	notices, err := n.read(ctx, n.business.Now())
	n.mu.Lock()
	defer n.mu.Unlock()
	if err != nil {
		if !n.failing {
			n.log.Log(ctx, logging.SystemNoticesCheckFailed, logging.F("error", err.Error()))
			n.failing = true
		}
		return
	}
	n.failing = false
	all, admins := fingerprint(visible(notices, false)), fingerprint(visible(notices, true))
	if n.seen {
		switch {
		case all != n.all:
			n.hub.Send(Hint{Type: HintSystemNotices}, nil)
		case admins != n.admins:
			n.hub.Send(Hint{Type: HintSystemNotices}, func(s Subscriber) bool { return s.Admin })
		}
	}
	n.seen, n.all, n.admins = true, all, admins
}

// Run checks the notices at every tick until ctx ends.
func (n *Notices) Run(ctx context.Context, ticks <-chan time.Time) {
	n.Check(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			n.Check(ctx)
		}
	}
}
