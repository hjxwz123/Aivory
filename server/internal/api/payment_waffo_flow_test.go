package api

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/payment"
	"aivory/server/internal/store"
)

func TestWaffoPaymentOrderSnapshotsConfiguredProductCurrency(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "waffo-currency.db"))
	t.Cleanup(func() { _ = db.Close() })
	if err := store.SetSetting(db, "settlement_currency", "CNY"); err != nil {
		t.Fatalf("set settlement currency: %v", err)
	}
	mustExec(t, db,
		`INSERT INTO users(id,email,password_hash,group_id) VALUES(?,?,?,?)`,
		"waffo_currency_user", "waffo-currency@example.test", "hash", store.DefaultGroupID,
	)
	_, privatePEM, _ := newAPIWaffoKeyPair(t)
	cfg := payment.WaffoConfig{
		MerchantID: "MER_0123456789abcdefghijkl", PrivateKey: privatePEM,
		StoreID: "STO_0123456789abcdefghijkl", ProductID: "PROD_0123456789abcdefghijkl",
		Currency: "USD", ConversionRate: "0.14", ConversionRateBaseCurrency: "CNY", Mode: "test",
	}
	rawConfig, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal Waffo currency config: %v", err)
	}
	channel, err := store.CreatePaymentChannel(context.Background(), db, store.PaymentChannel{
		ID: "paych_waffo_currency", Name: "Waffo USD", Provider: payment.ProviderWaffo,
		Config: rawConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Waffo currency channel: %v", err)
	}
	method, err := store.CreatePaymentMethod(context.Background(), db, store.PaymentMethod{
		ID: "paym_waffo_currency", ChannelID: channel.ID, Name: "Waffo card", Type: payment.ProviderWaffo,
		ProviderMethodConfig: json.RawMessage(`{}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Waffo currency method: %v", err)
	}
	pkg, err := store.CreateCreditPackage(context.Background(), db, store.CreditPackage{
		ID: "cp_waffo_currency", Name: "CNY package", Credits: 2800, PriceAmountMinor: 28000, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Waffo currency package: %v", err)
	}

	order, err := store.CreatePaymentOrder(context.Background(), db, store.PaymentOrderCreateInput{
		UserID: "waffo_currency_user", PaymentMethodID: method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: pkg.ID,
	})
	if err != nil {
		t.Fatalf("create Waffo currency order: %v", err)
	}
	if order.AmountMinor != 28000 || order.Currency != "CNY" ||
		order.ProviderAmountMinor != 3920 || order.ProviderCurrency != "USD" || order.ConversionRate != "0.14" {
		t.Fatalf("Waffo currency snapshots = %+v", order)
	}
}

func TestWaffoPancakeWebhookFulfillsCreditOrderExactlyOnce(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "waffo-payment.db"))
	t.Cleanup(func() { _ = db.Close() })
	if err := store.SetSetting(db, "settlement_currency", "CNY"); err != nil {
		t.Fatalf("set settlement currency: %v", err)
	}
	user := &store.User{ID: "waffo_user", Email: "waffo-buyer@example.test", GroupID: store.DefaultGroupID}
	mustExec(t, db,
		`INSERT INTO users(id,email,password_hash,group_id,credits_permanent) VALUES(?,?,?,?,?)`,
		user.ID, user.Email, "hash", user.GroupID, 25,
	)

	privateKey, privatePEM, publicPEM := newAPIWaffoKeyPair(t)
	cfg := payment.WaffoConfig{
		MerchantID: "MER_0123456789abcdefghijkl", PrivateKey: privatePEM,
		StoreID: "STO_0123456789abcdefghijkl", ProductID: "PROD_0123456789abcdefghijkl",
		Currency: "USD", ConversionRate: "0.14", ConversionRateBaseCurrency: "CNY",
		Mode: "test", WebhookPublicKey: publicPEM,
	}
	rawConfig, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal Waffo config: %v", err)
	}
	channel, err := store.CreatePaymentChannel(context.Background(), db, store.PaymentChannel{
		ID: "paych_waffo_flow", Name: "Waffo Pancake", Provider: payment.ProviderWaffo,
		Config: rawConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Waffo channel: %v", err)
	}
	method, err := store.CreatePaymentMethod(context.Background(), db, store.PaymentMethod{
		ID: "paym_waffo_flow", ChannelID: channel.ID, Name: "Global card", Type: payment.ProviderWaffo,
		Icon: "credit-card", ProviderMethodConfig: json.RawMessage(`{}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Waffo method: %v", err)
	}
	pkg, err := store.CreateCreditPackage(context.Background(), db, store.CreditPackage{
		ID: "cp_waffo_flow", Name: "1,200 credits", Credits: 1200, PriceAmountMinor: 28000, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Waffo credit package: %v", err)
	}
	order, err := store.CreatePaymentOrder(context.Background(), db, store.PaymentOrderCreateInput{
		UserID: user.ID, PaymentMethodID: method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: pkg.ID,
	})
	if err != nil {
		t.Fatalf("create Waffo payment order: %v", err)
	}
	if _, err := store.MarkPaymentOrderProcessing(context.Background(), db, order.ID, ""); err != nil {
		t.Fatalf("mark Waffo order processing: %v", err)
	}
	if order.ProviderAmountMinor != 3920 || order.ProviderCurrency != "USD" || order.Currency != "CNY" {
		t.Fatalf("cross-currency Waffo order snapshot = %+v", order)
	}

	payload := apiWaffoWebhookPayload(t, cfg, *order)
	signature := signAPIWaffoWebhook(t, payload, privateKey)
	d := Deps{DB: db}
	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/payments/webhooks/"+channel.ID, strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Waffo-Signature", signature)
		req = paymentAPIRequest(req, nil, map[string]string{"channelId": channel.ID})
		rec := httptest.NewRecorder()
		paymentWebhookHandler(d, rec, req)
		return rec
	}

	for attempt := 1; attempt <= 2; attempt++ {
		rec := serve()
		if rec.Code != http.StatusOK {
			t.Fatalf("Waffo webhook attempt %d status = %d; body=%s", attempt, rec.Code, rec.Body.String())
		}
	}

	var credits int64
	if err := db.QueryRow(`SELECT credits_permanent FROM users WHERE id=?`, user.ID).Scan(&credits); err != nil {
		t.Fatalf("read fulfilled credits: %v", err)
	}
	if credits != 1225 {
		t.Fatalf("credits after duplicate Waffo webhook = %d, want 1225", credits)
	}
	stored, err := store.GetPaymentOrder(context.Background(), db, order.ID)
	if err != nil {
		t.Fatalf("get fulfilled Waffo order: %v", err)
	}
	if stored.Status != store.PaymentOrderFulfilled || stored.ProviderOrderID != "ORD_0123456789abcdefghijkl" ||
		stored.PaidAmountMinor != 4312 || stored.TaxAmountMinor != 392 ||
		stored.PaidAt == 0 || stored.FulfilledAt == 0 {
		t.Fatalf("fulfilled Waffo order = %+v", stored)
	}
	events, err := store.ListPaymentEventsForOrder(context.Background(), db, order.ID)
	if err != nil {
		t.Fatalf("list Waffo payment events: %v", err)
	}
	if len(events) != 1 || events[0].EventID != "delivery_waffo_flow" || events[0].ProcessedAt == 0 {
		t.Fatalf("Waffo payment events = %+v", events)
	}

	historyReq := httptest.NewRequest(http.MethodGet, "/api/payments/orders?limit=10", nil)
	historyReq = paymentAPIRequest(historyReq, user, nil)
	historyRec := httptest.NewRecorder()
	listPaymentOrdersForUserHandler(d, historyRec, historyReq)
	if historyRec.Code != http.StatusOK {
		t.Fatalf("Waffo payment history status = %d; body=%s", historyRec.Code, historyRec.Body.String())
	}
	var history struct {
		Orders []publicPaymentOrderListItem `json:"orders"`
	}
	if err := json.Unmarshal(historyRec.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode Waffo payment history: %v", err)
	}
	if len(history.Orders) != 1 || history.Orders[0].ID != order.ID || history.Orders[0].Status != "paid" ||
		history.Orders[0].MethodName != method.Name || history.Orders[0].TargetName != pkg.Name ||
		history.Orders[0].AmountMinor != 4312 || history.Orders[0].TaxAmountMinor != 392 || history.Orders[0].Currency != "USD" {
		t.Fatalf("Waffo payment history = %+v", history.Orders)
	}
}

func newAPIWaffoKeyPair(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate Waffo RSA key: %v", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal Waffo private key: %v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal Waffo public key: %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
	publicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
	return key, privatePEM, publicPEM
}

func apiWaffoWebhookPayload(t *testing.T, cfg payment.WaffoConfig, order store.PaymentOrder) []byte {
	t.Helper()
	taxMinor := (order.ProviderAmountMinor + 9) / 10
	totalMinor := order.ProviderAmountMinor + taxMinor
	subtotal, err := payment.FormatMinorAmount(order.ProviderAmountMinor, order.ProviderCurrency)
	if err != nil {
		t.Fatalf("format Waffo webhook subtotal: %v", err)
	}
	tax, err := payment.FormatMinorAmount(taxMinor, order.ProviderCurrency)
	if err != nil {
		t.Fatalf("format Waffo webhook tax: %v", err)
	}
	total, err := payment.FormatMinorAmount(totalMinor, order.ProviderCurrency)
	if err != nil {
		t.Fatalf("format Waffo webhook total: %v", err)
	}
	payload := map[string]any{
		"id": "delivery_waffo_flow", "timestamp": time.Now().UTC().Format(time.RFC3339),
		"eventType": "order.completed", "eventId": "PAY_0123456789abcdefghijkl",
		"storeId": cfg.StoreID, "storeName": "Aivory", "mode": cfg.Mode,
		"data": map[string]any{
			"orderId": "ORD_0123456789abcdefghijkl", "orderStatus": "completed",
			"buyerEmail":                    order.UserEmail,
			"merchantProvidedBuyerIdentity": payment.WaffoBuyerIdentity(order.UserID),
			"orderMerchantExternalId":       order.ID,
			"orderMetadata":                 map[string]string{"aivory_order_id": order.ID},
			"currency":                      order.ProviderCurrency, "amount": total, "taxAmount": tax,
			"subtotal": subtotal, "total": total,
			"productName": order.ProductName, "paymentId": "PAY_0123456789abcdefghijkl",
			"paymentStatus": "succeeded", "paymentMethod": "card", "paymentLast4": "0110",
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal Waffo webhook: %v", err)
	}
	return encoded
}

func signAPIWaffoWebhook(t *testing.T, payload []byte, key *rsa.PrivateKey) string {
	t.Helper()
	timestamp := time.Now().UnixMilli()
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign Waffo webhook: %v", err)
	}
	return fmt.Sprintf("t=%d,v1=%s", timestamp, base64.StdEncoding.EncodeToString(signature))
}
