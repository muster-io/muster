// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"context"
	"fmt"

	"github.com/muster-io/muster/internal/delivery"
)

// Preview renders, without sending anything, what the Destination publicID would receive for the source src
// (C-16.FR-4): the Root message of a Mattermost (Markdown, with the attachment as the request's body) or a Telegram
// (HTML) Destination, or the requests of an outgoing webhook — the event body in the events mode and "create",
// "update", "open thread" and "reply in thread" in the template mode — with every Secret masked. It takes no limiter
// token and records nothing.
func (s *Service) Preview(ctx context.Context, publicID string, src Source) ([]delivery.PreviewItem, error) {
	d, tester, err := s.tester(ctx, publicID)
	if err != nil {
		return nil, err
	}
	in, err := s.input(ctx, d, src)
	if err != nil {
		return nil, err
	}
	items, err := tester.Preview(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("preview destination %s: %w", d.PublicID, err)
	}
	return items, nil
}
