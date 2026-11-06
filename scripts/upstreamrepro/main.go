// Command upstreamrepro checks the pinned upstream reproductions in disposable copies.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: upstreamrepro <prepared-local-source>")
		os.Exit(2)
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "upstreamrepro:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := newRunner(root, os.Args[1], os.Stdout)
	if err := r.run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "upstreamrepro:", err)
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		os.Exit(1)
	}
}
