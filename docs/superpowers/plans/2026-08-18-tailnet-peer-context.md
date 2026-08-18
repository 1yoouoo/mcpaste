# Tailnet Peer Context Implementation Plan

> Handoff status (2026-08-18): Tasks 1-3 are complete and independently approved. Task 4 is implemented, spec-approved, and passing tests; six final quality-review findings remain and are recorded in `docs/superpowers/handoffs/2026-08-18-tailnet-peer-context-task4.md`. Resolve those before Task 5.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace MCPaste's hosted workspace with one ephemeral text-and-image context that automatically converges across N macOS peers on the same Tailscale network and remains readable by any local MCP-compatible AI client.

**Architecture:** The bundled Go `mcpaste` helper runs beside each SwiftUI app as an in-memory peer runtime, discovers MCPaste peers through `tailscale status --json`, and converges complete context snapshots with deterministic hybrid-logical revisions. The Swift app owns native editing and image normalization, while the same Go binary in STDIO MCP mode reads the current source through authenticated loopback. No cloud service, database, S3 store, history, account, pairing, Linux companion, or permanent content storage remains.

**Tech Stack:** Swift 5.9/SwiftUI/AppKit, Swift Package Manager/XCTest, Go 1.26, Go standard-library HTTP and `os/exec`, Model Context Protocol Go SDK, Tailscale CLI, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-08-18-tailnet-peer-context-design.md`

---

## Execution constraints

- Do not start implementation from the current dirty `main` worktree. At execution time, use `superpowers:using-git-worktrees` only after the owner chooses an execution method and explicitly approves any required branch/worktree action.
- The current worktree contains user changes in:
  - `macos/MCPaste/Sources/MCPasteApp/ConnectorSetup.swift`
  - `macos/MCPaste/Sources/MCPasteApp/Views/StatusPopoverView.swift`
  - `macos/MCPaste/Sources/MCPasteCore/MCPasteAPI.swift`
  - `macos/MCPaste/Tests/MCPasteCoreTests/APIClientTests.swift`
- Preserve the useful intent of those edits: connector display names remain model-neutral, AI-tool rows stay separate from Mac rows, and response decoding remains correct until the hosted API is removed. Do not stash, reset, discard, or overwrite those changes without owner approval.
- Commit steps below are checkpoints for an authorized execution session. Do not commit, push, deploy, tag, release, sign, or notarize without the corresponding explicit approval.
- Never include context text, image bytes, connector tokens, complete Tailscale JSON, or real machine names in logs or fixtures.

## File structure map

### New Go peer runtime

- `internal/peer/model.go`: protocol constants, revision, manifest, assets, devices, and JSON wire types.
- `internal/peer/clock.go`: hybrid logical clock generation and observation.
- `internal/peer/store.go`: synchronized in-memory current snapshot and short-lived staged assets.
- `internal/peer/tailscale.go`: safe Tailscale CLI execution and minimal JSON parsing.
- `internal/peer/registry.go`: content-free known-peer metadata persistence.
- `internal/peer/http.go`: loopback and peer HTTP handlers, authorization, limits, and asset streaming.
- `internal/peer/sync.go`: peer probing, announcements, fetches, and convergence.
- `internal/peer/runtime.go`: lifecycle, polling loops, fixed-port listener, and stdin-EOF shutdown.
- Matching `*_test.go` files: unit, HTTP, race, and multi-runtime convergence tests.

### Swift local app boundary

- `macos/MCPaste/Sources/MCPasteCore/PeerRuntimeModels.swift`: Codable local API DTOs and four UI states.
- `macos/MCPaste/Sources/MCPasteCore/PeerRuntimeClient.swift`: authenticated loopback calls and atomic asset/context publication.
- `macos/MCPaste/Sources/MCPasteApp/PeerRuntimeProcess.swift`: credential bootstrap, helper launch, readiness, and EOF shutdown.
- `macos/MCPaste/Sources/MCPasteApp/AppModel.swift`: one draft, one attachment array, debounce, remote refresh, and peer status.
- Existing views: single-context UI and approved option-A device treatment.

### Retained Go connector surface

- `cmd/mcpaste/main.go`: only STDIO MCP mode, `peer`, and `register` commands.
- `internal/connector/credential.go`: loopback endpoint plus local token in an owner-only file.
- `internal/connector/local.go`: bounded local manifest and asset client.
- `internal/connector/proxy.go`: one local `get_latest_paste` MCP tool without a hosted proxy session.
- `internal/connector/config.go`: existing Codex/Claude config writers plus generic documented STDIO support.

### Removed hosted surface

- Go server commands/packages, database migrations, Docker/deploy files, endpoint injection, Swift hosted API/session/cache files, onboarding/recovery views, Linux release behavior, and hosted operations documentation are deleted only after local end-to-end tests pass.

## Task 1: Define deterministic context revisions

**Files:**

- Create: `internal/peer/model.go`
- Create: `internal/peer/clock.go`
- Create: `internal/peer/clock_test.go`

- [ ] **Step 1: Write revision-ordering tests**

```go
package peer

import (
	"testing"
	"time"
)

func TestRevisionCompareUsesWallLogicalThenDevice(t *testing.T) {
	a := Revision{WallMillis: 10, Logical: 1, DeviceID: "a"}
	checks := []struct {
		other Revision
		want  int
	}{
		{Revision{WallMillis: 11, Logical: 0, DeviceID: "a"}, -1},
		{Revision{WallMillis: 10, Logical: 2, DeviceID: "a"}, -1},
		{Revision{WallMillis: 10, Logical: 1, DeviceID: "b"}, -1},
		{a, 0},
	}
	for _, check := range checks {
		if got := a.Compare(check.other); got != check.want {
			t.Fatalf("Compare(%+v) = %d, want %d", check.other, got, check.want)
		}
	}
}

func TestClockTicksAfterObservedRemoteRevision(t *testing.T) {
	now := func() time.Time { return time.UnixMilli(100) }
	clock := NewClock("local", now)
	clock.Observe(Revision{WallMillis: 120, Logical: 4, DeviceID: "remote"})
	got := clock.Tick()
	want := Revision{WallMillis: 120, Logical: 5, DeviceID: "local"}
	if got != want {
		t.Fatalf("Tick() = %+v, want %+v", got, want)
	}
}
```

- [ ] **Step 2: Run the focused test and confirm the red state**

Run: `go test ./internal/peer -run 'TestRevision|TestClock'`

Expected: FAIL because `Revision`, `NewClock`, `Observe`, and `Tick` do not exist.

- [ ] **Step 3: Implement the wire model and hybrid logical clock**

Use these exact public shapes so later tasks do not invent parallel types:

```go
package peer

const (
	ProtocolVersion = 1
	DefaultPort     = 38421
	MaxTextBytes    = 4 << 20
	MaxAssets       = 8
	MaxAssetBytes   = 8 << 20
	MaxBundleBytes  = 32 << 20
)

type Revision struct {
	WallMillis int64  `json:"wall_millis"`
	Logical    uint32 `json:"logical"`
	DeviceID   string `json:"device_id"`
}

func (r Revision) Compare(other Revision) int {
	if r.WallMillis < other.WallMillis { return -1 }
	if r.WallMillis > other.WallMillis { return 1 }
	if r.Logical < other.Logical { return -1 }
	if r.Logical > other.Logical { return 1 }
	if r.DeviceID < other.DeviceID { return -1 }
	if r.DeviceID > other.DeviceID { return 1 }
	return 0
}

type AssetManifest struct {
	Digest   string `json:"sha256"`
	MIMEType string `json:"mime_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	ByteSize int    `json:"byte_size"`
}

type ContextManifest struct {
	ProtocolVersion int             `json:"protocol_version"`
	Revision        Revision        `json:"revision"`
	SourceDeviceID  string          `json:"source_device_id"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Text            string          `json:"text"`
	Assets          []AssetManifest `json:"assets"`
}

type SyncState string

const (
	SyncUpToDate     SyncState = "up_to_date"
	SyncWaiting      SyncState = "waiting_to_sync"
	SyncSourceOffline SyncState = "source_offline"
)

type LocalContextResponse struct {
	ContextManifest
	SourceReachable bool      `json:"source_reachable"`
	SyncState       SyncState `json:"sync_state"`
}

type Snapshot struct {
	Manifest ContextManifest
	Assets   map[string][]byte
}
```

Implement `Clock` with a mutex, injected `now`, `Observe(Revision)`, and `Tick()`. `Observe` advances the local wall/logical pair without copying the remote device ID. Reject empty device IDs at runtime construction rather than inside `Compare`.

- [ ] **Step 4: Run clock tests with the race detector**

Run: `go test -race ./internal/peer -run 'TestRevision|TestClock'`

Expected: PASS.

- [ ] **Step 5: Commit the model checkpoint after approval**

```bash
git add internal/peer/model.go internal/peer/clock.go internal/peer/clock_test.go
git commit -m "feat: define peer context revisions"
```

## Task 2: Build the ephemeral atomic context store

**Files:**

- Create: `internal/peer/store.go`
- Create: `internal/peer/store_test.go`

- [ ] **Step 1: Write store tests for atomic publication and source gating**

```go
func TestStorePublishesWholeSnapshotFromStagedAssets(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	data := []byte{1, 2, 3}
	digest := sha256Hex(data)
	if err := store.StageAsset(AssetManifest{Digest: digest, MIMEType: "image/png", Width: 1, Height: 1, ByteSize: len(data)}, data); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.PublishLocal(LocalUpdate{Text: "exact\r\ntext  ", AssetDigests: []string{digest}})
	if err != nil { t.Fatal(err) }
	if manifest.Text != "exact\r\ntext  " || len(manifest.Assets) != 1 {
		t.Fatalf("manifest = %#v", manifest)
	}
	view, err := store.ConnectorSnapshot()
	if err != nil || string(view.Assets[digest]) != string(data) {
		t.Fatalf("snapshot/error = %#v/%v", view, err)
	}
}

func newTestStore(t *testing.T, deviceID string, millis int64) *Store {
	t.Helper()
	now := func() time.Time { return time.UnixMilli(millis) }
	store, err := NewStore(deviceID, now)
	if err != nil { t.Fatal(err) }
	return store
}

func TestStoreRejectsPartialRemoteSnapshotWithoutReplacingCurrent(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	first, _ := store.PublishLocal(LocalUpdate{Text: "keep"})
	bad := ContextManifest{ProtocolVersion: 1, Revision: Revision{WallMillis: 200, DeviceID: "mac-b"}, SourceDeviceID: "mac-b", Assets: []AssetManifest{{Digest: strings.Repeat("a", 64), ByteSize: 2}}}
	if err := store.AdoptRemote(bad, map[string][]byte{}); err == nil {
		t.Fatal("AdoptRemote() unexpectedly succeeded")
	}
	got, _ := store.Manifest()
	if got.Revision != first.Revision || got.Text != "keep" {
		t.Fatalf("current = %#v", got)
	}
}

func TestConnectorSnapshotRequiresCurrentSourceReachability(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	remote := ContextManifest{ProtocolVersion: 1, Revision: Revision{WallMillis: 200, DeviceID: "mac-b"}, SourceDeviceID: "mac-b", Text: "remote"}
	if err := store.AdoptRemote(remote, map[string][]byte{}); err != nil { t.Fatal(err) }
	store.SetSourceReachable(false)
	if _, err := store.ConnectorSnapshot(); !errors.Is(err, ErrSourceOffline) {
		t.Fatalf("error = %v", err)
	}
}
```

- [ ] **Step 2: Run store tests and confirm they fail**

Run: `go test ./internal/peer -run TestStore`

Expected: FAIL because `Store`, staging, publication, adoption, and connector gating are absent.

- [ ] **Step 3: Implement one-lock atomic store semantics**

Define these exact errors and methods:

```go
var (
	ErrNoContext    = errors.New("no current context")
	ErrSourceOffline = errors.New("current source offline")
	ErrInvalidAsset = errors.New("invalid context asset")
)

type LocalUpdate struct {
	Text         string   `json:"text"`
	AssetDigests []string `json:"asset_digests"`
}

type Store struct {
	mu              sync.RWMutex
	clock           *Clock
	localDeviceID   string
	now             func() time.Time
	current         *Snapshot
	staged          map[string]stagedAsset
	sourceReachable bool
}
```

Implement `NewStore(deviceID string, now func() time.Time) (*Store, error)`, `StageAsset`, `PublishLocal`, `AdoptRemote`, `Manifest() (ContextManifest, error)`, `ConnectorSnapshot`, `SetSourceReachable`, and `SweepStaged`. `StageAsset` must verify MIME type (`image/png` or `image/jpeg`), dimensions, declared byte size, per-asset limit, bundle limit, and lowercase SHA-256. `PublishLocal` must resolve every digest before calling `clock.Tick`, copy all byte slices, and swap `current` once. `AdoptRemote` must validate the complete manifest and all bytes before observing the revision and swapping. `ConnectorSnapshot` returns a deep copy only when a context exists and its source is reachable. `SweepStaged` removes unreferenced staged assets older than 30 seconds. Define `sha256Hex([]byte) string` in `store.go` and reuse it for staging and adoption.

- [ ] **Step 4: Add tests for limits, digest mismatch, empty clearing, stale revisions, and staged expiry**

The exact assertions are: nine assets fail with `ErrInvalidAsset`; a 33 MiB bundle fails; an incorrect digest fails; `PublishLocal(LocalUpdate{})` produces an empty newer manifest; `AdoptRemote` ignores an equal or older revision without changing text; and an unreferenced staged asset disappears after the injected clock advances 31 seconds.

- [ ] **Step 5: Run store and race tests**

Run: `go test -race ./internal/peer -run 'TestStore|TestStage|TestPublish|TestAdopt'`

Expected: PASS with no race report.

- [ ] **Step 6: Commit the store checkpoint after approval**

```bash
git add internal/peer/store.go internal/peer/store_test.go
git commit -m "feat: add ephemeral context store"
```

## Task 3: Discover only MCPaste Macs through Tailscale

**Files:**

- Create: `internal/peer/tailscale.go`
- Create: `internal/peer/tailscale_test.go`
- Create: `internal/peer/registry.go`
- Create: `internal/peer/registry_test.go`

- [ ] **Step 1: Write fixture-driven Tailscale parser tests**

```go
func TestParseTailscaleStatusReturnsOnlyOnlinePeerAddresses(t *testing.T) {
	raw := []byte(`{"Self":{"TailscaleIPs":["100.64.0.1"]},"Peer":{"node-a":{"HostName":"Mac mini","TailscaleIPs":["100.64.0.2","fd7a:115c:a1e0::2"],"Online":true},"node-b":{"HostName":"Phone","TailscaleIPs":["100.64.0.3"],"Online":false}}}`)
	got, err := ParseTailscaleStatus(raw)
	if err != nil { t.Fatal(err) }
	want := []TailnetCandidate{{Name: "Mac mini", Addresses: []string{"100.64.0.2", "fd7a:115c:a1e0::2"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerNeverUsesAShell(t *testing.T) {
	runner := TailscaleRunner{Executable: "/fake/tailscale", Run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		if executable != "/fake/tailscale" || !reflect.DeepEqual(args, []string{"status", "--json"}) {
			t.Fatalf("command = %q %#v", executable, args)
		}
		return []byte(`{"Self":{},"Peer":{}}`), nil
	}}
	if _, err := runner.Status(context.Background()); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run the discovery tests and confirm the red state**

Run: `go test ./internal/peer -run 'TestParseTailscale|TestCommandRunner|TestRegistry'`

Expected: FAIL because the parser, runner, and registry do not exist.

- [ ] **Step 3: Implement minimal parsing and safe command execution**

`TailscaleRunner.Status` must use `exec.CommandContext(ctx, executable, "status", "--json")`, a two-second timeout supplied by the caller, and a 2 MiB stdout limit. It must return stable errors `tailscale unavailable`, `tailscale status failed`, and `invalid tailscale status` without embedding stdout, stderr, paths, node names, or addresses.

`ParseTailscaleStatus` must decode only `Self.TailscaleIPs` and `Peer.*.{HostName,DNSName,TailscaleIPs,Online}` with `json.Decoder.DisallowUnknownFields` disabled because the CLI may add fields. Validate every candidate with `net/netip`; sort candidates by name then address for deterministic tests.

- [ ] **Step 4: Implement a content-free peer registry**

Use these persisted fields only:

```go
type KnownPeer struct {
	DeviceID   string    `json:"device_id"`
	DisplayName string   `json:"display_name"`
	Addresses  []string  `json:"addresses"`
	LastSeenAt time.Time `json:"last_seen_at"`
}
```

`Registry.Record`, `Registry.List`, and `Registry.Load` must use a mutex and an atomic mode-0600 JSON file replacement. Reject empty/invalid UUIDs, control characters in names, invalid IPs, symlink registry files, files larger than 1 MiB, and unknown JSON fields. No revision, text, image metadata, or token may enter this file.

- [ ] **Step 5: Run parser and registry tests**

Run: `go test -race ./internal/peer -run 'TestParseTailscale|TestCommandRunner|TestRegistry'`

Expected: PASS.

- [ ] **Step 6: Commit discovery after approval**

```bash
git add internal/peer/tailscale.go internal/peer/tailscale_test.go internal/peer/registry.go internal/peer/registry_test.go
git commit -m "feat: discover tailnet peers"
```

## Task 4: Expose bounded peer and loopback HTTP APIs

**Files:**

- Create: `internal/peer/http.go`
- Create: `internal/peer/http_test.go`

- [ ] **Step 1: Write authorization and atomic-transfer HTTP tests**

Create `httptest` requests that assert:

```go
func TestLocalContextRequiresLoopbackAndBearer(t *testing.T) {
	handler := newTestHandler(t, "local-token")
	for _, request := range []*http.Request{
		requestFrom("GET", "/v1/local/context", "100.64.0.2:1234", "Bearer local-token", nil),
		requestFrom("GET", "/v1/local/context", "127.0.0.1:1234", "", nil),
		requestFrom("GET", "/v1/local/context", "127.0.0.1:1234", "Bearer wrong", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized { t.Fatalf("status = %d", response.Code) }
	}
}

func TestPeerRoutesRequireCurrentTailnetAddress(t *testing.T) {
	handler := newTestHandler(t, "local-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, requestFrom("GET", "/v1/health", "203.0.113.8:1234", "", nil))
	if response.Code != http.StatusForbidden { t.Fatalf("status = %d", response.Code) }
}

func requestFrom(method, path, remoteAddr, authorization string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, "http://mcpaste.local"+path, body)
	request.RemoteAddr = remoteAddr
	if authorization != "" { request.Header.Set("Authorization", authorization) }
	return request
}
```

Also test: health returns no text or asset bytes; local staging verifies `Content-Length` before reading; local publication is JSON-only and rejects unknown fields; asset responses set exact content type/length; malformed paths return 404; peer manifest and asset routes work from an allowlisted Tailscale IP; and errors contain fixed generic messages only.

The local asset upload carries `Content-Type`, `Content-Length`, `X-MCPaste-Width`, and `X-MCPaste-Height`; the digest remains in the path. Reject missing, repeated, non-decimal, zero, or overflow dimension headers before reading the body.

- [ ] **Step 2: Run HTTP tests and confirm they fail**

Run: `go test ./internal/peer -run 'TestLocal|TestPeerRoutes|TestHealth|TestAsset'`

Expected: FAIL because `Handler` and route authorization do not exist.

- [ ] **Step 3: Implement the exact route table**

```text
GET  /v1/health
GET  /v1/context
GET  /v1/context/assets/{index}
POST /v1/announce
PUT  /v1/local/assets/{sha256}
PUT  /v1/local/context
GET  /v1/local/context
GET  /v1/local/context/assets/{index}
GET  /v1/local/devices
```

Use `http.MaxBytesReader`, `io.LimitReader`, `http.Server{ReadHeaderTimeout: 2*time.Second, ReadTimeout: 10*time.Second, WriteTimeout: 30*time.Second, IdleTimeout: 30*time.Second}`, constant-time token comparison, `net.SplitHostPort`, and `netip.ParseAddr`. Only local routes accept loopback. Only peer routes accept an address currently provided by `AllowedPeerIPs`. Never authorize from `Host`, `X-Forwarded-For`, or query parameters.

Define `AllowedPeerIPs.Replace([]netip.Addr)` and `AllowedPeerIPs.Contains(netip.Addr) bool` with an immutable map swapped under a read/write mutex. Define `NewHandler(HandlerOptions) http.Handler`, where `HandlerOptions` contains `Store *Store`, `Registry *Registry`, `LocalDevice KnownPeer`, `LocalToken string`, `AllowedPeers *AllowedPeerIPs`, `SyncState func() SyncState`, and `Announce func(context.Context, Revision) error`. The test-only `newTestHandler` constructs those exact dependencies with fixed fake values and allowlists `100.64.0.2`.

- [ ] **Step 4: Add response-contract tests**

Assert exact status mapping: 200 for complete reads, including a last in-memory manifest with `source_reachable:false`; 204 for successful stage/publish/announce; 400 for invalid JSON or digest; 401 for bad local token; 403 for a non-tailnet peer; 404 for no context/path; 409 for an older announced revision; and 413 for size overflow. The runtime does not know whether a loopback reader is the UI or connector, so the connector—not the HTTP handler—refuses a manifest whose source is offline.

- [ ] **Step 5: Run HTTP tests with race detection**

Run: `go test -race ./internal/peer -run 'TestLocal|TestPeerRoutes|TestHealth|TestAsset|TestResponse'`

Expected: PASS.

- [ ] **Step 6: Commit HTTP boundaries after approval**

```bash
git add internal/peer/http.go internal/peer/http_test.go
git commit -m "feat: expose local peer runtime APIs"
```

## Task 5: Converge N peer runtimes and stop on app exit

**Files:**

- Create: `internal/peer/sync.go`
- Create: `internal/peer/sync_test.go`
- Create: `internal/peer/runtime.go`
- Create: `internal/peer/runtime_test.go`

- [ ] **Step 1: Write two- and three-peer convergence tests**

Use `httptest.Server` plus injected candidate discovery and clocks. Required assertions:

```go
func TestThreePeersConvergeOnHighestRevision(t *testing.T) {
	cluster := newRuntimeCluster(t, 3)
	a, b, c := cluster[0], cluster[1], cluster[2]
	a.publish(t, "from-a")
	b.publish(t, "from-b")
	clusterTick(t, a, b, c)
	want := b.manifest(t).Revision
	for _, runtime := range []*testRuntime{a, b, c} {
		got := runtime.manifest(t)
		if got.Revision != want || got.Text != "from-b" { t.Fatalf("manifest = %#v", got) }
	}
}

func TestMissedAnnouncementConvergesOnHealthPoll(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publishWithoutAnnounce(t, "eventually")
	b.poll(t)
	if got := b.manifest(t).Text; got != "eventually" { t.Fatalf("text = %q", got) }
}

func TestSourceOfflineBlocksConnectorUntilSourceReturns(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publish(t, "remote")
	b.poll(t)
	a.setReachable(false)
	b.poll(t)
	if _, err := b.store.ConnectorSnapshot(); !errors.Is(err, ErrSourceOffline) { t.Fatalf("error = %v", err) }
	a.setReachable(true)
	b.poll(t)
	if _, err := b.store.ConnectorSnapshot(); err != nil { t.Fatal(err) }
}
```

Implement the test-only `testRuntime` wrapper in `sync_test.go` with `store *Store`, `server *httptest.Server`, `coordinator *Coordinator`, `reachable atomic.Bool`, and methods `publish`, `publishWithoutAnnounce`, `poll`, `manifest`, and `setReachable`. `newRuntimeCluster(t, count)` returns `[]*testRuntime`; `clusterTick` calls `poll` on every member until all advertised revisions match or a one-second test deadline expires. Add deterministic tests for simultaneous equal-wall-time edits, offline edits followed by reconnect, one-time relay without announcement loops, partial asset fetch, protocol mismatch, IPv6 URL formatting, and a source restart rehydrating from a live replica.

- [ ] **Step 2: Run sync tests and confirm they fail**

Run: `go test ./internal/peer -run 'TestThreePeers|TestMissedAnnouncement|TestSourceOffline|TestSimultaneous|TestOffline'`

Expected: FAIL because the coordinator and runtime do not exist.

- [ ] **Step 3: Implement bounded peer synchronization**

`Coordinator.PollOnce(ctx)` must:

1. obtain candidates from the injected Tailscale status source;
2. replace the HTTP handler's allowed-address snapshot;
3. probe candidate health concurrently with a maximum of four workers and a 750 ms request timeout;
4. record only successful MCPaste identities;
5. mark the current source reachable only if local or successfully probed;
6. fetch a higher manifest and at most three assets concurrently;
7. verify and atomically adopt the snapshot;
8. announce a newly adopted revision once to peers that advertised an older revision.

Use clients with redirects disabled and no proxy inherited for Tailscale IP requests. Build IPv6 URLs with `net.JoinHostPort`. Do not send the loopback token to peer endpoints. `Coordinator.SyncState()` returns `SyncSourceOffline` when the current remote source is not reachable, `SyncWaiting` when the Tailscale status command is unavailable or the newest local announcement failed for every discovered peer, and `SyncUpToDate` after all currently reachable peers advertise the winning revision. Previously known but currently offline peers do not keep the global pill in a permanent waiting state.

- [ ] **Step 4: Implement runtime lifecycle**

```go
type RuntimeOptions struct {
	DeviceID      string
	DisplayName   string
	Port          int
	CredentialPath string
	RegistryPath  string
	Stdin         io.Reader
	Tailscale     StatusSource
	Now           func() time.Time
}

func Run(ctx context.Context, options RuntimeOptions) error
```

`Run` validates options, reads the local credential file, starts one HTTP server, runs immediate then three-second polls, sweeps staging memory, and shuts down within two seconds when the context is canceled or `options.Stdin` reaches EOF. A bind failure returns `peer runtime port unavailable` without printing another process's details.

- [ ] **Step 5: Verify lifecycle and race behavior**

Run: `go test -race ./internal/peer`

Expected: PASS, including EOF shutdown and no goroutine leak according to the test's bounded wait groups.

- [ ] **Step 6: Commit synchronization after approval**

```bash
git add internal/peer/sync.go internal/peer/sync_test.go internal/peer/runtime.go internal/peer/runtime_test.go
git commit -m "feat: synchronize tailnet peer contexts"
```

## Task 6: Convert the Go CLI from hosted proxy to local runtime/connector

**Files:**

- Modify: `cmd/mcpaste/main.go`
- Modify: `cmd/mcpaste/main_test.go`
- Modify: `cmd/mcpaste/register_test.go`
- Create: `internal/connector/local.go`
- Create: `internal/connector/local_test.go`
- Modify: `internal/connector/proxy.go`
- Modify: `internal/connector/proxy_test.go`
- Modify: `internal/connector/credential.go`
- Modify: `internal/connector/credential_test.go`
- Modify: `internal/connector/config.go`
- Modify: `internal/connector/config_test.go`

- [ ] **Step 1: Replace hosted-proxy tests with local MCP tests**

The connector test server must expose the runtime's local manifest/assets routes and verify the bearer header. Assert one tool named `get_latest_paste`, exact CRLF/whitespace text, ordered PNG/JPEG `mcp.ImageContent`, structured fields `available`, `revision`, `source_device_id`, and `assets`, plus `IsError: true` with a short fixed message for app absent/source offline. Assert that redirects are refused and the token never appears in URL, result metadata, or errors.

- [ ] **Step 2: Add CLI surface tests**

```go
func TestRunAcceptsOnlyPeerRegisterAndDefaultMCPModes(t *testing.T) {
	for _, args := range [][]string{{"setup"}, {"login"}, {"approve"}, {"--endpoint", "https://example.test"}} {
		if err := run(context.Background(), args); err == nil {
			t.Fatalf("run(%q) unexpectedly succeeded", args)
		}
	}
}
```

Add a `peer` test with injected stdin and an ephemeral test port, and keep config-writer tests showing that unrelated Codex TOML and Claude JSON fields survive registration.

- [ ] **Step 3: Run focused connector/CLI tests and confirm they fail**

Run: `go test ./cmd/mcpaste ./internal/connector`

Expected: FAIL because the current CLI still exposes hosted setup/login/approve and the connector opens a remote MCP session.

- [ ] **Step 4: Implement the local connector**

`connector.LocalClient` loads `Credential{Endpoint,Token}`, requires an `http://127.0.0.1` or `http://[::1]` origin with no user info/query/fragment, refuses redirects, bounds manifests/assets, and calls runtime local routes. It returns `ErrSourceOffline` immediately when `source_reachable` is false and does not fetch asset bodies. `NewProxy` no longer connects during construction; its MCP handler performs one local read per tool call and maps the manifest to MCP content.

Keep this tool declaration:

```go
server.AddTool(&mcp.Tool{
	Name:        "get_latest_paste",
	Description: "Retrieve the current MCPaste context.",
	InputSchema: map[string]any{"type": "object", "additionalProperties": false},
}, localGetLatest(client))
```

- [ ] **Step 5: Reduce the CLI command surface**

`run` dispatches only `peer`, `register`, or default STDIO MCP mode. `peer` accepts `--device-id`, `--name`, `--credential-file`, `--registry-file`, and `--port`; it calls `peer.Run` with stdin. `register` keeps `--codex-config` and `--claude-config`. Remove endpoint, setup, login, approve, pairing, admin credential, and Linux platform branches.

Change `ConfigureClients` to return `ConfiguredClients{Names []string}` in deterministic order. `runRegister` prints exactly one JSON line such as `{"configured_clients":["Codex","Claude Code"]}`; it contains no paths or tokens. Preserve the existing no-client error. This gives Swift the separate `AI tool connections` names without coupling the MCP protocol to either brand.

- [ ] **Step 6: Run connector and CLI tests**

Run: `go test -race ./cmd/mcpaste ./internal/connector ./internal/peer`

Expected: PASS.

- [ ] **Step 7: Commit the local connector after approval**

```bash
git add cmd/mcpaste internal/connector internal/peer
git commit -m "feat: connect MCP clients to the local peer runtime"
```

## Task 7: Add the Swift runtime process and loopback client

**Files:**

- Create: `macos/MCPaste/Sources/MCPasteCore/PeerRuntimeModels.swift`
- Create: `macos/MCPaste/Sources/MCPasteCore/PeerRuntimeClient.swift`
- Create: `macos/MCPaste/Sources/MCPasteApp/PeerRuntimeProcess.swift`
- Create: `macos/MCPaste/Tests/MCPasteCoreTests/PeerRuntimeClientTests.swift`
- Create: `macos/MCPaste/Tests/MCPasteAppTests/PeerRuntimeProcessTests.swift`
- Modify: `macos/MCPaste/Sources/MCPasteApp/ConnectorSetup.swift`
- Modify: `macos/MCPaste/Tests/MCPasteAppTests/ConnectorSetupTests.swift`

- [ ] **Step 1: Write Swift local-client tests against `URLProtocol`**

```swift
func testPublishStagesNewImagesThenPublishesOrderedDigests() async throws {
    let image = NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1, 2, 3]))
    let recorder = RequestRecorder()
    let client = makePeerRuntimeClient(recorder: recorder)

    try await client.publish(text: "exact\r\ntext  ", images: [image])

    let requests = await recorder.requests
    XCTAssertEqual(requests.map(\.url!.path), [
        "/v1/local/assets/039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81",
        "/v1/local/context"
    ])
    XCTAssertEqual(requests.map { $0.value(forHTTPHeaderField: "Authorization") }, ["Bearer local-test-token", "Bearer local-test-token"])
}
```

Add tests for exact text decoding, ordered asset download, a 200 manifest with `source_reachable:false`, 404 empty context, unknown JSON fields, response-size limits, token absence from URLs/errors, and `GET /v1/local/devices` mapping. Verify staged asset requests send exact MIME type, byte length, width, and height headers.

- [ ] **Step 2: Run the new Swift tests and confirm they fail**

Run: `swift test --filter 'PeerRuntime(Client|Process)Tests'` from `macos/MCPaste`.

Expected: FAIL because the DTOs, client, and process launcher do not exist.

- [ ] **Step 3: Implement stable Swift DTOs**

```swift
public enum PeerSyncState: String, Codable, Equatable {
    case upToDate = "up_to_date"
    case updating
    case waitingToSync = "waiting_to_sync"
    case sourceOffline = "source_offline"
}

public struct PeerDevice: Codable, Equatable, Identifiable {
    public let id: String
    public let displayName: String
    public let reachable: Bool
    public let isLocal: Bool
    public let isSource: Bool
    public let lastSeenAt: Date
}

public struct RuntimeRevision: Codable, Equatable, Comparable {
    public let wallMillis: Int64
    public let logical: UInt32
    public let deviceID: String
}

public struct RuntimeAsset: Equatable {
    public let digest: String
    public let mimeType: String
    public let width: Int
    public let height: Int
    public let data: Data
}

public struct RuntimeContext: Equatable {
    public let revision: RuntimeRevision
    public let sourceDeviceID: String
    public let updatedAt: Date
    public let text: String
    public let assets: [RuntimeAsset]
    public let sourceReachable: Bool
    public let syncState: PeerSyncState
}

public protocol PeerRuntimeServing: Sendable {
    func publish(text: String, images: [NormalizedImage]) async throws
    func current() async throws -> RuntimeContext?
    func devices() async throws -> [PeerDevice]
}
```

Use snake-case coding keys matching Task 1 for `RuntimeRevision`, `PeerDevice`, and private Codable manifest/asset DTOs inside `PeerRuntimeClient`. The client decodes `LocalContextResponse`, downloads asset bodies, and assembles the public non-Codable `RuntimeContext`/`RuntimeAsset` values. Do not reuse hosted `PasteRecord`, `DeviceRecord`, workspace IDs, expiry, or server sequence fields.

- [ ] **Step 4: Implement `PeerRuntimeClient`**

It receives an injected `URLSession`, fixed loopback base URL, and token; stages each normalized image under its SHA-256; publishes one JSON update listing digests; loads manifest first and assets concurrently with a maximum of three; uses ISO-8601 fractional-second dates; bounds data before decoding; and maps fixed runtime errors to `PeerRuntimeError.empty`, `.sourceOffline`, `.unavailable`, `.invalidResponse`, and `.rejectedImage`.

- [ ] **Step 5: Implement runtime credential and process lifecycle**

`PeerRuntimeProcess` must:

1. reuse `ConnectorSetup.credentialFileURL()`;
2. create a 32-byte random token with `SecRandomCopyBytes` only when no valid local credential exists;
3. atomically write `{"endpoint":"http://127.0.0.1:38421","token":"<base64url>"}` with mode `0600` and a mode-0700 parent directory;
4. persist only a random device UUID and sanitized display name in `UserDefaults`;
5. launch the embedded helper with argument-array `peer` options and an open stdin pipe;
6. poll authenticated health for at most two seconds;
7. close stdin and wait up to two seconds during shutdown, then terminate only that owned child process if needed.

- [ ] **Step 6: Simplify connector setup**

Remove short-code parsing and approval callbacks. `ConnectorSetup.run()` now launches only `mcpaste register` after the local credential exists, decodes the one-line `configured_clients` response, and returns those names. Preserve the user's model-neutral connector naming intent in UI copy, but do not create server device records.

- [ ] **Step 7: Run Swift runtime tests**

Run: `swift test --filter 'PeerRuntime(Client|Process)Tests|ConnectorSetupTests'` from `macos/MCPaste`.

Expected: PASS.

- [ ] **Step 8: Commit the Swift boundary after approval**

```bash
git add macos/MCPaste/Sources/MCPasteCore/PeerRuntimeModels.swift macos/MCPaste/Sources/MCPasteCore/PeerRuntimeClient.swift macos/MCPaste/Sources/MCPasteApp/PeerRuntimeProcess.swift macos/MCPaste/Sources/MCPasteApp/ConnectorSetup.swift macos/MCPaste/Tests
git commit -m "feat: launch the local peer runtime from macOS"
```

## Task 8: Replace workspace state with one auto-published context

**Files:**

- Modify: `macos/MCPaste/Sources/MCPasteApp/AppModel.swift`
- Modify: `macos/MCPaste/Sources/MCPasteApp/MCPasteApp.swift`
- Rewrite: `macos/MCPaste/Tests/MCPasteAppTests/AppModelTests.swift`

- [ ] **Step 1: Write new AppModel behavior tests**

Use an actor-backed fake `PeerRuntimeServing` and injected debounce clock. Required tests:

```swift
func testTextEditPublishesAfterOneSecondDebounce() async {
    let runtime = RuntimeSpy()
    let clock = ManualDebounceClock()
    let model = AppModel(runtime: runtime, debounceClock: clock)

    model.draft = "first"
    model.draft = "final  "
    await clock.advance(by: .seconds(1))

    let publications = await runtime.publications
    XCTAssertEqual(publications, [.init(text: "final  ", images: [])])
    XCTAssertEqual(model.syncState, .upToDate)
}

func testRemoteWinnerReplacesOpenEditorAndImages() async {
    let runtime = RuntimeSpy()
    let model = AppModel(runtime: runtime)
    await runtime.setDevices([PeerDevice(id: "mac-mini", displayName: "Mac mini", reachable: true, isLocal: false, isSource: true, lastSeenAt: .distantPast)])
    await runtime.setCurrent(RuntimeContext(
        revision: RuntimeRevision(wallMillis: 200, logical: 0, deviceID: "mac-mini"),
        sourceDeviceID: "mac-mini",
        updatedAt: .distantPast,
        text: "from mini",
        assets: [],
        sourceReachable: true,
        syncState: .upToDate
    ))
    await model.refreshNow()
    XCTAssertEqual(model.draft, "from mini")
    XCTAssertEqual(model.lastUpdatedBy, "Mac mini")
}
```

Define `DebounceClock.sleep(for:)` and an actor-backed `ManualDebounceClock` whose `advance(by:)` resumes stored continuations deterministically. Define `RuntimeSpy: PeerRuntimeServing` as an actor with `Publication: Equatable`, `private(set) var publications`, `setCurrent`, `setDevices`, and the three protocol methods from Task 7. Also test: rapid edits publish once; upload waits for normalization; image failure keeps the prior published snapshot; clear publishes empty text/assets; offline publish maps to `Waiting to sync`; remote refresh during no local change replaces immediately; a higher remote revision wins over a queued older edit; and all user-facing status strings are exact.

- [ ] **Step 2: Run AppModel tests and confirm they fail**

Run: `swift test --filter AppModelTests` from `macos/MCPaste`.

Expected: FAIL because the current model requires workspace/API/session/history state.

- [ ] **Step 3: Rewrite AppModel around `PeerRuntimeServing`**

Keep only:

```swift
@Published public var draft = ""
@Published public private(set) var attachments: [NormalizedImage] = []
@Published public private(set) var devices: [PeerDevice] = []
@Published public private(set) var syncState: PeerSyncState = .waitingToSync
@Published public private(set) var lastUpdatedBy: String?
@Published public private(set) var lastUpdatedAt: Date?
@Published public private(set) var errorMessage: String?
@Published public private(set) var uploadingCount = 0
@Published public private(set) var connectorNames: [String] = []
```

Remove `AppScreen`, workspace ID, pairing/recovery fields, history, selected paste, server sync status, offline queue count, approval state, hosted APIs, Keychain workspace restore, and delete/revoke methods. `draft` changes schedule a one-second `Task` debounce. Attachment operations remain serialized through `SerialGate` and call one whole-snapshot publication after normalization. Runtime refresh runs every 500 ms while the app is active; unchanged revisions do not touch editor bindings. Store the deterministic names returned by `ConnectorSetup.run()` in `connectorNames`; this is configuration presence, not model activity telemetry.

- [ ] **Step 4: Make app lifecycle unconditional**

`MCPasteApp` starts `PeerRuntimeProcess` once, always opens the content window, removes onboarding/recovery routing, closes the runtime stdin during termination, and does not save content on quit. A launch failure still opens the editor with `Waiting to sync` and a compact actionable error.

- [ ] **Step 5: Run AppModel and lifecycle tests**

Run: `swift test --filter 'AppModelTests|ContentWindowOpenerTests|PackageSmokeTests'` from `macos/MCPaste`.

Expected: PASS.

- [ ] **Step 6: Commit the single-context model after approval**

```bash
git add macos/MCPaste/Sources/MCPasteApp/AppModel.swift macos/MCPaste/Sources/MCPasteApp/MCPasteApp.swift macos/MCPaste/Tests/MCPasteAppTests/AppModelTests.swift
git commit -m "feat: replace paste history with one current context"
```

## Task 9: Apply the approved native UI

**Files:**

- Modify: `macos/MCPaste/Sources/MCPasteApp/Views/ContentWindowView.swift`
- Modify: `macos/MCPaste/Sources/MCPasteApp/Views/StatusPopoverView.swift`
- Modify: `macos/MCPaste/Tests/MCPasteAppTests/SnapshotRenderTests.swift`
- Modify: `macos/MCPaste/Tests/MCPasteAppTests/DesignReviewFixTests.swift`
- Delete: `macos/MCPaste/Sources/MCPasteApp/Views/DeviceListView.swift`
- Delete: `macos/MCPaste/Sources/MCPasteApp/Views/OnboardingView.swift`
- Delete: `macos/MCPaste/Sources/MCPasteApp/Views/RecoveryView.swift`
- Delete: `macos/MCPaste/Tests/MCPasteAppTests/PairingApprovalTests.swift`

- [ ] **Step 1: Replace UI assertions with the approved contract**

Tests must assert:

```swift
XCTAssertEqual(EditorStatus.text(for: .upToDate), "Up to date")
XCTAssertEqual(EditorStatus.text(for: .updating), "Updating…")
XCTAssertEqual(EditorStatus.text(for: .waitingToSync), "Waiting to sync")
XCTAssertEqual(EditorStatus.text(for: .sourceOffline), "Source offline")
```

Source searches in tests must reject `This Mac`, `Approve a device`, `Create workspace`, `Join workspace`, `Recovery code`, `Search history`, and `Paste #` from current SwiftUI views. Snapshot fixtures must contain a local blue selected row, a remote source green icon badge, normal green reachable dots, and a dim gray unavailable row.

- [ ] **Step 2: Run UI tests and confirm they fail**

Run: `swift test --filter 'SnapshotRenderTests|DesignReviewFixTests'` from `macos/MCPaste`.

Expected: FAIL because history, onboarding, approval, and textual `This Mac` remain.

- [ ] **Step 3: Simplify the main content window without changing tone**

Remove `NavigationSplitView`, `HistorySidebar`, `HistoryRow`, and `DeviceFooter`. Keep the existing detail header, divider rhythm, monospaced editor, attachment strip, exact-text footer, button style, control sizes, and minimum window size. The title is `Current context`; the subtitle is `Changes sync automatically` or `Updated from <device> · just now`; the square-and-pencil action calls `clearContext()` and has help text `Start a blank shared context (⌘N)`.

- [ ] **Step 4: Apply option A to the popover**

Keep the existing 300-point popover, native callout/caption fonts, plain sections, and dividers. Merge the local Mac into `Connected devices`. Apply `Color.accentColor.opacity(0.10)` only to the local row, tint its icon with accent color, overlay a six-point green badge on the source icon, use a six-point trailing green/gray presence dot, and dim unavailable rows. Remove workspace identity, approve, revoke, pairing, and recovery controls. Keep `AI tool connections` separate and render `model.connectorNames`; omit the section when that array is empty.

- [ ] **Step 5: Render and inspect snapshots**

Run: `swift test --filter 'SnapshotRenderTests|DesignReviewFixTests'` from `macos/MCPaste`.

Expected: PASS and write fresh render artifacts under the test's temporary directory. Inspect each rendered PNG using the existing snapshot workflow; confirm no clipping at 300-point popover width and 720-by-460 minimum content window.

- [ ] **Step 6: Commit the UI after approval**

```bash
git add macos/MCPaste/Sources/MCPasteApp/Views macos/MCPaste/Tests/MCPasteAppTests
git commit -m "feat: simplify the native single-context UI"
```

## Task 10: Remove the hosted architecture after local end-to-end passes

**Files:**

- Delete: `cmd/healthcheck/`
- Delete: `cmd/migrate/`
- Delete: `cmd/server/`
- Delete: `db/`
- Delete: `internal/config/`
- Delete: `internal/database/`
- Delete: `internal/httpserver/`
- Delete: `internal/identity/`
- Delete: `internal/images/`
- Delete: `internal/mcpserver/`
- Delete: `internal/secure/`
- Delete: `internal/testdb/`
- Delete: `compose.yaml`
- Delete: `Dockerfile`
- Delete: `.dockerignore`
- Delete: `.env.example`
- Delete: `deploy/`
- Delete: `scripts/configure-endpoint.sh`
- Delete: `macos/MCPaste/Sources/CSQLite/`
- Delete: `macos/MCPaste/Sources/MCPasteCore/APIModels.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/AttachmentCache.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/EndpointConfiguration.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/KeychainStore.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/MCPasteAPI.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/OfflineQueue.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/PendingAttachmentStore.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/RealtimeSyncLoop.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/SQLiteCache.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/SyncCoordinator.swift`
- Delete: `macos/MCPaste/Sources/MCPasteCore/WorkspaceSession.swift`
- Delete corresponding obsolete Swift tests under `macos/MCPaste/Tests/MCPasteCoreTests/`
- Modify: `macos/MCPaste/Package.swift`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `.gitignore`

- [ ] **Step 1: Prove the new local path before deletion**

Run:

```bash
go test -race ./cmd/mcpaste ./internal/connector ./internal/peer
cd macos/MCPaste && swift test
```

Expected: both commands PASS while old hosted files are still present.

- [ ] **Step 2: Resolve the dirty-file overlap before deleting hosted API files**

Review the owner changes listed in Execution constraints. Carry forward their surviving intent into Tasks 6 and 9. Obtain owner confirmation before deleting the now-obsolete modified `MCPasteAPI.swift` and `APIClientTests.swift`; do not use reset, checkout, clean, or stash.

- [ ] **Step 3: Delete only the exact hosted paths listed above**

Use `apply_patch` for tracked source/document deletions. Do not recursively remove a workspace root, broad source directory, user data, `.git`, `.superpowers`, `.codex`, `.claude`, or any path not listed.

- [ ] **Step 4: Simplify package dependencies**

Remove `CSQLite` from `macos/MCPaste/Package.swift`. Run `go mod tidy`; the resulting module must retain only dependencies reachable from the MCP SDK and TOML config writer. No PostgreSQL, S3, Argon2, hosted HTTP, or endpoint-injection package may remain.

- [ ] **Step 5: Verify absence and builds**

Run:

```bash
test ! -e cmd/server
test ! -e db
test ! -e deploy
test ! -e macos/MCPaste/Sources/MCPasteCore/MCPasteAPI.swift
go mod tidy -diff
go test -race ./...
cd macos/MCPaste && swift test && swift build -c release
```

Expected: every command exits 0; Go tests cover only `cmd/mcpaste`, `internal/connector`, and `internal/peer` plus any standard helper package intentionally retained.

- [ ] **Step 6: Commit hosted removal after explicit approval**

```bash
git add -A
git commit -m "refactor: remove hosted MCPaste infrastructure"
```

## Task 11: Update CI, release, installer, and current documentation

**Files:**

- Modify: `.github/workflows/ci.yml`
- Delete: `.github/workflows/deploy.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `.github/workflows/macos-release.yml`
- Modify: `install.sh`
- Modify: `README.md`
- Modify: `SECURITY.md`
- Modify: `docs/security-and-secrets.md`
- Modify: `docs/releases.md`
- Delete: `docs/operations.md`
- Modify: `THIRD_PARTY_NOTICES.md` if `go mod tidy` changes bundled notices

- [ ] **Step 1: Add repository contract checks before rewriting docs**

Create a shell check inside the CI workflow that fails if current product files contain hosted terms:

```bash
if git grep -n -E 'DigitalOcean|PostgreSQL|MCPASTE_ENDPOINT|recovery code|pairing code|Linux companion' -- README.md SECURITY.md docs install.sh .github/workflows macos/MCPaste/Sources cmd internal ':!docs/superpowers/**'; then
  printf '%s\n' 'Hosted architecture reference remains in a current product file.' >&2
  exit 1
fi
```

Exclude `docs/superpowers/` because it contains historical records and the superseded design for local reference.

- [ ] **Step 2: Simplify CI**

Remove PostgreSQL services, migrations, container build, deploy-script checks, endpoint generation, and hosted package lists. CI must run:

```bash
go mod tidy -diff
go mod verify
test -z "$(gofmt -l cmd/mcpaste internal/connector internal/peer)"
go vet ./cmd/mcpaste ./internal/connector ./internal/peer
go test -race ./cmd/mcpaste ./internal/connector ./internal/peer
(cd macos/MCPaste && swift test && swift build -c release)
```

Keep the existing pinned action SHAs, least-privilege permissions, and secret scan.

- [ ] **Step 3: Make releases macOS-only**

Remove Linux/server/migration artifacts and `MCPASTE_ENDPOINT` linker values. The release builds the bundled Darwin helper directly from `./cmd/mcpaste`, embeds it in `Contents/Helpers/mcpaste`, signs helper before app, and retains checksum/signing/notarization behavior. `install.sh` supports Darwin only and states: install Tailscale, open MCPaste, then restart the desired MCP client after registration.

- [ ] **Step 4: Rewrite current docs around the approved product**

README must include the simple architecture diagram, same-tailnet prerequisite, one current context, automatic replacement, four English states, generic MCP command, no history/cloud/S3, Mac-only scope, and ephemeral lifetime. SECURITY must define tailnet membership plus the local loopback token as trust boundaries. Releases must describe only the macOS app and embedded helper. Delete hosted operations because there is no service to operate.

- [ ] **Step 5: Run docs, source, workflow, and secret checks**

Run:

```bash
bash -n install.sh
git grep -n -E 'DigitalOcean|PostgreSQL|MCPASTE_ENDPOINT|recovery code|pairing code|Linux companion' -- README.md SECURITY.md docs install.sh .github/workflows macos/MCPaste/Sources cmd internal ':!docs/superpowers/**'
git grep -n -E 'sk-[A-Za-z0-9]{12,}|ghp_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY' -- . ':!.github/workflows/ci.yml' ':!docs/security-and-secrets.md' ':!docs/superpowers/**'
```

Expected: both `git grep` commands exit 1 with no matches; `bash -n` exits 0.

- [ ] **Step 6: Commit docs and workflows after approval**

```bash
git add .github install.sh README.md SECURITY.md docs THIRD_PARTY_NOTICES.md
git commit -m "docs: describe local tailnet MCPaste"
```

## Task 12: End-to-end verification and two-Mac acceptance

**Files:**

- Create: `docs/superpowers/records/2026-08-18-tailnet-peer-context.md`

- [ ] **Step 1: Run the complete automated suite from repository root**

```bash
go mod tidy -diff
go mod verify
test -z "$(gofmt -l cmd/mcpaste internal/connector internal/peer)"
go vet ./cmd/mcpaste ./internal/connector ./internal/peer
go test -race ./...
(cd macos/MCPaste && swift test && swift build -c release)
bash -n install.sh
```

Expected: every command exits 0 with no race, vet, formatting, package, build, or shell syntax failure.

- [ ] **Step 2: Run a local process-level smoke test**

Start two peer runtimes with injected fixture discovery, distinct loopback ports, separate temporary credential/registry files, and held stdin pipes. Publish exact text plus PNG/JPEG assets to A, wait for B to converge, invoke the STDIO MCP connector against B, and assert exact text and ordered image content. Close A's stdin and assert B returns `source_offline`; publish from B and assert its connector succeeds; close both pipes and verify both processes exit within two seconds.

- [ ] **Step 3: Inspect UI snapshots**

Open the generated main-window and popover PNGs with the local image viewer. Verify the existing native tone, no sidebar/history, option-A local row, source badge, offline dimming, English state copy, and absence of onboarding/approval controls. Record artifact paths, dimensions, and observed result.

- [ ] **Step 4: Perform the owner-assisted two-Mac acceptance**

This is the only step that requires the owner's two tailnet Macs:

1. Launch Tailscale and the same MCPaste build on A and B.
2. Enter exact multiline text and two images on A.
3. Confirm B updates within five seconds and marks A as source.
4. Ask an MCP-compatible client on B to call `get_latest_paste`; verify exact text and ordered images.
5. Edit on B; confirm A updates and source moves to B.
6. Disconnect B from Tailscale; confirm A shows `Source offline` and its MCP read refuses stale data.
7. Edit on A while disconnected; confirm `Waiting to sync`, reconnect, and verify convergence.
8. Quit every MCPaste app, reopen both, and confirm the context is empty while known device names remain.

- [ ] **Step 5: Write the evidence record**

Record goal, branch/worktree, exact commits if authorized, files created/modified/deleted, automated command outputs and counts, snapshot inspection, two-Mac results, any deviations, remaining risks, and the explicit statement that no push/deploy/tag/release occurred.

- [ ] **Step 6: Request final code review**

Use `superpowers:requesting-code-review`, then address findings with `superpowers:receiving-code-review`. Re-run the complete automated suite after every accepted fix.

- [ ] **Step 7: Commit the verification record after approval**

```bash
git add docs/superpowers/records/2026-08-18-tailnet-peer-context.md
git commit -m "docs: record tailnet peer verification"
```
