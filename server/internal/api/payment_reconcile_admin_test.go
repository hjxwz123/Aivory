package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	paymentcore "aivory/server/internal/payment"
	"aivory/server/internal/store"
)

func TestEPayAdminCloseAllowsEnabledChannelAndOptionalReason(t *testing.T) {
	fx, channel, order := createEPayReconciliationFixture(t)
	path := "/api/admin/payment-orders/" + order.ID + "/reconcile"

	reconcile := fx.request(t, http.MethodPost, path, map[string]any{"action": "reconcile"})
	if reconcile.Code != http.StatusConflict {
		t.Fatalf("EPay automatic reconciliation status = %d, want 409; body=%s", reconcile.Code, reconcile.Body.String())
	}
	unconfirmed := fx.request(t, http.MethodPost, path, map[string]any{
		"action": "close",
	})
	if unconfirmed.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed EPay close status = %d, want 400; body=%s", unconfirmed.Code, unconfirmed.Body.String())
	}
	closedRec := fx.request(t, http.MethodPost, path, map[string]any{
		"action": "close", "confirm": true,
	})
	closed := decodePaymentAdminResponse[adminPaymentOrderResponse](t, closedRec, http.StatusOK)
	if closed.Status != store.PaymentOrderCancelled || closed.FailureReason != nil || closed.LastReconciledAt == nil ||
		closed.AmountMinor != 1234 || closed.Currency != "USD" ||
		closed.ProviderAmountMinor != 8638 || closed.ProviderCurrency != "CNY" || closed.ConversionRate != "7" {
		t.Fatalf("closed EPay order response = %+v", closed)
	}

	stored, err := store.GetPaymentOrder(context.Background(), fx.db, order.ID)
	if err != nil {
		t.Fatalf("get manually closed EPay order: %v", err)
	}
	if stored.Status != store.PaymentOrderCancelled || stored.FailureCode != "admin_manual_close" ||
		stored.FailureMessage != "" || stored.LastReconciledAt == 0 ||
		stored.AmountMinor != 1234 || stored.Currency != "USD" ||
		stored.ProviderAmountMinor != 8638 || stored.ProviderCurrency != "CNY" || stored.ConversionRate != "7" {
		t.Fatalf("stored manually closed EPay order = %+v", stored)
	}
	storedChannel, err := store.GetPaymentChannel(context.Background(), fx.db, channel.ID)
	if err != nil || !storedChannel.Enabled {
		t.Fatalf("manual order close changed enabled EPay channel: %+v, %v", storedChannel, err)
	}
	events, err := store.ListPaymentEventsForOrder(context.Background(), fx.db, order.ID)
	if err != nil {
		t.Fatalf("list EPay close audit events: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "admin.manual_close" || events[0].ProcessedAt == 0 {
		t.Fatalf("EPay close audit events = %+v", events)
	}

	repeated := fx.request(t, http.MethodPost, path, map[string]any{
		"action": "close", "confirm": true,
	})
	if repeated.Code != http.StatusConflict {
		t.Fatalf("repeated EPay close status = %d, want 409; body=%s", repeated.Code, repeated.Body.String())
	}
	events, err = store.ListPaymentEventsForOrder(context.Background(), fx.db, order.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("repeated EPay close changed audit events: %+v, %v", events, err)
	}
}

func TestPaymentOrderCloseReasonAcceptsShortUnicodeAndRejectsOverLimit(t *testing.T) {
	t.Run("single character", func(t *testing.T) {
		fx, _, order := createEPayReconciliationFixture(t)
		rec := fx.request(t, http.MethodPost, "/api/admin/payment-orders/"+order.ID+"/reconcile", map[string]any{
			"action": "close", "confirm": true, "reason": "已",
		})
		closed := decodePaymentAdminResponse[adminPaymentOrderResponse](t, rec, http.StatusOK)
		if closed.FailureReason == nil || *closed.FailureReason != "已" {
			t.Fatalf("single-character close reason = %+v", closed.FailureReason)
		}
	})

	t.Run("five hundred unicode characters", func(t *testing.T) {
		fx, _, order := createEPayReconciliationFixture(t)
		reason := strings.Repeat("由", 500)
		rec := fx.request(t, http.MethodPost, "/api/admin/payment-orders/"+order.ID+"/reconcile", map[string]any{
			"action": "close", "confirm": true, "reason": reason,
		})
		closed := decodePaymentAdminResponse[adminPaymentOrderResponse](t, rec, http.StatusOK)
		if closed.FailureReason == nil || *closed.FailureReason != reason {
			t.Fatalf("500-character close reason length = %d", len(reason))
		}
	})

	t.Run("more than five hundred unicode characters", func(t *testing.T) {
		fx, _, order := createEPayReconciliationFixture(t)
		rec := fx.request(t, http.MethodPost, "/api/admin/payment-orders/"+order.ID+"/reconcile", map[string]any{
			"action": "close", "confirm": true, "reason": strings.Repeat("由", 501),
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("overlong close reason status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		stored, err := store.GetPaymentOrder(context.Background(), fx.db, order.ID)
		if err != nil || stored.Status != store.PaymentOrderPending {
			t.Fatalf("overlong reason changed payment order: %+v, %v", stored, err)
		}
	})
}

func TestEPayPendingOrderCanBeDisabledAfterSettlementCurrencyChanges(t *testing.T) {
	fx, channel, order := createEPayReconciliationFixture(t)
	if err := store.SetSetting(fx.db, "settlement_currency", "EUR"); err != nil {
		t.Fatalf("change settlement currency: %v", err)
	}

	changedConfig := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+channel.ID, map[string]any{
		"enabled": false,
		"config": map[string]any{
			"gateway_url": "https://changed.example.test", "merchant_id": "reconcile-merchant",
			"merchant_key": paymentSecretMask, "currency": "CNY",
			"conversion_rate": "7", "conversion_rate_base_currency": "USD",
		},
	})
	if changedConfig.Code != http.StatusBadRequest {
		t.Fatalf("disable with a changed invalid config status = %d, want 400; body=%s", changedConfig.Code, changedConfig.Body.String())
	}

	disable := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+channel.ID, map[string]any{"enabled": false})
	disabled := decodePaymentAdminResponse[adminPaymentChannelResponse](t, disable, http.StatusOK)
	if disabled.Enabled {
		t.Fatal("EPay channel remained enabled after settlement currency changed")
	}
	reenable := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+channel.ID, map[string]any{"enabled": true})
	if reenable.Code != http.StatusBadRequest {
		t.Fatalf("re-enable mismatched EPay channel status = %d, want 400; body=%s", reenable.Code, reenable.Body.String())
	}

	closedRec := fx.request(t, http.MethodPost, "/api/admin/payment-orders/"+order.ID+"/reconcile", map[string]any{
		"action": "close", "confirm": true, "reason": "Gateway order was verified and closed",
	})
	closed := decodePaymentAdminResponse[adminPaymentOrderResponse](t, closedRec, http.StatusOK)
	if closed.Status != store.PaymentOrderCancelled {
		t.Fatalf("closed EPay order status = %q, want %q", closed.Status, store.PaymentOrderCancelled)
	}
}

func TestPaymentOrdersAdminReturnsReconciliationMetadata(t *testing.T) {
	fx, _, order := createEPayReconciliationFixture(t)
	const (
		providerOrderID = "epay-provider-order"
		sessionID       = "checkout-session-id"
	)
	expiresAt := time.Now().Add(time.Hour).Unix()
	if _, err := store.MarkPaymentOrderCheckoutStarted(
		context.Background(), fx.db, order.ID, providerOrderID, sessionID, expiresAt,
	); err != nil {
		t.Fatalf("persist checkout metadata: %v", err)
	}
	if _, err := store.MarkPaymentOrderReconciled(context.Background(), fx.db, order.ID, "temporary provider timeout"); err != nil {
		t.Fatalf("persist reconciliation metadata: %v", err)
	}

	rec := fx.request(t, http.MethodGet, "/api/admin/payment-orders", nil)
	response := decodePaymentAdminResponse[struct {
		Orders []adminPaymentOrderResponse `json:"orders"`
		Total  int                         `json:"total"`
	}](t, rec, http.StatusOK)
	if response.Total != 1 || len(response.Orders) != 1 {
		t.Fatalf("payment order list = %+v", response)
	}
	got := response.Orders[0]
	if got.ProviderOrderID != providerOrderID || got.CheckoutSessionID != sessionID ||
		got.CheckoutExpiresAt == nil || *got.CheckoutExpiresAt != expiresAt || got.LastReconciledAt == nil ||
		got.ReconcileError == nil || *got.ReconcileError != "temporary provider timeout" ||
		got.Environment != store.PaymentEnvironmentLive ||
		got.AmountMinor != 1234 || got.Currency != "USD" ||
		got.ProviderAmountMinor != 8638 || got.ProviderCurrency != "CNY" || got.ConversionRate != "7" {
		t.Fatalf("payment reconciliation metadata = %+v", got)
	}
}

func createEPayReconciliationFixture(t *testing.T) (paymentAdminFixture, adminPaymentChannelResponse, *store.PaymentOrder) {
	t.Helper()
	fx := newPaymentAdminFixture(t)
	if err := store.SetSetting(fx.db, "settlement_currency", "USD"); err != nil {
		t.Fatalf("set settlement currency: %v", err)
	}
	mustExec(t, fx.db,
		`INSERT INTO users(id,email,password_hash,role,group_id) VALUES(?,?,?,?,?)`,
		"epay-reconcile-admin", "epay-reconcile@example.test", "hash", "admin", store.DefaultGroupID,
	)
	pkg, err := store.CreateCreditPackage(context.Background(), fx.db, store.CreditPackage{
		Name: "Reconciliation package", Credits: 500, PriceAmountMinor: 1234, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create reconciliation credit package: %v", err)
	}
	channel := createPaymentChannelForAdminTest(t, fx, "Reconciliation EPay", paymentcore.ProviderEPay,
		paymentcore.EPayConfig{
			GatewayURL: "https://epay.example.test", MerchantID: "reconcile-merchant",
			MerchantKey: "reconcile-secret", Currency: "CNY",
			ConversionRate: "7", ConversionRateBaseCurrency: "USD",
		}, 0)
	method := createPaymentMethodForAdminTest(t, fx, "EPay card", "credit-card", channel.ID,
		paymentcore.EPayMethodConfig{Type: "card"}, 0)
	order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: "epay-reconcile-admin", PaymentMethodID: method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: pkg.ID,
	})
	if err != nil {
		t.Fatalf("create EPay reconciliation order: %v", err)
	}
	return fx, channel, order
}
