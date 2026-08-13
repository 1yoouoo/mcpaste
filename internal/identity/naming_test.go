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
	invalid := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "whitespace", value: "   "},
		{name: "line feed", value: "bad\nname"},
		{name: "delete", value: "bad\u007fname"},
		{name: "too long", value: strings.Repeat("가", 81)},
	}
	for _, item := range invalid {
		t.Run(item.name, func(t *testing.T) {
			if _, err := NormalizeDisplayName(item.value); err == nil {
				t.Fatal("NormalizeDisplayName() accepted rejected input")
			}
		})
	}
}

func TestNormalizeDisplayNameNormalizesNFC(t *testing.T) {
	got, err := NormalizeDisplayName("  Cafe\u0301  ")
	if err != nil {
		t.Fatalf("NormalizeDisplayName() error = %v", err)
	}
	if got != "Caf\u00e9" {
		t.Fatalf("NormalizeDisplayName() did not normalize to NFC")
	}
}

func TestNormalizeDisplayNameRejectsDeceptiveUnicode(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "zero width format", value: "Build\u200bHost"},
		{name: "bidi format", value: "Build\u202eHost"},
		{name: "line separator", value: "Build\u2028Host"},
		{name: "paragraph separator", value: "Build\u2029Host"},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if _, err := NormalizeDisplayName(item.value); err == nil {
				t.Fatal("NormalizeDisplayName() accepted rejected Unicode category")
			}
		})
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
