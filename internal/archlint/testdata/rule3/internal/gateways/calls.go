package gateways

import (
	"context"

	"github.com/muster-io/muster/internal/delivery"
)

// Bad: every call below sends a notification through a Notification gateway outside the interactive path and the
// gateway sender.
func Call(ctx context.Context, g delivery.Gateway, n delivery.Notification) error {
	return g.Notify(ctx, n) // want: 3
}

type notifier interface {
	Notify(ctx context.Context, n delivery.Notification) error
}

func Page(ctx context.Context, p notifier, n delivery.Notification) error {
	return p.Notify(ctx, n) // want: 3
}

func Direct(ctx context.Context, g delivery.Gateway, n delivery.Notification) error {
	notify := g.Notify // want: 3
	return notify(ctx, n)
}

// Good: a method of the same name with another signature is not the gateway's.
type pager struct{}

func (pager) Notify(ctx context.Context, text string) error { return ctx.Err() }

func Other(ctx context.Context) error {
	return pager{}.Notify(ctx, "text")
}
