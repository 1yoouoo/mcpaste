package httpserver

import (
	"net/http"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/mcpserver"
)

func (s *apiServer) mcpEndpoint(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil && principal.Scope != "connector" {
		err = identity.ErrForbidden
	}
	if err != nil {
		writeError(w, err)
		return
	}
	s.mcp.ServeHTTP(w, r.WithContext(mcpserver.WithPrincipal(r.Context(), principal)))
}
