package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/connector"
)

func TestRunAcceptsOnlyPeerRegisterAndDefaultMCPModes(t *testing.T) {
	for _, args := range [][]string{{"setup"}, {"login"}, {"approve"}, {"--endpoint", "https://example.test"}} {
		if err := run(context.Background(), args); err == nil {
			t.Fatalf("run(%q) unexpectedly succeeded", args)
		}
	}
}

func TestRunPeerUsesOwnedPollableStdinPipe(t *testing.T) {
	directory := t.TempDir()
	credentialPath := filepath.Join(directory, "credential.json")
	registryPath := filepath.Join(directory, "registry.json")
	for attempt := 0; attempt < 3; attempt++ {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		port := freePeerPort(t)
		if err := connector.SaveCredential(credentialPath, connector.Credential{
			Endpoint: "http://127.0.0.1:" + strconv.Itoa(port),
			Token:    "peer-cli-test-token",
		}); err != nil {
			_ = reader.Close()
			_ = writer.Close()
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		readiness, readinessWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			done <- runPeer(ctx, []string{
				"--device-id", "11111111-1111-4111-8111-111111111111",
				"--name", "CLI Test Mac",
				"--credential-file", credentialPath,
				"--registry-file", registryPath,
				"--port", strconv.Itoa(port),
			}, reader, readinessWriter)
		}()
		if err := readiness.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		line := make([]byte, len("mcpaste-peer-ready\n"))
		if _, err := io.ReadFull(readiness, line); err != nil {
			t.Fatal(err)
		}
		if got := string(line); got != "mcpaste-peer-ready\n" {
			t.Fatalf("readiness = %q", got)
		}
		if err := writer.Close(); err != nil {
			cancel()
			_ = reader.Close()
			t.Fatal(err)
		}
		select {
		case err := <-done:
			cancel()
			_ = reader.Close()
			if err != nil && err.Error() == "peer runtime port unavailable" {
				continue
			}
			if err != nil {
				t.Fatalf("runPeer() error = %v", err)
			}
			return
		case <-time.After(5 * time.Second):
			cancel()
			_ = reader.Close()
			select {
			case err := <-done:
				t.Fatalf("runPeer() timed out, cleanup result = %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("runPeer() did not stop after timeout cleanup")
			}
		}
	}
	t.Fatal("runPeer() could not acquire an ephemeral test port after 3 attempts")
}

func TestRunPeerExposesOnlyPlannedFlags(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	for _, args := range [][]string{
		{"--endpoint", "https://example.test"},
		{"--codex-config", "/tmp/codex.toml"},
		{"extra"},
	} {
		if err := runPeer(context.Background(), args, reader, io.Discard); err == nil {
			t.Fatalf("runPeer(%q) unexpectedly succeeded", args)
		}
	}
}

func freePeerPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
