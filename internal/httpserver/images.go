package httpserver

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/images"
)

type imageAPI interface {
	CreateImagePaste(context.Context, identity.Principal, string, identity.CreateImagePasteInput) (identity.Result, error)
	ImageAsset(context.Context, identity.Principal, string, int) (identity.ImageAsset, []byte, error)
}

func (s *apiServer) createImagePaste(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	idempotencyKey := ""
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	service, ok := s.identity.(imageAPI)
	if err == nil && !ok {
		err = identity.ErrUnavailableContent
	}
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := parseImageMultipart(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := service.CreateImagePaste(r.Context(), principal, idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) downloadImage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Range") != "" {
		writeError(w, identity.ErrInvalid)
		return
	}
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	service, ok := s.identity.(imageAPI)
	if err == nil && !ok {
		err = identity.ErrUnavailableContent
	}
	index, parseErr := strconv.Atoi(r.PathValue("asset_index"))
	if err == nil && parseErr != nil {
		err = identity.ErrInvalid
	}
	if err == nil && index < 0 {
		err = identity.ErrInvalid
	}
	if err != nil {
		writeError(w, err)
		return
	}
	asset, data, err := service.ImageAsset(r.Context(), principal, r.PathValue("paste_id"), index)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", asset.MIMEType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func parseImageMultipart(w http.ResponseWriter, r *http.Request) (identity.CreateImagePasteInput, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return identity.CreateImagePasteInput{}, identity.ErrInvalid
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return identity.CreateImagePasteInput{}, identity.ErrInvalid
	}
	items := make([]images.AssetInput, 0, images.MaxBundleItems)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || part.FormName() != "images" || len(items) >= images.MaxBundleItems {
			return identity.CreateImagePasteInput{}, identity.ErrInvalid
		}
		data, err := io.ReadAll(io.LimitReader(part, images.MaxSourceBytes+1))
		_ = part.Close()
		if err != nil || len(data) > images.MaxSourceBytes {
			return identity.CreateImagePasteInput{}, identity.ErrInvalid
		}
		width, widthErr := strconv.Atoi(part.Header.Get("X-MCPaste-Width"))
		height, heightErr := strconv.Atoi(part.Header.Get("X-MCPaste-Height"))
		if widthErr != nil || heightErr != nil {
			return identity.CreateImagePasteInput{}, identity.ErrInvalid
		}
		mimeType, _, mimeErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if mimeErr != nil {
			return identity.CreateImagePasteInput{}, identity.ErrInvalid
		}
		items = append(items, images.AssetInput{MIMEType: strings.ToLower(strings.TrimSpace(mimeType)), Width: width, Height: height, Bytes: data})
	}
	if err := images.ValidateBundle(items); err != nil {
		return identity.CreateImagePasteInput{}, identity.ErrInvalid
	}
	return identity.CreateImagePasteInput{Assets: items}, nil
}
