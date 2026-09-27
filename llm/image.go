package llm

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Image is an image attachment carried by a user message. It can be embedded
// inline (Data, base64-encoded bytes, produced from a local path or from
// client-provided base64) or referenced by an external http(s) URL (URL,
// passed through to the provider, which downloads it). Exactly one of
// Data/URL is set once the image is resolved.
//
// Images are only meaningful on user messages: DeepSeek and Gemini both reject
// images in system or assistant messages.
type Image struct {
	// MIME is the image media type (image/png, image/jpeg, image/gif,
	// image/webp). It is detected from the image content for inline images; for
	// URL images it is whatever the client declared (may be empty).
	MIME string
	// Data is the base64-encoded image payload WITHOUT the data: URL prefix.
	// Set for inline images (path/data origins).
	Data string
	// URL is an external http(s) image URL, passed through to the provider
	// as-is. Mutually exclusive with Data.
	URL string
	// Detail optionally controls how the provider processes the image
	// ("low"/"high"/"original"/"auto" for DeepSeek). Empty leaves it unset.
	Detail string
}

// DataURL returns the inline data URL (data:<mime>;base64,<data>) for an inline
// image, or "" when the image is URL-based (or has no payload).
func (im Image) DataURL() string {
	if im.Data == "" {
		return ""
	}
	mime := im.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + im.Data
}

// supportedImageMIME reports whether mime is one of the image formats both
// DeepSeek and Gemini accept (the intersection of their supported lists).
func supportedImageMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// detectImageMIME sniffs the media type of the given raw bytes, returning the
// MIME only when it is a supported image format, or "" otherwise. Detection is
// based on the actual content (magic bytes), not on a file name or declared
// MIME type — the same rule both providers follow.
func detectImageMIME(data []byte) string {
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)
	if supportedImageMIME(ct) {
		return ct
	}
	return ""
}

// LoadImageFile reads a local image file, detects its format from the content
// and returns it base64-encoded for inline use. It errors when the file cannot
// be read, is empty, or is not a supported image format (PNG/JPEG/GIF/WebP).
func LoadImageFile(path string) (Image, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Image{}, err
	}
	if len(raw) == 0 {
		return Image{}, fmt.Errorf("empty image file: %s", path)
	}
	mime := detectImageMIME(raw)
	if mime == "" {
		return Image{}, fmt.Errorf("unsupported image format (need PNG, JPEG, GIF or WebP): %s", path)
	}
	return Image{MIME: mime, Data: base64.StdEncoding.EncodeToString(raw)}, nil
}

// NewInlineImage builds an inline image from client-provided data, which may be
// either a full data: URL (data:<mime>;base64,<payload>) or raw base64. The
// payload is decoded and its format is detected from the bytes, falling back to
// the declared mimeType when detection is inconclusive. It errors when the data
// is not valid base64 or not a supported image format.
func NewInlineImage(data, mimeType, detail string) (Image, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return Image{}, errors.New("empty image data")
	}
	payload := data
	if strings.HasPrefix(data, "data:") {
		comma := strings.IndexByte(data, ',')
		if comma < 0 {
			return Image{}, errors.New("malformed data URL")
		}
		header := data[len("data:"):comma]
		// Drop the ";base64" (or any other parameters) from the header.
		if i := strings.IndexByte(header, ';'); i >= 0 {
			header = header[:i]
		}
		if header != "" && mimeType == "" {
			mimeType = header
		}
		payload = data[comma+1:]
	}
	// Tolerate base64 that carries whitespace/newlines (e.g. from a clipboard).
	payload = strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(payload)
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return Image{}, fmt.Errorf("invalid base64 image data: %w", err)
	}
	mime := detectImageMIME(raw)
	if mime == "" {
		mime = mimeType
	}
	if !supportedImageMIME(mime) {
		return Image{}, errors.New("unsupported image format (need PNG, JPEG, GIF or WebP)")
	}
	return Image{MIME: mime, Data: base64.StdEncoding.EncodeToString(raw), Detail: detail}, nil
}

// ResolveImage builds an Image from the three origins the protocol supports.
// URL wins (an external http(s) link passed through to the provider), then data
// (inline base64 / data URL), then path (read from disk and embedded inline).
// path is expected to already be resolved against the working directory. It
// errors when no origin is given or when the selected origin cannot be read.
func ResolveImage(path, rawURL, data, mimeType, detail string) (Image, error) {
	switch {
	case rawURL != "":
		return Image{URL: rawURL, MIME: mimeType, Detail: detail}, nil
	case data != "":
		return NewInlineImage(data, mimeType, detail)
	case path != "":
		im, err := LoadImageFile(path)
		if err != nil {
			return Image{}, err
		}
		im.Detail = detail
		return im, nil
	}
	return Image{}, errors.New("image requires one of path, url or data")
}
