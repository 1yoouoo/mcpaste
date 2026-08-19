package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRunPollsImmediatelyAndStopsOnStdinEOF(t *testing.T) {
	reader, writer := runtimePipe(t)
	options, polled := validRuntimeOptions(t, reader)
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), options) }()

	waitForRuntimeSignal(t, polled, time.Second, "immediate poll")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waitForRuntimeResult(t, done, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	assertPortReusable(t, options.Port)
}

func TestRunCancellationStopsBlockedStdinPipe(t *testing.T) {
	reader, _ := runtimePipe(t)
	options, polled := validRuntimeOptions(t, reader)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()

	waitForRuntimeSignal(t, polled, time.Second, "immediate poll")
	cancel()
	if err := waitForRuntimeResult(t, done, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	assertPortReusable(t, options.Port)
}

func TestRunPollsEveryThreeSeconds(t *testing.T) {
	reader, writer := runtimePipe(t)
	options, polled := validRuntimeOptions(t, reader)
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), options) }()

	waitForRuntimeSignal(t, polled, time.Second, "immediate poll")
	started := time.Now()
	waitForRuntimeSignal(t, polled, 3500*time.Millisecond, "second poll")
	elapsed := time.Since(started)
	if elapsed < 2900*time.Millisecond || elapsed > 3400*time.Millisecond {
		t.Fatalf("poll interval = %v", elapsed)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waitForRuntimeResult(t, done, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRunBindFailureReturnsStableError(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	reader, _ := runtimePipe(t)
	options, _ := validRuntimeOptionsForPort(t, reader, port)
	var readiness bytes.Buffer
	options.Readiness = &readiness

	err = Run(context.Background(), options)
	if err == nil || err.Error() != "peer runtime port unavailable" {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(err.Error(), strconv.Itoa(port)) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("bind error leaked address details: %v", err)
	}
	if readiness.Len() != 0 {
		t.Fatalf("readiness emitted before successful listen: %q", readiness.String())
	}
}

func TestRunEmitsExactReadinessAfterListenerOwnership(t *testing.T) {
	stdin, stdinWriter := runtimePipe(t)
	readiness, readinessWriter := runtimePipe(t)
	options, _ := validRuntimeOptions(t, stdin)
	options.Readiness = readinessWriter
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), options) }()

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
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waitForRuntimeResult(t, done, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsInvalidOptionsAndCredentials(t *testing.T) {
	reader, _ := runtimePipe(t)
	valid, _ := validRuntimeOptions(t, reader)
	tests := []struct {
		name   string
		mutate func(*RuntimeOptions)
	}{
		{name: "device ID", mutate: func(options *RuntimeOptions) { options.DeviceID = "not-a-device-id" }},
		{name: "display name", mutate: func(options *RuntimeOptions) { options.DisplayName = "" }},
		{name: "port", mutate: func(options *RuntimeOptions) { options.Port = 0 }},
		{name: "credential path", mutate: func(options *RuntimeOptions) { options.CredentialPath = "" }},
		{name: "registry path", mutate: func(options *RuntimeOptions) { options.RegistryPath = "" }},
		{name: "stdin", mutate: func(options *RuntimeOptions) { options.Stdin = nil }},
		{name: "status source", mutate: func(options *RuntimeOptions) { options.Tailscale = nil }},
		{name: "clock", mutate: func(options *RuntimeOptions) { options.Now = nil }},
		{name: "readiness", mutate: func(options *RuntimeOptions) { options.Readiness = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.mutate(&options)
			if err := Run(context.Background(), options); err == nil {
				t.Fatal("Run() succeeded")
			}
		})
	}

	credentialTests := []struct {
		name       string
		credential any
	}{
		{name: "remote endpoint", credential: map[string]string{"endpoint": "https://example.invalid", "token": "test-token"}},
		{name: "wrong port", credential: map[string]string{"endpoint": "http://127.0.0.1:1", "token": "test-token"}},
		{name: "endpoint path", credential: map[string]string{"endpoint": runtimeEndpoint(valid.Port) + "/v1/local/context", "token": "test-token"}},
		{name: "empty token", credential: map[string]string{"endpoint": runtimeEndpoint(valid.Port), "token": ""}},
		{name: "unknown field", credential: map[string]string{"endpoint": runtimeEndpoint(valid.Port), "token": "test-token", "extra": "value"}},
	}
	for _, test := range credentialTests {
		t.Run(test.name, func(t *testing.T) {
			reader, _ := runtimePipe(t)
			options, _ := validRuntimeOptions(t, reader)
			writeRuntimeCredentialValue(t, options.CredentialPath, test.credential)
			if err := Run(context.Background(), options); err == nil {
				t.Fatal("Run() succeeded")
			}
		})
	}
}

func TestRunRejectsOversizedCredentialWithoutReadingPastBound(t *testing.T) {
	reader, _ := runtimePipe(t)
	options, _ := validRuntimeOptions(t, reader)
	data := []byte(`{"endpoint":"` + runtimeEndpoint(options.Port) + `","token":"` + strings.Repeat("x", 20<<10) + `"}`)
	if err := os.WriteFile(options.CredentialPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), options); err == nil {
		t.Fatal("Run() accepted oversized credential")
	}
}

func TestRunRejectsCredentialWithGroupOrOtherPermissions(t *testing.T) {
	reader, _ := runtimePipe(t)
	options, _ := validRuntimeOptions(t, reader)
	if err := os.Chmod(options.CredentialPath, 0o644); err != nil {
		t.Fatal(err)
	}

	err := Run(context.Background(), options)
	if !errors.Is(err, errInvalidCredential) {
		t.Fatalf("Run() error = %v, want %v", err, errInvalidCredential)
	}
	if strings.Contains(err.Error(), options.CredentialPath) || strings.Contains(err.Error(), "runtime-test-token") {
		t.Fatalf("credential error leaked sensitive content: %v", err)
	}
}

func TestRunRejectsCredentialSymlink(t *testing.T) {
	reader, _ := runtimePipe(t)
	options, _ := validRuntimeOptions(t, reader)
	symlinkPath := filepath.Join(filepath.Dir(options.CredentialPath), "credential-link.json")
	if err := os.Symlink(options.CredentialPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	options.CredentialPath = symlinkPath

	if err := Run(context.Background(), options); !errors.Is(err, errInvalidCredential) {
		t.Fatalf("Run() error = %v, want %v", err, errInvalidCredential)
	}
}

func TestRunRejectsCredentialFIFOWithoutBlocking(t *testing.T) {
	reader, _ := runtimePipe(t)
	options, _ := validRuntimeOptions(t, reader)
	if err := os.Remove(options.CredentialPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(options.CredentialPath, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), options) }()

	err := waitForRuntimeResult(t, done, time.Second)
	if !errors.Is(err, errInvalidCredential) {
		t.Fatalf("Run() error = %v, want %v", err, errInvalidCredential)
	}
}

func TestRunRejectsTrailingCredentialData(t *testing.T) {
	reader, _ := runtimePipe(t)
	options, _ := validRuntimeOptions(t, reader)
	data := []byte(`{"endpoint":"` + runtimeEndpoint(options.Port) + `","token":"runtime-test-token"}{}`)
	if err := os.WriteFile(options.CredentialPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), options); !errors.Is(err, errInvalidCredential) {
		t.Fatalf("Run() error = %v, want %v", err, errInvalidCredential)
	}
}

func validRuntimeOptions(t *testing.T, stdin io.Reader) (RuntimeOptions, <-chan struct{}) {
	t.Helper()
	return validRuntimeOptionsForPort(t, stdin, unusedLocalPort(t))
}

func validRuntimeOptionsForPort(t *testing.T, stdin io.Reader, port int) (RuntimeOptions, <-chan struct{}) {
	t.Helper()
	directory := t.TempDir()
	credentialPath := filepath.Join(directory, "credential.json")
	writeRuntimeCredentialValue(t, credentialPath, map[string]string{
		"endpoint": runtimeEndpoint(port),
		"token":    "runtime-test-token",
	})
	polled := make(chan struct{}, 8)
	return RuntimeOptions{
		DeviceID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		DisplayName:    "Runtime Test Device",
		Port:           port,
		CredentialPath: credentialPath,
		RegistryPath:   filepath.Join(directory, "peers.json"),
		Stdin:          stdin,
		Tailscale: testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
			polled <- struct{}{}
			return nil, nil
		}),
		Now:       func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) },
		Readiness: io.Discard,
	}, polled
}

func runtimePipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	return reader, writer
}

func writeRuntimeCredentialValue(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runtimeEndpoint(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

func unusedLocalPort(t *testing.T) int {
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

func assertPortReusable(t *testing.T, port int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("runtime port was not released: %v", err)
	}
	_ = listener.Close()
}

func waitForRuntimeSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, name string) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForRuntimeResult(t *testing.T, result <-chan error, timeout time.Duration) error {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		t.Fatal("runtime did not stop")
		return nil
	}
}

type stubbornReadCloser struct {
	startOnce   sync.Once
	releaseOnce sync.Once
	started     chan struct{}
	release     chan struct{}
}

func newStubbornReadCloser() *stubbornReadCloser {
	return &stubbornReadCloser{started: make(chan struct{}), release: make(chan struct{})}
}

func (reader *stubbornReadCloser) Read([]byte) (int, error) {
	reader.startOnce.Do(func() { close(reader.started) })
	<-reader.release
	return 0, io.EOF
}

func (*stubbornReadCloser) Close() error { return nil }

func (reader *stubbornReadCloser) unblock() {
	reader.releaseOnce.Do(func() { close(reader.release) })
}

var _ io.ReadCloser = (*stubbornReadCloser)(nil)

type blockingNonCloser struct {
	startOnce sync.Once
	started   chan struct{}
	release   chan struct{}
}

func newBlockingNonCloser() *blockingNonCloser {
	return &blockingNonCloser{started: make(chan struct{}), release: make(chan struct{})}
}

func (reader *blockingNonCloser) Read([]byte) (int, error) {
	reader.startOnce.Do(func() { close(reader.started) })
	<-reader.release
	return 0, io.EOF
}

func TestRunRejectsBlockingNonCloserBeforeRead(t *testing.T) {
	reader := newBlockingNonCloser()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer release()
	options, _ := validRuntimeOptions(t, reader)
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), options) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run() accepted a non-closer stdin")
		}
		select {
		case <-reader.started:
			t.Fatal("non-closer reader was invoked before rejection")
		default:
		}
	case <-reader.started:
		release()
		_ = waitForRuntimeResult(t, done, 2*time.Second)
		t.Fatal("non-closer reader was invoked")
	case <-time.After(time.Second):
		release()
		_ = waitForRuntimeResult(t, done, 2*time.Second)
		t.Fatal("Run() did not reject the non-closer")
	}
}

func TestRunRejectsBlockingCustomReadCloserBeforeRead(t *testing.T) {
	reader := newStubbornReadCloser()
	defer reader.unblock()
	options, _ := validRuntimeOptions(t, reader)
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), options) }()

	select {
	case err := <-done:
		if !errors.Is(err, errInvalidRuntimeOptions) {
			t.Fatalf("Run() error = %v, want %v", err, errInvalidRuntimeOptions)
		}
		select {
		case <-reader.started:
			t.Fatal("custom reader was invoked before rejection")
		default:
		}
	case <-reader.started:
		reader.unblock()
		_ = waitForRuntimeResult(t, done, 2*time.Second)
		t.Fatal("custom reader was invoked")
	case <-time.After(time.Second):
		reader.unblock()
		_ = waitForRuntimeResult(t, done, 2*time.Second)
		t.Fatal("Run() did not reject the custom reader")
	}
}

func TestRunRejectsNonPollableStdinFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular-stdin")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	options, _ := validRuntimeOptions(t, stdin)

	if err := Run(context.Background(), options); !errors.Is(err, errInvalidRuntimeOptions) {
		t.Fatalf("Run() error = %v, want %v", err, errInvalidRuntimeOptions)
	}
}

func TestRunRejectsTypedNilStdinFile(t *testing.T) {
	reader, _ := runtimePipe(t)
	options, _ := validRuntimeOptions(t, reader)
	var stdin *os.File
	options.Stdin = stdin

	if err := Run(context.Background(), options); !errors.Is(err, errInvalidRuntimeOptions) {
		t.Fatalf("Run() error = %v, want %v", err, errInvalidRuntimeOptions)
	}
}

func TestShutdownRuntimeReportsStdinWatcherTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stdin, _ := runtimePipe(t)
	serveDone := make(chan error)
	close(serveDone)

	err = shutdownRuntime(&http.Server{}, listener, stdin, serveDone, make(chan struct{}), nil)
	if err == nil || err.Error() != "peer runtime shutdown failed" {
		t.Fatalf("shutdownRuntime() error = %v", err)
	}
}

func TestRunClosesOutboundIdleConnectionsOnShutdown(t *testing.T) {
	idle := make(chan struct{}, 1)
	peerServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(healthResponse{
			ProtocolVersion: ProtocolVersion,
			DeviceID:        "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			DisplayName:     "Idle Connection Peer",
		})
	}))
	peerServer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateIdle {
			select {
			case idle <- struct{}{}:
			default:
			}
		}
	}
	peerServer.Start()
	defer peerServer.Close()

	connectionClosed := make(chan struct{})
	originalTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		connection, err := (&net.Dialer{}).DialContext(ctx, network, peerServer.Listener.Addr().String())
		if err != nil {
			return nil, err
		}
		return &closeSignalConn{Conn: connection, closed: connectionClosed}, nil
	}}
	defer func() { http.DefaultTransport = originalTransport }()

	stdin, writer := runtimePipe(t)
	options, _ := validRuntimeOptions(t, stdin)
	secondPoll := make(chan struct{})
	statusCalls := 0
	options.Tailscale = testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		statusCalls++
		if statusCalls == 1 {
			return []TailnetCandidate{{Addresses: []string{"100.64.0.2"}}}, nil
		}
		if statusCalls == 2 {
			close(secondPoll)
		}
		return nil, nil
	})
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), options) }()

	waitForRuntimeSignal(t, idle, time.Second, "outbound connection to become idle")
	waitForRuntimeSignal(t, secondPoll, 3500*time.Millisecond, "second status poll")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waitForRuntimeResult(t, done, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeSignal(t, connectionClosed, time.Second, "outbound idle connection close")
}

type closeSignalConn struct {
	net.Conn
	closeOnce sync.Once
	closed    chan struct{}
}

func (connection *closeSignalConn) Close() error {
	connection.closeOnce.Do(func() { close(connection.closed) })
	return connection.Conn.Close()
}
