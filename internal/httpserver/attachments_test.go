package httpserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/images"
)

const (
	attachmentHTTPPasteID = "00000000-0000-4000-8000-000000000111"
	attachmentHTTPKey     = "00000000-0000-4000-8000-000000000112"
)

type fakeAttachmentAPI struct {
	fakeIdentityAPI
	replaceCalls   int
	downloadCalls  int
	pasteID        string
	assetIndex     int
	idempotencyKey string
	input          identity.ReplaceAttachmentsInput
	replaceResult  identity.Result
	replaceErr     error
	asset          identity.ImageAsset
	data           []byte
	downloadErr    error
}

func (f *fakeAttachmentAPI) Authenticate(_ context.Context, token string) (identity.Principal, error) {
	switch token {
	case "full-runtime-marker":
		return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000710", DeviceID: "device", Scope: "full"}, nil
	case "connector-runtime-marker":
		return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000710", DeviceID: "device", Scope: "connector"}, nil
	default:
		return identity.Principal{}, identity.ErrUnauthorized
	}
}

func (f *fakeAttachmentAPI) ReplaceAttachments(_ context.Context, _ identity.Principal, pasteID, key string, input identity.ReplaceAttachmentsInput) (identity.Result, error) {
	f.replaceCalls++
	f.pasteID = pasteID
	f.idempotencyKey = key
	f.input = input
	if f.replaceResult.Status == 0 {
		f.replaceResult = identity.Result{Status: http.StatusOK, Body: []byte("{\"text\":\"exact\\ntext  \",\"assets\":[]}\n")}
	}
	return f.replaceResult, f.replaceErr
}

func (f *fakeAttachmentAPI) AttachmentAsset(_ context.Context, _ identity.Principal, pasteID string, assetIndex int) (identity.ImageAsset, []byte, error) {
	f.downloadCalls++
	f.pasteID = pasteID
	f.assetIndex = assetIndex
	if f.asset.MIMEType == "" {
		f.asset.MIMEType = "image/png"
	}
	if f.data == nil {
		f.data = []byte("exact image bytes")
	}
	return f.asset, f.data, f.downloadErr
}

type attachmentHTTPPart struct {
	name            string
	mimeType        string
	width           int
	height          int
	data            []byte
	duplicateHeader string
}

type attachmentHTTPReadGuard struct {
	reader    *bytes.Reader
	limit     int
	readBytes int
	blocked   bool
}

func (guard *attachmentHTTPReadGuard) Read(value []byte) (int, error) {
	if guard.readBytes >= guard.limit {
		guard.blocked = true
		return 0, errors.New("attachment read guard exceeded")
	}
	if remaining := guard.limit - guard.readBytes; len(value) > remaining {
		value = value[:remaining]
	}
	count, err := guard.reader.Read(value)
	guard.readBytes += count
	return count, err
}

func (guard *attachmentHTTPReadGuard) Close() error { return nil }

func attachmentHTTPBMP(width, height int) []byte {
	value := make([]byte, 26)
	copy(value[:2], "BM")
	binary.LittleEndian.PutUint32(value[18:22], uint32(width))
	binary.LittleEndian.PutUint32(value[22:26], uint32(height))
	return value
}

func attachmentHTTPMultipart(t *testing.T, parts []attachmentHTTPPart) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for index, item := range parts {
		header := make(textproto.MIMEHeader)
		name := item.name
		if name == "" {
			name = "images"
		}
		header.Set("Content-Disposition", `form-data; name="`+name+`"; filename="asset-`+strconv.Itoa(index)+`.bmp"`)
		header.Set("Content-Type", item.mimeType)
		header.Set("X-MCPaste-Width", strconv.Itoa(item.width))
		header.Set("X-MCPaste-Height", strconv.Itoa(item.height))
		if item.duplicateHeader != "" {
			header.Add(item.duplicateHeader, header.Get(item.duplicateHeader))
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(item.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func validAttachmentHTTPPart(index int) attachmentHTTPPart {
	width, height := index+1, index+2
	return attachmentHTTPPart{mimeType: "image/bmp", width: width, height: height, data: attachmentHTTPBMP(width, height)}
}

func attachmentHTTPPutRequest(body []byte, contentType string) *http.Request {
	request := httptest.NewRequest(http.MethodPut, "/v1/pastes/"+attachmentHTTPPasteID+"/attachments", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer full-runtime-marker")
	request.Header.Set("Idempotency-Key", attachmentHTTPKey)
	request.Header.Set("Content-Type", contentType)
	return request
}

func TestAttachmentReplaceParsesOrderedBundleAndPassesAggregateResult(t *testing.T) {
	parts := []attachmentHTTPPart{validAttachmentHTTPPart(0), validAttachmentHTTPPart(1)}
	body, contentType := attachmentHTTPMultipart(t, parts)
	api := &fakeAttachmentAPI{replaceResult: identity.Result{Status: http.StatusOK, Body: []byte("{\"text\":\"exact\\ntext  \",\"assets\":[{\"asset_index\":0}]}\n")}}
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, api, nil).ServeHTTP(response, attachmentHTTPPutRequest(body, contentType))

	if response.Code != http.StatusOK || response.Body.String() != string(api.replaceResult.Body) {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
	if api.replaceCalls != 1 || api.pasteID != attachmentHTTPPasteID || api.idempotencyKey != attachmentHTTPKey || len(api.input.Assets) != 2 {
		t.Fatalf("replace call = %d/%q/%q/%#v", api.replaceCalls, api.pasteID, api.idempotencyKey, api.input.Assets)
	}
	for index, asset := range api.input.Assets {
		if asset.MIMEType != parts[index].mimeType || asset.Width != parts[index].width || asset.Height != parts[index].height || !bytes.Equal(asset.Bytes, parts[index].data) {
			t.Fatalf("asset %d = %#v", index, asset)
		}
	}
}

func TestAttachmentReplaceRejectsCumulativeBytesWhileReadingCrossingPart(t *testing.T) {
	const readAheadAllowance = 64 << 10
	first := validAttachmentHTTPPart(0)
	first.data = make([]byte, images.MaxBundleBytes/2)
	copy(first.data, attachmentHTTPBMP(first.width, first.height))
	second := validAttachmentHTTPPart(1)
	remainingBundleBytes := images.MaxBundleBytes - len(first.data)
	second.data = make([]byte, remainingBundleBytes+2*readAheadAllowance)
	copy(second.data, attachmentHTTPBMP(second.width, second.height))
	body, contentType := attachmentHTTPMultipart(t, []attachmentHTTPPart{first, second})

	secondHeaderOffset := bytes.Index(body, []byte(`filename="asset-1.bmp"`))
	if secondHeaderOffset < 0 {
		t.Fatal("second attachment header not found")
	}
	headerEndOffset := bytes.Index(body[secondHeaderOffset:], []byte("\r\n\r\n"))
	if headerEndOffset < 0 {
		t.Fatal("second attachment header terminator not found")
	}
	secondDataOffset := secondHeaderOffset + headerEndOffset + len("\r\n\r\n")
	readLimit := secondDataOffset + remainingBundleBytes + readAheadAllowance
	secondDataEnd := secondDataOffset + len(second.data)
	if readLimit >= secondDataEnd {
		t.Fatalf("read guard %d is not inside crossing part ending at %d", readLimit, secondDataEnd)
	}

	guard := &attachmentHTTPReadGuard{reader: bytes.NewReader(body), limit: readLimit}
	request := attachmentHTTPPutRequest(nil, contentType)
	request.Body = guard
	request.ContentLength = int64(len(body))
	api := &fakeAttachmentAPI{}
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || api.replaceCalls != 0 {
		t.Fatalf("status/calls/body = %d/%d/%q", response.Code, api.replaceCalls, response.Body.String())
	}
	if guard.blocked {
		t.Fatalf("parser read past cumulative limit guard inside second part after %d bytes", guard.readBytes)
	}
}

func TestAttachmentReplaceAcceptsClearAndEightButRejectsNine(t *testing.T) {
	tests := []struct {
		name       string
		count      int
		wantStatus int
		wantCalls  int
	}{
		{name: "clear", count: 0, wantStatus: http.StatusOK, wantCalls: 1},
		{name: "eight", count: images.MaxAttachmentItems, wantStatus: http.StatusOK, wantCalls: 1},
		{name: "nine", count: images.MaxAttachmentItems + 1, wantStatus: http.StatusBadRequest},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			parts := make([]attachmentHTTPPart, item.count)
			for index := range parts {
				parts[index] = validAttachmentHTTPPart(index)
			}
			body, contentType := attachmentHTTPMultipart(t, parts)
			api := &fakeAttachmentAPI{}
			response := httptest.NewRecorder()
			NewApplicationHandler(nil, api, nil).ServeHTTP(response, attachmentHTTPPutRequest(body, contentType))
			if response.Code != item.wantStatus || api.replaceCalls != item.wantCalls {
				t.Fatalf("status/calls = %d/%d", response.Code, api.replaceCalls)
			}
			if api.replaceCalls == 1 && len(api.input.Assets) != item.count {
				t.Fatalf("assets = %d, want %d", len(api.input.Assets), item.count)
			}
		})
	}
}

func TestAttachmentReplaceAcceptsDirectEmptyMultipart(t *testing.T) {
	boundary := "attachment-direct-empty"
	body := []byte("--" + boundary + "--\r\n")
	api := &fakeAttachmentAPI{}
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, api, nil).ServeHTTP(
		response,
		attachmentHTTPPutRequest(body, "multipart/form-data; boundary="+boundary),
	)
	if response.Code != http.StatusOK || api.replaceCalls != 1 || len(api.input.Assets) != 0 {
		t.Fatalf("status/calls/assets = %d/%d/%d", response.Code, api.replaceCalls, len(api.input.Assets))
	}
}

func TestAttachmentReplaceRejectsStrictMultipartAndSecurityErrors(t *testing.T) {
	validBody, validType := attachmentHTTPMultipart(t, []attachmentHTTPPart{validAttachmentHTTPPart(0)})
	clearBody, clearType := attachmentHTTPMultipart(t, nil)
	unknownBody, unknownType := attachmentHTTPMultipart(t, []attachmentHTTPPart{{name: "files", mimeType: "image/bmp", width: 1, height: 2, data: attachmentHTTPBMP(1, 2)}})
	invalidBody, invalidType := attachmentHTTPMultipart(t, []attachmentHTTPPart{{mimeType: "image/bmp", width: 9, height: 2, data: attachmentHTTPBMP(1, 2)}})
	duplicateBody, duplicateType := attachmentHTTPMultipart(t, []attachmentHTTPPart{{mimeType: "image/bmp", width: 1, height: 2, data: attachmentHTTPBMP(1, 2), duplicateHeader: "X-MCPaste-Width"}})
	prefixBoundary := "attachment-prefix-boundary"
	prefixCollisionBody := []byte("--" + prefixBoundary + "X\r\n--" + prefixBoundary + "--\r\n")
	prefixCollisionType := "multipart/form-data; boundary=" + prefixBoundary
	trailingBody := append(bytes.Clone(clearBody), []byte("unexpected epilogue")...)
	tests := []struct {
		name       string
		request    func() *http.Request
		wantStatus int
	}{
		{name: "unknown field", request: func() *http.Request { return attachmentHTTPPutRequest(unknownBody, unknownType) }, wantStatus: http.StatusBadRequest},
		{name: "invalid metadata", request: func() *http.Request { return attachmentHTTPPutRequest(invalidBody, invalidType) }, wantStatus: http.StatusBadRequest},
		{name: "duplicate part metadata", request: func() *http.Request { return attachmentHTTPPutRequest(duplicateBody, duplicateType) }, wantStatus: http.StatusBadRequest},
		{name: "malformed multipart", request: func() *http.Request {
			return attachmentHTTPPutRequest([]byte("broken"), "multipart/form-data; boundary=missing")
		}, wantStatus: http.StatusBadRequest},
		{name: "boundary prefix collision", request: func() *http.Request {
			return attachmentHTTPPutRequest(prefixCollisionBody, prefixCollisionType)
		}, wantStatus: http.StatusBadRequest},
		{name: "trailing epilogue", request: func() *http.Request {
			return attachmentHTTPPutRequest(trailingBody, clearType)
		}, wantStatus: http.StatusBadRequest},
		{name: "missing idempotency", request: func() *http.Request {
			r := attachmentHTTPPutRequest(validBody, validType)
			r.Header.Del("Idempotency-Key")
			return r
		}, wantStatus: http.StatusBadRequest},
		{name: "duplicate idempotency", request: func() *http.Request {
			r := attachmentHTTPPutRequest(validBody, validType)
			r.Header.Add("Idempotency-Key", "00000000-0000-4000-8000-000000000113")
			return r
		}, wantStatus: http.StatusBadRequest},
		{name: "duplicate authorization", request: func() *http.Request {
			r := attachmentHTTPPutRequest(validBody, validType)
			r.Header.Add("Authorization", "Bearer second-runtime-marker")
			return r
		}, wantStatus: http.StatusUnauthorized},
		{name: "duplicate content type", request: func() *http.Request {
			r := attachmentHTTPPutRequest(validBody, validType)
			r.Header.Add("Content-Type", validType)
			return r
		}, wantStatus: http.StatusBadRequest},
		{name: "connector", request: func() *http.Request {
			r := attachmentHTTPPutRequest(validBody, validType)
			r.Header.Set("Authorization", "Bearer connector-runtime-marker")
			return r
		}, wantStatus: http.StatusForbidden},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			api := &fakeAttachmentAPI{}
			response := httptest.NewRecorder()
			NewApplicationHandler(nil, api, nil).ServeHTTP(response, item.request())
			if response.Code != item.wantStatus || api.replaceCalls != 0 {
				t.Fatalf("status/calls/body = %d/%d/%q", response.Code, api.replaceCalls, response.Body.String())
			}
		})
	}
}

func TestAttachmentDownloadReturnsExactCurrentAsset(t *testing.T) {
	api := &fakeAttachmentAPI{asset: identity.ImageAsset{MIMEType: "image/bmp"}, data: []byte("binary image payload")}
	request := httptest.NewRequest(http.MethodGet, "/v1/pastes/"+attachmentHTTPPasteID+"/attachments/19", nil)
	request.Header.Set("Authorization", "Bearer full-runtime-marker")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != string(api.data) {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "image/bmp" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Length") != strconv.Itoa(len(api.data)) {
		t.Fatalf("headers = %#v", response.Header())
	}
	if api.downloadCalls != 1 || api.pasteID != attachmentHTTPPasteID || api.assetIndex != 19 {
		t.Fatalf("download call = %d/%q/%d", api.downloadCalls, api.pasteID, api.assetIndex)
	}
}

func TestAttachmentDownloadRejectsRangeIndexesScopeAndServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		token      string
		configure  func(*fakeAttachmentAPI, *http.Request)
		wantStatus int
		wantCalls  int
	}{
		{name: "range", path: "0", token: "full-runtime-marker", configure: func(_ *fakeAttachmentAPI, r *http.Request) { r.Header.Set("Range", "bytes=0-1") }, wantStatus: http.StatusBadRequest},
		{name: "negative", path: "-1", token: "full-runtime-marker", wantStatus: http.StatusBadRequest},
		{name: "not integer", path: "one", token: "full-runtime-marker", wantStatus: http.StatusBadRequest},
		{name: "at legacy limit", path: strconv.Itoa(images.MaxBundleItems), token: "full-runtime-marker", wantStatus: http.StatusBadRequest},
		{name: "connector", path: "0", token: "connector-runtime-marker", wantStatus: http.StatusForbidden},
		{name: "not found", path: "0", token: "full-runtime-marker", configure: func(api *fakeAttachmentAPI, _ *http.Request) { api.downloadErr = identity.ErrNotFound }, wantStatus: http.StatusNotFound, wantCalls: 1},
		{name: "unavailable", path: "0", token: "full-runtime-marker", configure: func(api *fakeAttachmentAPI, _ *http.Request) { api.downloadErr = identity.ErrUnavailableContent }, wantStatus: http.StatusServiceUnavailable, wantCalls: 1},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			api := &fakeAttachmentAPI{}
			request := httptest.NewRequest(http.MethodGet, "/v1/pastes/"+attachmentHTTPPasteID+"/attachments/"+item.path, nil)
			request.Header.Set("Authorization", "Bearer "+item.token)
			if item.configure != nil {
				item.configure(api, request)
			}
			response := httptest.NewRecorder()
			NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)
			if response.Code != item.wantStatus || api.downloadCalls != item.wantCalls {
				t.Fatalf("status/calls/body = %d/%d/%q", response.Code, api.downloadCalls, response.Body.String())
			}
		})
	}
}

func TestAttachmentRoutesRequireAttachmentService(t *testing.T) {
	putBody, putType := attachmentHTTPMultipart(t, nil)
	putResponse := httptest.NewRecorder()
	NewApplicationHandler(nil, &fakeImageAPI{}, nil).ServeHTTP(putResponse, attachmentHTTPPutRequest(putBody, putType))
	if putResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT status = %d", putResponse.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/pastes/"+attachmentHTTPPasteID+"/attachments/0", nil)
	request.Header.Set("Authorization", "Bearer full-runtime-marker")
	getResponse := httptest.NewRecorder()
	NewApplicationHandler(nil, &fakeImageAPI{}, nil).ServeHTTP(getResponse, request)
	if getResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d", getResponse.Code)
	}
}

func TestAttachmentReplaceMapsServiceError(t *testing.T) {
	body, contentType := attachmentHTTPMultipart(t, nil)
	api := &fakeAttachmentAPI{replaceErr: errors.New("storage failed")}
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, api, nil).ServeHTTP(response, attachmentHTTPPutRequest(body, contentType))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "internal_error") {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
}
