package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

type fakeImageAPI struct {
	fakeIdentityAPI
	uploadCalls int
}

func (f *fakeImageAPI) Authenticate(_ context.Context, token string) (identity.Principal, error) {
	if token == "full-runtime-marker" {
		return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000710", DeviceID: "device", Scope: "full"}, nil
	}
	if token == "connector-runtime-marker" {
		return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000710", DeviceID: "device", Scope: "connector"}, nil
	}
	return identity.Principal{}, identity.ErrUnauthorized
}

func (f *fakeImageAPI) CreateImagePaste(_ context.Context, _ identity.Principal, _ string, input identity.CreateImagePasteInput) (identity.Result, error) {
	f.uploadCalls = len(input.Assets)
	return identity.Result{Status: http.StatusCreated, Body: []byte(`{"kind":"image_bundle"}`)}, nil
}

func (f *fakeImageAPI) ImageAsset(context.Context, identity.Principal, string, int) (identity.ImageAsset, []byte, error) {
	return identity.ImageAsset{MIMEType: "image/png"}, []byte("image"), nil
}

func TestImageUploadRequiresFullScopeAndParsesNormalizedMetadata(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="images"; filename="normalized.png"`)
	header.Set("Content-Type", "image/png")
	header.Set("X-MCPaste-Width", "1")
	header.Set("X-MCPaste-Height", "1")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	_, _ = part.Write(fixture)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	api := &fakeImageAPI{}
	request := httptest.NewRequest(http.MethodPost, "/v1/image-pastes", &body)
	request.Header.Set("Authorization", "Bearer full-runtime-marker")
	request.Header.Set("Idempotency-Key", "image-idempotency")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || api.uploadCalls != 1 {
		t.Fatalf("upload = %d/%d", response.Code, api.uploadCalls)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/image-pastes", bytes.NewReader(body.Bytes()))
	request.Header.Set("Authorization", "Bearer connector-runtime-marker")
	request.Header.Set("Idempotency-Key", "image-idempotency")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response = httptest.NewRecorder()
	NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("connector upload status = %d", response.Code)
	}
}
