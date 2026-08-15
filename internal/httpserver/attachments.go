package httpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/images"
)

type attachmentMultipartBody struct {
	io.Reader
	closer io.Closer
}

func (body attachmentMultipartBody) Close() error { return body.closer.Close() }

type attachmentFrameValidator struct {
	marker     []byte
	carry      []byte
	direct     []byte
	directSeen int
	closed     bool
	trailing   bool
	invalid    bool
}

func newAttachmentFrameValidator(boundary string, directEmpty bool) *attachmentFrameValidator {
	validator := &attachmentFrameValidator{marker: []byte("\r\n--" + boundary + "--\r\n")}
	if directEmpty {
		validator.direct = []byte("--" + boundary + "--\r\n")
	}
	return validator
}

func (validator *attachmentFrameValidator) Write(value []byte) (int, error) {
	if len(validator.direct) != 0 {
		for _, item := range value {
			if validator.directSeen >= len(validator.direct) {
				validator.trailing = true
				continue
			}
			if item != validator.direct[validator.directSeen] {
				validator.invalid = true
			}
			validator.directSeen++
			if validator.directSeen == len(validator.direct) {
				validator.closed = true
			}
		}
		return len(value), nil
	}
	if validator.closed {
		validator.trailing = validator.trailing || len(value) != 0
		return len(value), nil
	}
	combined := make([]byte, 0, len(validator.carry)+len(value))
	combined = append(combined, validator.carry...)
	combined = append(combined, value...)
	if index := bytes.Index(combined, validator.marker); index >= 0 {
		validator.closed = true
		validator.trailing = index+len(validator.marker) != len(combined)
		validator.carry = nil
		return len(value), nil
	}
	keep := len(validator.marker) - 1
	if keep > len(combined) {
		keep = len(combined)
	}
	validator.carry = append(validator.carry[:0], combined[len(combined)-keep:]...)
	return len(value), nil
}

func (validator *attachmentFrameValidator) Valid() bool {
	return validator.closed && !validator.trailing && !validator.invalid
}

type attachmentAPI interface {
	ReplaceAttachments(context.Context, identity.Principal, string, string, identity.ReplaceAttachmentsInput) (identity.Result, error)
	AttachmentAsset(context.Context, identity.Principal, string, int) (identity.ImageAsset, []byte, error)
}

func (s *apiServer) replaceAttachments(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	service, ok := s.identity.(attachmentAPI)
	if err == nil && !ok {
		err = identity.ErrUnavailableContent
	}
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := parseAttachmentMultipart(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := service.ReplaceAttachments(
		r.Context(), principal, r.PathValue("paste_id"), idempotencyKey, input,
	)
	writeResultOrError(w, result, err)
}

func (s *apiServer) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Range")) != 0 {
		writeError(w, identity.ErrInvalid)
		return
	}
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	service, ok := s.identity.(attachmentAPI)
	if err == nil && !ok {
		err = identity.ErrUnavailableContent
	}
	index, parseErr := strconv.Atoi(r.PathValue("asset_index"))
	if err == nil && (parseErr != nil || index < 0 || index >= images.MaxBundleItems) {
		err = identity.ErrInvalid
	}
	if err != nil {
		writeError(w, err)
		return
	}
	asset, data, err := service.AttachmentAsset(r.Context(), principal, r.PathValue("paste_id"), index)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", asset.MIMEType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func parseAttachmentMultipart(w http.ResponseWriter, r *http.Request) (identity.ReplaceAttachmentsInput, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	contentType, err := oneHeader(r, "Content-Type")
	if err != nil {
		return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
	}
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	boundary := parameters["boundary"]
	if err != nil || mediaType != "multipart/form-data" || boundary == "" {
		return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
	}
	prefix := []byte("--" + boundary)
	first := make([]byte, 2)
	if _, err := io.ReadFull(r.Body, first); err != nil {
		return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
	}
	consumed := first
	actualPrefix := first
	if bytes.Equal(first, []byte("\r\n")) {
		actualPrefix = make([]byte, len(prefix))
		if _, err := io.ReadFull(r.Body, actualPrefix); err != nil {
			return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
		}
		consumed = append(consumed, actualPrefix...)
	} else {
		rest := make([]byte, len(prefix)-len(first))
		if _, err := io.ReadFull(r.Body, rest); err != nil {
			return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
		}
		actualPrefix = append(actualPrefix, rest...)
		consumed = actualPrefix
	}
	if !bytes.Equal(actualPrefix, prefix) {
		return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
	}
	suffix := make([]byte, 2)
	if _, err := io.ReadFull(r.Body, suffix); err != nil {
		return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
	}
	consumed = append(consumed, suffix...)
	directEmpty := false
	switch string(suffix) {
	case "\r\n":
	case "--":
		ending := make([]byte, 2)
		if _, err := io.ReadFull(r.Body, ending); err != nil || !bytes.Equal(ending, []byte("\r\n")) {
			return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
		}
		consumed = append(consumed, ending...)
		directEmpty = !bytes.HasPrefix(consumed, []byte("\r\n"))
	default:
		return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
	}
	reconstructed := attachmentMultipartBody{
		Reader: io.MultiReader(bytes.NewReader(consumed), r.Body),
		closer: r.Body,
	}
	frame := newAttachmentFrameValidator(boundary, directEmpty)
	tracked := attachmentMultipartBody{
		Reader: io.TeeReader(reconstructed, frame),
		closer: reconstructed,
	}
	r.Body = http.MaxBytesReader(w, tracked, 512<<20)
	reader, err := r.MultipartReader()
	if err != nil {
		return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
	}
	items := make([]images.AssetInput, 0, images.MaxAttachmentItems)
	remainingBytes := images.MaxBundleBytes
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			if _, err := io.Copy(io.Discard, r.Body); err != nil || !frame.Valid() {
				return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
			}
			break
		}
		if err != nil || len(items) >= images.MaxAttachmentItems {
			return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
		}
		item, err := parseAttachmentPart(part, remainingBytes)
		if err != nil {
			return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
		}
		_ = part.Close()
		items = append(items, item)
		remainingBytes -= len(item.Bytes)
	}
	if err := images.ValidateAttachmentBundle(items); err != nil {
		return identity.ReplaceAttachmentsInput{}, identity.ErrInvalid
	}
	return identity.ReplaceAttachmentsInput{Assets: items}, nil
}

func parseAttachmentPart(part *multipart.Part, remainingBytes int) (images.AssetInput, error) {
	disposition, err := oneMIMEHeader(part.Header, "Content-Disposition")
	if err != nil {
		return images.AssetInput{}, identity.ErrInvalid
	}
	dispositionType, parameters, err := mime.ParseMediaType(disposition)
	if err != nil || dispositionType != "form-data" || parameters["name"] != "images" {
		return images.AssetInput{}, identity.ErrInvalid
	}
	contentType, err := oneMIMEHeader(part.Header, "Content-Type")
	if err != nil {
		return images.AssetInput{}, identity.ErrInvalid
	}
	mimeType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return images.AssetInput{}, identity.ErrInvalid
	}
	widthValue, err := oneMIMEHeader(part.Header, "X-MCPaste-Width")
	if err != nil {
		return images.AssetInput{}, identity.ErrInvalid
	}
	heightValue, err := oneMIMEHeader(part.Header, "X-MCPaste-Height")
	if err != nil {
		return images.AssetInput{}, identity.ErrInvalid
	}
	width, widthErr := strconv.Atoi(widthValue)
	height, heightErr := strconv.Atoi(heightValue)
	if widthErr != nil || heightErr != nil {
		return images.AssetInput{}, identity.ErrInvalid
	}
	maxBytes := images.MaxSourceBytes
	if remainingBytes < maxBytes {
		maxBytes = remainingBytes
	}
	data, err := io.ReadAll(io.LimitReader(part, int64(maxBytes)+1))
	if err != nil || len(data) > maxBytes {
		return images.AssetInput{}, identity.ErrInvalid
	}
	return images.AssetInput{
		MIMEType: strings.ToLower(strings.TrimSpace(mimeType)),
		Width:    width,
		Height:   height,
		Bytes:    data,
	}, nil
}

func oneMIMEHeader(header textproto.MIMEHeader, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", identity.ErrInvalid
	}
	return values[0], nil
}
