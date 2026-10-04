package alerts

import (
	"io"
	"strings"
)

// Good: functions of the package that are named like the builtins print and println.
func print(w io.Writer, parts ...string) {
	_, _ = io.WriteString(w, strings.Join(parts, " "))
}

func println(w io.Writer, parts ...string) {
	print(w, append(parts, "\n")...)
}

func Render(w io.Writer, id string) {
	println(w, "alert", id)
}
