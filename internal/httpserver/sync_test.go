package httpserver

import (
	"context"
	"encoding/json"
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
	called   bool
	response identity.SyncResponse
}

func (s *fullSyncSpy) Authenticate(context.Context, string) (identity.Principal, error) {
	return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000001", Scope: "full"}, nil
}

func (s *fullSyncSpy) Sync(context.Context, identity.Principal, int64, int) (identity.SyncResponse, error) {
	s.called = true
	return s.response, nil
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

func TestSyncHTTPPreservesAttachmentComponentEvents(t *testing.T) {
	exact := "line one\nline two  "
	assets := []identity.ImageAssetResponse{{AssetIndex: 0, MIMEType: "image/png", Width: 1, Height: 2, ByteSize: 30}}
	service := &fullSyncSpy{response: identity.SyncResponse{
		Cursor: 8, HasMore: true,
		Events: []identity.SyncEventResponse{
			{Sequence: 7, EventType: "paste.revised", PasteID: "00000000-0000-4000-8000-000000000701", RevisionID: "00000000-0000-4000-8000-000000000702", Kind: identity.RevisionContent, ServerSequence: 7, Text: &exact},
			{Sequence: 8, EventType: "paste.revised", PasteID: "00000000-0000-4000-8000-000000000701", RevisionID: "00000000-0000-4000-8000-000000000703", Kind: identity.RevisionAttachmentBundle, ServerSequence: 8, Assets: &assets},
		},
	}}
	request := httptest.NewRequest(http.MethodGet, "/v1/sync?after=6&limit=2", nil)
	request.Header.Set("Authorization", "Bearer full-runtime-marker")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, service, nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !service.called {
		t.Fatalf("status/called/body = %d/%t/%q", response.Code, service.called, response.Body.String())
	}
	var decoded identity.SyncResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if decoded.Cursor != 8 || !decoded.HasMore || len(decoded.Events) != 2 || decoded.Events[0].Text == nil || *decoded.Events[0].Text != exact || decoded.Events[1].Assets == nil || len(*decoded.Events[1].Assets) != 1 {
		t.Fatalf("sync response = %#v", decoded)
	}
}

type snapshotAggregateSpy struct {
	fakeIdentityAPI
	called   bool
	response identity.SnapshotResponse
}

func (service *snapshotAggregateSpy) Authenticate(context.Context, string) (identity.Principal, error) {
	return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000001", Scope: "full"}, nil
}

func (service *snapshotAggregateSpy) Snapshot(context.Context, identity.Principal) (identity.SnapshotResponse, error) {
	service.called = true
	return service.response, nil
}

func TestSnapshotHTTPReturnsOneAggregatePaste(t *testing.T) {
	exact := "aggregate text  "
	service := &snapshotAggregateSpy{response: identity.SnapshotResponse{
		Cursor: 9,
		Pastes: []identity.PasteResponse{{
			PasteID: "00000000-0000-4000-8000-000000000711", RevisionID: "00000000-0000-4000-8000-000000000712",
			Kind: identity.RevisionContent, ServerSequence: 9, Text: &exact,
			AttachmentRevisionID: "00000000-0000-4000-8000-000000000713",
			Assets:               []identity.ImageAssetResponse{{AssetIndex: 0, MIMEType: "image/png", Width: 1, Height: 2, ByteSize: 30}},
		}},
	}}
	request := httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil)
	request.Header.Set("Authorization", "Bearer full-runtime-marker")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, service, nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !service.called {
		t.Fatalf("status/called/body = %d/%t/%q", response.Code, service.called, response.Body.String())
	}
	var decoded identity.SnapshotResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if decoded.Cursor != 9 || len(decoded.Pastes) != 1 || decoded.Pastes[0].Text == nil || *decoded.Pastes[0].Text != exact || len(decoded.Pastes[0].Assets) != 1 {
		t.Fatalf("snapshot response = %#v", decoded)
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
