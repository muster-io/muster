// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakebridge

import (
	"net/http"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The channels of a new fake: ChannelAlerts ("alerts") and ChannelAlertsProd ("alerts-prod"), which the bot can use,
// and ChannelNoBot, which it cannot.
const (
	ChannelAlerts     = "ch-alerts"
	ChannelAlertsProd = "ch-alerts-prod"
	ChannelNoBot      = "ch-nobot"
)

// MessageNotMember is the detail of the forbidden answer of checkChannel for a channel the bot cannot use.
const MessageNotMember = "The bot is not a member of this channel."

// checkChannel answers checkChannel, which posts nothing: 200 with the channel's name for a channel of the
// configuration that has one, and forbidden with MessageNotMember for any other, ChannelNoBot and unknown ids
// included.
func (f *Fake) checkChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("channel_id")
	if name := f.Config().Channels[id]; name != nil {
		fakeserver.WriteJSON(w, http.StatusOK, gen.ChannelCheck{ChannelId: id, Name: *name})
		return
	}
	writeProblemDetail(w, http.StatusForbidden, gen.ErrorClassForbidden, "Forbidden", MessageNotMember)
}
