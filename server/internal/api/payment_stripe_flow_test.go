package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aivory/server/internal/payment"
	"aivory/server/internal/store"
	"github.com/stripe/stripe-go/v86/webhook"
)

func TestStripeWebhookFulfillsCreditOrderExactlyOnce(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	const (
		webhookSecret  = "whsec_stripe_flow"
		providerOrder  = "cs_test_credit_flow"
		providerEvent  = "evt_stripe_credit_flow"
		stripeMethodID = "paym_stripe_flow"
	)

	channelConfig, err := json.Marshal(payment.StripeConfig{
		SecretKey: "sk_test_stripe_flow", WebhookSecret: webhookSecret,
	})
	if err != nil {
		t.Fatalf("marshal Stripe channel config: %v", err)
	}
	channel, err := store.CreatePaymentChannel(context.Background(), fx.db, store.PaymentChannel{
		ID: "paych_stripe_flow", Name: "Stripe flow", Provider: payment.ProviderStripe,
		Config: channelConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Stripe channel: %v", err)
	}
	methodConfig, err := json.Marshal(payment.StripeMethodConfig{})
	if err != nil {
		t.Fatalf("marshal Stripe method config: %v", err)
	}
	method, err := store.CreatePaymentMethod(context.Background(), fx.db, store.PaymentMethod{
		ID: stripeMethodID, ChannelID: channel.ID, Name: "Stripe card", Type: payment.ProviderStripe,
		Icon: "credit-card", ProviderMethodConfig: methodConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Stripe method: %v", err)
	}
	order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: fx.user.ID, PaymentMethodID: method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
	})
	if err != nil {
		t.Fatalf("create Stripe payment order: %v", err)
	}
	if _, err := store.MarkPaymentOrderProcessing(context.Background(), fx.db, order.ID, providerOrder); err != nil {
		t.Fatalf("mark Stripe checkout started: %v", err)
	}

	payload, err := json.Marshal(map[string]any{
		"id": providerEvent, "object": "event", "type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": providerOrder, "object": "checkout.session",
			"client_reference_id": order.ID, "metadata": map[string]string{"order_id": order.ID},
			"mode": "payment", "status": "complete", "payment_status": "paid",
			"amount_total": order.AmountMinor, "currency": "usd",
		}},
	})
	if err != nil {
		t.Fatalf("marshal Stripe webhook: %v", err)
	}
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload, Secret: webhookSecret, Timestamp: time.Now(),
	})

	serveWebhook := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/payments/webhooks/"+channel.ID, bytes.NewReader(payload))
		req.Header.Set("Stripe-Signature", signed.Header)
		req = paymentAPIRequest(req, nil, map[string]string{"channelId": channel.ID})
		rec := httptest.NewRecorder()
		paymentWebhookHandler(fx.d, rec, req)
		return rec
	}
	for attempt := 1; attempt <= 2; attempt++ {
		rec := serveWebhook()
		if rec.Code != http.StatusOK {
			t.Fatalf("Stripe callback attempt %d status = %d, want %d; body=%s", attempt, rec.Code, http.StatusOK, rec.Body.String())
		}
		var response struct {
			Received bool `json:"received"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || !response.Received {
			t.Fatalf("Stripe callback attempt %d response = %q, decode error = %v", attempt, rec.Body.String(), err)
		}
	}

	var credits float64
	if err := fx.db.QueryRow(`SELECT credits_permanent FROM users WHERE id=?`, fx.user.ID).Scan(&credits); err != nil {
		t.Fatalf("query permanent credits: %v", err)
	}
	if credits != 25+fx.pkg.Credits {
		t.Fatalf("permanent credits = %v, want %v after duplicate Stripe callback", credits, 25+fx.pkg.Credits)
	}
	storedOrder, err := store.GetPaymentOrder(context.Background(), fx.db, order.ID)
	if err != nil {
		t.Fatalf("get fulfilled Stripe order: %v", err)
	}
	if storedOrder.Status != store.PaymentOrderFulfilled || storedOrder.ProviderOrderID != providerOrder ||
		storedOrder.PaidAt == 0 || storedOrder.FulfilledAt == 0 {
		t.Fatalf("fulfilled Stripe order = %+v", storedOrder)
	}
	var eventCount int
	var processedAt int64
	if err := fx.db.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(processed_at),0) FROM payment_events WHERE order_id=?`, order.ID,
	).Scan(&eventCount, &processedAt); err != nil {
		t.Fatalf("query Stripe payment events: %v", err)
	}
	if eventCount != 1 || processedAt == 0 {
		t.Fatalf("Stripe payment events = %d, processed_at = %d; want one processed event", eventCount, processedAt)
	}

	historyReq := httptest.NewRequest(http.MethodGet, "/api/payments/orders", nil)
	historyReq = paymentAPIRequest(historyReq, fx.user, nil)
	historyRec := httptest.NewRecorder()
	listPaymentOrdersForUserHandler(fx.d, historyRec, historyReq)
	if historyRec.Code != http.StatusOK {
		t.Fatalf("payment history status = %d, want %d; body=%s", historyRec.Code, http.StatusOK, historyRec.Body.String())
	}
	var history struct {
		Orders []publicPaymentOrderListItem `json:"orders"`
		Total  int                          `json:"total"`
	}
	if err := json.Unmarshal(historyRec.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode payment history: %v (%s)", err, historyRec.Body.String())
	}
	if history.Total != 1 || len(history.Orders) != 1 {
		t.Fatalf("payment history = %+v, want one order", history)
	}
	record := history.Orders[0]
	if record.ID != order.ID || record.Status != "paid" || record.Provider != payment.ProviderStripe ||
		record.MethodName != method.Name || record.TargetName != fx.pkg.Name ||
		record.AmountMinor != fx.pkg.PriceAmountMinor || record.Currency != "USD" ||
		record.CreatedAt == 0 || record.PaidAt == 0 {
		t.Fatalf("Stripe payment history record = %+v", record)
	}
}
