package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCommandExitCodes(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &out); code != 0 || !strings.Contains(out.String(), "schema 1") {
		t.Fatal(code, out.String())
	}
	out.Reset()
	if code := run(context.Background(), []string{"unknown"}, &out, &out); code != 1 {
		t.Fatal(code)
	}
}
