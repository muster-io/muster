package main

import (
	"fmt"
	"os"
)

// Good: the architecture lint prints its findings.
func main() {
	fmt.Println("finding")
	fmt.Fprintln(os.Stderr, "1 finding")
}
