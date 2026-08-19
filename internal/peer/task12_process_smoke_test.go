package peer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	task12PeerHelperEnvironment = "MCPASTE_TASK12_PEER_HELPER"
	task12PeerSourceHeader      = "X-MCPaste-Task12-Peer-Source"
	task12PeerReadyLine         = "mcpaste-task12-peer-ready\n"
	task12PollInterval          = 100 * time.Millisecond
	task12BuildTimeout          = 60 * time.Second
	task12ReadinessTimeout      = 3 * time.Second
	task12ShutdownTimeout       = 2 * time.Second
	task12ReapTimeout           = 2 * time.Second
)

var (
	task12DeviceA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	task12DeviceB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func TestTask12ProcessSmoke(t *testing.T) {
	runTask12ProcessSmoke(t)
}

func TestTask12ReservedListenerOwnsPortUntilClosed(t *testing.T) {
	reservation := task12ReserveLoopbackListener(t)
	address := reservation.listener.Addr().String()

	contender, err := net.Listen("tcp", address)
	if err == nil {
		_ = contender.Close()
		t.Fatalf("second listener acquired reserved address %s", address)
	}
	if err := reservation.close(); err != nil {
		t.Fatal(err)
	}

	replacement, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listen after releasing reservation %s: %v", address, err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTask12KillAndReapIsBounded(t *testing.T) {
	done := make(chan error)
	killed := make(chan struct{}, 1)
	start := time.Now()
	result := task12KillAndReap(done, func() error {
		killed <- struct{}{}
		return nil
	}, 20*time.Millisecond)

	if !result.timedOut {
		t.Fatalf("kill/reap result = %#v, want bounded reap timeout", result)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("kill/reap took %s, want bounded return", elapsed)
	}
	select {
	case <-killed:
	default:
		t.Fatal("kill/reap did not invoke kill")
	}
}

func TestTask12PeerProcessHelper(t *testing.T) {
	if os.Getenv(task12PeerHelperEnvironment) != "1" {
		return
	}
	config, err := task12PeerConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := task12InheritedListener(config.port)
	if err != nil {
		t.Fatal(err)
	}
	if err := runTask12PeerProcess(t.Context(), config, listener, os.Stdin, os.Stdout); err != nil {
		t.Fatal(err)
	}
}

type task12PeerConfig struct {
	deviceID       string
	displayName    string
	address        string
	port           int
	credentialPath string
	registryPath   string
	peerName       string
	peerAddress    string
	peerPort       int
}

type task12ChildProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	done    chan error
	stderr  *bytes.Buffer
	stopped bool
}

type task12ListenerReservation struct {
	listener *net.TCPListener
	closed   bool
}

type task12KillAndReapResult struct {
	exitErr  error
	killErr  error
	timedOut bool
}

type task12FixtureStatus struct {
	name    string
	address string
}

func (source task12FixtureStatus) Status(context.Context) ([]TailnetCandidate, error) {
	return []TailnetCandidate{{Name: source.name, Addresses: []string{source.address}}}, nil
}

type task12PeerTransport struct {
	targetAddress string
	targetPort    int
	sourceAddress string
	base          *http.Transport
}

func (transport *task12PeerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Scheme != "http" || request.URL.Hostname() != transport.targetAddress {
		return nil, errors.New("invalid task12 peer request")
	}
	clone := request.Clone(request.Context())
	cloneURL := *request.URL
	clone.URL = &cloneURL
	clone.URL.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(transport.targetPort))
	clone.Header.Set(task12PeerSourceHeader, transport.sourceAddress)
	return transport.base.RoundTrip(clone)
}

func (transport *task12PeerTransport) CloseIdleConnections() {
	transport.base.CloseIdleConnections()
}

type task12PeerRemoteHandler struct {
	peerAddress string
	next        http.Handler
}

func (handler task12PeerRemoteHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Header.Get(task12PeerSourceHeader) == handler.peerAddress {
		clone := request.Clone(request.Context())
		clone.Header.Del(task12PeerSourceHeader)
		clone.RemoteAddr = net.JoinHostPort(handler.peerAddress, "4242")
		handler.next.ServeHTTP(response, clone)
		return
	}
	handler.next.ServeHTTP(response, request)
}

func runTask12ProcessSmoke(t *testing.T) {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "mcpaste")
	buildContext, cancelBuild := context.WithTimeout(t.Context(), task12BuildTimeout)
	build := exec.CommandContext(buildContext, "go", "build", "-o", binary, "./cmd/mcpaste")
	build.Dir = root
	build.WaitDelay = task12ReapTimeout
	output, buildErr := build.CombinedOutput()
	cancelBuild()
	if buildErr != nil {
		t.Fatalf("build current mcpaste binary: %v (context: %v): %s", buildErr, buildContext.Err(), output)
	}

	listenerA := task12ReserveLoopbackListener(t)
	t.Cleanup(func() { _ = listenerA.close() })
	listenerB := task12ReserveLoopbackListener(t)
	t.Cleanup(func() { _ = listenerB.close() })
	portA := listenerA.listener.Addr().(*net.TCPAddr).Port
	portB := listenerB.listener.Addr().(*net.TCPAddr).Port
	if portA == portB {
		t.Fatal("task12 peers did not receive distinct loopback ports")
	}

	peerADir := filepath.Join(temporary, "peer-a")
	peerBDir := filepath.Join(temporary, "peer-b")
	credentialA := filepath.Join(peerADir, "credential.json")
	credentialB := filepath.Join(peerBDir, "credential.json")
	registryA := filepath.Join(peerADir, "registry.json")
	registryB := filepath.Join(peerBDir, "registry.json")
	tokenA := "task12-fixture-token-a"
	tokenB := "task12-fixture-token-b"
	task12WriteCredential(t, peerADir, credentialA, portA, tokenA)
	task12WriteCredential(t, peerBDir, credentialB, portB, tokenB)

	peerA := task12StartPeer(t, task12PeerConfig{
		deviceID:       task12DeviceA,
		displayName:    "Task 12 Peer A",
		address:        "100.64.12.1",
		port:           portA,
		credentialPath: credentialA,
		registryPath:   registryA,
		peerName:       "Task 12 Peer B",
		peerAddress:    "100.64.12.2",
		peerPort:       portB,
	}, listenerA)
	t.Cleanup(func() { peerA.cleanup(t) })
	peerB := task12StartPeer(t, task12PeerConfig{
		deviceID:       task12DeviceB,
		displayName:    "Task 12 Peer B",
		address:        "100.64.12.2",
		port:           portB,
		credentialPath: credentialB,
		registryPath:   registryB,
		peerName:       "Task 12 Peer A",
		peerAddress:    "100.64.12.1",
		peerPort:       portA,
	}, listenerB)
	t.Cleanup(func() { peerB.cleanup(t) })

	textA := "alpha\r\nbeta trailing  \nfinal line \t  "
	pngData := task12PNG(t)
	jpegData := task12JPEG(t)
	pngDigest := task12StageAsset(t, portA, tokenA, "image/png", 2, 1, pngData)
	jpegDigest := task12StageAsset(t, portA, tokenA, "image/jpeg", 1, 2, jpegData)
	revisionA := task12Publish(t, portA, tokenA, textA, []string{pngDigest, jpegDigest}, nil)

	var contextB LocalContextResponse
	task12WaitFor(t, 8*time.Second, "peer B convergence", func() error {
		current, err := task12ReadLocalContext(portB, tokenB)
		if err != nil {
			return err
		}
		if current.Revision != revisionA || current.SourceDeviceID != task12DeviceA || !current.SourceReachable || current.Text != textA {
			return fmt.Errorf("peer B context has not converged")
		}
		if len(current.Assets) != 2 || current.Assets[0].Digest != pngDigest || current.Assets[1].Digest != jpegDigest {
			return fmt.Errorf("peer B assets have not converged in order")
		}
		contextB = current
		return nil
	})
	if got := task12LocalRequest(t, http.MethodGet, portB, tokenB, localContextAssetsBase+"0", nil, nil, http.StatusOK); !bytes.Equal(got, pngData) {
		t.Fatalf("peer B local PNG bytes differ after convergence")
	}
	if got := task12LocalRequest(t, http.MethodGet, portB, tokenB, localContextAssetsBase+"1", nil, nil, http.StatusOK); !bytes.Equal(got, jpegData) {
		t.Fatalf("peer B local JPEG bytes differ after convergence")
	}

	resultA := task12CallConnector(t, binary, credentialB)
	task12AssertAvailableResult(t, resultA, textA, task12DeviceA, []task12ExpectedImage{
		{mimeType: "image/png", data: pngData},
		{mimeType: "image/jpeg", data: jpegData},
	})

	exitA := peerA.stop(t)
	if exitA > 2*time.Second {
		t.Fatalf("peer A exit took %s, want <= 2s", exitA)
	}
	task12WaitFor(t, 5*time.Second, "peer B source-offline state", func() error {
		current, err := task12ReadLocalContext(portB, tokenB)
		if err != nil {
			return err
		}
		if current.SourceReachable || current.SyncState != SyncSourceOffline || current.Text != textA {
			return fmt.Errorf("peer B has not retained an offline-gated replica")
		}
		contextB = current
		return nil
	})

	offline := task12CallConnector(t, binary, credentialB)
	if !offline.IsError || len(offline.Content) != 1 {
		t.Fatalf("offline MCP result = %#v, want one unavailable error block", offline)
	}
	offlineText, ok := offline.Content[0].(*mcp.TextContent)
	if !ok || offlineText.Text != "MCPaste context unavailable." || strings.Contains(offlineText.Text, textA) {
		t.Fatalf("offline MCP content = %#v, want fixed unavailable result without stale text", offline.Content)
	}
	structuredOffline, ok := offline.StructuredContent.(map[string]any)
	if !ok || structuredOffline["available"] != false {
		t.Fatalf("offline structured content = %#v, want available=false", offline.StructuredContent)
	}

	textB := "replacement from B\r\nexact tail  "
	revisionB := task12Publish(t, portB, tokenB, textB, []string{jpegDigest, pngDigest}, &contextB.Revision)
	if revisionB.DeviceID != task12DeviceB || revisionB.Compare(contextB.Revision) <= 0 {
		t.Fatalf("replacement revision = %#v, want newer B revision", revisionB)
	}
	resultB := task12CallConnector(t, binary, credentialB)
	task12AssertAvailableResult(t, resultB, textB, task12DeviceB, []task12ExpectedImage{
		{mimeType: "image/jpeg", data: jpegData},
		{mimeType: "image/png", data: pngData},
	})

	exitB := peerB.stop(t)
	if exitB > 2*time.Second {
		t.Fatalf("peer B exit took %s, want <= 2s", exitB)
	}
}

func task12PeerConfigFromEnvironment() (task12PeerConfig, error) {
	port, err := strconv.Atoi(os.Getenv("MCPASTE_TASK12_PORT"))
	if err != nil {
		return task12PeerConfig{}, errors.New("invalid task12 peer port")
	}
	peerPort, err := strconv.Atoi(os.Getenv("MCPASTE_TASK12_PEER_PORT"))
	if err != nil {
		return task12PeerConfig{}, errors.New("invalid task12 target port")
	}
	config := task12PeerConfig{
		deviceID:       os.Getenv("MCPASTE_TASK12_DEVICE_ID"),
		displayName:    os.Getenv("MCPASTE_TASK12_DISPLAY_NAME"),
		address:        os.Getenv("MCPASTE_TASK12_ADDRESS"),
		port:           port,
		credentialPath: os.Getenv("MCPASTE_TASK12_CREDENTIAL"),
		registryPath:   os.Getenv("MCPASTE_TASK12_REGISTRY"),
		peerName:       os.Getenv("MCPASTE_TASK12_PEER_NAME"),
		peerAddress:    os.Getenv("MCPASTE_TASK12_PEER_ADDRESS"),
		peerPort:       peerPort,
	}
	if _, ok := normalizeDeviceID(config.deviceID); !ok || !validDisplayName(config.displayName) ||
		config.port < 1 || config.port > 65535 || config.credentialPath == "" || config.registryPath == "" ||
		config.peerName == "" || config.peerPort < 1 || config.peerPort > 65535 {
		return task12PeerConfig{}, errors.New("invalid task12 peer configuration")
	}
	for _, raw := range []string{config.address, config.peerAddress} {
		address, err := netip.ParseAddr(raw)
		if err != nil || !address.IsValid() || address.IsLoopback() || address.Zone() != "" {
			return task12PeerConfig{}, errors.New("invalid task12 peer address")
		}
	}
	return config, nil
}

func runTask12PeerProcess(ctx context.Context, config task12PeerConfig, listener net.Listener, stdin *os.File, readiness io.Writer) error {
	defer listener.Close()
	credential, err := loadRuntimeCredential(config.credentialPath, config.port)
	if err != nil {
		return err
	}
	store, err := NewStore(config.deviceID, time.Now)
	if err != nil {
		return err
	}
	registry := NewRegistry(config.registryPath)
	if err := registry.Load(); err != nil {
		return err
	}
	allowed := &AllowedPeerIPs{}
	reachable := &AllowedPeerIPs{}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	transport := &task12PeerTransport{
		targetAddress: config.peerAddress,
		targetPort:    config.peerPort,
		sourceAddress: config.address,
		base:          base,
	}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		DeviceID:       config.deviceID,
		Port:           config.port,
		Store:          store,
		Registry:       registry,
		AllowedPeers:   allowed,
		ReachablePeers: reachable,
		Tailscale:      task12FixtureStatus{name: config.peerName, address: config.peerAddress},
		Now:            time.Now,
		HTTPClient:     &http.Client{Transport: transport},
	})
	if err != nil {
		return err
	}
	defer coordinator.client.CloseIdleConnections()
	handler := NewHandler(HandlerOptions{
		Store:    store,
		Registry: registry,
		LocalDevice: KnownPeer{
			DeviceID:    config.deviceID,
			DisplayName: config.displayName,
			LastSeenAt:  time.Now().UTC(),
		},
		LocalToken:     credential.Token,
		AllowedPeers:   allowed,
		ReachablePeers: reachable,
		SyncState:      coordinator.SyncState,
		Announce:       coordinator.HandleAnnouncement,
	})
	server := NewHTTPServer(listener.Addr().String(), task12PeerRemoteHandler{peerAddress: config.peerAddress, next: handler})
	if _, err := io.WriteString(readiness, task12PeerReadyLine); err != nil {
		_ = listener.Close()
		return errRuntimeReadiness
	}
	runtimeContext, cancel := context.WithCancel(ctx)
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	stdinDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		close(stdinDone)
		cancel()
	}()

	_ = coordinator.PollOnce(runtimeContext)
	store.SweepStaged()
	ticker := time.NewTicker(task12PollInterval)
	defer ticker.Stop()
	var serveErr error
	for {
		select {
		case <-runtimeContext.Done():
			return shutdownRuntime(server, listener, stdin, serveDone, stdinDone, serveErr)
		case serveErr = <-serveDone:
			cancel()
			return shutdownRuntime(server, listener, stdin, nil, stdinDone, serveErr)
		case <-ticker.C:
			_ = coordinator.PollOnce(runtimeContext)
			store.SweepStaged()
		}
	}
}

func task12ReserveLoopbackListener(t *testing.T) *task12ListenerReservation {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	return &task12ListenerReservation{listener: listener}
}

func (reservation *task12ListenerReservation) close() error {
	if reservation == nil || reservation.closed {
		return nil
	}
	reservation.closed = true
	return reservation.listener.Close()
}

func task12InheritedListener(port int) (net.Listener, error) {
	file := os.NewFile(3, "task12-peer-listener")
	if file == nil {
		return nil, errors.New("missing task12 inherited listener")
	}
	listener, err := net.FileListener(file)
	closeErr := file.Close()
	if err != nil {
		return nil, fmt.Errorf("open task12 inherited listener: %w", err)
	}
	if closeErr != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("close task12 inherited listener file: %w", closeErr)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() || address.Port != port {
		_ = listener.Close()
		return nil, errors.New("invalid task12 inherited listener")
	}
	return listener, nil
}

func task12WriteCredential(t *testing.T, directory, path string, port int, token string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{
		"endpoint": "http://127.0.0.1:" + strconv.Itoa(port),
		"token":    token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func task12StartPeer(t *testing.T, config task12PeerConfig, reservation *task12ListenerReservation) *task12ChildProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestTask12PeerProcessHelper$")
	command.Env = append(os.Environ(),
		task12PeerHelperEnvironment+"=1",
		"MCPASTE_TASK12_DEVICE_ID="+config.deviceID,
		"MCPASTE_TASK12_DISPLAY_NAME="+config.displayName,
		"MCPASTE_TASK12_ADDRESS="+config.address,
		"MCPASTE_TASK12_PORT="+strconv.Itoa(config.port),
		"MCPASTE_TASK12_CREDENTIAL="+config.credentialPath,
		"MCPASTE_TASK12_REGISTRY="+config.registryPath,
		"MCPASTE_TASK12_PEER_NAME="+config.peerName,
		"MCPASTE_TASK12_PEER_ADDRESS="+config.peerAddress,
		"MCPASTE_TASK12_PEER_PORT="+strconv.Itoa(config.peerPort),
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	inheritedListener, err := reservation.listener.File()
	if err != nil {
		t.Fatal(err)
	}
	command.ExtraFiles = []*os.File{inheritedListener}
	if err := command.Start(); err != nil {
		_ = inheritedListener.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if err := errors.Join(inheritedListener.Close(), reservation.close()); err != nil {
		result := task12KillAndReap(done, command.Process.Kill, task12ReapTimeout)
		if result.timedOut {
			t.Fatalf("release task12 listener reservation: %v; kill=%v; reap timed out", err, result.killErr)
		}
		t.Fatalf("release task12 listener reservation: %v; kill=%v; exit=%v; stderr=%q", err, result.killErr, result.exitErr, stderr.String())
	}
	ready := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		ready <- struct {
			line string
			err  error
		}{line: line, err: readErr}
	}()
	select {
	case result := <-ready:
		if result.err != nil || result.line != task12PeerReadyLine {
			reap := task12KillAndReap(done, command.Process.Kill, task12ReapTimeout)
			if reap.timedOut {
				t.Fatalf("task12 peer readiness = %q/%v; kill=%v; reap timed out", result.line, result.err, reap.killErr)
			}
			t.Fatalf("task12 peer readiness = %q/%v, kill=%v, exit=%v, stderr=%q", result.line, result.err, reap.killErr, reap.exitErr, stderr.String())
		}
	case exitErr := <-done:
		t.Fatalf("task12 peer exited before readiness: %v, stderr=%q", exitErr, stderr.String())
	case <-time.After(task12ReadinessTimeout):
		reap := task12KillAndReap(done, command.Process.Kill, task12ReapTimeout)
		if reap.timedOut {
			t.Fatalf("timed out waiting for task12 peer readiness; kill=%v; reap timed out", reap.killErr)
		}
		t.Fatalf("timed out waiting for task12 peer readiness, kill=%v, exit=%v, stderr=%q", reap.killErr, reap.exitErr, stderr.String())
	}
	return &task12ChildProcess{command: command, stdin: stdin, done: done, stderr: stderr}
}

func task12KillAndReap(done <-chan error, kill func() error, timeout time.Duration) task12KillAndReapResult {
	result := task12KillAndReapResult{killErr: kill()}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result.exitErr = <-done:
		return result
	case <-timer.C:
		result.timedOut = true
		return result
	}
}

func (child *task12ChildProcess) stop(t *testing.T) time.Duration {
	t.Helper()
	if child.stopped {
		return 0
	}
	start := time.Now()
	if err := child.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-child.done:
		child.stopped = true
		if err != nil {
			t.Fatalf("task12 peer exit: %v, stderr=%q", err, child.stderr.String())
		}
	case <-time.After(task12ShutdownTimeout):
		result := task12KillAndReap(child.done, child.command.Process.Kill, task12ReapTimeout)
		child.stopped = true
		if result.timedOut {
			t.Fatalf("task12 peer did not exit within 2s; kill=%v; reap timed out", result.killErr)
		}
		t.Fatalf("task12 peer did not exit within 2s, kill=%v, exit=%v, stderr=%q", result.killErr, result.exitErr, child.stderr.String())
	}
	elapsed := time.Since(start)
	if child.command.ProcessState == nil || !child.command.ProcessState.Exited() {
		t.Fatal("task12 peer process was not reaped")
	}
	if err := child.command.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("task12 peer process still signalable after reap: %v", err)
	}
	return elapsed
}

func (child *task12ChildProcess) cleanup(t *testing.T) {
	if child == nil || child.stopped {
		return
	}
	_ = child.stdin.Close()
	select {
	case <-child.done:
	case <-time.After(task12ShutdownTimeout):
		result := task12KillAndReap(child.done, child.command.Process.Kill, task12ReapTimeout)
		if result.timedOut {
			t.Errorf("cleanup task12 peer: kill=%v; reap timed out", result.killErr)
		} else if result.killErr != nil && !errors.Is(result.killErr, os.ErrProcessDone) {
			t.Errorf("cleanup task12 peer: kill=%v, exit=%v", result.killErr, result.exitErr)
		}
	}
	child.stopped = true
}

func task12PNG(t *testing.T) []byte {
	t.Helper()
	fixture := image.NewRGBA(image.Rect(0, 0, 2, 1))
	fixture.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	fixture.Set(1, 0, color.RGBA{B: 0xff, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, fixture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func task12JPEG(t *testing.T) []byte {
	t.Helper()
	fixture := image.NewRGBA(image.Rect(0, 0, 1, 2))
	fixture.Set(0, 0, color.RGBA{G: 0xff, A: 0xff})
	fixture.Set(0, 1, color.RGBA{R: 0xff, G: 0xff, A: 0xff})
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, fixture, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func task12StageAsset(t *testing.T, port int, token, mimeType string, width, height int, data []byte) string {
	t.Helper()
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	headers := map[string]string{
		"Content-Type":     mimeType,
		"X-MCPaste-Width":  strconv.Itoa(width),
		"X-MCPaste-Height": strconv.Itoa(height),
	}
	task12LocalRequest(t, http.MethodPut, port, token, "/v1/local/assets/"+digest, data, headers, http.StatusNoContent)
	return digest
}

func task12Publish(t *testing.T, port int, token, text string, digests []string, expected *Revision) Revision {
	t.Helper()
	payload, err := json.Marshal(struct {
		Text             string    `json:"text"`
		AssetDigests     []string  `json:"asset_digests"`
		ExpectedRevision *Revision `json:"expected_revision"`
	}{Text: text, AssetDigests: digests, ExpectedRevision: expected})
	if err != nil {
		t.Fatal(err)
	}
	body := task12LocalRequest(t, http.MethodPut, port, token, localContextRoute, payload,
		map[string]string{"Content-Type": "application/json"}, http.StatusOK)
	var result PublicationResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result.Revision
}

func task12ReadLocalContext(port int, token string) (LocalContextResponse, error) {
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+localContextRoute, nil)
	if err != nil {
		return LocalContextResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return LocalContextResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return LocalContextResponse{}, fmt.Errorf("local context status %d", response.StatusCode)
	}
	var current LocalContextResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, MaxContextJSONBytes+1))
	if err := decoder.Decode(&current); err != nil {
		return LocalContextResponse{}, err
	}
	return current, nil
}

func task12LocalRequest(t *testing.T, method string, port int, token, path string, body []byte, headers map[string]string, wantStatus int) []byte {
	t.Helper()
	request, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(port)+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, MaxContextJSONBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status/body = %d/%q, want %d", method, path, response.StatusCode, responseBody, wantStatus)
	}
	return responseBody
}

func task12WaitFor(t *testing.T, timeout time.Duration, description string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if err := check(); err == nil {
			return
		} else {
			last = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %v", description, last)
}

type task12ExpectedImage struct {
	mimeType string
	data     []byte
}

func task12CallConnector(t *testing.T, binary, credentialPath string) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.Command(binary, "--credential-file", credentialPath)
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "task12-smoke-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: time.Second}, nil)
	if err != nil {
		t.Fatalf("connect to built STDIO mcpaste: %v, stderr=%q", err, stderr.String())
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_latest_paste", Arguments: map[string]any{}})
	if err != nil {
		_ = session.Close()
		t.Fatalf("call built STDIO mcpaste: %v, stderr=%q", err, stderr.String())
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close built STDIO mcpaste: %v, stderr=%q", err, stderr.String())
	}
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		t.Fatal("built STDIO mcpaste process was not reaped")
	}
	if err := command.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("built STDIO mcpaste remains signalable after close: %v", err)
	}
	return result
}

func task12AssertAvailableResult(t *testing.T, result *mcp.CallToolResult, text, sourceDeviceID string, images []task12ExpectedImage) {
	t.Helper()
	if result == nil || result.IsError || len(result.Content) != len(images)+1 {
		var connectorText string
		if result != nil && len(result.Content) > 0 {
			if content, ok := result.Content[0].(*mcp.TextContent); ok {
				connectorText = content.Text
			}
		}
		t.Fatalf("MCP result = %#v (connector text %q), want available text plus %d images", result, connectorText, len(images))
	}
	textContent, ok := result.Content[0].(*mcp.TextContent)
	if !ok || textContent.Text != text {
		t.Fatalf("MCP text = %#v, want exact %q", result.Content[0], text)
	}
	for index, expected := range images {
		imageContent, ok := result.Content[index+1].(*mcp.ImageContent)
		if !ok || imageContent.MIMEType != expected.mimeType || !bytes.Equal(imageContent.Data, expected.data) {
			t.Fatalf("MCP image %d = %#v, want exact %s bytes", index, result.Content[index+1], expected.mimeType)
		}
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["available"] != true || structured["source_device_id"] != sourceDeviceID {
		t.Fatalf("MCP structured content = %#v, want available source %s", result.StructuredContent, sourceDeviceID)
	}
}
