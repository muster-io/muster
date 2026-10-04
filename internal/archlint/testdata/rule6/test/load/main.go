package main

import "context"

// Good: the load test's main may create the root context.
func main() {
	report(context.Background())
}
