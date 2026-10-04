package main

import (
	"fmt"
	"os"
)

// Good: the load test prints its report.
func main() {
	fmt.Println("p99")
	fmt.Fprintln(os.Stdout, "report")
}
