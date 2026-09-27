package llm

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The smallest valid PNG (1x1 transparent), used to exercise format detection.
const onePixelPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

func decodePNG(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(onePixelPNGBase64)
	if err != nil {
		t.Fatalf("decode test png: %v", err)
	}
	return raw
}

func TestLoadImageFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "img.png")
	if err := os.WriteFile(path, decodePNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	im, err := LoadImageFile(path)
	if err != nil {
		t.Fatalf("LoadImageFile: %v", err)
	}
	if im.MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", im.MIME)
	}
	if im.Data != onePixelPNGBase64 {
		t.Errorf("Data = %q, want the base64 payload", im.Data)
	}
	if im.URL != "" {
		t.Errorf("URL = %q, want empty for an inline image", im.URL)
	}
}

func TestLoadImageFileErrors(t *testing.T) {
	// missing file
	if _, err := LoadImageFile(filepath.Join(t.TempDir(), "nope.png")); err == nil {
		t.Error("expected error for missing file")
	}
	// empty file
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadImageFile(empty); err == nil {
		t.Error("expected error for empty file")
	}
	// unsupported content
	txt := filepath.Join(dir, "not.txt")
	if err := os.WriteFile(txt, []byte("just text, not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadImageFile(txt); err == nil {
		t.Error("expected error for non-image content")
	}
}

func TestDetectImageMIME(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", decodePNG(t), "image/png"},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, "image/jpeg"},
		{"gif", []byte("GIF89a"), "image/gif"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP"), "image/webp"},
		{"text", []byte("hello world"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectImageMIME(tc.data); got != tc.want {
				t.Errorf("detectImageMIME(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestSupportedImageMIME(t *testing.T) {
	for _, m := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if !supportedImageMIME(m) {
			t.Errorf("supportedImageMIME(%q) = false, want true", m)
		}
	}
	if supportedImageMIME("image/bmp") {
		t.Error("supportedImageMIME(image/bmp) = true, want false")
	}
}

func TestNewInlineImageRawBase64(t *testing.T) {
	im, err := NewInlineImage(onePixelPNGBase64, "", "high")
	if err != nil {
		t.Fatalf("NewInlineImage: %v", err)
	}
	if im.MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", im.MIME)
	}
	if im.Detail != "high" {
		t.Errorf("Detail = %q, want high", im.Detail)
	}
	if im.DataURL() != "data:image/png;base64,"+onePixelPNGBase64 {
		t.Errorf("DataURL() = %q", im.DataURL())
	}
}

func TestNewInlineImageDataURL(t *testing.T) {
	// A full data URL, with whitespace in the payload to be tolerated.
	withNewlines := onePixelPNGBase64[:10] + "\n" + onePixelPNGBase64[10:] + "\r\n"
	im, err := NewInlineImage("data:image/png;base64,"+withNewlines, "", "")
	if err != nil {
		t.Fatalf("NewInlineImage(data URL): %v", err)
	}
	if im.MIME != "image/png" || im.Data != onePixelPNGBase64 {
		t.Errorf("unexpected image: %+v", im)
	}
}

func TestNewInlineImageFallbackToDeclaredMIME(t *testing.T) {
	// Content is not a recognizable image, but the client declared a supported
	// MIME type: accept it using the declared type.
	raw := base64.StdEncoding.EncodeToString([]byte("not really an image"))
	im, err := NewInlineImage(raw, "image/png", "")
	if err != nil {
		t.Fatalf("NewInlineImage(declared mime): %v", err)
	}
	if im.MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png (declared fallback)", im.MIME)
	}
}

func TestNewInlineImageErrors(t *testing.T) {
	if _, err := NewInlineImage("", "", ""); err == nil {
		t.Error("expected error for empty data")
	}
	if _, err := NewInlineImage("data:image/png;base64", "", ""); err == nil {
		t.Error("expected error for malformed data URL (no comma)")
	}
	if _, err := NewInlineImage("!!!not base64!!!", "", ""); err == nil {
		t.Error("expected error for invalid base64")
	}
	notImage := base64.StdEncoding.EncodeToString([]byte("plain text"))
	if _, err := NewInlineImage(notImage, "", ""); err == nil {
		t.Error("expected error for unsupported content with no declared mime")
	}
}

func TestResolveImageOrigins(t *testing.T) {
	// URL wins and is passed through (with the declared mime/detail).
	im, err := ResolveImage("", "https://example.com/a.jpg", "", "image/jpeg", "low")
	if err != nil {
		t.Fatalf("ResolveImage(url): %v", err)
	}
	if im.URL != "https://example.com/a.jpg" || im.MIME != "image/jpeg" || im.Detail != "low" {
		t.Errorf("unexpected url image: %+v", im)
	}
	// Data is embedded inline.
	im, err = ResolveImage("", "", onePixelPNGBase64, "", "")
	if err != nil {
		t.Fatalf("ResolveImage(data): %v", err)
	}
	if im.Data != onePixelPNGBase64 {
		t.Errorf("unexpected inline image: %+v", im)
	}
	// Path is read from disk and carries the detail.
	dir := t.TempDir()
	path := filepath.Join(dir, "p.png")
	if err := os.WriteFile(path, decodePNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	im, err = ResolveImage(path, "", "", "", "original")
	if err != nil {
		t.Fatalf("ResolveImage(path): %v", err)
	}
	if im.MIME != "image/png" || im.Detail != "original" {
		t.Errorf("unexpected path image: %+v", im)
	}
	// No origin at all.
	if _, err := ResolveImage("", "", "", "", ""); err == nil {
		t.Error("expected error when no origin is given")
	}
}

func TestImageDataURL(t *testing.T) {
	if got := (Image{URL: "https://example.com/x.jpg"}).DataURL(); got != "" {
		t.Errorf("DataURL() for url image = %q, want empty", got)
	}
	if got := (Image{Data: "AAAA"}).DataURL(); !strings.HasPrefix(got, "data:application/octet-stream;base64,AAAA") {
		t.Errorf("DataURL() default mime = %q", got)
	}
}
