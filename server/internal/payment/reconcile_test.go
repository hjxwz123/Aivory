package payment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v86"
)

func TestStripeReconcilerRetrievesPaidSession(t *testing.T) {
	const (
		orderID   = "po_stripe_reconcile_paid"
		sessionID = "cs_test_reconcile_paid"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/checkout/sessions/"+sessionID {
			t.Fatalf("unexpected Stripe request: %s %s", r.Method, r.URL.Path)
		}
		writeStripeReconcileSession(t, w, orderID, sessionID, "complete", "paid")
	}))
	defer server.Close()

	event, err := (StripeReconciler{
		Config: StripeConfig{SecretKey: "sk_test_reconcile", WebhookSecret: "whsec_reconcile"},
		Backends: stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			URL: stripe.String(server.URL), HTTPClient: server.Client(),
		}),
	}).Reconcile(context.Background(), ReconcileRequest{OrderID: orderID, SessionID: sessionID})
	if err != nil {
		t.Fatalf("reconcile paid Stripe session: %v", err)
	}
	if event.Status != EventPaid || event.OrderID != orderID || event.ProviderOrderID != sessionID ||
		event.AmountMinor != 1234 || event.PaidAmountMinor != 1234 || event.Currency != "USD" {
		t.Fatalf("unexpected paid Stripe event: %+v", event)
	}
}

func TestStripeReconcilerOpenAndClose(t *testing.T) {
	const (
		orderID   = "po_stripe_reconcile_open"
		sessionID = "cs_test_reconcile_open"
	)
	var mu sync.Mutex
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/checkout/sessions/"+sessionID:
			writeStripeReconcileSession(t, w, orderID, sessionID, "open", "unpaid")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions/"+sessionID+"/expire":
			writeStripeReconcileSession(t, w, orderID, sessionID, "expired", "unpaid")
		default:
			t.Fatalf("unexpected Stripe request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	reconciler := StripeReconciler{
		Config: StripeConfig{SecretKey: "sk_test_reconcile", WebhookSecret: "whsec_reconcile"},
		Backends: stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			URL: stripe.String(server.URL), HTTPClient: server.Client(),
		}),
	}

	event, err := reconciler.Reconcile(context.Background(), ReconcileRequest{OrderID: orderID, SessionID: sessionID})
	if err != nil || event.Status != EventProcessing {
		t.Fatalf("reconcile open Stripe session = %+v, %v", event, err)
	}
	mu.Lock()
	if len(requests) != 1 {
		t.Fatalf("ordinary reconciliation made %d requests, want only retrieve: %v", len(requests), requests)
	}
	requests = requests[:0]
	mu.Unlock()

	event, err = reconciler.Reconcile(context.Background(), ReconcileRequest{OrderID: orderID, SessionID: sessionID, Close: true})
	if err != nil || event.Status != EventExpired {
		t.Fatalf("close open Stripe session = %+v, %v", event, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		http.MethodGet + " /v1/checkout/sessions/" + sessionID,
		http.MethodPost + " /v1/checkout/sessions/" + sessionID + "/expire",
	}
	if len(requests) != len(want) || requests[0] != want[0] || requests[1] != want[1] {
		t.Fatalf("Stripe close requests = %v, want %v", requests, want)
	}
}

func TestStripeReconcilerRefusesCompleteUnpaidClose(t *testing.T) {
	const (
		orderID   = "po_stripe_reconcile_unpaid"
		sessionID = "cs_test_reconcile_unpaid"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStripeReconcileSession(t, w, orderID, sessionID, "complete", "unpaid")
	}))
	defer server.Close()
	reconciler := StripeReconciler{
		Config: StripeConfig{SecretKey: "sk_test_reconcile", WebhookSecret: "whsec_reconcile"},
		Backends: stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			URL: stripe.String(server.URL), HTTPClient: server.Client(),
		}),
	}

	_, err := reconciler.Reconcile(context.Background(), ReconcileRequest{
		OrderID: orderID, SessionID: sessionID, Close: true,
	})
	if !errors.Is(err, ErrCheckoutNotClosable) {
		t.Fatalf("complete unpaid Stripe close error = %v, want ErrCheckoutNotClosable", err)
	}
}

func TestStripeReconcilerFindsSessionAfterCheckoutResponseTimeout(t *testing.T) {
	const (
		orderID   = "po_stripe_reconcile_lookup"
		sessionID = "cs_test_reconcile_lookup"
	)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/payment_intents/search":
			if !strings.Contains(r.URL.Query().Get("query"), "metadata['order_id']:'"+orderID+"'") {
				t.Fatalf("Stripe PaymentIntent search query = %q", r.URL.Query().Get("query"))
			}
			writeJSONResponse(t, w, map[string]any{
				"object": "search_result", "data": []any{map[string]any{
					"id": "pi_lookup", "metadata": map[string]string{"order_id": orderID},
				}}, "has_more": false,
			})
		case "/v1/checkout/sessions":
			if got := r.URL.Query().Get("payment_intent"); got != "pi_lookup" {
				t.Fatalf("Stripe Checkout Session list payment_intent = %q", got)
			}
			writeJSONResponse(t, w, map[string]any{
				"object": "list", "data": []any{map[string]any{
					"id": sessionID, "client_reference_id": orderID,
					"metadata": map[string]string{"order_id": orderID}, "mode": "payment",
					"status": "open", "payment_status": "unpaid", "amount_total": 1234, "currency": "usd",
				}}, "has_more": false,
			})
		default:
			t.Fatalf("unexpected Stripe lookup request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	event, err := (StripeReconciler{
		Config: StripeConfig{SecretKey: "sk_test_reconcile", WebhookSecret: "whsec_reconcile"},
		Backends: stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			URL: stripe.String(server.URL), HTTPClient: server.Client(),
		}),
	}).Reconcile(context.Background(), ReconcileRequest{OrderID: orderID})
	if err != nil || event.Status != EventProcessing || event.ProviderOrderID != sessionID {
		t.Fatalf("Stripe lookup reconciliation = %+v, %v", event, err)
	}
	want := []string{"GET /v1/payment_intents/search", "GET /v1/checkout/sessions"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("Stripe lookup requests = %v, want %v", paths, want)
	}
}

func writeStripeReconcileSession(t *testing.T, w http.ResponseWriter, orderID, sessionID, status, paymentStatus string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	writeJSONResponse(t, w, map[string]any{
		"id": sessionID, "object": "checkout.session", "client_reference_id": orderID,
		"metadata": map[string]string{"order_id": orderID}, "mode": "payment",
		"status": status, "payment_status": paymentStatus, "amount_total": int64(1234), "currency": "usd",
	})
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode JSON response: %v", err)
	}
}

func TestWaffoReconcilerPaidPayment(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)
	server := newWaffoReconcileServer(t, func(r *http.Request) any {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/graphql" {
			t.Fatalf("unexpected Waffo request: %s %s", r.Method, r.URL.Path)
		}
		return map[string]any{"data": map[string]any{
			"payments": []map[string]any{{
				"id": "PAY_AbCdEfGhIjKlMnOpQrStUv", "orderId": "ORD_AbCdEfGhIjKlMnOpQrStUv",
				"status": "succeeded", "orderMerchantExternalId": "po_waffo_reconcile_paid",
				"snapshotAmountDetails": map[string]any{
					"subtotal": "10.00", "taxAmount": "2.34", "total": "12.34", "currency": "USD",
				},
			}},
			"onetimeOrders": []any{},
		}}
	})
	defer server.Close()

	event, err := (WaffoReconciler{Config: cfg, BaseURL: server.URL, HTTPClient: server.Client()}).Reconcile(
		context.Background(), ReconcileRequest{
			OrderID: "po_waffo_reconcile_paid", UserID: "user_123", AmountMinor: 1000, Currency: "USD",
		},
	)
	if err != nil {
		t.Fatalf("reconcile paid Waffo order: %v", err)
	}
	if event.Status != EventPaid || event.AmountMinor != 1000 || event.TaxAmountMinor != 234 ||
		event.PaidAmountMinor != 1234 || event.ProviderOrderID != "ORD_AbCdEfGhIjKlMnOpQrStUv" ||
		event.UserID != WaffoBuyerIdentity("user_123") {
		t.Fatalf("unexpected paid Waffo event: %+v", event)
	}
}

func TestWaffoReconcilerPendingAndClose(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)
	const providerOrderID = "ORD_AbCdEfGhIjKlMnOpQrStUv"
	var mu sync.Mutex
	paths := make([]string, 0, 3)
	server := newWaffoReconcileServer(t, func(r *http.Request) any {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/graphql":
			if got := r.Header.Get("X-Environment"); got != "" {
				t.Fatalf("merchant GraphQL request unexpectedly has customer environment %q", got)
			}
			return map[string]any{"data": map[string]any{
				"payments": []any{},
				"onetimeOrders": []map[string]any{{
					"id": providerOrderID, "status": "pending", "orderMerchantExternalId": "po_waffo_reconcile_pending",
				}},
			}}
		case "/v1/actions/auth/issue-session-token":
			if got := r.Header.Get("X-Environment"); got != "" {
				t.Fatalf("session-token request unexpectedly has customer environment %q", got)
			}
			return map[string]any{"data": map[string]any{
				"token": "customer_test_token", "expiresAt": "2026-07-27T12:05:00Z",
			}}
		case "/v1/actions/onetime-order/cancel-order":
			if got := r.Header.Get("Authorization"); got != "Bearer customer_test_token" {
				t.Fatalf("Waffo cancel authorization = %q", got)
			}
			if got := r.Header.Get("X-Environment"); got != cfg.Mode {
				t.Fatalf("Waffo cancel environment = %q, want %q", got, cfg.Mode)
			}
			return map[string]any{"data": map[string]any{"orderId": providerOrderID, "status": "canceled"}}
		default:
			t.Fatalf("unexpected Waffo request: %s %s", r.Method, r.URL.Path)
			return nil
		}
	})
	defer server.Close()
	reconciler := WaffoReconciler{Config: cfg, BaseURL: server.URL, HTTPClient: server.Client()}
	req := ReconcileRequest{
		OrderID: "po_waffo_reconcile_pending", UserID: "user_123", AmountMinor: 1234, Currency: "USD",
	}

	event, err := reconciler.Reconcile(context.Background(), req)
	if err != nil || event.Status != EventProcessing {
		t.Fatalf("reconcile pending Waffo order = %+v, %v", event, err)
	}
	mu.Lock()
	if len(paths) != 1 || paths[0] != "/v1/graphql" {
		t.Fatalf("ordinary Waffo reconciliation paths = %v", paths)
	}
	paths = paths[:0]
	mu.Unlock()

	req.Close = true
	event, err = reconciler.Reconcile(context.Background(), req)
	if err != nil || event.Status != EventExpired || event.ProviderOrderID != providerOrderID {
		t.Fatalf("close pending Waffo order = %+v, %v", event, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/v1/graphql", "/v1/actions/auth/issue-session-token", "/v1/actions/onetime-order/cancel-order"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] || paths[2] != want[2] {
		t.Fatalf("Waffo close paths = %v, want %v", paths, want)
	}
}

func TestWaffoReconcilerMissingOrderUsesSavedSessionExpiry(t *testing.T) {
	keys := newWaffoTestKeys(t)
	cfg := testWaffoConfig(keys)
	server := newWaffoReconcileServer(t, func(r *http.Request) any {
		return map[string]any{"data": map[string]any{"payments": []any{}, "onetimeOrders": []any{}}}
	})
	defer server.Close()
	reconciler := WaffoReconciler{Config: cfg, BaseURL: server.URL, HTTPClient: server.Client()}
	req := ReconcileRequest{
		OrderID: "po_waffo_reconcile_missing", UserID: "user_123", AmountMinor: 1234, Currency: "USD",
		SessionExpiresAt: time.Now().Add(time.Hour).Unix(), Close: true,
	}

	if _, err := reconciler.Reconcile(context.Background(), req); !errors.Is(err, ErrCheckoutNotClosable) {
		t.Fatalf("unexpired missing Waffo checkout error = %v, want ErrCheckoutNotClosable", err)
	}
	req.SessionExpiresAt = time.Now().Add(-time.Minute).Unix()
	event, err := reconciler.Reconcile(context.Background(), req)
	if err != nil || event.Status != EventExpired {
		t.Fatalf("expired missing Waffo checkout = %+v, %v", event, err)
	}
}

func newWaffoReconcileServer(t *testing.T, response func(*http.Request) any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read Waffo request: %v", err)
		}
		if r.Header.Get("X-Merchant-Id") == "" && r.Header.Get("Authorization") == "" {
			t.Fatalf("Waffo request is not authenticated: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/v1/graphql" {
			var request struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatalf("decode Waffo GraphQL request: %v", err)
			}
			if !strings.Contains(request.Query, "orderMerchantExternalId") || request.Variables["ref"] == "" {
				t.Fatalf("Waffo GraphQL request is missing the local order reference: %+v", request)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response(r)); err != nil {
			t.Fatalf("encode Waffo response: %v", err)
		}
	}))
}
