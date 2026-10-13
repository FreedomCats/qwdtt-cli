//go:build !windows && !linux

// Снимок состояния — только для Windows и Linux; под macOS — в Э4.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "checktunnel: только для Windows и Linux")
	os.Exit(2)
}
