// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/messages/dbgen"
)

// Thread replies and the Storm summary (C-12.FR-3, FR-10), in the language of the Route.

// The lifecycle events a Thread reply is about (C-09.FR-22, C-10.FR-15, C-17.FR-11).
const (
	EventAlertsAdded        = "alerts_added"
	EventAlertReplaced      = "alert_replaced"
	EventReopened           = "reopened"
	EventTakeover           = "takeover"
	EventSnoozeEnded        = "snooze_ended"
	EventUrgencyRaised      = "urgency_raised"
	EventResolved           = "resolved"
	EventUnacknowledged     = "unacknowledged"
	EventAckTimeout         = "ack_timeout"
	EventUnclaimed          = "unclaimed"
	EventReminder           = "reminder"
	EventAutoUnacknowledged = "auto_unacknowledged"
	EventNoticesMissed      = "notices_missed"
)

// ReplyInput is a Thread reply to render: its lifecycle event, the number of the first event it stands for, the
// fingerprints of the new Alerts it lists and how many it lists at most.
type ReplyInput struct {
	Event        string
	Seq          int64
	Fingerprints []string
	Listed       int
}

// Reply renders the Thread reply in of the Alert Group groupID, reading through db.
func (r *Renderer) Reply(ctx context.Context, db DBTX, groupID int64, in ReplyInput) (Message, error) {
	src, err := r.loadGroup(ctx, db, groupID)
	if err != nil {
		return Message{}, err
	}
	lang := language(src.Route.Language)
	m := Message{Kind: KindReply, Language: lang, TimeZone: src.TimeZone, Colour: src.Status, Buttons: []Button{}}
	q := r.q(db)
	entry := func() (dbgen.GetReplyEntryRow, error) {
		e, err := q.GetReplyEntry(ctx, dbgen.GetReplyEntryParams{OrgID: r.orgID, AlertGroupID: groupID,
			EventSeq: in.Seq})
		if errors.Is(err, pgx.ErrNoRows) {
			return e, nil
		}
		if err != nil {
			return e, fmt.Errorf("read the event of the thread reply of alert group #%d: %w", src.Number, err)
		}
		return e, nil
	}
	switch in.Event {
	case EventAlertsAdded:
		lines, err := r.newAlerts(ctx, q, src, in)
		if err != nil {
			return Message{}, err
		}
		m.Lines = lines
	case EventAlertReplaced:
		e, err := entry()
		if err != nil {
			return Message{}, err
		}
		if e.ReplacedLabel == "" {
			m.Lines = []string{T(lang, "reply.replacedUnknown", nil)}
		} else {
			m.Lines = []string{T(lang, "reply.replaced", Args{"label": e.ReplacedLabel})}
		}
	case EventUnacknowledged:
		e, err := entry()
		if err != nil {
			return Message{}, err
		}
		key := "reply.ownerDisabled"
		if e.Reason == "owner_deleted" {
			key = "reply.ownerDeleted"
		}
		m.Lines = []string{T(lang, key, nil)}
	case EventNoticesMissed:
		e, err := entry()
		if err != nil {
			return Message{}, err
		}
		m.Lines = []string{N(lang, "reply.noticesMissed", max(e.MissedCount, 1), nil)}
	default:
		m.Lines = []string{replyText(src, lang, in.Event)}
	}
	return m, nil
}

// replyText is the text of a Thread reply that needs nothing but the Alert Group.
func replyText(src *Source, lang, event string) string {
	switch event {
	case EventReopened:
		return T(lang, "reply.reopened", nil)
	case EventTakeover:
		return T(lang, "reply.takeover", Args{"user": Value(src.Owner)})
	case EventSnoozeEnded:
		return T(lang, "reply.snoozeEnded", nil)
	case EventUrgencyRaised:
		return T(lang, "reply.urgent", nil)
	case EventResolved:
		return T(lang, "reply.resolvedAutomatically", Args{"reason": reason(lang, src.ResolveReason)})
	case EventAckTimeout:
		return T(lang, "reply.ackTimeout", nil)
	case EventUnclaimed:
		return T(lang, "reply.unclaimed", nil)
	case EventReminder:
		return T(lang, "reply.reminder", Args{"user": Value(src.Owner)})
	case EventAutoUnacknowledged:
		return T(lang, "reply.autoUnacknowledged", nil)
	}
	return "#" + strconv.FormatInt(src.Number, 10) + " " + event
}

// newAlerts are the lines of a new-Alert Thread reply: "New alerts (N):", one line per new Alert with its labels that
// the Alert Group's common labels do not already say, at most in.Listed of them, then "…and K more — open in Muster".
func (r *Renderer) newAlerts(ctx context.Context, q queries, src *Source, in ReplyInput) ([]string, error) {
	lang := language(src.Route.Language)
	lines := []string{T(lang, "reply.alertsAdded", Args{"count": strconv.Itoa(len(in.Fingerprints))})}
	listed := in.Fingerprints
	if in.Listed >= 0 && len(listed) > in.Listed {
		listed = listed[:in.Listed]
	}
	rows, err := q.ListAlertsByFingerprint(ctx, dbgen.ListAlertsByFingerprintParams{OrgID: r.orgID,
		AlertGroupID: src.ID, Fingerprints: listed})
	if err != nil {
		return nil, fmt.Errorf("read the new alerts of alert group #%d: %w", src.Number, err)
	}
	byFingerprint := make(map[string]map[string]string, len(rows))
	for _, a := range rows {
		byFingerprint[a.Fingerprint] = jsonMap(a.Labels)
	}
	for _, fp := range listed {
		labels := byFingerprint[fp]
		var parts []string
		for _, name := range slices.Sorted(maps.Keys(labels)) {
			if slices.Contains(excludedLabels, name) || src.CommonLabels[name] == labels[name] {
				continue
			}
			parts = append(parts, name+": "+Value(labels[name]))
		}
		if len(parts) == 0 {
			parts = []string{Value(cmp.Or(labels["alertname"], fp))}
		}
		lines = append(lines, "• "+strings.Join(parts, ", "))
	}
	if more := len(in.Fingerprints) - len(listed); more > 0 {
		lines = append(lines, T(lang, "reply.more", Args{"count": strconv.Itoa(more)}))
	}
	return lines, nil
}

// Storm is the Storm summary of an active Storm of a Route (C-12.FR-10): "⛈ Storm on {Route}: K Alert Groups, C
// urgent — open in Muster", with the link to the Route's Alert Groups.
func (r *Renderer) Storm(routePublicID, routeName, lang string, count, urgent int64) Message {
	lang = language(lang)
	return Message{Kind: KindStorm, Language: lang, Colour: ColourStorm, Buttons: []Button{},
		Lines: []string{T(lang, "storm.active", Args{"route": Value(routeName),
			"groups": N(lang, "storm.groups", count, nil), "urgent": N(lang, "storm.urgent", urgent, nil)})},
		Links: []Link{{Text: T(lang, "link.open", nil), URL: r.routeURL(routePublicID)}}}
}

// StormOver is the final state of the Storm summary: "Storm over: M Alert Groups still open".
func (r *Renderer) StormOver(routePublicID, lang string, open int64) Message {
	lang = language(lang)
	return Message{Kind: KindStorm, Language: lang, Colour: ColourStorm, Buttons: []Button{},
		Lines: []string{N(lang, "storm.over", open, nil)},
		Links: []Link{{Text: T(lang, "link.open", nil), URL: r.routeURL(routePublicID)}}}
}
