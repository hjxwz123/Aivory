// Package generationcfg owns limits shared by the API generation lifecycle
// and LLM-side background work that must not race that lifecycle.
package generationcfg

import (
	"time"

	"aivory/server/internal/envcfg"
)

const (
	DefaultMaxDuration = 90 * time.Minute
	FinalizationMargin = time.Minute
)

// MaxDuration returns the maximum wall-clock duration of a detached chat
// generation. Every lifecycle consumer reads it here so an operator override
// cannot leave compaction with a shorter protection window.
func MaxDuration() time.Duration {
	d := envcfg.Dur("AIVORY_API_MAX_GEN_DURATION", DefaultMaxDuration)
	if d <= 0 {
		return DefaultMaxDuration
	}
	return d
}

// ProtectedDuration includes the detached final-persistence window used after
// generation cancellation. Saturate defensively for extreme environment values.
func ProtectedDuration() time.Duration {
	d := MaxDuration()
	if d > time.Duration(1<<63-1)-FinalizationMargin {
		return d
	}
	return d + FinalizationMargin
}
