package generationcfg

import (
	"testing"
	"time"
)

func TestMaxDurationUsesSafeDefaultForInvalidOrNonPositiveValues(t *testing.T) {
	for _, value := range []string{"invalid", "0", "-1m"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AIVORY_API_MAX_GEN_DURATION", value)
			if got := MaxDuration(); got != DefaultMaxDuration {
				t.Fatalf("MaxDuration() = %v, want %v", got, DefaultMaxDuration)
			}
		})
	}
}

func TestProtectedDurationTracksGenerationOverride(t *testing.T) {
	t.Setenv("AIVORY_API_MAX_GEN_DURATION", "3h")
	if got, want := ProtectedDuration(), 3*time.Hour+FinalizationMargin; got != want {
		t.Fatalf("ProtectedDuration() = %v, want %v", got, want)
	}
}
