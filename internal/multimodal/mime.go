package multimodal

import (
	"encoding/base64"
)

// DecodeBase64Raw decodes a base64 string (standard or URL-safe) into bytes.
func DecodeBase64Raw(s string) ([]byte, error) {
	// Try standard encoding first
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Try URL-safe encoding
	if data, err := base64.URLEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Try raw standard (no padding)
	if data, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Try raw URL-safe (no padding)
	return base64.RawURLEncoding.DecodeString(s)
}

// DetectImageMime sniffs the MIME type from image data using magic bytes.
// Returns a MIME type string; falls back to "image/png" if unrecognized.
func DetectImageMime(data []byte) string {
	if len(data) < 12 {
		return "image/png"
	}

	// PNG: 89 50 4E 47 0D 0A 1A 0A
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 &&
		data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A {
		return "image/png"
	}

	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}

	// GIF: GIF87a or GIF89a
	if data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return "image/gif"
	}

	// WEBP: RIFF....WEBP
	if data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp"
	}

	// BMP: BM
	if data[0] == 'B' && data[1] == 'M' {
		return "image/bmp"
	}

	// TIFF: II (little-endian) or MM (big-endian)
	if (data[0] == 'I' && data[1] == 'I') || (data[0] == 'M' && data[1] == 'M') {
		return "image/tiff"
	}

	// AVIF/HEIC: ....ftyp
	if data[4] == 'f' && data[5] == 't' && data[6] == 'y' && data[7] == 'p' {
		if len(data) >= 12 {
			brand := string(data[8:12])
			switch brand {
			case "avif", "avis":
				return "image/avif"
			case "heic", "heix", "mif1", "msf1":
				return "image/heic"
			}
		}
		return "image/png" // fallback for unknown ftyp
	}

	// Upstream parity: unknown signatures fall back to image/png.
	return "image/png"
}
