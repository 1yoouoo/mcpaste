package peer

import "time"

const (
	ProtocolVersion = 1
	DefaultPort     = 38421
	MaxTextBytes    = 4 << 20
	MaxAssets       = 8
	MaxAssetBytes   = 8 << 20
	MaxBundleBytes  = 32 << 20
	// MaxContextJSONBytes allows MaxTextBytes to be encoded entirely as
	// six-byte JSON Unicode escapes, with bounded room for context metadata.
	MaxContextJSONBytes = MaxTextBytes*6 + 64<<10
)

type Revision struct {
	WallMillis int64  `json:"wall_millis"`
	Logical    uint32 `json:"logical"`
	DeviceID   string `json:"device_id"`
}

func (r Revision) Compare(other Revision) int {
	if r.WallMillis < other.WallMillis {
		return -1
	}
	if r.WallMillis > other.WallMillis {
		return 1
	}
	if r.Logical < other.Logical {
		return -1
	}
	if r.Logical > other.Logical {
		return 1
	}
	if r.DeviceID < other.DeviceID {
		return -1
	}
	if r.DeviceID > other.DeviceID {
		return 1
	}
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
	SyncUpToDate      SyncState = "up_to_date"
	SyncUpdating      SyncState = "updating"
	SyncWaiting       SyncState = "waiting_to_sync"
	SyncSourceOffline SyncState = "source_offline"
)

type LocalContextResponse struct {
	ContextManifest
	SourceReachable bool      `json:"source_reachable"`
	SyncState       SyncState `json:"sync_state"`
}

type PublicationResult struct {
	Revision  Revision  `json:"revision"`
	SyncState SyncState `json:"sync_state"`
}

type Snapshot struct {
	Manifest ContextManifest
	Assets   map[string][]byte
}
