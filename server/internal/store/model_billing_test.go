package store

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestModelBillingValidationRejectsNegativeAndNonUSDPrices(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO channels(id,name,type) VALUES('billing-channel','Billing','openai')`); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	cases := []Model{
		{ChannelID: "billing-channel", RequestID: "negative", Label: "Negative", PriceInput: -1, Currency: "USD"},
		{ChannelID: "billing-channel", RequestID: "nan", Label: "NaN", PriceOutput: math.NaN(), Currency: "USD"},
		{ChannelID: "billing-channel", RequestID: "eur", Label: "EUR", PriceInput: 1, Currency: "EUR"},
	}
	for _, model := range cases {
		if _, err := CreateModel(context.Background(), db, model); !errors.Is(err, ErrInvalidModelBilling) {
			t.Fatalf("CreateModel(%s) error = %v, want %v", model.RequestID, err, ErrInvalidModelBilling)
		}
	}
	created, err := CreateModel(ctx, db, Model{
		ChannelID: "billing-channel", RequestID: "usd", Label: "USD", PriceInput: 1, Currency: " usd ",
	})
	if err != nil {
		t.Fatalf("create USD model: %v", err)
	}
	if created.Currency != "USD" {
		t.Fatalf("normalized currency = %q, want USD", created.Currency)
	}
}
