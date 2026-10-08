// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/templates"
)

// CallbackPath is where Mattermost calls Muster for the button presses of a Connection, under MUSTER_INGEST_URL, with
// the Connection's public_id after it (C-13.FR-4).
const CallbackPath = "/api/v1/callbacks/mattermost/"

// markPath is the Muster mark the server serves, the icon of an attachment's footer.
const markPath = "/muster-mark-256.png"

// ErrNoTarget is a Destination that has no Mattermost Connection to post through: it, or its Connection, no longer
// exists, or it is not a Mattermost Destination.
var ErrNoTarget = errors.New("the destination has no mattermost connection")

// Target is where a Mattermost Destination posts: the client of its Connection, the Connection's id and public_id,
// and the team and channel of the Destination.
type Target struct {
	Client             *Client
	ConnectionID       int64
	ConnectionPublicID string
	TeamID             string
	TeamName           string
	ChannelID          string
}

// Targets find the Target of a Destination by its id, declared by their consumer; *connections.Service implements
// them. A Destination without one is ErrNoTarget.
type Targets interface {
	Target(ctx context.Context, destinationID int64) (Target, error)
}

// Adapter is the delivery adapter of Mattermost Destinations (C-11.FR-7, C-13): Publish, Update and Reply, called by
// the delivery worker and the interactive path only, in the client class of their Call, and Check, the Destination
// check of C-13.FR-10 for the Broken probe. PublicURL and IngestURL are MUSTER_PUBLIC_URL and MUSTER_INGEST_URL, and
// Version the version that the attachment's footer names.
type Adapter struct {
	Targets   Targets
	PublicURL string
	IngestURL string
	Version   string
}

var (
	_ delivery.Adapter = (*Adapter)(nil)
	_ delivery.Checker = (*Adapter)(nil)
)

// LengthLimit is the server's post length limit (F-057), which a Root message is shortened to.
func (a *Adapter) LengthLimit() int { return LengthLimit }

// Markup is the markup of Mattermost posts.
func (a *Adapter) Markup() messages.Markup { return messages.MarkupMarkdown }

// Publish creates the Root message m, with the Mentions of a Loud call, its targets' and those of the template
// tokens of m, in its message.
func (a *Adapter) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	l := a.look(c, t)
	p := rootPost(m, l, a.mentions(c, l, templateTokens(m)))
	p.ChannelID = t.ChannelID
	out, r := t.Client.createPost(ctx, c.Class, p)
	return created(r, out, permalink(t, out.ID))
}

// Update edits the Root message messageID to m: its summary line without Mentions and its attachment.
func (a *Adapter) Update(ctx context.Context, c delivery.Call, messageID string, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	p := rootPost(m, a.look(c, t), "")
	_, r := t.Client.patchPost(ctx, c.Class, messageID, patch{Message: p.Message, Props: p.Props})
	return edited(ctx, t.Client, c.Class, messageID, r)
}

// Reply posts m into the Thread of root, with the Mentions of a Loud call first.
func (a *Adapter) Reply(ctx context.Context, c delivery.Call, root delivery.Root, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	l := a.look(c, t)
	p := replyPost(m, l, a.mentions(c, l, nil))
	p.ChannelID, p.RootID = t.ChannelID, root.MessageID
	out, r := t.Client.createPost(ctx, c.Class, p)
	return replied(r, out)
}

// Check runs the Destination check of C-13.FR-10 at once, in the client class of c: the Broken probe calls it in the
// delivery class, the interactive path in the interactive class.
func (a *Adapter) Check(ctx context.Context, c delivery.Call) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	res, err := CheckDestination(ctx, t.Client, Direct(c), t.TeamID, t.ChannelID)
	if err != nil {
		return delivery.Outcome{Kind: delivery.OutcomeTransient, Error: outbound.Untrusted(err.Error())}
	}
	return res.Outcome
}

// target is the Target of the call's Destination, or the outcome of a call that cannot be made: Fatal without a
// Connection, Transient when it could not be read.
func (a *Adapter) target(ctx context.Context, c delivery.Call) (Target, *delivery.Outcome) {
	t, err := a.Targets.Target(ctx, c.Destination.ID)
	switch {
	case errors.Is(err, ErrNoTarget):
		return Target{}, &delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(ErrNoTarget.Error())}
	case err != nil:
		return Target{}, &delivery.Outcome{Kind: delivery.OutcomeTransient,
			Error: "the connection of the destination could not be read"}
	}
	return t, nil
}

// look is how the messages of the call are laid out for the Target.
func (a *Adapter) look(c delivery.Call, t Target) look {
	l := look{markup: messages.MarkupMarkdown, space: "mattermost:" + strconv.FormatInt(t.ConnectionID, 10),
		callback: strings.TrimSuffix(a.IngestURL, "/") + CallbackPath + t.ConnectionPublicID,
		footer:   "Muster v" + strings.TrimPrefix(a.Version, "v"),
		icon:     strings.TrimSuffix(a.PublicURL, "/") + markPath, limit: LengthLimit}
	if c.Plain {
		l.markup = messages.MarkupPlain
	}
	return l
}

// mentions are the targets of a Loud call and the template tokens in Mattermost's syntax; a Quiet call mentions
// nobody.
func (a *Adapter) mentions(c delivery.Call, l look, tokens []templates.Token) string {
	if c.Loudness != groups.Loud {
		return ""
	}
	return mentionText(c.Targets, tokens, l.markup)
}

// permalink is the link to the post id in the Destination's team, or through the server's redirect when the team's
// name is unknown.
func permalink(t Target, id string) string {
	if id == "" {
		return ""
	}
	team := t.TeamName
	if team == "" {
		team = "_redirect"
	}
	return strings.TrimSuffix(t.Client.settings.ServerURL, "/") + "/" + team + "/pl/" + id
}
