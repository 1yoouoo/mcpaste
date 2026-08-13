package identity

import (
	"fmt"
	"strings"
	"unicode"
)

const maxDisplayNameRunes = 80

func NormalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) < 1 || len(runes) > maxDisplayNameRunes {
		return "", ErrInvalid
	}
	for _, current := range runes {
		if unicode.IsControl(current) {
			return "", ErrInvalid
		}
	}
	return value, nil
}

func DisplayNameCandidate(base string, attempt int) string {
	if attempt <= 1 {
		return base
	}
	suffix := fmt.Sprintf(" (%d)", attempt)
	baseRunes := []rune(base)
	maximumBase := maxDisplayNameRunes - len([]rune(suffix))
	if len(baseRunes) > maximumBase {
		baseRunes = baseRunes[:maximumBase]
	}
	return strings.TrimSpace(string(baseRunes)) + suffix
}
