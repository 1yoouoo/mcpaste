package main

import (
	"io"
	"net"
	"os"
	"strings"
	"testing"
)

func TestRunDoesNotLogListeningWhenAddressCannotBind(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	defer listener.Close()

	t.Setenv("MCPASTE_HTTP_ADDR", listener.Addr().String())

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = writeEnd
	t.Cleanup(func() {
		os.Stdout = originalStdout
	})

	runErr := run()
	if err := writeEnd.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	output, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := readEnd.Close(); err != nil {
		t.Fatalf("close stdout reader: %v", err)
	}

	if runErr == nil {
		t.Fatal("run() succeeded with an occupied address")
	}
	if strings.Contains(string(output), "server listening") {
		t.Fatal("run() logged listening before binding the address")
	}
}
