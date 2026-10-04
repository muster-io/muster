package groups

import (
	"fmt"
	"io"
	"os"
)

// Good: formatting into values and writers, and a parameter named log.
func Render(w io.Writer, log io.Writer, id int) (string, error) {
	if _, err := fmt.Fprintf(w, "group %d\n", id); err != nil {
		return "", err
	}
	if _, err := log.Write([]byte("rendered")); err != nil {
		return "", fmt.Errorf("render group %d: %w", id, err)
	}
	return fmt.Sprintf("group %d %s", id, os.Getenv("TZ")), nil
}
