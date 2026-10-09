// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"context"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// EphemeralPost lets the tests of the package make the ephemeral post of a press's answer.
func (c *Client) EphemeralPost(ctx context.Context, class outbound.Class, userID, channelID, rootID,
	message string) Result {
	return c.ephemeralPost(ctx, class, userID, channelID, rootID, message)
}

// PressAnswer lets the tests of the package decode the answer to a press as the callback encodes it.
type PressAnswer = pressAnswer

// NewTestPublisher is the publisher of a test message, which only publishes.
func NewTestPublisher() delivery.Adapter { return &testPublisher{} }
