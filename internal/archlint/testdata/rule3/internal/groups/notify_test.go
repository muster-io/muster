package groups

import (
	"context"

	"github.com/muster-io/muster/internal/delivery"
)

// Good: test files are exempt.
func publishForTest(ctx context.Context, a delivery.Adapter) error {
	_, err := a.Publish(ctx, delivery.Message{})
	return err
}
