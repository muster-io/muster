package groups

import (
	"context"
	"testing"
)

// Good: tests may create contexts.
func TestDetached(t *testing.T) {
	_ = context.Background()
}
