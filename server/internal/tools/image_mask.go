package tools

import (
	"bytes"
	"errors"
	"image"
	"image/png"
)

const maxMaskImagePixels = 16 * 1024 * 1024
const maxMaskImageBytes = 50 * 1024 * 1024

// Normalize the base to PNG without resizing. Mask coordinates must stay in
// the source's original pixel space; transparent mask pixels are editable.
func prepareImageMask(base, mask imageBytes) (imageBytes, imageBytes, error) {
	invalid := errors.New("image_edit_invalid_mask")
	if mask.mime != "image/png" || len(base.data) >= maxMaskImageBytes || len(mask.data) >= maxMaskImageBytes {
		return base, mask, invalid
	}
	baseConfig, _, err := image.DecodeConfig(bytes.NewReader(base.data))
	if err != nil {
		return base, mask, invalid
	}
	maskConfig, format, err := image.DecodeConfig(bytes.NewReader(mask.data))
	if err != nil || format != "png" || baseConfig.Width != maskConfig.Width || baseConfig.Height != maskConfig.Height ||
		baseConfig.Width <= 0 || baseConfig.Height <= 0 || baseConfig.Width > 8192 || baseConfig.Height > 8192 ||
		int64(baseConfig.Width)*int64(baseConfig.Height) > maxMaskImagePixels {
		return base, mask, invalid
	}
	decodedMask, err := png.Decode(bytes.NewReader(mask.data))
	if err != nil {
		return base, mask, invalid
	}
	hasTransparent := false
	bounds := decodedMask.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y && !hasTransparent; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := decodedMask.At(x, y).RGBA()
			if alpha == 0 {
				hasTransparent = true
				break
			}
		}
	}
	if !hasTransparent {
		return base, mask, invalid
	}
	decodedBase, baseFormat, err := image.Decode(bytes.NewReader(base.data))
	if err != nil {
		return base, mask, invalid
	}
	if baseFormat != "png" {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, decodedBase); err != nil {
			return base, mask, invalid
		}
		if encoded.Len() >= maxMaskImageBytes {
			return base, mask, invalid
		}
		base = imageBytes{data: encoded.Bytes(), mime: "image/png"}
	}
	base.mime = "image/png"
	return base, mask, nil
}
