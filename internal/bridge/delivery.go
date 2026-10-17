// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"errors"
	"strconv"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// ErrNoTarget is a Destination that has no Bridge Connection to post through: its Connection no longer exists, or it
// is not a Bridge Destination.
var ErrNoTarget = errors.New("the destination has no bridge connection")

// Target is where a Bridge Destination posts: its Connection, with the protocol client, and its channel.
type Target struct {
	Conn      Conn
	ChannelID string
}

// Targets find the Target of a Destination by its id — of a deleted Destination too, for its final edit — and read
// the info of its Connection through the InfoCache, again past bridge.info_max_age; declared by their consumer,
// *connections.Service implements them. A Destination without a Target is ErrNoTarget.
type Targets interface {
	BridgeTarget(ctx context.Context, destinationID int64) (Target, error)
	BridgeInfo(ctx context.Context, conn Conn, class outbound.Class) (Info, Result)
}

// Adapter is the Bridge delivery (C-22.FR-4 to FR-8, ADR-0017): Publish, Update and Reply post, edit and reply as
// envelopes, called by the delivery worker and the interactive path only, in the client class of their Call, with the
// idempotency key of the Call; and Check, the Destination check of C-22.FR-3, for the Broken probe. Each body is fitted
// to the limit of the Destination's Connection, which its info gives.
type Adapter struct {
	Targets Targets
}

var (
	_ delivery.Adapter    = (*Adapter)(nil)
	_ delivery.Checker    = (*Adapter)(nil)
	_ delivery.Idempotent = (*Adapter)(nil)
)

// LengthLimit is the protocol's floor of limits.max_body_length. Delivery never shortens for a bridge: the adapter
// fits each body to its Connection's limit itself.
func (a *Adapter) LengthLimit() int { return MinBodyLength }

// PublicationKey is the idempotency key of a Publication (C-22.FR-6): pub-<Destination public_id>-<delivery
// id>-<publication>, the same for every retry of it.
func (a *Adapter) PublicationKey(d delivery.Destination, deliveryID, publication int64) string {
	return "pub-" + d.PublicID + "-" + strconv.FormatInt(deliveryID, 10) + "-" + strconv.FormatInt(publication, 10)
}

// ReplyKey is the idempotency key of a Thread reply (C-22.FR-6): reply-<Destination public_id>-<thread reply id>.
func (a *Adapter) ReplyKey(d delivery.Destination, replyID int64) string {
	return "reply-" + d.PublicID + "-" + strconv.FormatInt(replyID, 10)
}

// Publish posts m as a Root message, or a Storm summary, with the Mentions of a Loud call before its body. Its
// outcome names the message, its link when the bridge sent a safe one, the Thread the answer named and whether the
// answer was replayed; a truncated body is delivered.
func (a *Adapter) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	t, i, fail := a.prepare(ctx, c)
	if fail != nil {
		return *fail
	}
	kind := gen.Root
	if m.Kind == messages.KindStorm {
		kind = gen.StormSummary
	}
	in := postInput(kind, t.ChannelID, content(m, i, mentionLine(c)), c.Loudness == groups.Loud)
	out, r := t.Conn.Client.PostMessage(ctx, c.Class, c.Key, in)
	o := r.Outcome()
	if r.OK() {
		o.MessageID, o.MessageURL = out.MessageId, messages.SafeURL(out.Url.GetOrEmpty())
		o.ThreadRef, o.Replayed = out.ThreadRef.GetOrEmpty(), out.Replayed
	}
	return o
}

// Update replaces the message messageID with the whole envelope of m, Quiet and without Mentions.
func (a *Adapter) Update(ctx context.Context, c delivery.Call, messageID string, m delivery.Message) delivery.Outcome {
	t, i, fail := a.prepare(ctx, c)
	if fail != nil {
		return *fail
	}
	_, r := t.Conn.Client.EditMessage(ctx, c.Class, messageID, content(m, i, nil))
	o := r.Outcome()
	if r.OK() {
		o.MessageID = messageID
	}
	return o
}

// Reply posts m into the Thread of root under its last thread_ref, null without one, with the Mentions of a Loud call
// before its body; its outcome names the reply, the thread_ref the answer returned and whether it was replayed. A
// Reminder carries its buttons when the bridge has them.
func (a *Adapter) Reply(ctx context.Context, c delivery.Call, root delivery.Root, m delivery.Message) delivery.Outcome {
	t, i, fail := a.prepare(ctx, c)
	if fail != nil {
		return *fail
	}
	in := replyInput(root.ThreadAnchorID, m, i, mentionLine(c), c.Loudness == groups.Loud)
	out, r := t.Conn.Client.ReplyInThread(ctx, c.Class, root.MessageID, c.Key, in)
	o := r.Outcome()
	if r.OK() {
		o.MessageID, o.ThreadRef, o.Replayed = out.MessageId, out.ThreadRef, out.Replayed
	}
	return o
}

// Check runs the Destination check of C-22.FR-3 — one checkChannel, which posts nothing — at once in the client class
// of c: the Broken probe calls it in the delivery class (C-11.FR-9).
func (a *Adapter) Check(ctx context.Context, c delivery.Call) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	res, err := CheckDestination(ctx, t.Conn.Client, Direct(c), t.ChannelID)
	if err != nil {
		return delivery.Outcome{Kind: delivery.OutcomeTransient, Error: outbound.Untrusted(err.Error())}
	}
	return res.Outcome
}

// prepare is the Target of the call's Destination and the info of its Connection, or the outcome of a call that cannot
// be made.
func (a *Adapter) prepare(ctx context.Context, c delivery.Call) (Target, Info, *delivery.Outcome) {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return Target{}, Info{}, fail
	}
	i, _, fail := a.info(ctx, t, c)
	if fail != nil {
		return Target{}, Info{}, fail
	}
	return t, i, nil
}

// info is the info of the Connection of t and how its read ended, or also the outcome of a call that cannot be made:
// an info that cannot be read, without an earlier copy, ends the call as its read classifies it; that read posted
// nothing, so its outcome is never Unanswered.
func (a *Adapter) info(ctx context.Context, t Target, c delivery.Call) (Info, Result, *delivery.Outcome) {
	i, r := a.Targets.BridgeInfo(ctx, t.Conn, c.Class)
	if !r.OK() && i.ProtocolVersion == "" {
		o := r.Outcome()
		o.Unanswered = false
		return Info{}, r, &o
	}
	return i, r, nil
}

// target is the Target of the call's Destination, or the outcome of a call that cannot be made: Fatal without a
// Connection, Transient when it could not be read.
func (a *Adapter) target(ctx context.Context, c delivery.Call) (Target, *delivery.Outcome) {
	t, err := a.Targets.BridgeTarget(ctx, c.Destination.ID)
	switch {
	case errors.Is(err, ErrNoTarget):
		return Target{}, &delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(ErrNoTarget.Error())}
	case err != nil:
		return Target{}, &delivery.Outcome{Kind: delivery.OutcomeTransient,
			Error: "the connection of the destination could not be read"}
	}
	return t, nil
}
