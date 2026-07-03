package api

import (
	"encoding/json"
	"testing"
)

func TestCreateModelReqTracksExplicitResearchEnabled(t *testing.T) {
	var disabled createModelReq
	if err := json.Unmarshal([]byte(`{"channel_id":"ch1","request_id":"m1","label":"M1","research_enabled":false}`), &disabled); err != nil {
		t.Fatalf("unmarshal disabled: %v", err)
	}
	if disabled.ResearchEnabled == nil {
		t.Fatal("expected explicit research_enabled=false to be tracked")
	}
	if *disabled.ResearchEnabled {
		t.Fatal("expected explicit research_enabled=false to decode as false")
	}

	var omitted createModelReq
	if err := json.Unmarshal([]byte(`{"channel_id":"ch1","request_id":"m2","label":"M2"}`), &omitted); err != nil {
		t.Fatalf("unmarshal omitted: %v", err)
	}
	if omitted.ResearchEnabled != nil {
		t.Fatal("expected omitted research_enabled to stay nil")
	}
}
