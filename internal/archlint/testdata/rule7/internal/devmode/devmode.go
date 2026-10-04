package devmode

import (
	"fmt"
	"os"
)

// Good: muster dev prints the addresses it listens on.
func Banner() {
	fmt.Printf("app on %s\n", ":8080")
	fmt.Fprintln(os.Stdout, "fakes ready")
}
