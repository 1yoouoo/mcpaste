package peer

import (
	"errors"
	"math"
	"sync"
	"time"
)

var ErrClockExhausted = errors.New("clock exhausted")

type Clock struct {
	mu         sync.Mutex
	deviceID   string
	now        func() time.Time
	wallMillis int64
	logical    uint32
}

func NewClock(deviceID string, now func() time.Time) *Clock {
	if deviceID == "" {
		panic("peer: empty device ID")
	}
	if now == nil {
		panic("peer: nil clock")
	}
	return &Clock{
		deviceID: deviceID,
		now:      now,
	}
}

// Observe incorporates a remote revision. It returns false for the reserved
// terminal wall, which no clock may emit.
func (c *Clock) Observe(remote Revision) bool {
	if remote.WallMillis == math.MaxInt64 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	nowMillis := c.now().UnixMilli()
	if nowMillis == math.MaxInt64 {
		return false
	}
	if nowMillis > c.wallMillis {
		c.wallMillis = nowMillis
		c.logical = 0
	}
	if remote.WallMillis > c.wallMillis {
		c.wallMillis = remote.WallMillis
		c.logical = remote.Logical
	} else if remote.WallMillis == c.wallMillis && remote.Logical > c.logical {
		c.logical = remote.Logical
	}
	return true
}

func (c *Clock) Tick() Revision {
	revision, err := c.TryTick()
	if err != nil {
		panic("peer: logical clock exhausted")
	}
	return revision
}

func (c *Clock) TryTick() (Revision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.wallMillis == math.MaxInt64 {
		return Revision{}, ErrClockExhausted
	}

	nowMillis := c.now().UnixMilli()
	if nowMillis == math.MaxInt64 {
		return Revision{}, ErrClockExhausted
	}
	if nowMillis > c.wallMillis {
		c.wallMillis = nowMillis
		c.logical = 0
	} else if c.logical == math.MaxUint32 {
		if c.wallMillis == math.MaxInt64-1 {
			return Revision{}, ErrClockExhausted
		}
		c.wallMillis++
		c.logical = 0
	} else {
		c.logical++
	}
	return Revision{
		WallMillis: c.wallMillis,
		Logical:    c.logical,
		DeviceID:   c.deviceID,
	}, nil
}
