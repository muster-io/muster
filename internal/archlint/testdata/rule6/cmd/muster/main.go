package main

import (
	"context"

	"github.com/muster-io/muster/internal/runtime"
)

// Good: main may create the root context.
func main() {
	runtime.Start(context.Background())
}
