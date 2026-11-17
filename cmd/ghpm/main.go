// Command ghpm manages binaries published on GitHub releases.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"ghpm/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Execute(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ghpm: "+err.Error())
		os.Exit(1)
	}
}
