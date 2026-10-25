// pwnbridge-pan performs one bounded reconnect of an existing allowlisted PAN profile.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kuroiko0429/pwnbridge/internal/pan"
)

func main() {
	profile := flag.String("profile", "", "existing NetworkManager PAN connection UUID")
	peer := flag.String("peer", "", "expected laptop Bluetooth MAC")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	run := func(ctx context.Context, args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "/usr/bin/nmcli", args...).CombinedOutput()
		text := string(out)
		if err != nil {
			return "", fmt.Errorf("nmcli: %w: %s", err, strings.TrimSpace(text))
		}
		return text, nil
	}
	if err := pan.Ensure(ctx, run, *profile, *peer); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
