# MCPaste Tailnet Peer Context Design

**Status:** Approved

**Date:** 2026-08-18

**Supersedes:** `2026-08-12-mcpaste-system-design.md` and `2026-08-14-official-endpoint-design.md` for the supported product architecture

## 1. Purpose

MCPaste is a local-first macOS app that makes one temporary text-and-image context available to MCP-compatible AI clients across a user's Tailscale network. Any running MCPaste Mac can replace that context. There is no permanent host, cloud service, workspace account, pairing code, database, S3 bucket, or retained paste history.

The intended interaction is deliberately small:

1. Run Tailscale and MCPaste on two or more Macs in the same tailnet.
2. Enter text or add images on any one Mac.
3. Within a few seconds, every reachable MCPaste Mac shows the same current context.
4. Codex, Claude Code, or another MCP-compatible client running on any reachable Mac reads that context through its local MCPaste connector.

## 2. Success criteria

The redesign is successful when all of the following are true:

1. Two or more Macs on one tailnet discover each other without selecting a host or entering a pairing code.
2. The app exposes one current context containing exact text plus zero or more normalized static images.
3. Editing on any Mac automatically publishes a complete replacement snapshot after a short debounce; there is no Share or Save action.
4. Reachable peers converge on the same deterministic latest revision after ordinary edits, simultaneous edits, offline edits, and reconnection.
5. A remote update replaces the open editor on every peer and identifies the updating Mac without adding a conflict-resolution workflow.
6. An MCP client can retrieve the current text and images only while the current source Mac is reachable.
7. No context body or image survives after every MCPaste process that held a copy has exited.
8. The app preserves its existing native macOS visual tone while removing history, onboarding, recovery, pairing, and approval UI.
9. The bundled STDIO connector remains client-neutral MCP; support is not coupled to Codex alone.
10. The repository no longer builds, deploys, or documents the superseded central server, PostgreSQL, S3, or Linux companion product.

## 3. Scope

### Included

- Native SwiftUI macOS authoring app
- N peer Macs in the same Tailscale tailnet
- One in-memory current context containing exact text and up to the existing image limit
- Automatic text debounce and atomic image publication
- Deterministic last-write-wins convergence
- Offline local editing and convergence after reconnect
- Automatic peer discovery using the locally installed Tailscale CLI
- A small peer service reachable only from loopback and discovered tailnet peers
- A bundled client-neutral STDIO MCP connector
- Automatic configuration for the MCP clients already supported by the repository, plus documented generic command configuration
- Persistent non-content metadata only: local device ID, display name, known peer names, last-seen timestamps, and the local connector credential

### Excluded

- Central cloud service, public API, PostgreSQL, SQLite content cache, S3, deployment infrastructure, and retention workers
- Accounts, workspaces, recovery codes, QR codes, pairing codes, approval, revocation, and device roles
- Permanent content or image history
- Linux authoring peers or a headless Linux companion
- Internet discovery outside Tailscale
- Multi-user collaboration, permissions finer than tailnet membership, and per-device sharing controls
- Conflict dialogs, merge UI, named contexts, folders, search, or version history
- AI write access; MCP remains read-only

## 4. Architecture

```text
                         one tailnet

  MacBook A                                           Mac mini B
  +----------------------------+                     +----------------------------+
  | SwiftUI editor             |                     | SwiftUI editor             |
  | bundled peer runtime       |<-- peer protocol -->| bundled peer runtime       |
  | - in-memory context        |                     | - in-memory context        |
  | - Tailscale discovery      |                     | - Tailscale discovery      |
  | - local/peer HTTP service  |                     | - local/peer HTTP service  |
  +-------------+--------------+                     +-------------+--------------+
                ^                                                  ^
                | same binary, STDIO MCP mode                      | same binary, STDIO MCP mode
        +-------+--------+                                 +-------+--------+
        | MCP connector  |                                 | MCP connector  |
        +-------+--------+                                 +-------+--------+
                ^                                                  ^
        MCP-compatible AI                                 MCP-compatible AI
```

Every app bundle has the same responsibilities. No peer is elected leader and no hostname is configured as a server. The Mac that most recently produced the winning revision is the current source, but that role moves automatically with the next edit.

The existing bundled Go `mcpaste` executable gains a peer-runtime mode and owns discovery, replication, the in-memory context, and HTTP protocol handling. Swift owns native UI, image normalization, and local user intent. The app launches the runtime with a held stdin pipe; EOF shuts the runtime down after a normal quit or crash, preventing an orphaned process from extending content lifetime. The same binary continues to run in STDIO MCP mode when launched by an AI client and reads from the runtime over authenticated loopback.

## 5. Current-context model

The replicated unit is one complete `ContextEnvelope`:

- `protocolVersion`
- `revision`
- `sourceDeviceID`
- `updatedAt`
- exact `text`
- ordered image manifest
- normalized image bytes held separately in memory

Each revision represents the entire text-and-image snapshot. Text and images never merge independently across devices. Replacing text, adding or removing an image, or starting a blank context creates a new complete revision.

An empty envelope is a valid revision used by the new-context action. It clears the current context across reachable peers but creates no history item.

### 5.1 Revision ordering

Revisions use a hybrid logical clock tuple:

```text
(physical milliseconds, logical counter, source device UUID)
```

The physical component approximates the user's intended “most recent edit” across devices. The logical component preserves ordering when several writes happen within one clock tick or after a remote revision is observed. The device UUID is the deterministic final tie-breaker.

The ordering rule is intentionally last-write-wins. A losing simultaneous edit is overwritten without a merge dialog. System clock skew can affect which offline edit wins, so macOS automatic time should remain enabled; deterministic convergence is guaranteed even when wall clocks are imperfect.

### 5.2 In-memory lifetime

Context text and normalized image bytes are never written to SQLite, files, UserDefaults, logs, crash annotations, or connector configuration. Each running app keeps only the current winning snapshot in memory.

Peers may hold replicas so their editors update immediately and a restarted source can recover from another still-running peer. MCP reads are nevertheless gated on current-source reachability. If every process holding the snapshot exits, the context is gone. Restarting later begins empty unless another still-running peer can provide a replica.

## 6. Discovery and presence

The bundled peer runtime polls `tailscale status --json` every three seconds through an injectable command runner. It reads only the local node state, peer node names, and Tailscale IP addresses needed for discovery. It does not use the Tailscale administrative REST API and therefore needs no tailnet API key.

For each candidate peer address, the app probes the fixed MCPaste peer port with a short timeout. A successful versioned health response proves that MCPaste, not merely Tailscale, is running on that Mac. Discovery therefore has two layers:

1. Tailscale knows the device and provides a private route.
2. MCPaste responds with its stable device UUID, display name, protocol version, and current revision metadata.

Only Macs that have successfully answered an MCPaste health probe are added to the product's device registry. The registry persists device ID, display name, last-seen time, and last known addresses so a previously seen Mac may remain visible in an offline state. Tailnet phones, servers, and unrelated devices never appear merely because Tailscale lists them.

The runtime listener accepts requests only from loopback or addresses present in the current Tailscale status snapshot. Tailscale membership is the approved trust boundary; there is no second MCPaste pairing ceremony.

## 7. Peer protocol and synchronization

The bundled Go peer runtime hosts a small versioned HTTP/1.1 service on a fixed port using the standard library. This reuses the already bundled cross-client helper, keeps socket and HTTP parsing out of SwiftUI state, and allows deterministic loopback integration tests. The protocol is private to MCPaste and contains no public-cloud endpoint.

Required routes are:

- `GET /v1/health`: peer identity, protocol version, source ID, and winning revision metadata
- `GET /v1/context`: exact text and ordered image manifest for the winning in-memory snapshot
- `GET /v1/context/assets/{index}`: one normalized image body with MIME type, byte length, and digest verification
- `POST /v1/announce`: a lightweight revision announcement that prompts an immediate comparison and fetch
- `PUT /v1/local/assets/{sha256}`: loopback-only staging of a normalized image, authorized by the local connector token
- `PUT /v1/local/context`: loopback-only atomic publication of exact text plus an ordered list of staged/current asset digests, authorized by the local connector token
- `GET /v1/local/context`: loopback-only app/connector manifest, source reachability, and sync state, authorized by the local connector token
- `GET /v1/local/context/assets/{index}`: loopback-only connector image bytes, authorized by the same token
- `GET /v1/local/devices`: loopback-only current and previously discovered MCPaste Macs for the native device UI

Text and manifests are JSON. Image bytes use their normalized binary representation rather than base64 in JSON. The Swift app stages a newly normalized image once by digest, then publishes a small manifest that references ordered digests; ordinary text edits therefore do not resend unchanged image bytes. Receivers bound response sizes before decoding, fetch at most three assets concurrently, verify declared size and SHA-256 digest, and publish the assembled snapshot only after every asset succeeds. A partial or invalid transfer never replaces the previous snapshot. Unreferenced staged assets expire from memory after thirty seconds.

Synchronization combines push hints with polling:

1. A local edit creates a revision immediately in memory.
2. Text announcements are debounced for about one second after the last keystroke.
3. Image publication waits for normalization and then creates one atomic snapshot.
4. The source sends `POST /v1/announce` to every reachable peer.
5. A receiver fetches only when the announced revision outranks its own.
6. The three-second discovery/health loop is the recovery path for missed announcements and partitions.
7. On reconnection, every peer compares revision tuples and fetches the winner, so all reachable peers converge.

Peers never rebroadcast an identical revision indefinitely. A fetched winning revision may be announced once to other peers that have not advertised it, allowing convergence when the original source cannot directly reach every node.

## 8. Offline and failure behavior

- **Local edit while disconnected:** The local app accepts it, becomes the source, and shows `Waiting to sync`. The snapshot exists only in that process until another peer receives it.
- **Reconnection:** Revision comparison selects one winner and reachable editors update automatically.
- **Current source disappears:** The local manifest remains readable with `source_reachable: false` so the native editor can display the in-memory replica for recovery, but the STDIO connector refuses it and returns a structured unavailable result. The UI shows `Source offline`.
- **User edits an offline-source replica:** That intentional edit creates a new local revision, makes the editing Mac the source, and permits local MCP reads again.
- **Tailscale unavailable:** Local editing and the local MCP connector continue to work when this Mac is the source. Cross-device sync shows `Waiting to sync`.
- **Invalid peer response or asset:** The app keeps the previous complete snapshot, marks that peer temporarily unhealthy, and retries through the next health poll.
- **Protocol-version mismatch:** The peer remains listed but unavailable; the UI uses the existing compact error-banner treatment for an actionable app-update message.

No stale replica is returned to MCP while its source is offline. This is the same conservative rule as the previous connector's no-stale fallback, applied without a central service.

## 9. Local MCP connector

The existing Go `mcpaste` executable remains a client-neutral STDIO MCP server. It no longer proxies a hosted Streamable HTTP MCP endpoint and no longer performs setup, login, pairing, approval, or cloud credential flows.

The macOS app generates a random local connector token, stores it with owner-only file permissions, and gives the token file path to its bundled runtime without putting the token in process arguments. The app then runs the connector's registration command for supported client configurations. The token protects context from unrelated local processes that merely know the loopback port; it never leaves the Mac.

On `get_latest_paste`, the connector:

1. asks the local app for the current manifest;
2. returns a structured unavailable result when the app is absent, there is no current context, or the current source is offline;
3. fetches and verifies each image from the local app;
4. returns exact text and ordered MCP image content blocks.

The tool name remains `get_latest_paste` for compatibility, while its description changes to “Retrieve the current MCPaste context.” The connector does not inspect or authorize by model brand. Codex, Claude Code, and any MCP-compatible client may launch the same command. Automatic config writing remains limited to formats the repository already knows; other clients use the documented STDIO command.

## 10. macOS user experience

### 10.1 Main window

The history sidebar and paste selection are removed. The window contains only the existing detail surface:

- header title: `Current context`
- subtitle: `Changes sync automatically`, or briefly `Updated from <device> · just now`
- compact status pill: `Up to date`, `Updating…`, `Waiting to sync`, or `Source offline`
- existing monospaced editor
- existing image attachment strip and normalization progress
- existing exact-text statistics and MCP connector count
- existing square-and-pencil control repurposed as “start a blank shared context”

Remote winning revisions replace the open text and images immediately. The app does not add a conflict banner or confirmation step.

### 10.2 Menu-bar popover

The existing popover structure, type scale, spacing, plain rows, and dividers remain. Workspace, recovery, registration-code, approval, and revoke controls disappear.

The device list follows approved visual option A:

- the local Mac uses a quiet system-blue selected-row background and blue icon;
- the current source gets a small green badge on its device icon;
- reachable peers use the normal row treatment and a small green presence dot;
- unavailable peers are dimmed with a gray presence dot;
- no row says `This Mac` or `Source`.

AI tool connections remain a separate compact section. Device rows include only current or previously discovered MCPaste Macs, never every Tailscale node.

## 11. Security and privacy

- Tailscale encryption protects peer traffic in transit; MCPaste does not add application TLS inside the tailnet.
- Same-tailnet membership is explicit trust. Any tailnet device capable of reaching the peer port is within the network trust boundary, although the listener rejects addresses absent from the current Tailscale snapshot.
- The local MCP connector uses a random loopback bearer token stored with mode `0600`.
- Context text, image bytes, tokens, complete peer responses, and Tailscale status JSON are never logged.
- Decoders enforce request, text, manifest, asset-count, per-asset, and complete-bundle limits before allocation.
- Peer display names are untrusted text and are never interpolated into shell commands.
- Tailscale commands use an executable plus argument array, never a shell string.
- MCP remains read-only.

## 12. Migration from the hosted architecture

The migration is a replacement, not a compatibility layer:

1. Add and test the bundled Go peer runtime, in-memory context model, discovery, peer service, synchronization, and local connector boundary.
2. Add the Swift runtime launcher/local client and replace `AppModel`'s workspace/API/session dependencies with them.
3. Remove onboarding, recovery, history, pairing, approval, and server-derived device flows from the app.
4. Rewrite the Go connector to read from the local app and remove cloud commands and credentials.
5. Remove central-server commands and packages, migrations, Docker/deployment files, endpoint injection, PostgreSQL/S3 dependencies, and obsolete tests.
6. Rewrite README, security, release, and CI documentation around the local Mac-only product.

The old hosted API and new peer protocol do not interoperate. No production data migration is offered because the approved product explicitly treats the new context as ephemeral and history-free.

## 13. Verification

Automated verification must cover:

- hybrid-clock ordering and deterministic ties;
- whole-snapshot replacement and empty-context clearing;
- two- and three-peer convergence;
- missed announcements recovered by polling;
- simultaneous and offline edits converging after reconnect;
- source-offline MCP refusal and source-return recovery;
- Tailscale JSON parsing, missing CLI, malformed output, IPv4, and IPv6;
- listener address allowlisting, loopback token authorization, path parsing, body limits, and digest validation;
- image normalization and atomic assembly;
- generic MCP text and image result construction;
- no history/onboarding/approval UI and the four exact English state strings;
- existing-tone snapshot renders for the main window and option-A device rows;
- absence of server, database, S3, hosted endpoint, pairing, and deployment references from the supported build and current documentation.

Manual acceptance requires two tailnet-connected Macs. Edit on A, read through an MCP client on B, reverse the source by editing on B, disconnect and reconnect one peer, verify `Source offline`, and finally quit every app to confirm that reopening starts empty.
