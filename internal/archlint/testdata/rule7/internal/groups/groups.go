package groups

import (
	"fmt"
	"log"        // want: 7
	"log/slog"   // want: 7
	"log/syslog" // want: 7
	"os"

	"go.uber.org/zap"         // want: 7
	"go.uber.org/zap/zapcore" // want: 7
)

// Bad: domain code logs through the domain logger only.
func Report(id int) {
	log.Printf("group %d", id)
	slog.Info("group", "id", id)
	_, _ = syslog.New(syslog.LOG_INFO, "muster")
	zap.L().Info("group", zapcore.Field{})
	fmt.Println("group", id)                // want: 7
	fmt.Printf("group %d\n", id)            // want: 7
	fmt.Print("group")                      // want: 7
	fmt.Fprintln(os.Stdout, "group", id)    // want: 7
	_, _ = os.Stderr.WriteString("group\n") // want: 7
	println("group", id)                    // want: 7
	print("group")                          // want: 7
}
