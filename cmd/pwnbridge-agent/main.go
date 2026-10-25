// pwnbridge-agent runs as pi and exposes only telemetry and bounded local UI RPC.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kuroiko0429/pwnbridge/internal/device"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := device.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, device.NewCollector()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
