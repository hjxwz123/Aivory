package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"
)

func TestMinorAmountRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		currency  string
		minor     int64
		formatted string
	}{
		{currency: "USD", minor: 1234, formatted: "12.34"},
		{currency: "JPY", minor: 1234, formatted: "1234"},
		{currency: "KWD", minor: 1234, formatted: "1.234"},
		{currency: "CLF", minor: 1234, formatted: "0.1234"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.currency, func(t *testing.T) {
			formatted, err := FormatMinorAmount(tc.minor, tc.currency)
			if err != nil {
				t.Fatal(err)
			}
			if formatted != tc.formatted {
				t.Fatalf("FormatMinorAmount() = %q, want %q", formatted, tc.formatted)
			}
			parsed, err := ParseMinorAmount(formatted, tc.currency)
			if err != nil {
				t.Fatal(err)
			}
			if parsed != tc.minor {
				t.Fatalf("ParseMinorAmount() = %d, want %d", parsed, tc.minor)
			}
		})
	}
}

func TestIsCheckoutStateUnknownClassifiesAmbiguousTransportErrors(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		ErrCheckoutStateUnknown,
		context.Canceled,
		context.DeadlineExceeded,
		io.EOF,
		io.ErrUnexpectedEOF,
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
	} {
		if !IsCheckoutStateUnknown(err) {
			t.Errorf("IsCheckoutStateUnknown(%v) = false", err)
		}
	}
	if IsCheckoutStateUnknown(errors.New("provider rejected request")) {
		t.Fatal("definitive provider error was classified as unknown")
	}
}

func TestParseMinorAmountRejectsPrecisionAndSigns(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"-1.00", "+1.00", "1.001", "1e2", ""} {
		if _, err := ParseMinorAmount(value, "USD"); err == nil {
			t.Fatalf("ParseMinorAmount(%q) unexpectedly succeeded", value)
		}
	}
}

func TestEPaySignAndVerify(t *testing.T) {
	t.Parallel()
	params := map[string]string{
		"pid":           "1000",
		"type":          "alipay",
		"out_trade_no":  "po_test",
		"trade_status":  "TRADE_SUCCESS",
		"money":         "1.00",
		"sign_type":     "MD5",
		"ignored_empty": "",
	}
	params["sign"] = EPaySign(params, "secret")
	if params["sign"] != "6c4611edf3c690852340b9f2f454ce08" {
		t.Fatalf("EPaySign() = %q", params["sign"])
	}
	event, err := VerifyEPayEvent(params, EPayConfig{MerchantID: "1000", MerchantKey: "secret", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if event.Status != EventPaid || event.OrderID != "po_test" || event.AmountMinor != 100 ||
		event.PaidAmountMinor != 100 || event.TaxAmountMinor != 0 || event.MethodType != "alipay" {
		t.Fatalf("unexpected EPay event: %#v", event)
	}
	params["money"] = "0.01"
	if _, err := VerifyEPayEvent(params, EPayConfig{MerchantID: "1000", MerchantKey: "secret", Currency: "USD"}); err == nil {
		t.Fatal("tampered EPay payload unexpectedly verified")
	}
}

func TestEPayCreateCheckoutAcceptsGatewayRootAndSubmitURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		gatewayURL string
		wantURL    string
	}{
		{name: "gateway root", gatewayURL: "https://epay.example.test/gateway", wantURL: "https://epay.example.test/gateway/submit.php"},
		{name: "complete submit URL", gatewayURL: "https://epay.example.test/gateway/submit.php", wantURL: "https://epay.example.test/gateway/submit.php"},
		{name: "case insensitive submit URL", gatewayURL: "https://epay.example.test/Submit.PHP", wantURL: "https://epay.example.test/Submit.PHP"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, err := (EPayGateway{
				Config: EPayConfig{
					GatewayURL: tc.gatewayURL, MerchantID: "1000", MerchantKey: "secret", Currency: "USD",
				},
				Method: EPayMethodConfig{Type: "alipay"},
			}).CreateCheckout(context.Background(), CheckoutRequest{
				OrderID: "po_epay_url", Name: "Credits", AmountMinor: 1299, Currency: "USD",
				NotifyURL:  "https://app.example.test/api/payments/webhooks/paych_epay",
				SuccessURL: "https://app.example.test/subscription?payment=return",
			})
			if err != nil {
				t.Fatal(err)
			}
			if action.Type != ActionFormPost || action.URL != tc.wantURL {
				t.Fatalf("checkout action = %+v, want form POST to %q", action, tc.wantURL)
			}
		})
	}
}

func TestValidateStripeConfigSecretKeyPrefixes(t *testing.T) {
	t.Parallel()
	for _, key := range []string{
		"sk_test_example",
		"rk_test_example",
		"rkcs_test_example",
		"  rkcs_test_trimmed  ",
	} {
		if err := ValidateStripeConfig(StripeConfig{SecretKey: key, WebhookSecret: "whsec_test"}); err != nil {
			t.Errorf("ValidateStripeConfig() rejected supported key prefix for %q: %v", key, err)
		}
	}
	for _, key := range []string{
		"",
		"pk_test_public_key",
		"rkcs-test-invalid",
		"stripe_secret_key",
	} {
		if err := ValidateStripeConfig(StripeConfig{SecretKey: key, WebhookSecret: "whsec_test"}); err == nil {
			t.Errorf("ValidateStripeConfig() accepted invalid secret key %q", key)
		}
	}
}

func TestValidateStripeSetupConfigAllowsOnlyMissingWebhookSecret(t *testing.T) {
	t.Parallel()
	if err := ValidateStripeSetupConfig(StripeConfig{SecretKey: "sk_test_setup"}); err != nil {
		t.Fatalf("disabled setup config was rejected: %v", err)
	}
	if err := ValidateStripeConfig(StripeConfig{SecretKey: "sk_test_setup"}); err == nil {
		t.Fatal("enabled Stripe config without webhook secret was accepted")
	}
	if err := ValidateStripeSetupConfig(StripeConfig{SecretKey: "sk_test_setup", WebhookSecret: "invalid"}); err == nil {
		t.Fatal("disabled Stripe config with malformed webhook secret was accepted")
	}
	if err := ValidateStripeSetupConfig(StripeConfig{WebhookSecret: "whsec_setup"}); err == nil {
		t.Fatal("disabled Stripe config without a secret key was accepted")
	}
}

func TestStripeGatewayCreatesHostedCheckout(t *testing.T) {
	t.Parallel()
	const (
		orderID   = "po_stripe_checkout"
		sessionID = "cs_test_checkout"
	)
	expiresAt := time.Now().Add(30 * time.Minute).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
			t.Fatalf("unexpected Stripe request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk_test_checkout" {
			t.Fatalf("Stripe authorization = %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != orderID {
			t.Fatalf("Stripe idempotency key = %q, want %q", got, orderID)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse Stripe checkout form: %v", err)
		}
		want := map[string]string{
			"client_reference_id": orderID,
			"customer_email":      "buyer@example.test",
			"mode":                "payment",
			"success_url":         "https://app.example.test/subscription?payment=return",
			"cancel_url":          "https://app.example.test/subscription?payment=cancel",
			"metadata[order_id]":  orderID,
			"payment_intent_data[metadata][order_id]":       orderID,
			"line_items[0][price_data][currency]":           "eur",
			"line_items[0][price_data][unit_amount]":        "1299",
			"line_items[0][price_data][product_data][name]": "1,000 credits",
			"line_items[0][quantity]":                       "1",
		}
		for key, value := range want {
			if got := r.Form.Get(key); got != value {
				t.Errorf("Stripe checkout %s = %q, want %q", key, got, value)
			}
		}
		for key := range r.Form {
			if strings.HasPrefix(key, "payment_method_types") {
				t.Errorf("Stripe checkout unexpectedly pins payment methods with %q", key)
			}
		}
		if got := r.Form.Get("integration_identifier"); !strings.HasPrefix(got, "aivory_") {
			t.Errorf("Stripe integration identifier = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"id": sessionID, "object": "checkout.session",
			"url":        "https://checkout.stripe.com/c/pay/" + sessionID,
			"expires_at": expiresAt,
		}); err != nil {
			t.Fatalf("encode Stripe response: %v", err)
		}
	}))
	defer server.Close()

	action, err := (StripeGateway{
		Config: StripeConfig{SecretKey: "sk_test_checkout", WebhookSecret: "whsec_checkout"},
		Backends: stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			URL: stripe.String(server.URL), HTTPClient: server.Client(),
		}),
	}).CreateCheckout(context.Background(), CheckoutRequest{
		OrderID: orderID, Name: "1,000 credits", AmountMinor: 1299, Currency: "EUR",
		UserEmail:  "buyer@example.test",
		SuccessURL: "https://app.example.test/subscription?payment=return",
		CancelURL:  "https://app.example.test/subscription?payment=cancel",
	})
	if err != nil {
		t.Fatalf("create Stripe checkout: %v", err)
	}
	if action.Type != ActionRedirect || action.URL != "https://checkout.stripe.com/c/pay/"+sessionID ||
		action.ProviderOrderID != sessionID || action.SessionID != sessionID || action.ExpiresAt != expiresAt {
		t.Fatalf("Stripe checkout action = %+v", action)
	}
}

func TestStripeIntegrationIdentifierIsStableAndOrderScoped(t *testing.T) {
	t.Parallel()
	first := stripeIntegrationIdentifier("po_example_one")
	if first != stripeIntegrationIdentifier("po_example_one") {
		t.Fatal("Stripe integration identifier changed for the same order")
	}
	if first == stripeIntegrationIdentifier("po_example_two") {
		t.Fatal("Stripe integration identifier collided for distinct test orders")
	}
	const prefix = "aivory_"
	if !strings.HasPrefix(first, prefix) || len(first) != len(prefix)+8 {
		t.Fatalf("Stripe integration identifier = %q, want %q plus eight letters", first, prefix)
	}
	for _, char := range first[len(prefix):] {
		if char < 'a' || char > 'z' {
			t.Fatalf("Stripe integration identifier suffix contains non-letter %q: %q", char, first)
		}
	}
}

func TestVerifyStripePaidCheckout(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
  "id":"evt_paid",
  "object":"event",
  "type":"checkout.session.completed",
  "data":{"object":{
    "id":"cs_test_1",
    "object":"checkout.session",
    "client_reference_id":"po_test",
    "metadata":{"order_id":"po_test"},
    "mode":"payment",
    "status":"complete",
    "payment_status":"paid",
    "amount_total":1299,
    "currency":"usd"
  }}
}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   payload,
		Secret:    "whsec_test",
		Timestamp: time.Now(),
	})
	event, err := VerifyStripeEvent(payload, signed.Header, StripeConfig{WebhookSecret: "whsec_test"})
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "evt_paid" || event.Status != EventPaid || event.OrderID != "po_test" ||
		event.AmountMinor != 1299 || event.PaidAmountMinor != 1299 || event.TaxAmountMinor != 0 || event.Currency != "USD" {
		t.Fatalf("unexpected Stripe event: %#v", event)
	}
}

func TestVerifyStripeRejectsMismatchedOrderReferences(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
  "id":"evt_bad",
  "object":"event",
  "type":"checkout.session.completed",
  "data":{"object":{
    "id":"cs_test_2",
    "client_reference_id":"po_one",
    "metadata":{"order_id":"po_two"},
    "mode":"payment",
    "status":"complete",
    "payment_status":"paid",
    "amount_total":100,
    "currency":"usd"
  }}
}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_test"})
	if _, err := VerifyStripeEvent(payload, signed.Header, StripeConfig{WebhookSecret: "whsec_test"}); err == nil {
		t.Fatal("mismatched Stripe order references unexpectedly verified")
	}
}

func TestVerifyStripeCheckoutLifecycleEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		eventType     string
		sessionStatus string
		paymentStatus string
		wantStatus    string
		wantFailure   string
	}{
		{
			name:          "completed unpaid remains processing",
			eventType:     "checkout.session.completed",
			sessionStatus: "complete",
			paymentStatus: "unpaid",
			wantStatus:    EventProcessing,
		},
		{
			name:          "asynchronous payment succeeded",
			eventType:     "checkout.session.async_payment_succeeded",
			sessionStatus: "complete",
			paymentStatus: "paid",
			wantStatus:    EventPaid,
		},
		{
			name:          "asynchronous payment failed",
			eventType:     "checkout.session.async_payment_failed",
			sessionStatus: "complete",
			paymentStatus: "unpaid",
			wantStatus:    EventFailed,
			wantFailure:   "Stripe asynchronous payment failed",
		},
		{
			name:          "checkout expired",
			eventType:     "checkout.session.expired",
			sessionStatus: "expired",
			paymentStatus: "unpaid",
			wantStatus:    EventExpired,
		},
	}
	for index, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eventID := fmt.Sprintf("evt_lifecycle_%d", index)
			sessionID := fmt.Sprintf("cs_test_lifecycle_%d", index)
			payload := stripeCheckoutEventPayload(t, stripeCheckoutEventFixture{
				EventID: eventID, EventType: tc.eventType, OrderID: "po_lifecycle",
				SessionID: sessionID, SessionStatus: tc.sessionStatus, PaymentStatus: tc.paymentStatus,
			})
			signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
				Payload: payload, Secret: "whsec_lifecycle", Timestamp: time.Now(),
			})

			event, err := VerifyStripeEvent(payload, signed.Header, StripeConfig{WebhookSecret: "whsec_lifecycle"})
			if err != nil {
				t.Fatalf("verify Stripe event: %v", err)
			}
			if event.ID != eventID || event.Type != tc.eventType || event.Status != tc.wantStatus ||
				event.OrderID != "po_lifecycle" || event.ProviderOrderID != sessionID ||
				event.AmountMinor != 1299 || event.Currency != "USD" || event.FailureReason != tc.wantFailure {
				t.Fatalf("Stripe lifecycle event = %#v", event)
			}
		})
	}
}

func TestVerifyStripeRejectsInvalidAndExpiredSignatures(t *testing.T) {
	t.Parallel()
	payload := stripeCheckoutEventPayload(t, stripeCheckoutEventFixture{
		EventID: "evt_signature", EventType: "checkout.session.completed", OrderID: "po_signature",
		SessionID: "cs_test_signature", SessionStatus: "complete", PaymentStatus: "paid",
	})
	tests := []struct {
		name      string
		signature string
	}{
		{
			name: "wrong secret",
			signature: webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
				Payload: payload, Secret: "whsec_another_secret", Timestamp: time.Now(),
			}).Header,
		},
		{
			name: "expired timestamp",
			signature: webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
				Payload: payload, Secret: "whsec_signature", Timestamp: time.Now().Add(-10 * time.Minute),
			}).Header,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := VerifyStripeEvent(payload, tc.signature, StripeConfig{WebhookSecret: "whsec_signature"}); err == nil {
				t.Fatal("invalid Stripe signature unexpectedly verified")
			}
		})
	}
}

type stripeCheckoutEventFixture struct {
	EventID       string
	EventType     string
	OrderID       string
	SessionID     string
	SessionStatus string
	PaymentStatus string
}

func stripeCheckoutEventPayload(t *testing.T, fixture stripeCheckoutEventFixture) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"id": fixture.EventID, "object": "event", "type": fixture.EventType,
		"data": map[string]any{"object": map[string]any{
			"id": fixture.SessionID, "object": "checkout.session",
			"client_reference_id": fixture.OrderID, "metadata": map[string]string{"order_id": fixture.OrderID},
			"mode": "payment", "status": fixture.SessionStatus, "payment_status": fixture.PaymentStatus,
			"amount_total": int64(1299), "currency": "usd",
		}},
	})
	if err != nil {
		t.Fatalf("marshal Stripe webhook fixture: %v", err)
	}
	return payload
}

func TestWaffoPancakeCheckoutUsesAuthenticatedDynamicPrice(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)

	var mu sync.Mutex
	requests := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read Waffo request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := verifyWaffoRequestSignature(r, body, &keys.private.PublicKey); err != nil {
			t.Errorf("verify Waffo request signature: %v", err)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Idempotency-Key") == "" {
			t.Error("Waffo request is missing X-Idempotency-Key")
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode Waffo request: %v", err)
			http.Error(w, "bad JSON", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests[r.URL.Path] = decoded
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/actions/auth/issue-session-token":
			_, _ = io.WriteString(w, `{"data":{"token":"jwt_test_token","expiresAt":"2026-07-27T12:05:00Z"}}`)
		case "/v1/actions/checkout/create-session":
			_, _ = io.WriteString(w, `{"data":{"sessionId":"SES_0123456789abcdefghijkl","checkoutUrl":"https://pancake.example.test/checkout/SES_0123456789abcdefghijkl","expiresAt":"2026-07-27T12:45:00Z"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	action, err := (WaffoGateway{Config: cfg, BaseURL: server.URL, HTTPClient: server.Client()}).CreateCheckout(
		context.Background(),
		CheckoutRequest{
			OrderID: "po_waffo_test", Name: "Annual Pro", AmountMinor: 12345, Currency: "USD",
			TaxCategory: TaxCategorySaaS, UserID: "user_123", UserEmail: "buyer@example.test",
			SuccessURL: "https://aivory.example.test/subscription?payment=return",
		},
	)
	if err != nil {
		t.Fatalf("create Waffo Pancake checkout: %v", err)
	}
	if action.Type != ActionRedirect || action.ProviderOrderID != "" ||
		action.URL != "https://pancake.example.test/checkout/SES_0123456789abcdefghijkl#token=jwt_test_token" {
		t.Fatalf("unexpected Waffo checkout action: %#v", action)
	}

	mu.Lock()
	authBody := requests["/v1/actions/auth/issue-session-token"]
	checkoutBody := requests["/v1/actions/checkout/create-session"]
	mu.Unlock()
	if authBody["productId"] != cfg.ProductID || authBody["buyerIdentity"] != WaffoBuyerIdentity("user_123") {
		t.Fatalf("unexpected Waffo auth body: %#v", authBody)
	}
	if _, ok := checkoutBody["buyerIdentity"]; ok {
		t.Fatalf("buyerIdentity leaked into checkout body: %#v", checkoutBody)
	}
	if checkoutBody["productId"] != cfg.ProductID || checkoutBody["currency"] != "USD" ||
		checkoutBody["buyerEmail"] != "buyer@example.test" || checkoutBody["orderMerchantExternalId"] != "po_waffo_test" {
		t.Fatalf("unexpected Waffo checkout body: %#v", checkoutBody)
	}
	price, _ := checkoutBody["priceSnapshot"].(map[string]any)
	if price["amount"] != "123.45" || price["taxCategory"] != TaxCategorySaaS {
		t.Fatalf("unexpected Waffo price snapshot: %#v", price)
	}
	metadata, _ := checkoutBody["metadata"].(map[string]any)
	if metadata["aivory_order_id"] != "po_waffo_test" {
		t.Fatalf("unexpected Waffo metadata: %#v", metadata)
	}
}

func TestVerifyWaffoPancakeCompletedOrder(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)
	payload := waffoWebhookPayload(t, cfg, map[string]any{})
	signature := signWaffoWebhook(t, payload, keys.private, time.Now().UnixMilli())

	event, err := VerifyWaffoEvent(payload, signature, cfg)
	if err != nil {
		t.Fatalf("verify Waffo Pancake webhook: %v", err)
	}
	if event.ID != "delivery_waffo_test" || event.Type != "order.completed" || event.Status != EventPaid ||
		event.OrderID != "po_waffo_test" || event.ProviderOrderID != "ORD_0123456789abcdefghijkl" ||
		event.AmountMinor != 12345 || event.PaidAmountMinor != 12345 || event.TaxAmountMinor != 0 ||
		event.Currency != "USD" || event.MethodType != "card" ||
		event.UserID != WaffoBuyerIdentity("user_123") {
		t.Fatalf("unexpected Waffo event: %#v", event)
	}

	tampered := []byte(strings.Replace(string(payload), "123.45", "0.01", 1))
	if _, err := VerifyWaffoEvent(tampered, signature, cfg); err == nil {
		t.Fatal("tampered Waffo Pancake webhook unexpectedly verified")
	}

	wrongStore := waffoWebhookPayload(t, cfg, map[string]any{"storeId": "STO_zyxwvutsrqponmlkjihgfe"})
	if _, err := VerifyWaffoEvent(wrongStore, signWaffoWebhook(t, wrongStore, keys.private, time.Now().UnixMilli()), cfg); err == nil {
		t.Fatal("Waffo webhook for another store unexpectedly verified")
	}
	wrongMode := waffoWebhookPayload(t, cfg, map[string]any{"mode": "prod"})
	if _, err := VerifyWaffoEvent(wrongMode, signWaffoWebhook(t, wrongMode, keys.private, time.Now().UnixMilli()), cfg); err == nil {
		t.Fatal("Waffo webhook for another environment unexpectedly verified")
	}
}

func TestVerifyWaffoPancakeCompletedOrderTaxAmounts(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)
	tests := []struct {
		name     string
		currency string
		gross    string
		tax      string
		subtotal string
		total    string
		wantNet  int64
		wantPaid int64
		wantTax  int64
	}{
		{name: "USD", currency: "USD", gross: "135.80", tax: "12.35", subtotal: "123.45", total: "135.80", wantNet: 12345, wantPaid: 13580, wantTax: 1235},
		{name: "JPY", currency: "JPY", gross: "1100", tax: "100", subtotal: "1000", total: "1100", wantNet: 1000, wantPaid: 1100, wantTax: 100},
		{name: "KWD", currency: "KWD", gross: "1.296", tax: "0.062", subtotal: "1.234", total: "1.296", wantNet: 1234, wantPaid: 1296, wantTax: 62},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := waffoWebhookPayload(t, cfg, map[string]any{"data": map[string]any{
				"currency": tc.currency, "amount": tc.gross, "taxAmount": tc.tax,
				"subtotal": tc.subtotal, "total": tc.total,
			}})
			signature := signWaffoWebhook(t, payload, keys.private, time.Now().UnixMilli())
			event, err := VerifyWaffoEvent(payload, signature, cfg)
			if err != nil {
				t.Fatalf("verify taxed Waffo Pancake webhook: %v", err)
			}
			if event.AmountMinor != tc.wantNet || event.PaidAmountMinor != tc.wantPaid ||
				event.TaxAmountMinor != tc.wantTax || event.Currency != tc.currency {
				t.Fatalf("taxed Waffo event = %#v, want net/paid/tax %d/%d/%d %s",
					event, tc.wantNet, tc.wantPaid, tc.wantTax, tc.currency)
			}
		})
	}
}

func TestVerifyWaffoPancakeRejectsInvalidCompletedAmounts(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)
	tests := []struct {
		name string
		data map[string]any
	}{
		{name: "missing amount", data: map[string]any{"amount": nil}},
		{name: "missing tax amount", data: map[string]any{"taxAmount": nil}},
		{name: "negative amount", data: map[string]any{"amount": "-1.00"}},
		{name: "negative tax amount", data: map[string]any{"taxAmount": "-0.01"}},
		{name: "tax exceeds amount", data: map[string]any{"amount": "1.00", "taxAmount": "1.01"}},
		{name: "amount exceeds currency precision", data: map[string]any{"amount": "123.450"}},
		{name: "tax exceeds currency precision", data: map[string]any{"taxAmount": "0.001"}},
		{name: "amount is numeric", data: map[string]any{"amount": 123.45}},
		{name: "amount is out of range", data: map[string]any{"amount": "999999999999999999.99"}},
		{name: "subtotal mismatch", data: map[string]any{
			"amount": "135.80", "taxAmount": "12.35", "subtotal": "123.44", "total": "135.80",
		}},
		{name: "total mismatch", data: map[string]any{
			"amount": "135.80", "taxAmount": "12.35", "subtotal": "123.45", "total": "135.79",
		}},
		{name: "invalid subtotal", data: map[string]any{"subtotal": "123.450"}},
		{name: "invalid total", data: map[string]any{"total": "123.450"}},
		{name: "missing order status", data: map[string]any{"orderStatus": nil}},
		{name: "non-completed order status", data: map[string]any{"orderStatus": "active"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := waffoWebhookPayload(t, cfg, map[string]any{"data": tc.data})
			signature := signWaffoWebhook(t, payload, keys.private, time.Now().UnixMilli())
			if _, err := VerifyWaffoEvent(payload, signature, cfg); err == nil {
				t.Fatal("invalid Waffo Pancake webhook unexpectedly verified")
			}
		})
	}
}

type waffoTestKeys struct {
	private    *rsa.PrivateKey
	privatePEM string
	publicPEM  string
}

func newWaffoTestKeys(t *testing.T) waffoTestKeys {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return waffoTestKeys{
		private:    privateKey,
		privatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
		publicPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
	}
}

func testWaffoConfig(keys waffoTestKeys) WaffoConfig {
	return WaffoConfig{
		MerchantID: "MER_0123456789abcdefghijkl",
		PrivateKey: keys.privatePEM,
		StoreID:    "STO_0123456789abcdefghijkl", ProductID: "PROD_0123456789abcdefghijkl",
		Mode: "test", WebhookPublicKey: keys.publicPEM,
	}
}

func verifyWaffoRequestSignature(r *http.Request, body []byte, publicKey *rsa.PublicKey) error {
	timestamp := r.Header.Get("X-Timestamp")
	if r.Header.Get("X-Merchant-Id") != "MER_0123456789abcdefghijkl" || timestamp == "" {
		return errors.New("missing Waffo authentication headers")
	}
	bodyHash := sha256.Sum256(body)
	canonical := r.Method + "\n" + r.URL.Path + "\n" + timestamp + "\n" + base64.StdEncoding.EncodeToString(bodyHash[:])
	digest := sha256.Sum256([]byte(canonical))
	signature, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Signature"))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	return rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature)
}

func signWaffoWebhook(t *testing.T, payload []byte, privateKey *rsa.PrivateKey, timestamp int64) string {
	t.Helper()
	input := fmt.Sprintf("%d.%s", timestamp, payload)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign Waffo webhook: %v", err)
	}
	return fmt.Sprintf("t=%d,v1=%s", timestamp, base64.StdEncoding.EncodeToString(signature))
}

func waffoWebhookPayload(t *testing.T, cfg WaffoConfig, overrides map[string]any) []byte {
	t.Helper()
	completed := "completed"
	succeeded := "succeeded"
	identity := WaffoBuyerIdentity("user_123")
	externalID := "po_waffo_test"
	data := map[string]any{
		"orderId": "ORD_0123456789abcdefghijkl", "orderStatus": completed,
		"buyerEmail": "buyer@example.test", "merchantProvidedBuyerIdentity": identity,
		"orderMerchantExternalId": externalID, "orderMetadata": map[string]string{"aivory_order_id": externalID},
		"currency": "USD", "amount": "123.45", "taxAmount": "0.00",
		"productName": "Annual Pro", "paymentId": "PAY_0123456789abcdefghijkl",
		"paymentStatus": succeeded, "paymentMethod": "card", "paymentLast4": "0110",
	}
	payload := map[string]any{
		"id": "delivery_waffo_test", "timestamp": "2026-07-27T12:00:00Z",
		"eventType": "order.completed", "eventId": "PAY_0123456789abcdefghijkl",
		"storeId": cfg.StoreID, "storeName": "Aivory", "mode": cfg.Mode,
		"data": data,
	}
	for key, value := range overrides {
		if key == "data" {
			dataOverrides, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("Waffo data override has type %T, want map[string]any", value)
			}
			for dataKey, dataValue := range dataOverrides {
				if dataValue == nil {
					delete(data, dataKey)
					continue
				}
				data[dataKey] = dataValue
			}
			continue
		}
		if value == nil {
			delete(payload, key)
			continue
		}
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal Waffo webhook: %v", err)
	}
	return encoded
}
