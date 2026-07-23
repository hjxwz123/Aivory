package llm

import (
	"encoding/json"
	"math"
	"strconv"

	"aivory/server/internal/envcfg"
)

var imageGenerationCountMax = envcfg.Int("AIVORY_TOOLS_IN_N", 4)

// ClampImageGenerationCount is the single quantity boundary used by image-mode
// quota projection and image_generate execution. Keeping the clamp here avoids
// a param-control mapping being metered as one image but sent upstream as many.
func ClampImageGenerationCount(n int) int {
	return clampImageGenerationCount(n, imageGenerationCountMax)
}

func clampImageGenerationCount(n, maxCount int) int {
	if maxCount < 1 {
		maxCount = 1
	}
	if n < 1 {
		return 1
	}
	if n > maxCount {
		return maxCount
	}
	return n
}

// imageGenerationCountFromParams reads the top-level provider "n" emitted by
// a declared param-control mapping. JSON decoding normally yields float64, but
// the other scalar forms keep this helper reliable in direct/internal callers.
func imageGenerationCountFromParams(params map[string]any) int {
	if len(params) == 0 {
		return ClampImageGenerationCount(1)
	}
	raw, ok := params["n"]
	if !ok {
		return ClampImageGenerationCount(1)
	}
	var n int
	switch value := raw.(type) {
	case int:
		n = value
	case int8:
		n = int(value)
	case int16:
		n = int(value)
	case int32:
		n = int(value)
	case int64:
		n = int(value)
	case uint:
		if uint64(value) <= uint64(math.MaxInt) {
			n = int(value)
		}
	case uint8:
		n = int(value)
	case uint16:
		n = int(value)
	case uint32:
		n = int(value)
	case uint64:
		if value <= uint64(math.MaxInt) {
			n = int(value)
		}
	case float32:
		n = int(value)
	case float64:
		n = int(value)
	case json.Number:
		parsed, _ := value.Int64()
		n = int(parsed)
	case string:
		n, _ = strconv.Atoi(value)
	}
	return ClampImageGenerationCount(n)
}
