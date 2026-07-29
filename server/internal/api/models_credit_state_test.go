package api

import (
	"context"
	"testing"

	"aivory/server/internal/store"
)

func TestModelUsesCreditsWhenGroupHasNoFreeGrant(t *testing.T) {
	model := store.Model{ID: "m_paid"}
	if !modelUsesCredits(context.Background(), Deps{}, "u_paid", model, map[string]store.ModelGroupQuota{}) {
		t.Fatal("model without a free grant must be shown as credit-paid")
	}
}

func TestModelWithExplicitUnlimitedGrantDoesNotUseCredits(t *testing.T) {
	model := store.Model{ID: "m_free"}
	grants := map[string]store.ModelGroupQuota{
		model.ID: {ModelID: model.ID, GroupID: "ug_free", LimitValue: 0},
	}
	if modelUsesCredits(context.Background(), Deps{}, "u_free", model, grants) {
		t.Fatal("explicit unlimited free grant must not be shown as credit-paid")
	}
}
