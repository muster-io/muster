package cli

import (
	"fmt"
	"os"
)

// Good: the CLI prints to the standard streams.
func Main() int {
	fmt.Println("muster")
	fmt.Fprintln(os.Stdout, "usage")
	fmt.Fprintln(os.Stderr, "error")
	return 0
}
