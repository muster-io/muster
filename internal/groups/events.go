// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import "fmt"

// Event is a lifecycle event (C-09.FR-22, C-10.FR-15, C-17.FR-11): the name a Timeline entry carries.
type Event string

// The lifecycle events of C-09.FR-22.
const (
	EventCreated             Event = "created"
	EventAlertsAdded         Event = "alerts_added"
	EventAlertReplaced       Event = "alert_replaced"
	EventAlertResolved       Event = "alert_resolved"
	EventAlertContinued      Event = "alert_continued"
	EventAnnotationsChanged  Event = "annotations_changed"
	EventSeverityRaised      Event = "severity_raised"
	EventUrgencyRaised       Event = "urgency_raised"
	EventReopened            Event = "reopened"
	EventSnoozeEnded         Event = "snooze_ended"
	EventResolved            Event = "resolved"
	EventMovedToDefaultRoute Event = "moved_to_default_route"
	EventUnacknowledged      Event = "unacknowledged"
)

// The lifecycle events of the Commands (C-10.FR-15); note_added is recorded by its Note in notes, not in
// timeline_entries.
const (
	EventAcknowledged Event = "acknowledged"
	EventTakeover     Event = "takeover"
	EventUnresolved   Event = "unresolved"
	EventSnoozed      Event = "snoozed"
	EventUnsnoozed    Event = "unsnoozed"
	EventNoteAdded    Event = "note_added"
)

// The lifecycle events of the timers (C-17.FR-11). notices_missed stands for overdue ack timeout notices or Reminders
// collapsed into one, and carries the Mentions of the notices it stands for.
const (
	EventAckTimeout         Event = "ack_timeout"
	EventUnclaimed          Event = "unclaimed"
	EventReminder           Event = "reminder"
	EventReminderAnswered   Event = "reminder_answered"
	EventAutoUnacknowledged Event = "auto_unacknowledged"
	EventNoticesMissed      Event = "notices_missed"
)

// The lifecycle events of the Teams of an Alert Group (C-25.FR-6, FR-7, revised C-09.FR-22): a Team that joins its
// Teams, and a value of the Team label that names no Team, once per value.
const (
	EventTeamJoined       Event = "team_joined"
	EventUnknownTeamValue Event = "unknown_team_value"
)

// Kind is the Timeline kind of an entry.
type Kind string

// The Timeline kinds stored in timeline_entries; Notes and delivery events are merged in from their own tables.
const (
	KindStatus   Kind = "status"
	KindAlerts   Kind = "alerts"
	KindTimers   Kind = "timers"
	KindSystem   Kind = "system"
	KindNotes    Kind = "notes"
	KindDelivery Kind = "delivery"
)

// Loudness says whether an event asks for attention (Loud) or only updates what is shown (Quiet).
type Loudness string

// The loudness of a lifecycle event.
const (
	Loud  Loudness = "loud"
	Quiet Loudness = "quiet"
)

// Mention is a symbolic Mention that each Destination turns into real Mentions from C-12 on.
type Mention string

// The symbolic Mentions: the Owner before the transition, the previous Owner of a Takeover, or the name of a
// Destination setting of C-12.FR-8.
const (
	MentionOwner         Mention = "owner"
	MentionPreviousOwner Mention = "previous_owner"
	MentionNewAlertGroup Mention = "new_alert_group"
	MentionNewAlerts     Mention = "new_alerts"
	MentionReopen        Mention = "reopen"
	MentionAckTimeout    Mention = "ack_timeout"
	MentionSnoozeEnded   Mention = "snooze_ended"
	MentionRiseToUrgent  Mention = "rise_to_urgent"
)

// Variant tells apart the rows of one event: the status an Alert Group had or returns to, or whether a rise to
// Urgent removed an acknowledgement or a Snooze.
type Variant string

// The variants of the rows of C-09.FR-22 and C-10.FR-15; VariantAny is an event with a single row. unacknowledged
// has two: by the Command Unacknowledge, and when its Owner is disabled or deleted.
const (
	VariantAny           Variant = ""
	VariantFiring        Variant = "firing"
	VariantAcknowledged  Variant = "acknowledged"
	VariantSnoozed       Variant = "snoozed"
	VariantRemoves       Variant = "removes"
	VariantRemovesNone   Variant = "removes_none"
	VariantCommand       Variant = "command"
	VariantOwnerReleased Variant = "owner_released"
)

// Row is one row of the lifecycle event table: its event and variant, its Timeline kind, loudness and Mentions.
type Row struct {
	Event    Event
	Variant  Variant
	Kind     Kind
	Loudness Loudness
	Mentions []Mention
}

// Table is the lifecycle event table of C-09.FR-22 with the rows of C-10.FR-15, C-17.FR-11 and C-25, a closed list: every
// transition and change of an Alert Group records exactly one entry of one of its rows. The CHECKs of timeline_entries
// back the kinds and Mentions. notices_missed has no Mentions of its own: its entry carries those of the notices it
// stands for (ack_timeout or owner).
var Table = []Row{
	{EventCreated, VariantAny, KindStatus, Loud, []Mention{MentionNewAlertGroup}},
	{EventAlertsAdded, VariantFiring, KindAlerts, Loud, []Mention{MentionNewAlerts}},
	{EventAlertsAdded, VariantAcknowledged, KindAlerts, Quiet, nil},
	{EventAlertsAdded, VariantSnoozed, KindAlerts, Quiet, nil},
	{EventAlertReplaced, VariantAny, KindAlerts, Quiet, nil},
	{EventAlertResolved, VariantAny, KindAlerts, Quiet, nil},
	{EventAlertContinued, VariantAny, KindAlerts, Quiet, nil},
	{EventAnnotationsChanged, VariantAny, KindAlerts, Quiet, nil},
	{EventSeverityRaised, VariantAny, KindAlerts, Quiet, nil},
	{EventUrgencyRaised, VariantRemoves, KindStatus, Loud, []Mention{MentionOwner, MentionRiseToUrgent}},
	{EventUrgencyRaised, VariantRemovesNone, KindStatus, Quiet, nil},
	{EventReopened, VariantFiring, KindStatus, Loud, []Mention{MentionReopen}},
	{EventReopened, VariantAcknowledged, KindStatus, Loud, []Mention{MentionOwner}},
	{EventReopened, VariantSnoozed, KindStatus, Quiet, nil},
	{EventSnoozeEnded, VariantAny, KindStatus, Loud, []Mention{MentionSnoozeEnded}},
	{EventResolved, VariantAny, KindStatus, Quiet, nil},
	{EventMovedToDefaultRoute, VariantAny, KindSystem, Quiet, nil},
	{EventUnacknowledged, VariantOwnerReleased, KindStatus, Loud, nil},
	{EventAcknowledged, VariantAny, KindStatus, Quiet, nil},
	{EventTakeover, VariantAny, KindStatus, Loud, []Mention{MentionPreviousOwner}},
	{EventUnacknowledged, VariantCommand, KindStatus, Quiet, nil},
	{EventUnresolved, VariantAny, KindStatus, Quiet, nil},
	{EventSnoozed, VariantAny, KindStatus, Quiet, nil},
	{EventUnsnoozed, VariantAny, KindStatus, Quiet, nil},
	{EventNoteAdded, VariantAny, KindNotes, Quiet, nil},
	{EventAckTimeout, VariantAny, KindTimers, Loud, []Mention{MentionAckTimeout}},
	{EventUnclaimed, VariantAny, KindTimers, Quiet, nil},
	{EventReminder, VariantAny, KindTimers, Loud, []Mention{MentionOwner}},
	{EventReminderAnswered, VariantAny, KindTimers, Quiet, nil},
	{EventAutoUnacknowledged, VariantAny, KindStatus, Loud, []Mention{MentionOwner}},
	{EventNoticesMissed, VariantAny, KindTimers, Loud, nil},
	{EventTeamJoined, VariantAny, KindAlerts, Quiet, nil},
	{EventUnknownTeamValue, VariantAny, KindSystem, Quiet, nil},
}

// rowOf is the row of the event and variant; an event outside the table is a programming error.
func rowOf(e Event, v Variant) Row {
	for _, r := range Table {
		if r.Event == e && r.Variant == v {
			return r
		}
	}
	panic(fmt.Sprintf("groups: no lifecycle event %s (%s)", e, v))
}

// statusVariant is the variant of the rows that depend on a status: alerts_added and reopened.
func statusVariant(s Status) Variant {
	switch s {
	case StatusAcknowledged:
		return VariantAcknowledged
	case StatusSnoozed:
		return VariantSnoozed
	default:
		return VariantFiring
	}
}

// SystemEvent is the system_event of a system entry.
type SystemEvent string

// The system entries this capability writes, fallback_template_used, which the dispatcher records when the
// re-render step reports a Route template that failed (C-12.FR-6), and unknown_team_value, the system entry of the
// lifecycle event of the same name (C-25.FR-7).
const (
	SystemMovedToDefaultRoute  SystemEvent = "moved_to_default_route"
	SystemMusterUnavailable    SystemEvent = "muster_unavailable"
	SystemFallbackTemplateUsed SystemEvent = "fallback_template_used"
	SystemUnknownTeamValue     SystemEvent = "unknown_team_value"
)

// ReasonMinimumSeverity is the reason of a severity_raised, or of the Quiet urgency_raised that removes nothing, that
// the Minimum severity of the Route made rather than an Alert (C-26.FR-9); ReasonCodeMinimumSeverity is its code.
const (
	ReasonMinimumSeverity     = "Minimum severity of the Route"
	ReasonCodeMinimumSeverity = "minimum_severity"
)
