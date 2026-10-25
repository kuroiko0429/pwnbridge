package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/kuroiko0429/pwnbridge/internal/host"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func run(ctx context.Context, args []string, out, errout io.Writer) int {
	if err := host.RunCLI(ctx, args, out, errout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(errout, "pwnbridge:", err)
		return 1
	}
	return 0
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
