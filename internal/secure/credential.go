package secure

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

const credentialDomain = "mcpaste-credential-v1"
const claimDomain = "mcpaste-pairing-claim-v1"
const canonicalSecretFormatLength = 108

type IssuedCredential struct {
	Kind    string
	Token   string
	Locator string
	Hash    []byte
}

type ParsedCredential struct {
	WorkspaceID string
	Locator     string
	Hash        []byte
}

func NewCredential(workspaceID, kind string, random Random) (IssuedCredential, error) {
	if !validUUID(workspaceID) || (kind != "full" && kind != "connector") {
		return IssuedCredential{}, errors.New("credential metadata is invalid")
	}
	locatorBytes, err := randomBytes(random, 16)
	if err != nil {
		return IssuedCredential{}, err
	}
	secret, err := randomBytes(random, 32)
	if err != nil {
		return IssuedCredential{}, err
	}
	locator := base64.RawURLEncoding.EncodeToString(locatorBytes)
	token := "mcp1." + workspaceID + "." + locator + "." + base64.RawURLEncoding.EncodeToString(secret)
	return IssuedCredential{Kind: kind, Token: token, Locator: locator, Hash: hashSecret(credentialDomain, secret)}, nil
}

func ParseCredential(token string) (ParsedCredential, error) {
	parts, ok := cutCanonicalSecretFormat(token)
	if !ok || parts[0] != "mcp1" || !validUUID(parts[1]) {
		return ParsedCredential{}, errors.New("credential is invalid")
	}
	if _, err := decodeCanonicalRawURL(parts[2], 16); err != nil {
		return ParsedCredential{}, errors.New("credential is invalid")
	}
	secret, err := decodeCanonicalRawURL(parts[3], 32)
	if err != nil {
		return ParsedCredential{}, errors.New("credential is invalid")
	}
	return ParsedCredential{WorkspaceID: parts[1], Locator: parts[2], Hash: hashSecret(credentialDomain, secret)}, nil
}

func cutCanonicalSecretFormat(value string) ([4]string, bool) {
	var parts [4]string
	if len(value) != canonicalSecretFormatLength {
		return parts, false
	}
	remainder := value
	for index := 0; index < len(parts)-1; index++ {
		part, rest, ok := strings.Cut(remainder, ".")
		if !ok {
			return parts, false
		}
		parts[index] = part
		remainder = rest
	}
	if _, _, ok := strings.Cut(remainder, "."); ok {
		return parts, false
	}
	parts[len(parts)-1] = remainder
	return parts, true
}

func NewClaimSecret(random Random) (string, []byte, error) {
	secret, err := randomBytes(random, 32)
	if err != nil {
		return "", nil, err
	}
	return base64.RawURLEncoding.EncodeToString(secret), hashSecret(claimDomain, secret), nil
}

func HashClaimSecret(value string) ([]byte, error) {
	secret, err := decodeCanonicalRawURL(value, 32)
	if err != nil {
		return nil, errors.New("claim secret is invalid")
	}
	return hashSecret(claimDomain, secret), nil
}

func hashSecret(domain string, secret []byte) []byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(secret)
	return digest.Sum(nil)
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16 && strings.ToLower(value) == value
}
