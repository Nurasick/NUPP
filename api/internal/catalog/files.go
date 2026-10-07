package catalog

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Nurasick/NUPP/api/internal/httpx"
	"github.com/Nurasick/NUPP/api/internal/storage"
)

// fileExtensions maps each allowed MIME type (see the material_files CHECK in
// the migration) to the extension used in download filenames.
var fileExtensions = map[string]string{
	"application/pdf":    ".pdf",
	"image/png":          ".png",
	"image/jpeg":         ".jpg",
	"application/msword": ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
}

// Download time budget (hardening spec H-HTTP-3): a fixed allowance plus the
// time the file takes at a deliberately low minimum speed. Normal clients
// never come close; a client trickling the download to hold the connection
// open is cut off.
const (
	downloadBaseTime = 30 * time.Second
	minDownloadSpeed = 32 << 10 // bytes per second (32 KiB/s)
)

// downloadDeadline is how long a client gets to download size bytes.
func downloadDeadline(size int64) time.Duration {
	return downloadBaseTime + time.Duration(size)*time.Second/minDownloadSpeed
}

// inlineTypes are shown in the browser; everything else is downloaded (R-EP-18).
// Browsers render PDFs and images safely; Office files they can't render anyway.
var inlineTypes = map[string]bool{
	"application/pdf": true,
	"image/png":       true,
	"image/jpeg":      true,
}

// downloadFilename builds a readable filename such as
// "csci-151-midterm-1-questions-p1.pdf" from server-side values only.
//
// Because every part goes through a slug (or is a fixed extension), the result
// contains only [a-z0-9.-]: no quotes, slashes or newlines that could break the
// Content-Disposition header. A title with no ASCII letters is left out.
func downloadFilename(courseSlug, materialTitle string, position int32, mimeType string) string {
	parts := []string{courseSlug}
	if titleSlug := Slugify(materialTitle); titleSlug != "" {
		parts = append(parts, titleSlug)
	}
	ext, ok := fileExtensions[mimeType]
	if !ok {
		ext = ".bin"
	}
	// Pages are numbered from 1 for humans; position starts at 0.
	return fmt.Sprintf("%s-p%d%s", strings.Join(parts, "-"), position+1, ext)
}

// getFile handles GET /api/v1/files/{id} (spec §5.6).
func (h *Handler) getFile(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseUUID(r.PathValue("id"))
	if err != nil {
		httpx.BadRequest(w, "invalid file id")
		return
	}
	// The query only returns files of visible materials, and only from
	// material_files, whose keys always start with "materials/". A pending or
	// hidden upload can therefore never be served here (R-EP-17).
	file, err := h.q.GetVisibleMaterialFile(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.NotFound(w, "file")
		return
	}
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("get material file: %w", err))
		return
	}

	obj, err := h.files.Open(r.Context(), file.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		// The database says the file exists but the bytes are gone: that is
		// data loss an operator must investigate, so log loudly (R-EP-20).
		slog.ErrorContext(r.Context(), "material file missing from storage",
			"file_id", id, "key", file.StorageKey)
		httpx.NotFound(w, "file")
		return
	}
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("open stored file: %w", err))
		return
	}
	defer obj.Close()

	disposition := "attachment"
	if inlineTypes[file.MimeType] {
		disposition = "inline"
	}
	name := downloadFilename(file.CourseSlug, file.MaterialTitle, file.Position, file.MimeType)

	header := w.Header()
	// We set Content-Type ourselves from the allow-listed DB value, and
	// nosniff forbids the browser from second-guessing it.
	header.Set("Content-Type", file.MimeType)
	header.Set("X-Content-Type-Options", "nosniff")
	// Even if a malicious file slipped through, the sandbox CSP stops it from
	// running scripts or loading anything on our origin.
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	header.Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, name))
	// The content hash is a perfect ETag: same bytes ⇔ same tag.
	header.Set("ETag", `"`+file.Sha256+`"`)
	// Browsers may reuse the file for 1 hour, shared caches (Cloudflare) for
	// 1 day. Not "immutable": a takedown must stop serving it (R-EP-18).
	header.Set("Cache-Control", "public, max-age=3600, s-maxage=86400")

	httpx.SetWriteDeadline(w, downloadDeadline(file.SizeBytes))

	// ServeContent does the hard HTTP parts for us: Range requests (206),
	// If-None-Match / If-Range against our ETag (304), HEAD, Content-Length.
	// The zero time means "no Last-Modified": the ETag is the validator.
	http.ServeContent(w, r, "", time.Time{}, obj)
}
