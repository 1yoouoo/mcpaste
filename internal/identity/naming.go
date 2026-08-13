package identity

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const maxDisplayNameRunes = 80

func NormalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = norm.NFC.String(value)
	runes := []rune(value)
	if len(runes) < 1 || len(runes) > maxDisplayNameRunes {
		return "", ErrInvalid
	}
	for _, current := range runes {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf, unicode.Zl, unicode.Zp) {
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
