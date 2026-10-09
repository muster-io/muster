// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery/dbgen"
)

// Telegram comment Threads (C-14.FR-3, C-11.FR-16): the Thread of a Root message is the comment Thread of the
// channel post's automatic copy in the discussion group. The copy and the answer to sendMessage meet in the copy buffer
// telegram_post_copies in either order and on any replica: the update path learns the copy (LearnCopy), the worker
// records the Publication, each under the lock of the post, and whichever comes second attaches the Thread. A
// Publication without a known copy leaves its Thread waiting_for_copy; a Thread reply that comes due then looks the
// copy up again and waits at most telegram.copy_wait from the Publication, after which it goes to the discussion group
// as a chain of replies not attached to the post (threads.go). A copy learned later, from a person's comment, attaches
// the following replies.

// The built-in settings of comment Threads (defaults.md): telegram.copy_wait (P-30), and how long a buffered copy is
// kept before short-lived pruning deletes it.
const (
	CopyWait          = 60 * time.Second
	PostCopyRetention = 24 * time.Hour
)

// postCopyLockClass is the first key of the transaction advisory lock of one channel post's copy (LockPostCopy).
const postCopyLockClass int32 = 0x6d75_0003

// The states of a delivery's Thread (deliveries.thread_state).
const (
	threadNone       = "none"
	threadWaiting    = "waiting_for_copy"
	threadAttached   = "attached"
	threadUnattached = "unattached"
)

// The ways a copy is learned (telegram_post_copies.learned_from).
const (
	// LearnedFromAutomaticForward is the automatic copy itself (F-005).
	LearnedFromAutomaticForward = "automatic_forward"
	// LearnedFromComment is a person's comment, a reply to the copy (F-007).
	LearnedFromComment = "comment"
)

// Copy is the automatic copy of a channel post, as the update path learns it: the Connection that received it, the
// channel and the post, the discussion group and the copy's message id there, and how it was learned.
type Copy struct {
	ConnectionID     int64
	ChannelChatID    int64
	PostID           int64
	DiscussionChatID int64
	CopyID           int64
	LearnedFrom      string
}

// ErrBadCopy is a copy that names no channel post, no discussion group or no message: it can never be learned.
var ErrBadCopy = errors.New("the copy names no channel post or no message")

// copyQueries are the queries of the copy buffer and of the Threads of Telegram deliveries.
type copyQueries interface {
	LockPostCopy(ctx context.Context, arg dbgen.LockPostCopyParams) error
	InsertPostCopy(ctx context.Context, arg dbgen.InsertPostCopyParams) error
	GetPostCopy(ctx context.Context, arg dbgen.GetPostCopyParams) (dbgen.GetPostCopyRow, error)
	AttachCopy(ctx context.Context, arg dbgen.AttachCopyParams) ([]string, error)
	WakeCopyReplies(ctx context.Context, arg dbgen.WakeCopyRepliesParams) (int64, error)
	LockDeliveryThread(ctx context.Context, arg dbgen.LockDeliveryThreadParams) (dbgen.LockDeliveryThreadRow, error)
	SetThread(ctx context.Context, arg dbgen.SetThreadParams) error
	PrunePostCopies(ctx context.Context, arg dbgen.PrunePostCopiesParams) (int64, error)
}

// LearnCopy buffers the copy of a channel post, the first one learned winning, and attaches to it, in the same
// transaction, the Threads of the Alert Groups whose Root message is that post in a Telegram Destination of the
// Connection with that channel and discussion group, unless that Thread lost this very copy. Then the Thread replies
// that wait for the copy come due at once. Learning the same copy again changes nothing.
func (s *Service) LearnCopy(ctx context.Context, c Copy) error {
	if c.ConnectionID <= 0 || c.ChannelChatID == 0 || c.PostID <= 0 || c.DiscussionChatID == 0 || c.CopyID <= 0 ||
		(c.LearnedFrom != LearnedFromAutomaticForward && c.LearnedFrom != LearnedFromComment) {
		return ErrBadCopy
	}
	now := s.clock.Now().UTC()
	p := post{connection: c.ConnectionID, channel: c.ChannelChatID, id: c.PostID}
	err := s.store.inTx(ctx, func(q queries) error {
		if err := lockPost(ctx, q, p); err != nil {
			return err
		}
		if err := q.InsertPostCopy(ctx, dbgen.InsertPostCopyParams{ConnectionID: c.ConnectionID,
			ChannelChatID: c.ChannelChatID, ChannelMessageID: c.PostID, OrgID: s.orgID,
			DiscussionChatID: c.DiscussionChatID, CopyMessageID: c.CopyID, LearnedFrom: c.LearnedFrom,
			Now: now}); err != nil {
			return fmt.Errorf("buffer the copy of post %d: %w", c.PostID, err)
		}
		stored, err := q.GetPostCopy(ctx, dbgen.GetPostCopyParams{OrgID: s.orgID, ConnectionID: c.ConnectionID,
			ChannelChatID: c.ChannelChatID, ChannelMessageID: c.PostID})
		if err != nil {
			return fmt.Errorf("read the copy of post %d: %w", c.PostID, err)
		}
		groups, err := q.AttachCopy(ctx, dbgen.AttachCopyParams{CopyMessageID: idText(stored.CopyMessageID),
			Now: now, OrgID: s.orgID, ConnectionID: c.ConnectionID, ChannelChatID: c.ChannelChatID,
			DiscussionChatID: stored.DiscussionChatID, MessageID: idText(c.PostID)})
		if err != nil {
			return fmt.Errorf("attach the threads of post %d: %w", c.PostID, err)
		}
		for _, g := range groups {
			if err := hintGroup(ctx, q, s.orgID, g); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	q := s.store.q()
	n, err := q.WakeCopyReplies(ctx, dbgen.WakeCopyRepliesParams{Now: now, OrgID: s.orgID,
		ConnectionID: c.ConnectionID, ChannelChatID: c.ChannelChatID, MessageID: idText(c.PostID)})
	if err != nil {
		return fmt.Errorf("wake the thread replies of post %d: %w", c.PostID, err)
	}
	if n > 0 {
		return q.NotifyDelivery(ctx, Channel)
	}
	return nil
}

// PrunePostCopies deletes at most limit buffered copies of the Organization received PostCopyRetention before now,
// for the short_lived_pruning Leader task, and returns how many it deleted. Running it twice deletes nothing more.
func (s *Service) PrunePostCopies(ctx context.Context, now time.Time, limit int32) (int64, error) {
	n, err := s.store.q().PrunePostCopies(ctx, dbgen.PrunePostCopiesParams{OrgID: s.orgID,
		Before: now.UTC().Add(-PostCopyRetention), BatchSize: limit})
	if err != nil {
		return 0, fmt.Errorf("delete the telegram post copies: %w", err)
	}
	return n, nil
}

// post is a channel post as the copy buffer keys it: the Connection, the channel and the post's id, with the
// discussion group a copy must be in.
type post struct {
	connection, channel, discussion, id int64
}

// telegramPost is the channel post of a Telegram Root message, false when the Destination's channel or discussion
// group is not known or the message id is not a Telegram one.
func telegramPost(connection, channel, discussion pgtype.Int8, messageID string) (post, bool) {
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil || id <= 0 || !connection.Valid || !channel.Valid || !discussion.Valid {
		return post{}, false
	}
	return post{connection: connection.Int64, channel: channel.Int64, discussion: discussion.Int64, id: id}, true
}

// lockPost takes the lock of the post's copy until the transaction of q ends.
func lockPost(ctx context.Context, q queries, p post) error {
	if err := q.LockPostCopy(ctx, dbgen.LockPostCopyParams{LockClass: postCopyLockClass, ConnectionID: p.connection,
		ChannelChatID: p.channel, ChannelMessageID: p.id}); err != nil {
		return fmt.Errorf("lock the copy of post %d: %w", p.id, err)
	}
	return nil
}

// knownCopy is the buffered copy of the post in its discussion group, empty when none is known.
func knownCopy(ctx context.Context, q queries, org int64, p post) (string, error) {
	c, err := q.GetPostCopy(ctx, dbgen.GetPostCopyParams{OrgID: org, ConnectionID: p.connection,
		ChannelChatID: p.channel, ChannelMessageID: p.id})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read the copy of post %d: %w", p.id, err)
	case c.DiscussionChatID != p.discussion:
		return "", nil
	}
	return idText(c.CopyMessageID), nil
}

// setThread sets the Thread of the delivery id.
func setThread(ctx context.Context, q queries, org, id int64, state, anchor, chain string, now time.Time) error {
	if err := q.SetThread(ctx, dbgen.SetThreadParams{ThreadState: state, AnchorID: nonEmpty(anchor),
		ChainLastID: nonEmpty(chain), Now: now, OrgID: org, ID: id}); err != nil {
		return fmt.Errorf("set the thread of the delivery: %w", err)
	}
	return nil
}

// publicationPost is the channel post that a Telegram Publication of an Alert Group created, false for any other
// call: its Thread is then attached to the post's known copy, or waits for it.
func publicationPost(a attempt, messageID string) (post, bool) {
	if !a.publication || a.destination.Type != TypeTelegram || a.row.StormID.Valid {
		return post{}, false
	}
	return telegramPost(a.row.ConnectionID, a.row.TelegramChannelChatID, a.row.TelegramDiscussionChatID, messageID)
}

// publishedThread attaches the Thread of a delivery whose Publication created the post p to its copy when the copy is
// known, and makes it wait for the copy otherwise; the lock of the post is held.
func publishedThread(ctx context.Context, q queries, org, id int64, p post, now time.Time) error {
	anchor, err := knownCopy(ctx, q, org, p)
	if err != nil {
		return err
	}
	if anchor != "" {
		return setThread(ctx, q, org, id, threadAttached, anchor, "", now)
	}
	return setThread(ctx, q, org, id, threadWaiting, "", "", now)
}

func idText(id int64) string { return strconv.FormatInt(id, 10) }
