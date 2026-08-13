package identity

import (
	"testing"
	"time"
)

func TestRetentionExpiryMatchesPostgreSQLOnLeapDay(t *testing.T) {
	leapDay := time.Date(2024, time.February, 29, 12, 0, 0, 0, time.UTC)
	want := time.Date(2025, time.February, 28, 12, 0, 0, 0, time.UTC)
	if got := retentionExpiry(leapDay); !got.Equal(want) {
		t.Fatalf("retentionExpiry(leap day) = %s, want %s", got, want)
	}
}
