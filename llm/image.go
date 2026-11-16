// Copyright 2026 Juca Crispim <juca@poraodojuca.dev>
//
// This file is part of llm-bridge.
//
// llm-bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// llm-bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with llm-bridge. If not, see <http://www.gnu.org/licenses/>.

package llm

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Image is an image attachment on a user message: inline (Data) or an external
// http(s) URL passed through to the provider. Once resolved, one of the two is
// set. Both providers reject an image on a system or assistant message.
type Image struct {
	// MIME is the media type (image/png, image/jpeg, image/gif, image/webp).
	// Detected from the bytes for an inline image; for a URL image it is
	// whatever the client declared, and may be empty.
	MIME string
	// Data is the base64 payload without the data: URL prefix.
	Data string
	// URL is an external http(s) image, passed through to the provider.
	URL string
	// Detail is DeepSeek's image processing hint ("low", "high", "original",
	// "auto"). Empty leaves it unset.
	Detail string
}

// DataURL returns the data:<mime>;base64,<data> form of an inline image, or ""
// for a URL image.
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

// supportedImageMIME reports whether mime is one of the four formats both
// providers accept.
func supportedImageMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// detectImageMIME sniffs the media type from the bytes and returns it only when
// it is a supported format, "" otherwise. The bytes decide, not the file name or
// a declared type.
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

// LoadImageFile reads a local image, detects its format from the bytes and
// returns it base64-encoded. An empty or unsupported file is an error.
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

// NewInlineImage builds an inline image from client data, a data: URL or raw
// base64. The format comes from the bytes, falling back to the declared type.
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

// ResolveImage builds an Image from the three origins the protocol supports, in
// that order of precedence: url, data, path. path is expected to be resolved
// against the working directory already.
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
