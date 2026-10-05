package runtime

import "context"

// Good: the wiring package may create contexts.
func Start(ctx context.Context) {
	_ = ctx
	_ = context.Background()
}
