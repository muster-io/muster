// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery/dbgen"
)

// ErrNotBound is a button press whose post is not the Root message, or a Reminder, of the Alert Group in a Destination
// of the Connection.
var ErrNotBound = errors.New("the post is not a message of the alert group")

// Binding is what a button press on a Root message or a Reminder is bound to (C-13.FR-4): the Destination the message
// was delivered to, its channel, and the language and Snooze durations of the Alert Group's Route. RootMessageID is,
// for a press on a Reminder in Mattermost, the Root message it replies to, whose Thread the answers go to; empty for a
// press on a Root message and in Telegram.
type Binding struct {
	Destination   Destination
	ChannelID     string
	RootMessageID string
	Language      string
	SnoozeSeconds []int64
}

// PressBinding reads the binding of a press on the post postID of the Connection connectionID that names the Alert
// Group groupPublicID: the delivery of that Alert Group to a Mattermost Destination of the Connection, not deleted,
// whose Root message is postID; ErrNotBound when there is none.
func (s *Service) PressBinding(ctx context.Context, connectionID int64, groupPublicID, postID string) (Binding,
	error) {
	conn := pgtype.Int8{Int64: connectionID, Valid: true}
	r, err := s.store.q().GetPressBinding(ctx, dbgen.GetPressBindingParams{OrgID: s.orgID, ConnectionID: conn,
		GroupPublicID: groupPublicID, MessageID: postID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotBound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("read the binding of a press on the alert group %s: %w", groupPublicID, err)
	}
	return Binding{Destination: Destination{ID: r.DestinationID, PublicID: r.DestinationPublicID,
		Name: r.DestinationName, Type: TypeMattermost, Connection: &connectionID}, ChannelID: r.ChannelID,
		Language: r.Language, SnoozeSeconds: r.SnoozeDurationsSeconds}, nil
}

// ReplyBinding reads the binding of a press on the post postID of the Connection connectionID that names the Alert
// Group groupPublicID in a Reminder's action id (C-13.FR-4): a sent Reminder of that Alert Group whose message id is
// postID, delivered to a Mattermost Destination of the Connection, not deleted; ErrNotBound when there is none.
func (s *Service) ReplyBinding(ctx context.Context, connectionID int64, groupPublicID, postID string) (Binding,
	error) {
	r, err := s.store.q().GetReplyPressBinding(ctx, dbgen.GetReplyPressBindingParams{OrgID: s.orgID,
		ConnectionID: pgtype.Int8{Int64: connectionID, Valid: true}, GroupPublicID: groupPublicID, MessageID: postID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotBound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("read the binding of a press on a reminder of the alert group %s: %w",
			groupPublicID, err)
	}
	return Binding{Destination: Destination{ID: r.DestinationID, PublicID: r.DestinationPublicID,
		Name: r.DestinationName, Type: TypeMattermost, Connection: &connectionID}, ChannelID: r.ChannelID,
		RootMessageID: r.RootMessageID, Language: r.Language, SnoozeSeconds: r.SnoozeDurationsSeconds}, nil
}

// TelegramReplyBinding reads the binding of a press on the message messageID of the chat chatID of the Telegram
// Connection connectionID that names the Alert Group groupPublicID in a Reminder's data (C-14.FR-4): a sent Reminder of
// that Alert Group whose message is messageID in the discussion group chatID of a Telegram Destination of the
// Connection, not deleted; ErrNotBound when there is none. Its ChannelID is the chat id in decimal.
func (s *Service) TelegramReplyBinding(ctx context.Context, connectionID int64, groupPublicID string, chatID,
	messageID int64) (Binding, error) {
	r, err := s.store.q().GetTelegramReplyPressBinding(ctx, dbgen.GetTelegramReplyPressBindingParams{OrgID: s.orgID,
		ConnectionID: pgtype.Int8{Int64: connectionID, Valid: true}, ChatID: chatID, GroupPublicID: groupPublicID,
		MessageID: strconv.FormatInt(messageID, 10)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotBound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("read the binding of a press on a reminder of the alert group %s: %w",
			groupPublicID, err)
	}
	return Binding{Destination: Destination{ID: r.DestinationID, PublicID: r.DestinationPublicID,
		Name: r.DestinationName, Type: TypeTelegram, Connection: &connectionID},
		ChannelID: strconv.FormatInt(chatID, 10), Language: r.Language, SnoozeSeconds: r.SnoozeDurationsSeconds}, nil
}

// PostDestination reads the Mattermost Destination of the Connection connectionID, not deleted, whose channel is
// channelID and to which a delivery posted postID, and whether there is one: a press that cannot be verified is
// answered only on such a post.
func (s *Service) PostDestination(ctx context.Context, connectionID int64, postID, channelID string) (Destination,
	bool, error) {
	r, err := s.store.q().GetPostDestination(ctx, dbgen.GetPostDestinationParams{OrgID: s.orgID,
		ConnectionID: pgtype.Int8{Int64: connectionID, Valid: true}, ChannelID: channelID, MessageID: postID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Destination{}, false, nil
	}
	if err != nil {
		return Destination{}, false, fmt.Errorf("read the destination of a post of the connection %d: %w",
			connectionID, err)
	}
	return Destination{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Type: TypeMattermost,
		Connection: &connectionID}, true, nil
}

// TelegramPressBinding reads the binding of a press on the message messageID of the chat chatID of the Telegram
// Connection connectionID that names the Alert Group groupPublicID (C-14.FR-4): the delivery of that Alert Group to a
// Telegram Destination of the Connection, not deleted, whose channel is chatID and whose Root message is the channel
// post messageID; ErrNotBound when there is none. Its ChannelID is the chat id in decimal.
func (s *Service) TelegramPressBinding(ctx context.Context, connectionID int64, groupPublicID string, chatID,
	messageID int64) (Binding, error) {
	r, err := s.store.q().GetTelegramPressBinding(ctx, dbgen.GetTelegramPressBindingParams{OrgID: s.orgID,
		ConnectionID: pgtype.Int8{Int64: connectionID, Valid: true}, ChatID: chatID, GroupPublicID: groupPublicID,
		MessageID: strconv.FormatInt(messageID, 10)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotBound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("read the binding of a press on the alert group %s: %w", groupPublicID, err)
	}
	return Binding{Destination: Destination{ID: r.DestinationID, PublicID: r.DestinationPublicID,
		Name: r.DestinationName, Type: TypeTelegram, Connection: &connectionID},
		ChannelID: strconv.FormatInt(chatID, 10), Language: r.Language, SnoozeSeconds: r.SnoozeDurationsSeconds}, nil
}

// TestDestination reads the Mattermost Destination publicID of the Connection connectionID, not deleted, whose channel
// is channelID, and whether there is one: the Destination whose test message a person pressed, which limits the
// private answer (C-16.FR-3).
func (s *Service) TestDestination(ctx context.Context, connectionID int64, publicID, channelID string) (Destination,
	bool, error) {
	r, err := s.store.q().GetTestDestination(ctx, dbgen.GetTestDestinationParams{OrgID: s.orgID,
		ConnectionID: pgtype.Int8{Int64: connectionID, Valid: true}, PublicID: publicID, ChannelID: channelID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Destination{}, false, nil
	}
	if err != nil {
		return Destination{}, false, fmt.Errorf("read the destination %s of the connection %d: %w", publicID,
			connectionID, err)
	}
	return Destination{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Type: TypeMattermost,
		Connection: &connectionID}, true, nil
}

// bridgePressQueries are the reads of what a press on a message of a Bridge Connection is bound to.
type bridgePressQueries interface {
	GetBridgeRootBinding(ctx context.Context, arg dbgen.GetBridgeRootBindingParams) (dbgen.GetBridgeRootBindingRow,
		error)
	GetBridgeSupersededBinding(ctx context.Context, arg dbgen.GetBridgeSupersededBindingParams) (
		dbgen.GetBridgeSupersededBindingRow, error)
	GetBridgeReminderBinding(ctx context.Context, arg dbgen.GetBridgeReminderBindingParams) (
		dbgen.GetBridgeReminderBindingRow, error)
}

// BridgeBinding is what a press on a message of a Bridge Connection is bound to (C-22.FR-12): the kind of the message
// — a Root message, buttons.SubjectRoot, or a sent Reminder, buttons.SubjectReply — the Bridge Destination it was
// delivered to, the Alert Group, by public_id and #N, and its Route's Snooze durations. Superseded is an older Root
// message that a republication past the bridge's edit window replaced (C-22.FR-9): a press there acts on the Alert
// Group as on the current one, and nothing ever edits it again, since the worker reconciles only the current Root
// message of a delivery.
type BridgeBinding struct {
	Kind          buttons.Subject
	Destination   Destination
	GroupPublicID string
	GroupNumber   int64
	SnoozeSeconds []int64
	Superseded    bool
}

// BridgePressBinding reads the binding of a press on the message messageID of the Bridge Connection connectionID
// (C-22.FR-12): the delivery of an Alert Group to a Bridge Destination of the Connection, not deleted, whose current
// Root message it is, else whose older Root message it is, else a sent Reminder of such a Destination whose message it
// is; ErrNotBound when there is none, as for a message Muster did not post through the Connection, a test message or a
// Storm summary.
func (s *Service) BridgePressBinding(ctx context.Context, connectionID int64, messageID string) (BridgeBinding,
	error) {
	conn := pgtype.Int8{Int64: connectionID, Valid: true}
	q := s.store.q()
	bound := func(kind buttons.Subject, superseded bool, id int64, publicID, name, group string, number int64,
		snooze []int64) BridgeBinding {
		return BridgeBinding{Kind: kind, Destination: Destination{ID: id, PublicID: publicID, Name: name,
			Type: TypeBridge, Connection: &connectionID}, GroupPublicID: group, GroupNumber: number,
			SnoozeSeconds: snooze, Superseded: superseded}
	}
	failed := func(what string, err error) (BridgeBinding, error) {
		return BridgeBinding{}, fmt.Errorf("read the binding of a press on %s of the connection %d: %w", what,
			connectionID, err)
	}
	r, err := q.GetBridgeRootBinding(ctx, dbgen.GetBridgeRootBindingParams{OrgID: s.orgID, ConnectionID: conn,
		MessageID: messageID})
	switch {
	case err == nil:
		return bound(buttons.SubjectRoot, false, r.DestinationID, r.DestinationPublicID, r.DestinationName,
			r.GroupPublicID, r.GroupNumber, r.SnoozeDurationsSeconds), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return failed("a root message", err)
	}
	o, err := q.GetBridgeSupersededBinding(ctx, dbgen.GetBridgeSupersededBindingParams{OrgID: s.orgID,
		ConnectionID: conn, MessageID: messageID})
	switch {
	case err == nil:
		return bound(buttons.SubjectRoot, true, o.DestinationID, o.DestinationPublicID, o.DestinationName,
			o.GroupPublicID, o.GroupNumber, o.SnoozeDurationsSeconds), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return failed("an older root message", err)
	}
	rr, err := q.GetBridgeReminderBinding(ctx, dbgen.GetBridgeReminderBindingParams{OrgID: s.orgID,
		ConnectionID: conn, MessageID: messageID})
	switch {
	case err == nil:
		return bound(buttons.SubjectReply, false, rr.DestinationID, rr.DestinationPublicID, rr.DestinationName,
			rr.GroupPublicID, rr.GroupNumber, rr.SnoozeDurationsSeconds), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return failed("a reminder", err)
	}
	return BridgeBinding{}, ErrNotBound
}
