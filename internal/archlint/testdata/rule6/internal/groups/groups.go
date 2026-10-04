package groups

import (
	"context"
	stdctx "context"
)

// Bad: domain code takes its context from the caller.
func Detached() (context.Context, context.Context, func() context.Context, context.Context) {
	a := context.Background()        // want: 6
	b := stdctx.Background()         // want: 6
	background := context.Background // want: 6
	todo := context.TODO()           // want: 6
	return a, b, background, todo
}
