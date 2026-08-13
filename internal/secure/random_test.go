package secure

import (
	"context"
	"testing"
)

func TestSecretConstructorsRejectNilRandom(t *testing.T) {
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "credential",
			call: func() error {
				_, err := NewCredential(testWorkspaceID, "full", nil)
				return err
			},
		},
		{
			name: "claim secret",
			call: func() error {
				_, _, err := NewClaimSecret(nil)
				return err
			},
		},
		{
			name: "recovery",
			call: func() error {
				_, err := NewRecovery(context.Background(), testWorkspaceID, nil)
				return err
			},
		},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			err := item.call()
			if err == nil || err.Error() != "random source is invalid" {
				t.Fatal("constructor did not return the generic random-source error")
			}
		})
	}
}
