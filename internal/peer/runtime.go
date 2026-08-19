package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const (
	runtimePollInterval      = 3 * time.Second
	runtimeShutdownTimeout   = 2 * time.Second
	runtimeReadinessLine     = "mcpaste-peer-ready\n"
	maxRuntimeCredentialSize = 16 << 10
	maxRuntimeTokenSize      = 4 << 10
)

var (
	errInvalidRuntimeOptions  = errors.New("invalid peer runtime options")
	errInvalidCredential      = errors.New("invalid peer runtime credential")
	errRuntimePortUnavailable = errors.New("peer runtime port unavailable")
	errRuntimeReadiness       = errors.New("peer runtime readiness failed")
	errRuntimeServer          = errors.New("peer runtime server failed")
	errRuntimeShutdown        = errors.New("peer runtime shutdown failed")
)

type RuntimeOptions struct {
	DeviceID       string
	DisplayName    string
	Port           int
	CredentialPath string
	RegistryPath   string
	Stdin          io.Reader
	Readiness      io.Writer
	Tailscale      StatusSource
	Now            func() time.Time
}

type runtimeCredential struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

func Run(ctx context.Context, options RuntimeOptions) error {
	deviceID, err := validateRuntimeOptions(ctx, options)
	if err != nil {
		return err
	}
	stdin := options.Stdin.(*os.File)
	credential, err := loadRuntimeCredential(options.CredentialPath, options.Port)
	if err != nil {
		return err
	}
	store, err := NewStore(deviceID, options.Now)
	if err != nil {
		return errInvalidRuntimeOptions
	}
	registry := NewRegistry(options.RegistryPath)
	if err := registry.Load(); err != nil {
		return err
	}
	allowedPeers := &AllowedPeerIPs{}
	reachablePeers := &AllowedPeerIPs{}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		DeviceID:       deviceID,
		Port:           options.Port,
		Store:          store,
		Registry:       registry,
		AllowedPeers:   allowedPeers,
		ReachablePeers: reachablePeers,
		Tailscale:      options.Tailscale,
		Now:            options.Now,
	})
	if err != nil {
		return errInvalidRuntimeOptions
	}
	defer coordinator.client.CloseIdleConnections()
	handler := NewHandler(HandlerOptions{
		Store:    store,
		Registry: registry,
		LocalDevice: KnownPeer{
			DeviceID:    deviceID,
			DisplayName: options.DisplayName,
			LastSeenAt:  options.Now().UTC(),
		},
		LocalToken:     credential.Token,
		AllowedPeers:   allowedPeers,
		ReachablePeers: reachablePeers,
		SyncState:      coordinator.SyncState,
		Announce:       coordinator.HandleAnnouncement,
	})
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(options.Port))
	if err != nil {
		return errRuntimePortUnavailable
	}
	written, err := io.WriteString(options.Readiness, runtimeReadinessLine)
	if err != nil || written != len(runtimeReadinessLine) {
		_ = listener.Close()
		return errRuntimeReadiness
	}
	server := NewHTTPServer(listener.Addr().String(), handler)
	runtimeContext, cancel := context.WithCancel(ctx)
	defer cancel()

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	stdinDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		close(stdinDone)
		cancel()
	}()

	_ = coordinator.PollOnce(runtimeContext)
	store.SweepStaged()
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()

	var serveErr error
	for {
		select {
		case <-runtimeContext.Done():
			return shutdownRuntime(server, listener, stdin, serveDone, stdinDone, serveErr)
		case err := <-serveDone:
			serveErr = err
			cancel()
			return shutdownRuntime(server, listener, stdin, nil, stdinDone, serveErr)
		case <-ticker.C:
			_ = coordinator.PollOnce(runtimeContext)
			store.SweepStaged()
		}
	}
}

func validateRuntimeOptions(ctx context.Context, options RuntimeOptions) (string, error) {
	deviceID, ok := normalizeDeviceID(options.DeviceID)
	stdin, stdinFile := options.Stdin.(*os.File)
	if ctx == nil || !ok || !validDisplayName(options.DisplayName) || options.Port < 1 || options.Port > 65535 ||
		options.CredentialPath == "" || options.RegistryPath == "" || !stdinFile || options.Readiness == nil || options.Tailscale == nil || options.Now == nil {
		return "", errInvalidRuntimeOptions
	}
	if err := stdin.SetReadDeadline(time.Time{}); err != nil {
		return "", errInvalidRuntimeOptions
	}
	return deviceID, nil
}

func loadRuntimeCredential(path string, port int) (runtimeCredential, error) {
	file, err := openRuntimeCredential(path)
	if err != nil {
		return runtimeCredential{}, errInvalidCredential
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxRuntimeCredentialSize+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > maxRuntimeCredentialSize {
		return runtimeCredential{}, errInvalidCredential
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var credential runtimeCredential
	if err := decoder.Decode(&credential); err != nil {
		return runtimeCredential{}, errInvalidCredential
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return runtimeCredential{}, errInvalidCredential
	}
	if !validRuntimeEndpoint(credential.Endpoint, port) || !validRuntimeToken(credential.Token) {
		return runtimeCredential{}, errInvalidCredential
	}
	return credential, nil
}

func openRuntimeCredential(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errInvalidCredential
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxRuntimeCredentialSize || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errInvalidCredential
	}
	return file, nil
}

func validRuntimeEndpoint(value string, port int) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() != strconv.Itoa(port) ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.Opaque != "" {
		return false
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	return err == nil && address.IsValid() && address.Zone() == "" && address.IsLoopback()
}

func validRuntimeToken(token string) bool {
	if token == "" || len(token) > maxRuntimeTokenSize {
		return false
	}
	return strings.IndexFunc(token, func(character rune) bool {
		return unicode.IsControl(character) || unicode.IsSpace(character)
	}) == -1
}

func shutdownRuntime(server *http.Server, listener net.Listener, stdin *os.File, serveDone <-chan error, stdinDone <-chan struct{}, serveErr error) error {
	deadline := time.Now().Add(runtimeShutdownTimeout)
	_ = stdin.SetReadDeadline(time.Now())
	_ = stdin.Close()
	shutdownContext, cancel := context.WithDeadline(context.Background(), deadline)
	shutdownErr := server.Shutdown(shutdownContext)
	cancel()
	if shutdownErr != nil {
		_ = server.Close()
	}
	_ = listener.Close()

	if serveDone != nil {
		_ = waitUntilRuntimeDeadline(deadline, serveDone)
	}
	stdinStopped := waitUntilRuntimeDeadline(deadline, stdinDone)
	if !stdinStopped {
		return errRuntimeShutdown
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errRuntimeServer
	}
	return nil
}

func waitUntilRuntimeDeadline[T any](deadline time.Time, done <-chan T) bool {
	if done == nil {
		return true
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
