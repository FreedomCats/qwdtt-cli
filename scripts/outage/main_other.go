//go:build !windows

// Обрыв через WFP есть только под Windows.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "outage: только для Windows")
	os.Exit(2)
}
