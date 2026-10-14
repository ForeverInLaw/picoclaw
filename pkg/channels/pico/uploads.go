package pico

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/h2non/filetype"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

const maxImagesPerMessage = 4

// SetMaxMediaSize shares the runtime's image encoding limit with upload admission.
func (c *PicoChannel) SetMaxMediaSize(size int) { c.maxMediaSize = size }

func (c *PicoChannel) imageLimit() int {
	if c.maxMediaSize > 0 {
		return c.maxMediaSize
	}
	return config.DefaultMaxMediaSize
}

func (c *PicoChannel) imageTypes() []string {
	supported := []string{"image/jpeg", "image/png", "image/gif", "image/webp"}
	if len(c.config.AllowedImageTypes) == 0 {
		return supported
	}
	return slices.DeleteFunc(
		supported,
		func(value string) bool { return !slices.Contains(c.config.AllowedImageTypes, value) },
	)
}

func (c *PicoChannel) mediaOwner(session string) string { return c.Name() + ":upload-owner:" + session }

func (c *PicoChannel) uploadScope(path string) string {
	return c.Name() + ":upload:" + filepath.Base(path)
}

func (c *PicoChannel) mediaRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !c.IsRunning() || c.GetMediaStore() == nil {
		http.Error(w, "media unavailable", http.StatusServiceUnavailable)
		return "", false
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !c.upgrader.CheckOrigin(r) {
		http.Error(w, "origin rejected", http.StatusForbidden)
		return "", false
	}
	session := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if session == "" || len(session) > 128 {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return "", false
	}
	return session, true
}

func (c *PicoChannel) handleUpload(w http.ResponseWriter, r *http.Request) {
	session, ok := c.mediaRequest(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).
			Encode(map[string]any{"max_bytes": c.imageLimit(), "allowed_types": c.imageTypes(), "max_images": maxImagesPerMessage})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(c.imageLimit())+64*1024)
	if err := r.ParseMultipartForm(64 * 1024); err != nil {
		http.Error(w, "Image upload exceeds the size limit or is invalid.", http.StatusRequestEntityTooLarge)
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) != 1 {
		http.Error(w, "Upload one image per request.", http.StatusBadRequest)
		return
	}
	if files[0].Size > int64(c.imageLimit()) {
		http.Error(w, "Image exceeds the configured size limit.", http.StatusRequestEntityTooLarge)
		return
	}
	input, err := files[0].Open()
	if err != nil {
		http.Error(w, "Cannot read image.", http.StatusBadRequest)
		return
	}
	defer input.Close()
	head := make([]byte, 512)
	n, err := io.ReadFull(input, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		http.Error(w, "Cannot read image.", http.StatusBadRequest)
		return
	}
	kind, err := filetype.Match(head[:n])
	if err != nil || !slices.Contains(c.imageTypes(), kind.MIME.Value) {
		http.Error(
			w,
			"Unsupported image type. Use an allowed JPEG, PNG, GIF or WebP image.",
			http.StatusUnsupportedMediaType,
		)
		return
	}
	if err = os.MkdirAll(media.TempDir(), 0o700); err != nil {
		http.Error(w, "Cannot store image.", http.StatusInternalServerError)
		return
	}
	file, err := os.CreateTemp(media.TempDir(), "pico-upload-*")
	if err != nil {
		http.Error(w, "Cannot store image.", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(file.Name())
		}
	}()
	if _, err = io.Copy(
		file,
		io.MultiReader(bytes.NewReader(head[:n]), input),
	); err != nil ||
		r.Context().Err() != nil {
		http.Error(w, "Image upload canceled or could not be stored.", http.StatusBadRequest)
		return
	}
	meta := media.MediaMeta{
		Filename:    filepath.Base(files[0].Filename),
		ContentType: kind.MIME.Value,
		Source:      c.mediaOwner(session),
	}
	// A private temporary file gives each upload its own cleanup scope.
	ref, err := c.GetMediaStore().Store(file.Name(), meta, c.uploadScope(file.Name()))
	if err != nil {
		http.Error(w, "Cannot store image.", http.StatusInternalServerError)
		return
	}
	success = true
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	if err = json.NewEncoder(w).Encode(map[string]any{
		"ref": ref,
		"url": "/pico/media/" + strings.TrimPrefix(
			ref,
			"media://",
		) + "?session_id=" + url.QueryEscape(
			session,
		),
		"filename":     meta.Filename,
		"content_type": meta.ContentType,
		"size":         files[0].Size,
	}); err != nil {
		_ = c.GetMediaStore().ReleaseAll(c.uploadScope(file.Name()))
	}
}

func (c *PicoChannel) handleUploadedMedia(w http.ResponseWriter, r *http.Request) {
	session, ok := c.mediaRequest(w, r)
	if !ok {
		return
	}
	ref := "media://" + strings.TrimPrefix(r.URL.Path, "/pico/media/")
	path, meta, err := c.GetMediaStore().ResolveWithMeta(ref)
	if err != nil || meta.Source != c.mediaOwner(session) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		_ = c.GetMediaStore().ReleaseAll(c.uploadScope(path))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": meta.Filename}))
	http.ServeFile(w, r, path)
}

func (c *PicoChannel) inputImages(raw any, session string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok || len(values) > maxImagesPerMessage {
		return nil, fmt.Errorf("Attach at most %d uploaded images.", maxImagesPerMessage)
	}
	if len(values) == 0 {
		return nil, nil
	}
	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("Image uploads are unavailable.")
	}
	refs := make([]string, 0, len(values))
	var total int64
	for _, value := range values {
		ref, ok := value.(string)
		if !ok || !strings.HasPrefix(ref, "media://") || slices.Contains(refs, ref) {
			return nil, fmt.Errorf("Invalid image attachment. Upload it again.")
		}
		path, meta, err := store.ResolveWithMeta(ref)
		if err != nil || meta.Source != c.mediaOwner(session) || !slices.Contains(c.imageTypes(), meta.ContentType) {
			return nil, fmt.Errorf("Image is unavailable or belongs to another chat. Upload it again.")
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("Image is no longer available. Upload it again.")
		}
		total += info.Size()
		if total > int64(c.imageLimit()) {
			return nil, fmt.Errorf("Attached images exceed the configured size limit.")
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (c *PicoChannel) admitImages(refs []string, session, chatID, messageID string) ([]string, error) {
	if len(refs) == 0 {
		return refs, nil
	}
	store := c.GetMediaStore()
	scope := channels.BuildMediaScope(c.Name(), chatID, messageID)
	admitted := make([]string, 0, len(refs))
	for _, ref := range refs {
		path, meta, err := store.ResolveWithMeta(ref)
		if err != nil {
			_ = store.ReleaseAll(scope)
			return nil, err
		}
		meta.Source = c.Name() + ":message-owner:" + session
		next, err := store.Store(path, meta, scope)
		if err != nil {
			_ = store.ReleaseAll(scope)
			return nil, err
		}
		admitted = append(admitted, next)
	}
	// Keep the authenticated preview ref until its existing TTL expires. The
	// message's separate ref also protects processing if the draft is removed.
	return admitted, nil
}
