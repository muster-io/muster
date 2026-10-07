// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

// Allowed is allowed_commands of the Alert Group for the caller (C-10.FR-16): the Commands its status allows for the
// caller's Permissions and identity, in the order of CommandName. Acknowledge of an Alert Group another user owns is a
// Takeover, and is not offered to the Owner, for whom it would change nothing; a Service account is never offered
// Acknowledge. Unresolve is offered only while every precondition holds: a person resolved the Alert Group, its Route
// was not deleted, no newer open Alert Group of its Route and key takes part in grouping, and one of its Alerts still
// fires — the API still refuses it when that changes before the command arrives. add_note is offered in every status
// to a caller with alert-groups:note; still_on_it comes with its story (S-049).
func (v View) Allowed(c Caller) []Command {
	ack, resolve, snooze := c.can(PermissionAcknowledge), c.can(PermissionResolve), c.can(PermissionSnooze)
	user := c.user()
	out := []Command{}
	add := func(ok bool, cmd Command) {
		if ok {
			out = append(out, cmd)
		}
	}
	switch v.Status {
	case StatusFiring:
		add(ack && user != 0, CommandAcknowledge)
		add(resolve, CommandResolve)
		add(snooze, CommandSnooze)
	case StatusAcknowledged:
		add(ack && user != 0 && v.ownerID != user, CommandAcknowledge)
		add(ack, CommandUnacknowledge)
		add(resolve, CommandResolve)
		add(snooze, CommandSnooze)
	case StatusSnoozed:
		add(ack && user != 0, CommandAcknowledge)
		add(resolve, CommandResolve)
		add(snooze, CommandSnooze)
		add(snooze, CommandUnsnooze)
	case StatusResolved:
		add(resolve && v.unresolvable(), CommandUnresolve)
	}
	add(c.can(PermissionNote), CommandAddNote)
	return out
}

// unresolvable reports whether every precondition of Unresolve holds as the Alert Group was read.
func (v View) unresolvable() bool {
	return v.Resolution != nil && v.Resolution.By == ResolvedByUser && !v.routeDeleted && v.Newer == nil &&
		v.stillFiring > 0
}
