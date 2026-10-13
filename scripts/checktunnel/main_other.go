//go:build !windows

// Снимок состояния пока только для Windows; под Linux и macOS — в Э4.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "checktunnel: пока только для Windows")
	os.Exit(2)
}
