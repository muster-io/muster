package main

import "context"

// Bad: only test/load/main.go is exempt, not the rest of the load test.
func report(ctx context.Context) {
	_ = ctx
	_ = context.Background() // want: 6
}
