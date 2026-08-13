package main

import (
	"context"
	"testing"
)

func TestRunRejectsUnknownCommandWithoutPrintingArguments(t *testing.T) {
	if err := run(context.Background(), []string{"unknown", "example-token-not-real"}); err == nil {
		t.Fatal("run() error = nil")
	}
}
