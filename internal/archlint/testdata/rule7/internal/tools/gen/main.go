package main

import (
	"fmt"
	"os"
)

// Good: build tooling prints its own report.
func main() {
	fmt.Println("generated")
	fmt.Fprintln(os.Stderr, "done")
}
