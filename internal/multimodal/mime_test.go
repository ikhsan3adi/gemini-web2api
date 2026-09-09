package multimodal

import (
	"encoding/base64"
	"testing"
)

func TestDetectImageMime(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "PNG",
			data: []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x00},
			want: "image/png",
		},
		{
			name: "JPEG",
			data: []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01},
			want: "image/jpeg",
		},
		{
			name: "GIF89a",
			data: []byte("GIF89a\x00\x00\x00\x00\x00\x00"),
			want: "image/gif",
		},
		{
			name: "WEBP",
			data: []byte("RIFF\x00\x00\x00\x00WEBP"),
			want: "image/webp",
		},
		{
			name: "BMP",
			data: []byte{'B', 'M', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			want: "image/bmp",
		},
		{
			name: "TIFF little-endian",
			data: []byte{'I', 'I', 0x2A, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			want: "image/tiff",
		},
		{
			name: "TIFF big-endian",
			data: []byte{'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			want: "image/tiff",
		},
		{
			name: "AVIF",
			data: []byte{0x00, 0x00, 0x00, 0x00, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f'},
			want: "image/avif",
		},
		{
			name: "HEIC",
			data: []byte{0x00, 0x00, 0x00, 0x00, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'},
			want: "image/heic",
		},
		{
			name: "too short",
			data: []byte{0x89, 0x50},
			want: "image/png", // fallback
		},
		{
			name: "empty",
			data: []byte{},
			want: "image/png", // fallback
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectImageMime(tt.data)
			if got != tt.want {
				t.Errorf("DetectImageMime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeBase64Raw(t *testing.T) {
	// Standard base64
	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	got, err := DecodeBase64Raw(encoded)
	if err != nil {
		t.Fatalf("DecodeBase64Raw standard: unexpected error: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("DecodeBase64Raw standard = %q, want %q", string(got), "hello")
	}

	// URL-safe base64
	encodedURL := base64.URLEncoding.EncodeToString([]byte("hello world"))
	got, err = DecodeBase64Raw(encodedURL)
	if err != nil {
		t.Fatalf("DecodeBase64Raw URL-safe: unexpected error: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("DecodeBase64Raw URL-safe = %q, want %q", string(got), "hello world")
	}

	// Raw standard (no padding)
	encodedRaw := base64.RawStdEncoding.EncodeToString([]byte("test"))
	got, err = DecodeBase64Raw(encodedRaw)
	if err != nil {
		t.Fatalf("DecodeBase64Raw raw: unexpected error: %v", err)
	}
	if string(got) != "test" {
		t.Errorf("DecodeBase64Raw raw = %q, want %q", string(got), "test")
	}
}
