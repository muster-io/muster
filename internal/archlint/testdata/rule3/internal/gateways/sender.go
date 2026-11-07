package gateways

import (
	"context"

	"github.com/muster-io/muster/internal/delivery"
)

// Good: the gateway sender is the one place outside the interactive path that sends a call.
func Send(ctx context.Context, g delivery.Gateway, n delivery.Notification) error {
	return g.Notify(ctx, n)
}
