package delivery

import "context"

type Message struct {
	Text string
}

// Adapter is the messenger adapter: Publish sends a Root message, Update edits it, Reply sends into its thread.
type Adapter interface {
	Publish(ctx context.Context, msg Message) (string, error)
	Update(ctx context.Context, id string, msg Message) error
	Reply(ctx context.Context, id string, msg Message) (string, error)
}
