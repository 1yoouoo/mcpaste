package secure

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRecoveryRoundTripWrongCodeAndCorruption(t *testing.T) {
	randomInput := make([]byte, 64)
	copy(randomInput[:16], bytes.Repeat([]byte{0x21}, 16))
	copy(randomInput[16:48], bytes.Repeat([]byte{0x32}, 32))
	copy(randomInput[48:], bytes.Repeat([]byte{0x43}, 16))
	issued, err := NewRecovery(context.Background(), testWorkspaceID, bytes.NewReader(randomInput))
	if err != nil {
		t.Fatalf("NewRecovery() error = %v", err)
	}
	workspaceID, locator, err := RecoveryLocator(issued.Code)
	if err != nil || workspaceID != testWorkspaceID || locator != issued.Locator {
		t.Fatalf("RecoveryLocator() = %q, %q, %v", workspaceID, locator, err)
	}
	if err := VerifyRecovery(context.Background(), issued.Code, testWorkspaceID, issued.Locator, issued.Verifier); err != nil {
		t.Fatalf("VerifyRecovery() error = %v", err)
	}
	wrong := issued.Code[:len(issued.Code)-1] + "A"
	if err := VerifyRecovery(context.Background(), wrong, testWorkspaceID, issued.Locator, issued.Verifier); err == nil {
		t.Fatal("wrong recovery code verified")
	}
	corrupt := issued.Verifier
	corrupt.Hash = []byte{0x01}
	if err := VerifyRecovery(context.Background(), issued.Code, testWorkspaceID, issued.Locator, corrupt); err == nil {
		t.Fatal("corrupt verifier accepted")
	}
}

func TestRecoveryRejectsWrongLocatorAndParameters(t *testing.T) {
	randomInput := make([]byte, 64)
	copy(randomInput[:16], bytes.Repeat([]byte{0x51}, 16))
	copy(randomInput[16:48], bytes.Repeat([]byte{0x62}, 32))
	copy(randomInput[48:], bytes.Repeat([]byte{0x73}, 16))
	issued, err := NewRecovery(context.Background(), testWorkspaceID, bytes.NewReader(randomInput))
	if err != nil {
		t.Fatalf("NewRecovery() error = %v", err)
	}
	if err := VerifyRecovery(context.Background(), issued.Code, testWorkspaceID, "AAAAAAAAAAAAAAAAAAAAAA", issued.Verifier); err == nil {
		t.Fatal("wrong locator verified")
	}
	changed := issued.Verifier
	changed.MemoryKiB = 32768
	if err := VerifyRecovery(context.Background(), issued.Code, testWorkspaceID, issued.Locator, changed); err == nil {
		t.Fatal("unsupported Argon2 parameters accepted")
	}
}

func TestRecoveryRejectsMalformedCodesGenerically(t *testing.T) {
	validLocator := strings.Repeat("A", 22)
	validSecret := strings.Repeat("A", 43)
	code := func(workspaceID, locator, secret string) string {
		return strings.Join([]string{"mcr1", workspaceID, locator, secret}, ".")
	}
	tests := []struct {
		name  string
		value string
	}{
		{name: "bad prefix", value: "mcr2." + testWorkspaceID + "." + validLocator + "." + validSecret},
		{name: "noncanonical workspace UUID", value: code("AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", validLocator, validSecret)},
		{name: "bad workspace UUID", value: code("not-a-workspace-uuid", validLocator, validSecret)},
		{name: "wrong segment count", value: strings.Join([]string{"mcr1", testWorkspaceID, validLocator}, ".")},
		{name: "locator wrong length", value: code(testWorkspaceID, strings.Repeat("A", 21), validSecret)},
		{name: "locator padding", value: code(testWorkspaceID, validLocator+"=", validSecret)},
		{name: "secret wrong length", value: code(testWorkspaceID, validLocator, strings.Repeat("A", 42))},
		{name: "secret padding", value: code(testWorkspaceID, validLocator, validSecret+"=")},
		{name: "invalid locator base64", value: code(testWorkspaceID, strings.Repeat("!", 22), validSecret)},
		{name: "invalid secret base64", value: code(testWorkspaceID, validLocator, strings.Repeat("!", 43))},
		{name: "noncanonical locator alias", value: code(testWorkspaceID, strings.Repeat("A", 21)+"B", validSecret)},
		{name: "noncanonical secret alias", value: code(testWorkspaceID, validLocator, strings.Repeat("A", 42)+"B")},
		{name: "locator carriage return", value: code(testWorkspaceID, validLocator[:11]+"\r"+validLocator[11:], validSecret)},
		{name: "locator line feed", value: code(testWorkspaceID, validLocator[:11]+"\n"+validLocator[11:], validSecret)},
		{name: "secret carriage return", value: code(testWorkspaceID, validLocator, validSecret[:21]+"\r"+validSecret[21:])},
		{name: "secret line feed", value: code(testWorkspaceID, validLocator, validSecret[:21]+"\n"+validSecret[21:])},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			_, _, locatorErr := RecoveryLocator(item.value)
			if !errors.Is(locatorErr, ErrInvalidRecovery) {
				t.Fatal("RecoveryLocator did not return generic invalid-recovery error")
			}
			verifyErr := VerifyRecovery(context.Background(), item.value, testWorkspaceID, validLocator, RecoveryVerifier{})
			if !errors.Is(verifyErr, ErrInvalidRecovery) {
				t.Fatal("VerifyRecovery did not return generic invalid-recovery error")
			}
		})
	}
}

func TestNewRecoveryHonorsCanceledContextBeforeArgon2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	randomInput := bytes.NewReader(bytes.Repeat([]byte{0x91}, 64))
	if _, err := NewRecovery(ctx, testWorkspaceID, randomInput); !errors.Is(err, context.Canceled) {
		t.Fatal("NewRecovery() did not return context cancellation")
	}
}

func TestRecoveryPermitSupportsSequentialGenerationAndVerification(t *testing.T) {
	permit, err := AcquireRecoveryPermit(context.Background())
	if err != nil {
		t.Fatal("acquire recovery permit failed")
	}
	defer permit.Release()
	randomInput := make([]byte, 64)
	copy(randomInput[:16], bytes.Repeat([]byte{0xa1}, 16))
	copy(randomInput[16:48], bytes.Repeat([]byte{0xa2}, 32))
	copy(randomInput[48:], bytes.Repeat([]byte{0xa3}, 16))
	issued, err := NewRecoveryWithPermit(context.Background(), permit, testWorkspaceID, bytes.NewReader(randomInput))
	if err != nil {
		t.Fatal("permit-backed recovery generation failed")
	}
	if occupied := len(processArgon2Limiter.slots); occupied != 1 {
		t.Fatalf("slots after generation = %d", occupied)
	}
	if err := VerifyRecoveryWithPermit(context.Background(), permit, issued.Code, testWorkspaceID, issued.Locator, issued.Verifier); err != nil {
		t.Fatal("permit-backed recovery verification failed")
	}
	if occupied := len(processArgon2Limiter.slots); occupied != 1 {
		t.Fatalf("slots after verification = %d", occupied)
	}
}
