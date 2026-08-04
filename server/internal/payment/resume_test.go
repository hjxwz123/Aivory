package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v86"
)

func TestStripeResumeCheckoutReturnsExactActiveSession(t *testing.T) {
	const (
		orderID   = "po_stripe_resume"
		sessionID = "cs_test_resume"
	)
	expiresAt := time.Now().Add(time.Hour).Unix()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/checkout/sessions/"+sessionID {
			t.Fatalf("Stripe resume created or retrieved the wrong session: %s %s", r.Method, r.URL.Path)
		}
		writeJSONResponse(t, w, map[string]any{
			"id": sessionID, "object": "checkout.session", "client_reference_id": orderID,
			"metadata": map[string]string{"order_id": orderID}, "mode": "payment",
			"status": "open", "payment_status": "unpaid", "expires_at": expiresAt,
			"url": "https://checkout.stripe.com/c/pay/" + sessionID,
		})
	}))
	defer server.Close()

	action, err := stripeResumeGateway(server).ResumeCheckout(context.Background(), CheckoutResumeRequest{
		CheckoutRequest: CheckoutRequest{OrderID: orderID},
		ProviderOrderID: sessionID,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatalf("resume Stripe Checkout session: %v", err)
	}
	if requests.Load() != 1 || action.Type != ActionRedirect ||
		action.URL != "https://checkout.stripe.com/c/pay/"+sessionID ||
		action.ResumeMode != CheckoutResumeOriginalSession ||
		action.ProviderOrderID != sessionID || action.SessionID != sessionID ||
		action.SessionURL != "" || action.ExpiresAt != expiresAt {
		t.Fatalf("Stripe resume action = %+v, requests = %d", action, requests.Load())
	}
}

func TestStripeResumeCheckoutRejectsInactiveOrUnrelatedSessions(t *testing.T) {
	const (
		orderID   = "po_stripe_resume_state"
		sessionID = "cs_test_resume_state"
	)
	tests := []struct {
		name          string
		reference     string
		mode          string
		status        string
		paymentStatus string
		expiresAt     int64
		want          error
	}{
		{name: "another order", reference: "po_other", mode: "payment", status: "open", paymentStatus: "unpaid", expiresAt: time.Now().Add(time.Hour).Unix(), want: ErrCheckoutNotResumable},
		{name: "subscription mode", reference: orderID, mode: "subscription", status: "open", paymentStatus: "unpaid", expiresAt: time.Now().Add(time.Hour).Unix(), want: ErrCheckoutNotResumable},
		{name: "completed", reference: orderID, mode: "payment", status: "complete", paymentStatus: "unpaid", expiresAt: time.Now().Add(time.Hour).Unix(), want: ErrCheckoutNotResumable},
		{name: "paid", reference: orderID, mode: "payment", status: "open", paymentStatus: "paid", expiresAt: time.Now().Add(time.Hour).Unix(), want: ErrCheckoutNotResumable},
		{name: "expired status", reference: orderID, mode: "payment", status: "expired", paymentStatus: "unpaid", expiresAt: time.Now().Add(time.Hour).Unix(), want: ErrCheckoutExpired},
		{name: "expired timestamp", reference: orderID, mode: "payment", status: "open", paymentStatus: "unpaid", expiresAt: time.Now().Add(-time.Minute).Unix(), want: ErrCheckoutExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(t, w, map[string]any{
					"id": sessionID, "object": "checkout.session", "client_reference_id": tc.reference,
					"metadata": map[string]string{"order_id": tc.reference}, "mode": tc.mode,
					"status": tc.status, "payment_status": tc.paymentStatus, "expires_at": tc.expiresAt,
					"url": "https://checkout.stripe.com/c/pay/" + sessionID,
				})
			}))
			defer server.Close()
			_, err := stripeResumeGateway(server).ResumeCheckout(context.Background(), CheckoutResumeRequest{
				CheckoutRequest: CheckoutRequest{OrderID: orderID}, SessionID: sessionID,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Stripe resume error = %v, want %v", err, tc.want)
			}
		})
	}
}

func stripeResumeGateway(server *httptest.Server) StripeGateway {
	return StripeGateway{
		Config: StripeConfig{SecretKey: "sk_test_resume", WebhookSecret: "whsec_resume"},
		Backends: stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			URL: stripe.String(server.URL), HTTPClient: server.Client(),
		}),
	}
}

func TestWaffoResumeCheckoutReusesSessionAndRefreshesOnlyToken(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)
	const (
		orderID   = "po_waffo_resume"
		sessionID = "SES_0123456789abcdefghijkl"
	)
	sessionURL := "https://pancake.example.test/store/checkout/" + sessionID
	expiresAt := time.Now().Add(30 * time.Minute).Unix()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/actions/auth/issue-session-token" {
			t.Fatalf("Waffo resume created a new checkout: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode Waffo token request: %v", err)
		}
		if body["productId"] != cfg.ProductID || body["buyerIdentity"] != WaffoBuyerIdentity("user_123") {
			t.Fatalf("Waffo token request = %#v", body)
		}
		writeJSONResponse(t, w, map[string]any{
			"data": map[string]any{"token": "fresh.jwt.token", "expiresAt": time.Now().Add(5 * time.Minute).Format(time.RFC3339)},
		})
	}))
	defer server.Close()
	action, err := (WaffoGateway{Config: cfg, BaseURL: server.URL, HTTPClient: server.Client()}).ResumeCheckout(
		context.Background(),
		CheckoutResumeRequest{
			CheckoutRequest:  CheckoutRequest{OrderID: orderID, UserID: "user_123"},
			ProviderOrderID:  "ORD_existing",
			SessionID:        sessionID,
			SessionURL:       sessionURL,
			SessionExpiresAt: expiresAt,
		},
	)
	if err != nil {
		t.Fatalf("resume Waffo Checkout session: %v", err)
	}
	if requests.Load() != 1 || action.Type != ActionRedirect ||
		action.URL != sessionURL+"#token=fresh.jwt.token" || action.SessionURL != sessionURL ||
		action.ResumeMode != CheckoutResumeOriginalSession || action.SessionID != sessionID ||
		action.ProviderOrderID != "ORD_existing" || action.ExpiresAt != expiresAt {
		t.Fatalf("Waffo resume action = %+v, requests = %d", action, requests.Load())
	}
}

func TestWaffoResumeCheckoutRejectsExpiredOrCredentialBearingSnapshot(t *testing.T) {
	keys := newWaffoTestKeys(t)
	gateway := WaffoGateway{Config: testWaffoConfig(keys)}
	const sessionID = "SES_0123456789abcdefghijkl"
	base := CheckoutResumeRequest{
		CheckoutRequest:  CheckoutRequest{OrderID: "po_waffo_invalid_resume", UserID: "user_123"},
		SessionID:        sessionID,
		SessionURL:       "https://pancake.example.test/checkout/" + sessionID,
		SessionExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	credentialBearing := base
	credentialBearing.SessionURL += "#token=stale"
	if _, err := gateway.ResumeCheckout(context.Background(), credentialBearing); !errors.Is(err, ErrCheckoutNotResumable) {
		t.Fatalf("credential-bearing Waffo resume error = %v, want ErrCheckoutNotResumable", err)
	}
	expired := base
	expired.SessionExpiresAt = time.Now().Add(-time.Minute).Unix()
	if _, err := gateway.ResumeCheckout(context.Background(), expired); !errors.Is(err, ErrCheckoutExpired) {
		t.Fatalf("expired Waffo resume error = %v, want ErrCheckoutExpired", err)
	}
}

func TestWaffoResumeCheckoutRequiresFreshToken(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)
	const sessionID = "SES_0123456789abcdefghijkl"
	tests := []struct {
		name       string
		statusCode int
		response   map[string]any
	}{
		{name: "token endpoint error", statusCode: http.StatusBadGateway, response: map[string]any{"error": "temporarily unavailable"}},
		{name: "empty token", statusCode: http.StatusOK, response: map[string]any{
			"data": map[string]any{"token": "", "expiresAt": time.Now().Add(5 * time.Minute).Format(time.RFC3339)},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/actions/auth/issue-session-token" {
					t.Fatalf("Waffo resume called an unexpected endpoint: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.statusCode)
				if err := json.NewEncoder(w).Encode(tc.response); err != nil {
					t.Fatalf("encode Waffo token response: %v", err)
				}
			}))
			defer server.Close()
			_, err := (WaffoGateway{Config: cfg, BaseURL: server.URL, HTTPClient: server.Client()}).ResumeCheckout(
				context.Background(),
				CheckoutResumeRequest{
					CheckoutRequest:  CheckoutRequest{OrderID: "po_waffo_upstream_state", UserID: "user_123"},
					SessionID:        sessionID,
					SessionURL:       "https://pancake.example.test/checkout/" + sessionID,
					SessionExpiresAt: time.Now().Add(time.Hour).Unix(),
				},
			)
			if err == nil {
				t.Fatal("Waffo resume succeeded without a fresh token")
			}
			if requests.Load() != 1 {
				t.Fatalf("Waffo resume made %d requests, want only the token request", requests.Load())
			}
		})
	}
}

func TestEPayResumeCheckoutReusesOutstandingMerchantOrder(t *testing.T) {
	gateway := EPayGateway{
		Config: EPayConfig{
			GatewayURL: "https://pay.example.test", MerchantID: "1000", MerchantKey: "secret", Currency: "CNY",
		},
		Method: EPayMethodConfig{Type: "wxpay"},
	}
	request := CheckoutRequest{
		OrderID: "po_epay_resume", Name: "2,000 credits", AmountMinor: 28000, Currency: "CNY",
		NotifyURL:  "https://app.example.test/api/payments/webhooks/channel",
		SuccessURL: "https://app.example.test/subscription?payment=return&order=po_epay_resume",
	}
	original, err := gateway.CreateCheckout(context.Background(), request)
	if err != nil {
		t.Fatalf("create original EPay form: %v", err)
	}
	retryRequest := request
	retryRequest.MerchantOrderID = original.Fields["out_trade_no"]
	resumed, err := gateway.ResumeCheckout(context.Background(), CheckoutResumeRequest{CheckoutRequest: retryRequest})
	if err != nil {
		t.Fatalf("regenerate EPay form: %v", err)
	}
	if resumed.ResumeMode != CheckoutResumeRetrySubmission || resumed.Type != ActionFormPost ||
		resumed.SessionID != "" || resumed.ProviderOrderID != "" ||
		resumed.Fields["out_trade_no"] != retryRequest.MerchantOrderID ||
		resumed.Fields["money"] != original.Fields["money"] || resumed.Fields["name"] != original.Fields["name"] ||
		resumed.URL != original.URL || resumed.Fields["sign"] != original.Fields["sign"] {
		t.Fatalf("EPay retry action = %+v, original = %+v", resumed, original)
	}
	if got, want := resumed.Fields["sign"], EPaySign(resumed.Fields, gateway.Config.MerchantKey); got != want {
		t.Fatalf("EPay retry signature = %q, want %q", got, want)
	}
}
