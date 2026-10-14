//go:build !windows

// Ctrl+Break по PID — только на Windows: на Linux то же делает kill -INT.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ctrlbreak: только для Windows; на Linux — kill -INT <pid>")
	os.Exit(2)
}
