package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

func TestSyncQueryParsing(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantAfter int64
		wantLimit int
		wantError bool
	}{
		{name: "defaults", path: "/v1/sync", wantLimit: 100},
		{name: "explicit", path: "/v1/sync?after=42&limit=7", wantAfter: 42, wantLimit: 7},
		{name: "caps limit", path: "/v1/sync?limit=1000", wantLimit: 100},
		{name: "negative after", path: "/v1/sync?after=-1", wantError: true},
		{name: "zero limit", path: "/v1/sync?limit=0", wantError: true},
		{name: "duplicate after", path: "/v1/sync?after=1&after=2", wantError: true},
		{name: "unknown parameter", path: "/v1/sync?cursor=1", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			after, limit, err := parseSyncQuery(httptest.NewRequest(http.MethodGet, test.path, nil))
			if (err != nil) != test.wantError || after != test.wantAfter || (!test.wantError && limit != test.wantLimit) {
				t.Fatalf("parseSyncQuery() = %d/%d/%v", after, limit, err)
			}
		})
	}
}

func TestEventCursorUsesLastEventIDFallback(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	request.Header.Set("Last-Event-ID", "17")
	after, err := parseEventCursor(request)
	if err != nil || after != 17 {
		t.Fatalf("parseEventCursor() = %d/%v", after, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/v1/events?after=19", nil)
	request.Header.Set("Last-Event-ID", "17")
	after, err = parseEventCursor(request)
	if err != nil || after != 19 {
		t.Fatalf("query cursor precedence = %d/%v", after, err)
	}
}

type fullSyncSpy struct {
	fakeIdentityAPI
	called bool
}

func (s *fullSyncSpy) Authenticate(context.Context, string) (identity.Principal, error) {
	return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000001", Scope: "full"}, nil
}

func (s *fullSyncSpy) Sync(context.Context, identity.Principal, int64, int) (identity.SyncResponse, error) {
	s.called = true
	return identity.SyncResponse{}, nil
}

func TestSyncRejectsMalformedCursorBeforeService(t *testing.T) {
	service := &fullSyncSpy{}
	request := httptest.NewRequest(http.MethodGet, "/v1/sync?after=not-a-sequence", nil)
	request.Header.Set("Authorization", "Bearer full-runtime-marker")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, service, nil).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.called {
		t.Fatalf("malformed sync response/service call = %d/%t", response.Code, service.called)
	}
}

func TestConnectorCannotOpenEventStream(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/events?after=0", nil)
	request.Header.Set("Authorization", "Bearer connector-runtime-marker")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, &fakeIdentityAPI{}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":{\"code\":\"forbidden\"}}\n" {
		t.Fatalf("connector SSE response = %d/%q", response.Code, response.Body.String())
	}
}
