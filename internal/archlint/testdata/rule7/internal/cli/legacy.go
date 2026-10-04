package cli

import (
	"log" // want: 7
)

// Bad: printing is allowed in the CLI, but the log packages are not.
func Legacy() {
	log.Print("legacy")
}
