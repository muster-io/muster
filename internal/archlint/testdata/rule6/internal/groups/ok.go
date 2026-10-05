package groups

import (
	"context"
	"time"
)

// Good: contexts derived from the caller's, and a Background method of a type that is not package context.
func WithDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Second)
}

type scheduler struct{}

func (scheduler) Background() string { return "background" }

func Mode() string {
	return scheduler{}.Background()
}
