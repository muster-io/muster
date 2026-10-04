package groups

import (
	"fmt"
	"log"      // want: 7
	"log/slog" // want: 7
	"os"
)

// Bad: domain code logs through the domain logger only.
func Report(id int) {
	log.Printf("group %d", id)
	slog.Info("group", "id", id)
	fmt.Println("group", id)                // want: 7
	fmt.Printf("group %d\n", id)            // want: 7
	fmt.Print("group")                      // want: 7
	fmt.Fprintln(os.Stdout, "group", id)    // want: 7
	_, _ = os.Stderr.WriteString("group\n") // want: 7
}
