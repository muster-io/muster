// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// How the person who pressed was answered, as slack_press logs it.
const (
	answeredEphemeral   = "ephemeral"
	answeredResponseURL = "response_url"
)

// private is the answer to a press: its text, to the user who pressed in the channel of the press, in the Thread of
// threadTS when set; the press's response_url, which takes it when chat.postEphemeral fails; and the Destination and
// the Alert Group the press is bound to, which the Request log links its requests to.
type private struct {
	channel, user, threadTS string
	responseURL             string
	text                    string
	links                   outbound.Links
}

// answerText escapes text for Slack's mrkdwn: `&`, `<` and `>` become entities, and nothing else changes, so that a
// link in it stays a link.
var answerText = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// answer shows a to the person who pressed on the Connection c (C-24.FR-11): first with chat.postEphemeral to that
// person in the channel of the press — in the Thread of the Root message for a press on a Reminder — on the interactive
// path, limited by the Connection's limiter, whose token it takes ahead of deliveries, within the Budget; when that
// fails — the person is no longer in the channel, Slack refuses it, or no token is free within the Budget — the same
// text goes to the press's response_url as an ephemeral answer that replaces nothing, on the interactive path without
// a limiter, since response_url is no Web API method, within a Budget of its own. It returns how the person was
// answered, empty when neither way worked, and why the ephemeral, and then the response_url, failed: masked, and never
// with the response_url. Neither way is tried again.
func (p *Presses) answer(ctx context.Context, c Conn, a private) (string, error) {
	budget := cmp.Or(p.Budget, delivery.InteractiveBudget)
	ctx = outbound.WithLinks(ctx, a.links)
	text := answerText.Replace(a.text)
	id := c.ID
	ephemeral := func() error {
		ctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		out, err := p.Path.Do(ctx, delivery.Subject{Connection: &id}, delivery.AnswerOp(
			func(ctx context.Context, call delivery.Call) delivery.Outcome {
				return c.Client.postEphemeral(ctx, call.Class, a.channel, a.user, a.threadTS, text).Outcome()
			}))
		switch {
		case err != nil:
			return fmt.Errorf("the ephemeral answer was not sent: %w", err)
		case out.Kind != delivery.OutcomeOK:
			return errors.New("the ephemeral answer was not sent: " + cmp.Or(string(out.Error), string(out.Kind)))
		}
		return nil
	}
	first := ephemeral()
	if first == nil {
		return answeredEphemeral, nil
	}
	if a.responseURL == "" {
		return "", errors.Join(first, errors.New("the press has no response_url"))
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	out, err := p.Path.Do(ctx, delivery.Subject{NoLimiter: true}, delivery.AnswerOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			return c.Client.respond(ctx, call.Class, a.responseURL, text).Outcome()
		}))
	switch {
	case err != nil:
		// The interactive path's own error, which names no address.
		return "", errors.Join(first, fmt.Errorf("the response_url answer was not sent: %w", err))
	case out.Kind != delivery.OutcomeOK:
		return "", errors.Join(first, errors.New("the response_url answer was not sent: "+
			cmp.Or(string(out.Error), string(out.Kind))))
	}
	return answeredResponseURL, first
}
