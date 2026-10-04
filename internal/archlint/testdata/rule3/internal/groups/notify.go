package groups

import (
	"context"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/messengers/telegram"
)

// Bad: every call below sends or edits a messenger message outside delivery.
func Notify(ctx context.Context, a delivery.Adapter, tg *telegram.Client, msg delivery.Message) error {
	if _, err := a.Publish(ctx, msg); err != nil { // want: 3
		return err
	}
	if err := tg.Update(ctx, "1", msg); err != nil { // want: 3
		return err
	}
	reply := a.Reply // want: 3
	_, err := reply(ctx, "1", msg)
	return err
}

type publisher interface {
	Publish(ctx context.Context, msg delivery.Message) (string, error)
}

func Announce(ctx context.Context, p publisher, msg delivery.Message) error {
	_, err := p.Publish(ctx, msg) // want: 3
	return err
}

type forwarder struct {
	delivery.Adapter
}

func (f forwarder) Forward(ctx context.Context, msg delivery.Message) error {
	_, err := f.Publish(ctx, msg) // want: 3
	return err
}

func Edit(ctx context.Context, a delivery.Adapter, msg delivery.Message) error {
	return delivery.Adapter.Update(a, ctx, "1", msg) // want: 3
}
