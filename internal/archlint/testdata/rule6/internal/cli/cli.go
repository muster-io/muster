package cli

import "context"

// Good: the CLI wiring may create contexts.
func Main() context.Context {
	return context.Background()
}
