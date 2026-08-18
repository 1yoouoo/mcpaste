package peer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseTailscaleStatusReturnsOnlyOnlinePeerAddresses(t *testing.T) {
	raw := []byte(`{
		"Self": {"TailscaleIPs": ["100.64.0.1", "fd7a:115c:a1e0::1"], "Extra": "ignored"},
		"Peer": {
			"opaque-node-key": {
				"HostName": "Mac mini",
				"TailscaleIPs": ["100.64.0.2", "fd7a:115c:a1e0::2"],
				"Online": true,
				"NewCLIField": {"ignored": true}
			},
			"offline-node-key": {
				"HostName": "Phone",
				"TailscaleIPs": ["100.64.0.3"],
				"Online": false
			}
		}
	}`)

	got, err := ParseTailscaleStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []TailnetCandidate{{
		Name:      "Mac mini",
		Addresses: []string{"100.64.0.2", "fd7a:115c:a1e0::2"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
	for _, candidate := range got {
		if candidate.Name == "opaque-node-key" {
			t.Fatal("parser leaked the raw Peer map key as a name")
		}
	}
}

func TestParseTailscaleStatusIgnoresMalformedOfflinePeerAddresses(t *testing.T) {
	raw := []byte(`{
		"Peer": {
			"online-key": {"HostName": "Online Mac", "TailscaleIPs": ["100.64.0.2"], "Online": true},
			"offline-key": {"HostName": "Offline Device", "TailscaleIPs": ["not-an-ip"], "Online": false}
		}
	}`)

	got, err := ParseTailscaleStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []TailnetCandidate{{Name: "Online Mac", Addresses: []string{"100.64.0.2"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestParseTailscaleStatusSkipsOnlinePeerWithoutUsableAddresses(t *testing.T) {
	raw := []byte(`{
		"Peer": {
			"usable-key": {"HostName": "Usable Mac", "TailscaleIPs": ["100.64.0.2"], "Online": true},
			"empty-key": {"HostName": "No Address Mac", "Online": true}
		}
	}`)

	got, err := ParseTailscaleStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []TailnetCandidate{{Name: "Usable Mac", Addresses: []string{"100.64.0.2"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestParseTailscaleStatusUsesDNSNameFallbackAndSortsCandidates(t *testing.T) {
	raw := []byte(`{
		"Peer": {
			"first-opaque-key": {
				"HostName": "Zulu",
				"DNSName": "zulu.example.ts.net.",
				"TailscaleIPs": ["100.64.0.20", "100.64.0.2", "100.64.0.20", "fd7a:115c:a1e0::20"],
				"Online": true
			},
			"second-opaque-key": {
				"DNSName": "Alpha",
				"TailscaleIPs": ["100.64.0.10"],
				"Online": true
			},
			"third-opaque-key": {
				"HostName": "Beta",
				"DNSName": "ignored.example.ts.net.",
				"TailscaleIPs": ["fd7a:115c:a1e0::10"],
				"Online": true
			}
		}
	}`)

	got, err := ParseTailscaleStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []TailnetCandidate{
		{Name: "Alpha", Addresses: []string{"100.64.0.10"}},
		{Name: "Beta", Addresses: []string{"fd7a:115c:a1e0::10"}},
		{Name: "Zulu", Addresses: []string{"100.64.0.2", "100.64.0.20", "fd7a:115c:a1e0::20"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestParseTailscaleStatusSkipsOnlineShareeNodes(t *testing.T) {
	raw := []byte(`{
		"Peer": {
			"regular-key": {"HostName": "Regular Mac", "TailscaleIPs": ["100.64.0.2"], "Online": true, "ShareeNode": false},
			"sharee-key": {"HostName": "Shared Node", "TailscaleIPs": ["100.64.0.3"], "Online": true, "ShareeNode": true, "FutureField": "ignored"}
		}
	}`)

	got, err := ParseTailscaleStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []TailnetCandidate{{Name: "Regular Mac", Addresses: []string{"100.64.0.2"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestParseTailscaleStatusDoesNotReturnSelfListedAsPeer(t *testing.T) {
	raw := []byte(`{
		"Self": {"TailscaleIPs": ["100.64.0.1"]},
		"Peer": {
			"self-opaque-key": {"HostName": "This Mac", "TailscaleIPs": ["100.64.0.1"], "Online": true},
			"remote-opaque-key": {"HostName": "Other Mac", "TailscaleIPs": ["100.64.0.2"], "Online": true}
		}
	}`)

	got, err := ParseTailscaleStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []TailnetCandidate{{Name: "Other Mac", Addresses: []string{"100.64.0.2"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestParseTailscaleStatusRejectsMalformedJSONAndInvalidAddresses(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed JSON", raw: `{"Peer":`},
		{name: "prefix instead of address", raw: `{"Peer":{"node":{"TailscaleIPs":["100.64.0.2/32"],"Online":true}}}`},
		{name: "address with zone", raw: `{"Peer":{"node":{"TailscaleIPs":["fe80::2%en0"],"Online":true}}}`},
		{name: "invalid self address", raw: `{"Self":{"TailscaleIPs":["not-an-ip"]},"Peer":{}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseTailscaleStatus([]byte(test.raw)); !errors.Is(err, ErrInvalidTailscaleStatus) {
				t.Fatalf("ParseTailscaleStatus() error = %v, want %v", err, ErrInvalidTailscaleStatus)
			}
		})
	}
}

func TestParseTailscaleStatusRejectsOutputOverTwoMiB(t *testing.T) {
	raw := []byte(`{"Peer":{}}`)
	raw = append(raw, []byte(strings.Repeat(" ", (2<<20)-len(raw)+1))...)
	if _, err := ParseTailscaleStatus(raw); !errors.Is(err, ErrInvalidTailscaleStatus) {
		t.Fatalf("ParseTailscaleStatus() error = %v, want %v", err, ErrInvalidTailscaleStatus)
	}
}

func TestCommandRunnerNeverUsesAShell(t *testing.T) {
	runner := TailscaleRunner{
		Executable: "/fake/tailscale",
		Run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			if executable != "/fake/tailscale" || !reflect.DeepEqual(args, []string{"status", "--json"}) {
				t.Fatalf("command = %q %#v", executable, args)
			}
			return []byte(`{"Self":{},"Peer":{}}`), nil
		},
	}
	if _, err := runner.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTailscaleStatusUsesDefaultExecutableAndRedactsCommandErrors(t *testing.T) {
	const sensitive = "/private/secret/tailscale-node"
	var gotExecutable string
	runner := TailscaleRunner{
		Run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			gotExecutable = executable
			return nil, errors.New("command failed for " + sensitive)
		},
	}

	_, err := runner.Status(context.Background())
	if gotExecutable != "tailscale" {
		t.Fatalf("default executable = %q, want tailscale", gotExecutable)
	}
	if !errors.Is(err, ErrTailscaleStatusFailed) || err.Error() != ErrTailscaleStatusFailed.Error() {
		t.Fatalf("Status() error = %v, want stable %q", err, ErrTailscaleStatusFailed)
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatalf("Status() leaked sensitive command detail: %v", err)
	}
}

func TestTailscaleStatusDistinguishesUnavailableExecutable(t *testing.T) {
	const sensitive = "/private/secret/missing-tailscale"
	runner := TailscaleRunner{
		Executable: sensitive,
		Run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			return nil, exec.ErrNotFound
		},
	}

	_, err := runner.Status(context.Background())
	if !errors.Is(err, ErrTailscaleUnavailable) || err.Error() != ErrTailscaleUnavailable.Error() {
		t.Fatalf("Status() error = %v, want stable %q", err, ErrTailscaleUnavailable)
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatalf("Status() leaked executable path: %v", err)
	}
}

func TestTailscaleStatusRejectsInjectedOutputOverTwoMiB(t *testing.T) {
	runner := TailscaleRunner{
		Run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			return []byte(strings.Repeat("x", (2<<20)+1)), nil
		},
	}
	if _, err := runner.Status(context.Background()); !errors.Is(err, ErrInvalidTailscaleStatus) {
		t.Fatalf("Status() error = %v, want %v", err, ErrInvalidTailscaleStatus)
	}
}

func TestTailscaleStatusHonorsContextCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	runner := TailscaleRunner{
		Run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := runner.Status(ctx)
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner was not invoked")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe context cancellation")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrTailscaleStatusFailed) {
			t.Fatalf("Status() error = %v, want %v", err, ErrTailscaleStatusFailed)
		}
	case <-time.After(time.Second):
		t.Fatal("Status() did not return after cancellation")
	}
}

func TestRunTailscaleCommandCancellationKillsHelper(t *testing.T) {
	t.Setenv("MCPASTE_TAILSCALE_HELPER", "cancel")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	started := time.Now()
	output, err := runTailscaleCommand(ctx, os.Args[0], "-test.run=TestTailscaleCommandHelperProcess")
	if err == nil {
		t.Fatal("runTailscaleCommand() unexpectedly succeeded")
	}
	if len(output) != 0 {
		t.Fatalf("runTailscaleCommand() returned %d bytes after cancellation", len(output))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("runTailscaleCommand() took %s after cancellation", elapsed)
	}
}

func TestRunTailscaleCommandCapsHelperOutput(t *testing.T) {
	t.Setenv("MCPASTE_TAILSCALE_HELPER", "large")
	output, err := runTailscaleCommand(context.Background(), os.Args[0], "-test.run=TestTailscaleCommandHelperProcess")
	if !errors.Is(err, errTailscaleOutputTooLarge) {
		t.Fatalf("runTailscaleCommand() error = %v, want %v", err, errTailscaleOutputTooLarge)
	}
	if len(output) != 0 {
		t.Fatalf("runTailscaleCommand() returned %d bytes after oversized output", len(output))
	}
}

func TestTailscaleCommandHelperProcess(t *testing.T) {
	if os.Getenv("MCPASTE_TAILSCALE_HELPER") == "" {
		return
	}
	switch os.Getenv("MCPASTE_TAILSCALE_HELPER") {
	case "cancel":
		select {}
	case "large":
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", (2<<20)+1)))
	default:
		os.Exit(2)
	}
}
