package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/cache"
	paymentcore "aivory/server/internal/payment"
	"aivory/server/internal/store"
)

type paymentAdminFixture struct {
	db  *sql.DB
	d   Deps
	mux *mux
}

func newPaymentAdminFixture(t *testing.T) paymentAdminFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "payment-admin.db"))
	t.Cleanup(func() { _ = db.Close() })
	d := Deps{DB: db}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/payment-channels", wrap(d, listPaymentChannelsAdmin))
	mx.handle(http.MethodPost, "/api/admin/payment-channels", wrap(d, createPaymentChannelAdmin))
	mx.handle(http.MethodPatch, "/api/admin/payment-channels/:id", wrap(d, updatePaymentChannelAdmin))
	mx.handle(http.MethodDelete, "/api/admin/payment-channels/:id", wrap(d, deletePaymentChannelAdmin))
	mx.handle(http.MethodGet, "/api/admin/payment-methods", wrap(d, listPaymentMethodsAdmin))
	mx.handle(http.MethodPost, "/api/admin/payment-methods", wrap(d, createPaymentMethodAdmin))
	mx.handle(http.MethodPatch, "/api/admin/payment-methods/reorder", wrap(d, reorderPaymentMethodsAdmin))
	mx.handle(http.MethodPatch, "/api/admin/payment-methods/:id", wrap(d, updatePaymentMethodAdmin))
	mx.handle(http.MethodDelete, "/api/admin/payment-methods/:id", wrap(d, deletePaymentMethodAdmin))
	mx.handle(http.MethodGet, "/api/admin/payment-orders", wrap(d, listPaymentOrdersAdmin))
	mx.handle(http.MethodPost, "/api/admin/payment-orders/:id/reconcile", wrap(d, reconcilePaymentOrderAdmin))
	return paymentAdminFixture{db: db, d: d, mux: mx}
}

func (fx paymentAdminFixture) request(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s request: %v", method, path, err)
		}
		reader = strings.NewReader(string(encoded))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

func decodePaymentAdminResponse[T any](t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("response status = %d, want %d; body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	var result T
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	return result
}

func createPaymentChannelForAdminTest(t *testing.T, fx paymentAdminFixture, name, provider string, config any, sortOrder int) adminPaymentChannelResponse {
	t.Helper()
	rec := fx.request(t, http.MethodPost, "/api/admin/payment-channels", map[string]any{
		"name": name, "provider": provider, "config": config, "sort_order": sortOrder,
	})
	return decodePaymentAdminResponse[adminPaymentChannelResponse](t, rec, http.StatusCreated)
}

func createPaymentMethodForAdminTest(t *testing.T, fx paymentAdminFixture, name, icon, channelID string, config any, sortOrder int) adminPaymentMethodResponse {
	t.Helper()
	rec := fx.request(t, http.MethodPost, "/api/admin/payment-methods", map[string]any{
		"name": name, "icon": icon, "channel_id": channelID,
		"provider_method_config": config, "sort_order": sortOrder,
	})
	return decodePaymentAdminResponse[adminPaymentMethodResponse](t, rec, http.StatusCreated)
}

func paymentConfigStrings(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decode payment config %s: %v", raw, err)
	}
	result := make(map[string]string, len(config))
	for key, value := range config {
		if text, ok := value.(string); ok {
			result[key] = text
		}
	}
	return result
}

func assertMaskedPaymentKeys(t *testing.T, raw json.RawMessage, keys ...string) {
	t.Helper()
	config := paymentConfigStrings(t, raw)
	for _, key := range keys {
		if got := config[key]; got != paymentSecretMask {
			t.Errorf("config[%q] = %q, want mask", key, got)
		}
	}
}

func TestPaymentChannelsAdminCRUDMasksAndPreservesSecrets(t *testing.T) {
	fx := newPaymentAdminFixture(t)
	waffoPrivateKey := paymentAdminTestPrivateKey(t)

	stripeSecrets := paymentcore.StripeConfig{
		SecretKey: "sk_test_admin_original", WebhookSecret: "whsec_admin_original",
	}
	epaySecrets := paymentcore.EPayConfig{
		GatewayURL: "https://epay.example.test/gateway", MerchantID: "epay-merchant",
		MerchantKey: "epay-admin-secret", Currency: "usd",
	}
	waffoSecrets := paymentcore.WaffoConfig{
		MerchantID: "MER_0123456789abcdefghijkl", PrivateKey: waffoPrivateKey,
		StoreID: "STO_0123456789abcdefghijkl", ProductID: "PROD_0123456789abcdefghijkl",
		Mode: "test", WebhookPublicKey: "optional-public-key",
	}

	stripe := createPaymentChannelForAdminTest(t, fx, "Stripe primary", paymentcore.ProviderStripe, stripeSecrets, 20)
	epay := createPaymentChannelForAdminTest(t, fx, "EPay primary", paymentcore.ProviderEPay, epaySecrets, 10)
	waffo := createPaymentChannelForAdminTest(t, fx, "Waffo primary", paymentcore.ProviderWaffo, waffoSecrets, 30)
	assertMaskedPaymentKeys(t, stripe.Config, "secret_key", "webhook_secret")
	assertMaskedPaymentKeys(t, epay.Config, "merchant_key")
	assertMaskedPaymentKeys(t, waffo.Config, "private_key")
	for _, channel := range []adminPaymentChannelResponse{stripe, epay, waffo} {
		wantSuffix := "/api/payments/webhooks/" + url.PathEscape(channel.ID)
		if !strings.HasSuffix(channel.WebhookURL, wantSuffix) {
			t.Errorf("channel %s webhook URL = %q, want suffix %q", channel.Provider, channel.WebhookURL, wantSuffix)
		}
		if strings.Contains(channel.WebhookURL, "/"+channel.Provider+"/") {
			t.Errorf("channel %s webhook URL unexpectedly contains provider: %q", channel.Provider, channel.WebhookURL)
		}
	}

	listRec := fx.request(t, http.MethodGet, "/api/admin/payment-channels", nil)
	listed := decodePaymentAdminResponse[[]adminPaymentChannelResponse](t, listRec, http.StatusOK)
	if len(listed) != 3 {
		t.Fatalf("listed channels = %d, want 3: %+v", len(listed), listed)
	}
	listedByProvider := make(map[string]adminPaymentChannelResponse, len(listed))
	for _, channel := range listed {
		listedByProvider[channel.Provider] = channel
	}
	assertMaskedPaymentKeys(t, listedByProvider[paymentcore.ProviderStripe].Config, "secret_key", "webhook_secret")
	assertMaskedPaymentKeys(t, listedByProvider[paymentcore.ProviderEPay].Config, "merchant_key")
	assertMaskedPaymentKeys(t, listedByProvider[paymentcore.ProviderWaffo].Config, "private_key")
	for _, secret := range []string{
		stripeSecrets.SecretKey, stripeSecrets.WebhookSecret, epaySecrets.MerchantKey,
		waffoSecrets.PrivateKey,
	} {
		if strings.Contains(listRec.Body.String(), secret) {
			t.Fatalf("admin channel list leaked a credential beginning with %.24q", secret)
		}
	}

	stripeUpdate := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+stripe.ID, map[string]any{
		"name": "Stripe renamed", "enabled": false, "sort_order": 4,
		"config": map[string]any{"secret_key": paymentSecretMask, "webhook_secret": ""},
	})
	stripe = decodePaymentAdminResponse[adminPaymentChannelResponse](t, stripeUpdate, http.StatusOK)
	if stripe.Name != "Stripe renamed" || stripe.Enabled || stripe.SortOrder != 4 {
		t.Fatalf("updated Stripe channel = %+v", stripe)
	}

	epayUpdate := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+epay.ID, map[string]any{
		"config": map[string]any{
			"gateway_url": "https://epay-two.example.test", "merchant_id": "epay-merchant",
			"merchant_key": "", "currency": "usd",
		},
	})
	decodePaymentAdminResponse[adminPaymentChannelResponse](t, epayUpdate, http.StatusOK)

	waffoUpdate := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+waffo.ID, map[string]any{
		"environment": store.PaymentEnvironmentLive,
		"config": map[string]any{
			"merchant_id": "MER_zyxwvutsrqponmlkjihgfe", "private_key": "",
			"store_id": waffoSecrets.StoreID, "product_id": waffoSecrets.ProductID,
			"mode": "prod", "webhook_public_key": waffoSecrets.WebhookPublicKey,
		},
	})
	decodePaymentAdminResponse[adminPaymentChannelResponse](t, waffoUpdate, http.StatusOK)

	storedStripe, err := store.GetPaymentChannel(context.Background(), fx.db, stripe.ID)
	if err != nil {
		t.Fatalf("get stored Stripe channel: %v", err)
	}
	var gotStripe paymentcore.StripeConfig
	if err := json.Unmarshal(storedStripe.Config, &gotStripe); err != nil {
		t.Fatalf("decode stored Stripe config: %v", err)
	}
	if gotStripe != stripeSecrets {
		t.Fatalf("stored Stripe credentials changed: %+v", gotStripe)
	}

	storedEPay, err := store.GetPaymentChannel(context.Background(), fx.db, epay.ID)
	if err != nil {
		t.Fatalf("get stored EPay channel: %v", err)
	}
	var gotEPay paymentcore.EPayConfig
	if err := json.Unmarshal(storedEPay.Config, &gotEPay); err != nil {
		t.Fatalf("decode stored EPay config: %v", err)
	}
	if gotEPay.MerchantKey != epaySecrets.MerchantKey || gotEPay.GatewayURL != "https://epay-two.example.test" || gotEPay.Currency != "USD" {
		t.Fatalf("stored EPay config = %+v", gotEPay)
	}

	storedWaffo, err := store.GetPaymentChannel(context.Background(), fx.db, waffo.ID)
	if err != nil {
		t.Fatalf("get stored Waffo channel: %v", err)
	}
	var gotWaffo paymentcore.WaffoConfig
	if err := json.Unmarshal(storedWaffo.Config, &gotWaffo); err != nil {
		t.Fatalf("decode stored Waffo config: %v", err)
	}
	if strings.TrimSpace(gotWaffo.PrivateKey) != strings.TrimSpace(waffoSecrets.PrivateKey) || gotWaffo.MerchantID != "MER_zyxwvutsrqponmlkjihgfe" ||
		gotWaffo.StoreID != waffoSecrets.StoreID || gotWaffo.ProductID != waffoSecrets.ProductID ||
		gotWaffo.Mode != "prod" || gotWaffo.WebhookPublicKey != waffoSecrets.WebhookPublicKey {
		t.Fatalf("stored Waffo credentials were not preserved: %+v", gotWaffo)
	}

	providerUpdate := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+stripe.ID, map[string]any{
		"provider": paymentcore.ProviderEPay,
		"config": map[string]any{
			"gateway_url": "https://replacement.example.test", "merchant_id": "replacement",
			"merchant_key": "replacement-secret", "currency": "EUR",
		},
	})
	changed := decodePaymentAdminResponse[adminPaymentChannelResponse](t, providerUpdate, http.StatusOK)
	if changed.Provider != paymentcore.ProviderEPay {
		t.Fatalf("changed provider = %q, want epay", changed.Provider)
	}

	for _, id := range []string{stripe.ID, epay.ID, waffo.ID} {
		rec := fx.request(t, http.MethodDelete, "/api/admin/payment-channels/"+id, nil)
		decodePaymentAdminResponse[map[string]bool](t, rec, http.StatusOK)
		if _, err := store.GetPaymentChannel(context.Background(), fx.db, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("deleted channel %s lookup error = %v, want not found", id, err)
		}
	}
}

func TestStripeChannelSupportsTwoStageWebhookSetup(t *testing.T) {
	fx := newPaymentAdminFixture(t)

	incomplete := fx.request(t, http.MethodPost, "/api/admin/payment-channels", map[string]any{
		"name": "Stripe setup", "provider": paymentcore.ProviderStripe,
		"environment": store.PaymentEnvironmentTest, "enabled": false,
		"config": map[string]any{"secret_key": "sk_test_two_stage", "webhook_secret": ""},
	})
	channel := decodePaymentAdminResponse[adminPaymentChannelResponse](t, incomplete, http.StatusCreated)
	if channel.Enabled {
		t.Fatal("incomplete Stripe channel was unexpectedly enabled")
	}
	config := paymentConfigStrings(t, channel.Config)
	if config["secret_key"] != paymentSecretMask || config["webhook_secret"] != "" {
		t.Fatalf("masked incomplete Stripe config = %#v", config)
	}

	for _, body := range []map[string]any{
		{
			"name": "Enabled incomplete", "provider": paymentcore.ProviderStripe,
			"environment": store.PaymentEnvironmentTest, "enabled": true,
			"config": map[string]any{"secret_key": "sk_test_incomplete", "webhook_secret": ""},
		},
		{
			"name": "Disabled malformed", "provider": paymentcore.ProviderStripe,
			"environment": store.PaymentEnvironmentTest, "enabled": false,
			"config": map[string]any{"secret_key": "sk_test_malformed", "webhook_secret": "not-a-secret"},
		},
	} {
		rec := fx.request(t, http.MethodPost, "/api/admin/payment-channels", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid Stripe setup status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	}

	enabledMethod := fx.request(t, http.MethodPost, "/api/admin/payment-methods", map[string]any{
		"name": "Card", "icon": "credit-card", "channel_id": channel.ID,
		"provider_method_config": map[string]any{}, "enabled": true,
	})
	if enabledMethod.Code != http.StatusBadRequest {
		t.Fatalf("enabled method on incomplete channel status = %d, want 400; body=%s", enabledMethod.Code, enabledMethod.Body.String())
	}

	disabledMethod := fx.request(t, http.MethodPost, "/api/admin/payment-methods", map[string]any{
		"name": "Card", "icon": "credit-card", "channel_id": channel.ID,
		"provider_method_config": map[string]any{}, "enabled": false,
	})
	method := decodePaymentAdminResponse[adminPaymentMethodResponse](t, disabledMethod, http.StatusCreated)

	enableWithoutWebhook := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+channel.ID, map[string]any{
		"enabled": true,
	})
	if enableWithoutWebhook.Code != http.StatusBadRequest {
		t.Fatalf("enable incomplete channel status = %d, want 400; body=%s", enableWithoutWebhook.Code, enableWithoutWebhook.Body.String())
	}

	completed := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+channel.ID, map[string]any{
		"enabled": true,
		"config":  map[string]any{"secret_key": paymentSecretMask, "webhook_secret": "whsec_two_stage"},
	})
	channel = decodePaymentAdminResponse[adminPaymentChannelResponse](t, completed, http.StatusOK)
	if !channel.Enabled {
		t.Fatal("completed Stripe channel was not enabled")
	}

	enabledMethod = fx.request(t, http.MethodPatch, "/api/admin/payment-methods/"+method.ID, map[string]any{
		"enabled": true,
	})
	method = decodePaymentAdminResponse[adminPaymentMethodResponse](t, enabledMethod, http.StatusOK)
	if !method.Enabled {
		t.Fatal("method was not enabled after Stripe setup completed")
	}
}

func paymentAdminTestPrivateKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate Waffo private key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal Waffo private key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestPaymentChannelsAdminProtectPendingAndProcessingOrders(t *testing.T) {
	for _, status := range []string{store.PaymentOrderPending, store.PaymentOrderProcessing} {
		t.Run(status, func(t *testing.T) {
			fx := newPaymentAdminFixture(t)
			if err := store.SetSetting(fx.db, "settlement_currency", "USD"); err != nil {
				t.Fatalf("set settlement currency: %v", err)
			}
			mustExec(t, fx.db,
				`INSERT INTO users(id,email,password_hash,role,group_id) VALUES(?,?,?,?,?)`,
				"payment-admin-user", "payment-admin@example.test", "hash", "admin", store.DefaultGroupID,
			)
			pkg, err := store.CreateCreditPackage(context.Background(), fx.db, store.CreditPackage{
				Name: "Admin protection package", Credits: 500, PriceAmountMinor: 1299, Enabled: true,
			})
			if err != nil {
				t.Fatalf("create credit package: %v", err)
			}
			channel := createPaymentChannelForAdminTest(t, fx, "Protected Stripe", paymentcore.ProviderStripe,
				paymentcore.StripeConfig{SecretKey: "sk_test_protected", WebhookSecret: "whsec_protected"}, 0)
			method := createPaymentMethodForAdminTest(t, fx, "Protected card", "credit-card", channel.ID,
				paymentcore.StripeMethodConfig{}, 0)
			order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
				UserID: "payment-admin-user", PaymentMethodID: method.ID,
				ProductType: store.PaymentProductCreditPackage, ProductID: pkg.ID,
			})
			if err != nil {
				t.Fatalf("create %s order: %v", status, err)
			}
			if status == store.PaymentOrderProcessing {
				if _, err := store.MarkPaymentOrderProcessing(context.Background(), fx.db, order.ID, "provider-order-admin"); err != nil {
					t.Fatalf("mark order processing: %v", err)
				}
			}

			credentialUpdate := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+channel.ID, map[string]any{
				"config": map[string]any{"secret_key": "sk_test_changed", "webhook_secret": paymentSecretMask},
			})
			if credentialUpdate.Code != http.StatusConflict {
				t.Fatalf("credential update with %s order status = %d, want 409; body=%s", status, credentialUpdate.Code, credentialUpdate.Body.String())
			}

			deleteMethod := fx.request(t, http.MethodDelete, "/api/admin/payment-methods/"+method.ID, nil)
			decodePaymentAdminResponse[map[string]bool](t, deleteMethod, http.StatusOK)
			providerUpdate := fx.request(t, http.MethodPatch, "/api/admin/payment-channels/"+channel.ID, map[string]any{
				"provider": paymentcore.ProviderEPay,
				"config": map[string]any{
					"gateway_url": "https://epay.example.test", "merchant_id": "merchant",
					"merchant_key": "secret", "currency": "USD",
				},
			})
			if providerUpdate.Code != http.StatusConflict {
				t.Fatalf("provider update with %s order status = %d, want 409; body=%s", status, providerUpdate.Code, providerUpdate.Body.String())
			}

			deleteChannel := fx.request(t, http.MethodDelete, "/api/admin/payment-channels/"+channel.ID, nil)
			if deleteChannel.Code != http.StatusConflict {
				t.Fatalf("channel delete with %s order status = %d, want 409; body=%s", status, deleteChannel.Code, deleteChannel.Body.String())
			}
		})
	}
}

func TestPaymentMethodsAdminCRUDBindingAndReorder(t *testing.T) {
	fx := newPaymentAdminFixture(t)
	stripe := createPaymentChannelForAdminTest(t, fx, "Stripe methods", paymentcore.ProviderStripe,
		paymentcore.StripeConfig{SecretKey: "sk_test_methods", WebhookSecret: "whsec_methods"}, 0)
	epay := createPaymentChannelForAdminTest(t, fx, "EPay methods", paymentcore.ProviderEPay,
		paymentcore.EPayConfig{
			GatewayURL: "https://epay.example.test", MerchantID: "methods-merchant",
			MerchantKey: "methods-secret", Currency: "USD",
		}, 1)

	card := createPaymentMethodForAdminTest(t, fx, "Card", "credit-card", stripe.ID,
		map[string]any{"payment_method_types": []string{"card", "link"}}, 20)
	alipay := createPaymentMethodForAdminTest(t, fx, "Alipay", "scan-line", epay.ID,
		paymentcore.EPayMethodConfig{Type: "alipay"}, 10)
	if card.Provider != paymentcore.ProviderStripe || card.ChannelID != stripe.ID || card.Icon != "credit-card" || !card.Enabled {
		t.Fatalf("created Stripe method = %+v", card)
	}
	if string(card.ProviderMethodConfig) != `{}` {
		t.Fatalf("legacy Stripe method config was not discarded: %s", card.ProviderMethodConfig)
	}

	filteredRec := fx.request(t, http.MethodGet, "/api/admin/payment-methods?channel_id="+url.QueryEscape(stripe.ID), nil)
	filtered := decodePaymentAdminResponse[[]adminPaymentMethodResponse](t, filteredRec, http.StatusOK)
	if len(filtered) != 1 || filtered[0].ID != card.ID || filtered[0].Provider != paymentcore.ProviderStripe {
		t.Fatalf("Stripe-filtered methods = %+v", filtered)
	}

	updateRec := fx.request(t, http.MethodPatch, "/api/admin/payment-methods/"+card.ID, map[string]any{
		"name": "QR card", "icon": "qr-code", "channel_id": epay.ID,
		"provider_method_config": map[string]any{"type": "wxpay"},
		"enabled":                false, "sort_order": 99,
	})
	updated := decodePaymentAdminResponse[adminPaymentMethodResponse](t, updateRec, http.StatusOK)
	if updated.Name != "QR card" || updated.Icon != "qr-code" || updated.ChannelID != epay.ID ||
		updated.Provider != paymentcore.ProviderEPay || updated.Enabled || updated.SortOrder != 99 {
		t.Fatalf("updated and rebound method = %+v", updated)
	}
	var epayMethodConfig paymentcore.EPayMethodConfig
	if err := json.Unmarshal(updated.ProviderMethodConfig, &epayMethodConfig); err != nil || epayMethodConfig.Type != "wxpay" {
		t.Fatalf("updated EPay method config = %+v, err=%v", epayMethodConfig, err)
	}

	reorderRec := fx.request(t, http.MethodPatch, "/api/admin/payment-methods/reorder", map[string]any{
		"ids": []string{card.ID, alipay.ID},
	})
	decodePaymentAdminResponse[map[string]bool](t, reorderRec, http.StatusOK)
	methodsRec := fx.request(t, http.MethodGet, "/api/admin/payment-methods", nil)
	methods := decodePaymentAdminResponse[[]adminPaymentMethodResponse](t, methodsRec, http.StatusOK)
	if len(methods) != 2 || methods[0].ID != card.ID || methods[0].SortOrder != 0 || methods[1].ID != alipay.ID || methods[1].SortOrder != 1 {
		t.Fatalf("reordered methods = %+v", methods)
	}

	badReorder := fx.request(t, http.MethodPatch, "/api/admin/payment-methods/reorder", map[string]any{
		"ids": []string{card.ID, card.ID},
	})
	if badReorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate reorder status = %d, want 400; body=%s", badReorder.Code, badReorder.Body.String())
	}

	for _, id := range []string{card.ID, alipay.ID} {
		deleteRec := fx.request(t, http.MethodDelete, "/api/admin/payment-methods/"+id, nil)
		decodePaymentAdminResponse[map[string]bool](t, deleteRec, http.StatusOK)
		if _, err := store.GetPaymentMethod(context.Background(), fx.db, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("deleted method %s lookup error = %v, want not found", id, err)
		}
	}
	for _, id := range []string{stripe.ID, epay.ID} {
		deleteRec := fx.request(t, http.MethodDelete, "/api/admin/payment-channels/"+id, nil)
		decodePaymentAdminResponse[map[string]bool](t, deleteRec, http.StatusOK)
	}
}

func TestPaymentOrdersAdminRejectsInvalidPagination(t *testing.T) {
	fx := newPaymentAdminFixture(t)
	for _, query := range []string{
		"limit=0", "limit=-1", "limit=501", "limit=not-a-number",
		"offset=-1", "offset=not-a-number", "offset=999999999999999999999999999999999999",
	} {
		t.Run(query, func(t *testing.T) {
			rec := fx.request(t, http.MethodGet, "/api/admin/payment-orders?"+query, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s status = %d, want 400; body=%s", query, rec.Code, rec.Body.String())
			}
		})
	}
	valid := fx.request(t, http.MethodGet, "/api/admin/payment-orders?limit=500&offset=0", nil)
	response := decodePaymentAdminResponse[struct {
		Orders []adminPaymentOrderResponse `json:"orders"`
		Total  int                         `json:"total"`
	}](t, valid, http.StatusOK)
	if response.Total != 0 || len(response.Orders) != 0 {
		t.Fatalf("empty payment orders response = %+v", response)
	}
}

func TestPaymentRouterExposesCanonicalRoutesOnly(t *testing.T) {
	fx := newPaymentAdminFixture(t)
	channel := createPaymentChannelForAdminTest(t, fx, "Router EPay", paymentcore.ProviderEPay,
		paymentcore.EPayConfig{
			GatewayURL: "https://epay.example.test", MerchantID: "router-merchant",
			MerchantKey: "router-secret", Currency: "USD",
		}, 0)
	d := Deps{DB: fx.db, Cache: cache.NewMemory()}
	router := NewRouter(d)

	for _, route := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/payment-methods?target_type=credit_package", http.StatusUnauthorized},
		{http.MethodPost, "/api/payments/checkout", http.StatusUnauthorized},
		{http.MethodGet, "/api/payments/orders/order-id", http.StatusUnauthorized},
		{http.MethodGet, "/api/admin/payment-channels", http.StatusUnauthorized},
		{http.MethodPost, "/api/admin/payment-channels", http.StatusUnauthorized},
		{http.MethodPatch, "/api/admin/payment-channels/channel-id", http.StatusUnauthorized},
		{http.MethodDelete, "/api/admin/payment-channels/channel-id", http.StatusUnauthorized},
		{http.MethodGet, "/api/admin/payment-methods", http.StatusUnauthorized},
		{http.MethodPost, "/api/admin/payment-methods", http.StatusUnauthorized},
		{http.MethodPatch, "/api/admin/payment-methods/reorder", http.StatusUnauthorized},
		{http.MethodPatch, "/api/admin/payment-methods/method-id", http.StatusUnauthorized},
		{http.MethodDelete, "/api/admin/payment-methods/method-id", http.StatusUnauthorized},
		{http.MethodGet, "/api/admin/payment-orders", http.StatusUnauthorized},
		{http.MethodGet, "/api/payments/webhooks/" + channel.ID, http.StatusOK},
		{http.MethodPost, "/api/payments/webhooks/" + channel.ID, http.StatusOK},
		{http.MethodGet, "/api/payments/methods", http.StatusNotFound},
		{http.MethodGet, fmt.Sprintf("/api/payments/webhooks/%s/%s", paymentcore.ProviderEPay, channel.ID), http.StatusNotFound},
		{http.MethodPost, fmt.Sprintf("/api/payments/webhooks/%s/%s", paymentcore.ProviderEPay, channel.ID), http.StatusNotFound},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != route.want {
				t.Fatalf("%s %s status = %d, want %d; body=%s", route.method, route.path, rec.Code, route.want, rec.Body.String())
			}
			if route.want == http.StatusOK && strings.TrimSpace(rec.Body.String()) != "fail" {
				t.Fatalf("%s %s body = %q, want EPay failure acknowledgement", route.method, route.path, rec.Body.String())
			}
		})
	}
}
