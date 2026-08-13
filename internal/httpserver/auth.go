package httpserver

import (
	"context"
	"net/http"
	"strings"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

type identityAPI interface {
	Authenticate(context.Context, string) (identity.Principal, error)
	CreateWorkspace(context.Context, string, string, identity.CreateWorkspaceInput) (identity.Result, error)
	CreatePairing(context.Context, string, string, identity.CreatePairingInput) (identity.Result, error)
	PairingByID(context.Context, identity.Principal, string) (identity.PairingDetails, error)
	PairingByShortCode(context.Context, identity.Principal, string) (identity.PairingDetails, error)
	ApprovePairing(context.Context, identity.Principal, string, string) (identity.Result, error)
	ClaimPairing(context.Context, string, string, string) (identity.Result, error)
	ListDevices(context.Context, identity.Principal) ([]identity.DeviceSummary, error)
	RenameDevice(context.Context, identity.Principal, string, string, identity.RenameInput) (identity.Result, error)
	RevokeDevice(context.Context, identity.Principal, string, string) (identity.Result, error)
	Recover(context.Context, string, string, identity.RecoveryInput) (identity.Result, error)
}

func authenticate(r *http.Request, service identityAPI) (identity.Principal, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.Count(values[0], " ") != 1 {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	return service.Authenticate(r.Context(), token)
}

func requireFull(principal identity.Principal) error {
	if principal.Scope != "full" {
		return identity.ErrForbidden
	}
	return nil
}
