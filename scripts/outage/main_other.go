//go:build !windows && !linux

// Обрыв есть только под Windows (WFP) и Linux (nftables, netlink).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "outage: только для Windows и Linux")
	os.Exit(2)
}
