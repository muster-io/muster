// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	// RetryMillis is the reconnect delay the stream gives the browser in its first line.
	RetryMillis = 3000
	// KeepaliveInterval is how often a stream without hints sends a comment, so that proxies keep it open.
	KeepaliveInterval = 25 * time.Second
)

// event is the data of a hint event (HintEvent in the API specification); id is null for a whole collection.
type event struct {
	Type string  `json:"type"`
	ID   *string `json:"id"`
}

// Stream writes the server-sent events of sub to w, calling flush after each, until ctx ends, the subscription is
// closed — its session ended, the Hub closed or the stream fell behind — or a write fails. It starts with
// "retry: 3000", sends ": keepalive" every KeepaliveInterval and each hint as an event named hint with an id line, the
// count of the stream's events, and the data {"type": …, "id": …}. The caller unsubscribes afterwards.
func (h *Hub) Stream(ctx context.Context, w io.Writer, flush func() error, sub *Subscription) error {
	keepalive, stop := h.every(KeepaliveInterval)
	defer stop()
	write := func(format string, args ...any) error {
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return err
		}
		return flush()
	}
	if err := write("retry: %d\n\n", RetryMillis); err != nil {
		return err
	}
	var seq int64
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.done:
			return nil
		case <-keepalive:
			if err := write(": keepalive\n\n"); err != nil {
				return err
			}
		case hint := <-sub.hints:
			e := event{Type: hint.Type}
			if hint.ID != "" {
				e.ID = &hint.ID
			}
			data, err := json.Marshal(e)
			if err != nil {
				return err
			}
			seq++
			if err := write("event: hint\nid: %d\ndata: %s\n\n", seq, data); err != nil {
				return err
			}
		}
	}
}
