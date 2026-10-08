// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"context"
	"net/http"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// The response mapping of posts (C-13.FR-5) on top of the mapping of every call (client.go): 429 holds the whole
// Connection, 5xx, timeouts and network errors are Transient, 401 and 403 are Fatal, and so is a 404 on creating a post,
// whose channel is unknown or archived; anything else is unknown. A deleted Root message is not answered with 404
// (F-058): an edit of it with 403, which a plain read of the post tells from a real permission error, and a reply
// under it with 400 and errorRootID.

// errorRootID is the error id of a reply whose Root message was deleted (F-058).
const errorRootID = "api.post.create_post.root_id.app_error"

// created is the outcome of a new post: an ok one names the post and its permalink; a success without a post id is
// unknown.
func created(r Result, p post, permalink string) delivery.Outcome {
	switch {
	case !r.OK():
		return r.Outcome
	case p.ID == "":
		return delivery.Outcome{Kind: delivery.OutcomeUnknown, Error: "the post was answered without its id"}
	}
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: p.ID, MessageURL: permalink}
}

// replied is the outcome of a Thread reply: refused with 400 and errorRootID, its Root message was deleted and is gone
// at once, with no read.
func replied(r Result, p post) delivery.Outcome {
	if r.Status == http.StatusBadRequest && r.ErrorID == errorRootID {
		return gone(r)
	}
	return created(r, p, "")
}

// edited is the outcome of an edit of the post id: 404 is a post that is gone; 403 is followed by the plain read of the
// post in the same client class, whose 404 means the post was deleted, whose 200 keeps the 403 Fatal, and whose other
// answers are classified by the mapping.
func edited(ctx context.Context, c *Client, class outbound.Class, id string, r Result) delivery.Outcome {
	switch r.Status {
	case http.StatusNotFound:
		return gone(r)
	case http.StatusForbidden:
		read := c.getPost(ctx, class, id)
		switch {
		case read.Status == http.StatusNotFound:
			return gone(r)
		case read.OK():
			return r.Outcome
		}
		return read.Outcome
	}
	if r.OK() {
		return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: id}
	}
	return r.Outcome
}

// gone is the outcome of a Root message that no longer exists, with the masked text of the answer that said so.
func gone(r Result) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeGone, Error: r.Outcome.Error}
}
