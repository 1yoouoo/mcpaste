package database

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestOpenRejectsMalformedURLWithoutEcho(t *testing.T) {
	marker := "database-url-secret-marker"
	_, err := Open(context.Background(), marker)
	if err == nil {
		t.Fatal("Open() error = nil")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("Open() error echoed the database URL")
	}
}

func TestOpenAndReadyIntegration(t *testing.T) {
	databaseURL := os.Getenv("MCPASTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCPASTE_TEST_DATABASE_URL is not set")
	}
	pool, err := Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer pool.Close()
	if err := Ready(context.Background(), pool); err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
}
