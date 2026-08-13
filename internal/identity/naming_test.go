package identity

import (
	"strings"
	"testing"
)

func TestNormalizeDisplayName(t *testing.T) {
	got, err := NormalizeDisplayName("  MacBook Pro  ")
	if err != nil || got != "MacBook Pro" {
		t.Fatalf("NormalizeDisplayName() = %q, %v", got, err)
	}
	for _, value := range []string{"", "   ", "bad\nname", "bad\u007fname", strings.Repeat("가", 81)} {
		if _, err := NormalizeDisplayName(value); err == nil {
			t.Fatalf("NormalizeDisplayName(%q) error = nil", value)
		}
	}
}

func TestDisplayNameCandidateUsesSmallestSuffixAndLengthLimit(t *testing.T) {
	if got := DisplayNameCandidate("MacBook Pro", 1); got != "MacBook Pro" {
		t.Fatalf("attempt 1 = %q", got)
	}
	if got := DisplayNameCandidate("MacBook Pro", 2); got != "MacBook Pro (2)" {
		t.Fatalf("attempt 2 = %q", got)
	}
	got := DisplayNameCandidate(strings.Repeat("가", 80), 12)
	if len([]rune(got)) != 80 || !strings.HasSuffix(got, " (12)") {
		t.Fatalf("length-limited candidate = %q (%d runes)", got, len([]rune(got)))
	}
}
