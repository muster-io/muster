package logging

import (
	"fmt"
	"log"
	"log/slog"
	"os"
)

// Good: the domain logger is the one place that may use log, log/slog and the standard streams.
func New() *slog.Logger {
	log.SetFlags(0)
	fmt.Println("logger ready")
	return slog.New(slog.NewJSONHandler(os.Stderr, nil))
}
