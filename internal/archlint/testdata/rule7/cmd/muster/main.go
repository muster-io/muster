package main

import "fmt"

// Bad: the binary's main is not exempt from rule 7; it calls internal/cli.
func main() {
	fmt.Println("muster") // want: 7
}
