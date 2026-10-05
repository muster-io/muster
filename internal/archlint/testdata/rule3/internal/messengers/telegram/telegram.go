package telegram

import (
	"context"

	"github.com/muster-io/muster/internal/delivery"
)

// Client implements delivery.Adapter; declaring the methods is good.
type Client struct{}

func (c *Client) Publish(ctx context.Context, msg delivery.Message) (string, error) {
	return "1", nil
}

func (c *Client) Update(ctx context.Context, id string, msg delivery.Message) error {
	return nil
}

func (c *Client) Reply(ctx context.Context, id string, msg delivery.Message) (string, error) {
	return "2", nil
}

// Bad: an adapter calling its own send method decides about delivery outside the worker.
func (c *Client) Resend(ctx context.Context, msg delivery.Message) error {
	_, err := c.Publish(ctx, msg) // want: 3
	return err
}
