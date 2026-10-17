package multimodal

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"math"

	"golang.org/x/image/draw"
)

const (
	MaxImageB64Size    = 50000
	MaxImageDimension  = 256
	DefaultJPEGQuality = 60
)

func CompressIfNeeded(b64 string, maxSize int) (string, error) {
	if maxSize <= 0 {
		maxSize = MaxImageB64Size
	}

	if len(b64) <= maxSize {
		return b64, nil
	}

	imgData, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 image: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(imgData))
	if err != nil {
		return "", fmt.Errorf("failed to decode image format: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	ratio := math.Min(float64(MaxImageDimension)/float64(width), float64(MaxImageDimension)/float64(height))

	dstImg := img
	if ratio < 1.0 {
		newWidth := int(float64(width) * ratio)
		newHeight := int(float64(height) * ratio)
		dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
		dstImg = dst
	}

	var buf bytes.Buffer
	err = jpeg.Encode(&buf, dstImg, &jpeg.Options{Quality: DefaultJPEGQuality})
	if err != nil {
		return "", fmt.Errorf("failed to encode jpeg: %w", err)
	}

	compressedB64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	return compressedB64, nil
}
