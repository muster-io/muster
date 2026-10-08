// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery/dbgen"
)

// ErrNotBound is a button press whose post is not the Root message of the Alert Group in a Destination of the
// Connection.
var ErrNotBound = errors.New("the post is not the root message of the alert group")

// Binding is what a button press on a Root message is bound to (C-13.FR-4): the Destination the message was delivered
// to, its channel, and the language and Snooze durations of the Alert Group's Route.
type Binding struct {
	Destination   Destination
	ChannelID     string
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
