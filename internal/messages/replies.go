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

	"github.com/muster-io/muster/internal/buttons"
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
// fingerprints of the new Alerts it lists and how many it lists at most. An ack timeout notice renders the Route's
// template for Markup, plain text when empty; a failing template is reported to Fallback with its detail, and what to
// run once the transaction of the render committed — the counter and the log line of a failure — is handed to After,
// or run at once when After is nil.
type ReplyInput struct {
	Event        string
	Seq          int64
	Fingerprints []string
	Listed       int
	Markup       Markup
	After        func(f func(context.Context))
	Fallback     func(detail string)
}

// Reply renders the Thread reply in of the Alert Group groupID, reading through db.
func (r *Renderer) Reply(ctx context.Context, db DBTX, groupID int64, in ReplyInput) (Message, error) {
	if in.Event == EventAckTimeout {
		return r.ackTimeoutNotice(ctx, db, groupID, in)
	}
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
	case EventReminder:
		e, err := entry()
		if err != nil {
			return Message{}, err
		}
		m.Lines = []string{T(lang, "reply.reminder", Args{"user": Value(cmp.Or(e.OwnerName, src.Owner))})}
		m.Buttons = r.reminderButtons(src.PublicID, lang)
	default:
		m.Lines = []string{replyText(src, lang, in.Event)}
	}
	return m, nil
}

// reminderButtons are the buttons of a Reminder (C-17.FR-4): "Still on it" and "Unack", whose action ids of the
// subject reply name the Alert Group publicID, signed with the active key when the reply is rendered. A Reminder whose
// buttons cannot both be signed — no Keyring, or no active key — goes without buttons.
func (r *Renderer) reminderButtons(publicID, lang string) []Button {
	if r.keys == nil {
		return []Button{}
	}
	out := make([]Button, 0, 2)
	for _, cmd := range []string{buttons.CommandStillOnIt, buttons.CommandUnacknowledge} {
		id, kid, err := buttons.Sign(r.keys, buttons.Action{Subject: buttons.SubjectReply, PublicID: publicID,
			Command: cmd})
		if err != nil {
			return []Button{}
		}
		out = append(out, Button{Command: cmd, Label: buttonLabel(lang, buttons.Button{Command: cmd}, nil),
			ActionID: id, KeyID: kid})
	}
	return out
}

// Done is the answer to a press whose Command ran: "Done: {command}", the Command named in lang.
func Done(lang, command string) string {
	key := map[string]string{buttons.CommandStillOnIt: "stillOnIt"}[command]
	return T(lang, "press.done", Args{"command": T(lang, "command."+cmp.Or(key, command), nil)})
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
	case EventUnclaimed:
		return T(lang, "reply.unclaimed", nil)
	case EventAutoUnacknowledged:
		return T(lang, "reply.autoUnacknowledged", nil)
	}
	return "#" + strconv.FormatInt(src.Number, 10) + " " + event
}

// ackTimeoutNotice is an ack timeout notice (C-17.FR-2, C-12.FR-4): the Route's ack_timeout_notice template, or the
// built-in one, rendered in the sandbox on the Alert Group with its Alerts, and only links — "Open in Muster" and the
// Alert Group's links — without buttons. A Route template that fails gives way to the built-in one and is settled as
// for the Root message (C-12.FR-6): the Route's template error and MusterTemplateError in the transaction db, and
// muster_template_errors_total and the log line once it committed.
func (r *Renderer) ackTimeoutNotice(ctx context.Context, db DBTX, groupID int64, in ReplyInput) (Message, error) {
	src, err := r.Load(ctx, db, groupID)
	if err != nil {
		return Message{}, err
	}
	lang, markup := language(src.Route.Language), cmp.Or(in.Markup, MarkupPlain)
	builtin := BuiltinSource(TemplateAckTimeoutNotice, lang)
	source := cmp.Or(deref(src.Route.AckTimeoutNotice), builtin)
	var failures []Failure
	var rendered []string
	text, err := r.notice(source, src, markup)
	switch {
	case err != nil && source != builtin:
		failures = []Failure{{Template: TemplateAckTimeoutNotice, Error: sandboxError(err)}}
		if text, err = r.notice(builtin, src, markup); err != nil {
			return Message{}, fmt.Errorf("render the built-in ack timeout notice: %w", err)
		}
	case err != nil:
		return Message{}, fmt.Errorf("render the built-in ack timeout notice: %w", err)
	case source != builtin:
		rendered = []string{TemplateAckTimeoutNotice}
	}
	after, err := r.Settle(ctx, db, src, failures, rendered)
	if err != nil {
		return Message{}, err
	}
	for _, f := range failures {
		if in.Fallback != nil {
			in.Fallback(f.Detail())
		}
	}
	if in.After != nil {
		in.After(after)
	} else {
		after(ctx)
	}
	return Message{Kind: KindReply, Language: lang, TimeZone: src.TimeZone, Colour: src.Status, Buttons: []Button{},
		Body: &Body{Text: Neutralize(text), Markup: markup},
		Links: append([]Link{{Text: T(lang, "link.open", nil), URL: r.GroupURL(src.PublicID)}},
			linksOf(src.Links, lang)...)}, nil
}

// notice runs the ack_timeout_notice template source on src for markup and records how long it took.
func (r *Renderer) notice(source string, src *Source, markup Markup) (string, error) {
	start := r.real.Now()
	defer func() { observe(TemplateAckTimeoutNotice, r.real.Now().Sub(start)) }()
	return r.execute(TemplateAckTimeoutNotice, source, SafeData(r.data(src), markup))
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
			parts = append(parts, Value(name)+": "+Value(labels[name]))
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
