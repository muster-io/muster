package logging

import (
	"fmt"
	"log"
	"log/slog"
	"log/syslog"
	"os"

	"go.uber.org/zap"
)

// Good: the domain logger is the one place that may use the log packages, a third-party logger and the standard
// streams.
func New() *slog.Logger {
	log.SetFlags(0)
	_, _ = syslog.New(syslog.LOG_INFO, "muster")
	zap.L().Info("logger ready")
	fmt.Println("logger ready")
	println("logger ready")
	return slog.New(slog.NewJSONHandler(os.Stderr, nil))
}
