package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/payment"
	"aivory/server/internal/store"
)

func TestEPayResumeUsesOwnedOrderSnapshotAndFreshMerchantOrder(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, original := createEPayCheckoutForTest(t, fx)

	maliciousBody := `{"amount_minor":1,"currency":"JPY","target_id":"other","url":"https://attacker.invalid"}`
	req := httptest.NewRequest(http.MethodPost, "https://aivory.example.test/api/payments/orders/"+orderID+"/resume", strings.NewReader(maliciousBody))
	req = paymentAPIRequest(req, fx.user, map[string]string{"id": orderID})
	rec := httptest.NewRecorder()
	resumePaymentOrderHandler(fx.d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume EPay status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		OrderID string                 `json:"order_id"`
		Action  payment.CheckoutAction `json:"action"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode resume response: %v", err)
	}
	if response.OrderID != orderID || response.Action.Type != payment.ActionFormPost ||
		response.Action.ResumeMode != payment.CheckoutResumeRetrySubmission {
		t.Fatalf("resume response = %+v", response)
	}
	retryMerchantOrderID := response.Action.Fields["out_trade_no"]
	if retryMerchantOrderID == "" || retryMerchantOrderID == orderID ||
		retryMerchantOrderID == original.Fields["out_trade_no"] || response.Action.Fields["money"] != original.Fields["money"] ||
		response.Action.Fields["name"] != fx.pkg.Name {
		t.Fatalf("resumed EPay fields do not use immutable order snapshot: %+v", response.Action.Fields)
	}
	if got, want := response.Action.Fields["sign"], payment.EPaySign(response.Action.Fields, testEPayMerchantKey); got != want {
		t.Fatalf("resumed EPay signature = %q, want %q", got, want)
	}
	var count int
	if err := fx.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM payment_orders WHERE user_id=?`, fx.user.ID).Scan(&count); err != nil {
		t.Fatalf("count orders after resume: %v", err)
	}
	if count != 1 {
		t.Fatalf("orders after retry submission = %d, want 1", count)
	}
	if err := fx.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM payment_order_attempts WHERE order_id=?`, orderID,
	).Scan(&count); err != nil {
		t.Fatalf("count attempts after retry: %v", err)
	}
	if count != 2 {
		t.Fatalf("attempts after retry submission = %d, want initial + retry", count)
	}
	attempt, err := store.GetPaymentOrderAttemptByMerchantID(
		context.Background(), fx.db, payment.ProviderEPay, fx.channel.ID, retryMerchantOrderID,
	)
	if err != nil || attempt.OrderID != orderID || attempt.Status != store.PaymentOrderAttemptIssued {
		t.Fatalf("retry attempt = %+v, %v", attempt, err)
	}
	for _, internalKey := range []string{"provider_order_id", "session_id", "session_url", "expires_at"} {
		if strings.Contains(rec.Body.String(), `"`+internalKey+`"`) {
			t.Fatalf("resume response leaked internal field %q: %s", internalKey, rec.Body.String())
		}
	}
}

func TestEPayDistinctAttemptsBothSucceedButFulfillOrderOnce(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, original := createEPayCheckoutForTest(t, fx)

	req := httptest.NewRequest(http.MethodPost, "/api/payments/orders/"+orderID+"/resume", nil)
	req = paymentAPIRequest(req, fx.user, map[string]string{"id": orderID})
	rec := httptest.NewRecorder()
	resumePaymentOrderHandler(fx.d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume EPay status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Action payment.CheckoutAction `json:"action"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode resume response: %v", err)
	}
	retryMerchantOrderID := response.Action.Fields["out_trade_no"]
	if retryMerchantOrderID == "" || retryMerchantOrderID == orderID {
		t.Fatalf("retry merchant order id = %q", retryMerchantOrderID)
	}

	callbacks := []map[string]string{
		{
			"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": retryMerchantOrderID,
			"trade_no": "epay_retry_paid_first", "trade_status": "TRADE_SUCCESS", "money": original.Fields["money"],
		},
		{
			"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
			"trade_no": "epay_initial_paid_late", "trade_status": "TRADE_SUCCESS", "money": original.Fields["money"],
		},
	}
	for index, params := range callbacks {
		callback := serveEPayCallback(t, fx, params)
		if callback.Code != http.StatusOK || strings.TrimSpace(callback.Body.String()) != "success" {
			t.Fatalf("attempt callback %d = status %d body %q", index+1, callback.Code, callback.Body.String())
		}
	}

	var credits float64
	if err := fx.db.QueryRowContext(context.Background(),
		`SELECT credits_permanent FROM users WHERE id=?`, fx.user.ID,
	).Scan(&credits); err != nil {
		t.Fatalf("read credits after two paid attempts: %v", err)
	}
	if credits != 25+fx.pkg.Credits {
		t.Fatalf("credits after two paid attempts = %v, want %v", credits, 25+fx.pkg.Credits)
	}
	order, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil {
		t.Fatalf("get fulfilled order: %v", err)
	}
	if order.Status != store.PaymentOrderFulfilled || order.ProviderOrderID != "epay_retry_paid_first" {
		t.Fatalf("fulfilled order after two attempts = %+v", order)
	}
	for merchantOrderID, providerOrderID := range map[string]string{
		retryMerchantOrderID: "epay_retry_paid_first",
		orderID:              "epay_initial_paid_late",
	} {
		attempt, getErr := store.GetPaymentOrderAttemptByMerchantID(
			context.Background(), fx.db, payment.ProviderEPay, fx.channel.ID, merchantOrderID,
		)
		if getErr != nil || attempt.Status != store.PaymentOrderAttemptPaid ||
			attempt.ProviderOrderID != providerOrderID || attempt.PaidAt == 0 {
			t.Fatalf("paid attempt %q = %+v, %v", merchantOrderID, attempt, getErr)
		}
	}
	var eventCount int
	if err := fx.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM payment_events WHERE order_id=? AND processed_at>0`, orderID,
	).Scan(&eventCount); err != nil {
		t.Fatalf("count processed attempt events: %v", err)
	}
	if eventCount != 2 {
		t.Fatalf("processed attempt events = %d, want 2", eventCount)
	}
}

func TestEPayAttemptCallbackWithoutProviderTradeNumberStillFulfills(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, original := createEPayCheckoutForTest(t, fx)

	callback := serveEPayCallback(t, fx, map[string]string{
		"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
		"trade_status": "TRADE_SUCCESS", "money": original.Fields["money"],
	})
	if callback.Code != http.StatusOK || strings.TrimSpace(callback.Body.String()) != "success" {
		t.Fatalf("callback without EPay trade_no = status %d body %q", callback.Code, callback.Body.String())
	}
	order, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil || order.Status != store.PaymentOrderFulfilled {
		t.Fatalf("order after callback without EPay trade_no = %+v, %v", order, err)
	}
	attempt, err := store.GetPaymentOrderAttemptByMerchantID(
		context.Background(), fx.db, payment.ProviderEPay, fx.channel.ID, orderID,
	)
	if err != nil || attempt.Status != store.PaymentOrderAttemptPaid || attempt.ProviderOrderID != "" || attempt.PaidAt == 0 {
		t.Fatalf("attempt after callback without EPay trade_no = %+v, %v", attempt, err)
	}
}

func TestCheckoutPersistenceUsesTokenFreeProviderSessionURL(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: fx.user.ID, PaymentMethodID: fx.method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	const (
		sessionID  = "SES_credentialless"
		sessionURL = "https://checkout.waffo.example.test/store/checkout/SES_credentialless"
	)
	action := payment.CheckoutAction{
		Type: payment.ActionRedirect, URL: sessionURL + "#token=short-lived-secret",
		SessionID: sessionID, SessionURL: sessionURL, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := persistPaymentCheckoutAction(context.Background(), fx.d, order.ID, action); err != nil {
		t.Fatalf("persist checkout action: %v", err)
	}
	stored, err := store.GetPaymentOrder(context.Background(), fx.db, order.ID)
	if err != nil {
		t.Fatalf("get persisted checkout order: %v", err)
	}
	if stored.CheckoutURL != sessionURL || strings.Contains(stored.CheckoutURL, "token") || strings.Contains(stored.CheckoutURL, "#") {
		t.Fatalf("stored checkout URL = %q, want token-free %q", stored.CheckoutURL, sessionURL)
	}
}

func TestResumePaymentOrderEnforcesOwnershipAndMutableStatus(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, _ := createEPayCheckoutForTest(t, fx)
	other := &store.User{ID: "payment_other_user", Email: "other@example.test", GroupID: store.DefaultGroupID}
	mustExec(t, fx.db,
		`INSERT INTO users(id,email,password_hash,group_id) VALUES(?,?,?,?)`,
		other.ID, other.Email, "hash", other.GroupID,
	)

	otherReq := httptest.NewRequest(http.MethodPost, "/api/payments/orders/"+orderID+"/resume", nil)
	otherReq = paymentAPIRequest(otherReq, other, map[string]string{"id": orderID})
	otherRec := httptest.NewRecorder()
	resumePaymentOrderHandler(fx.d, otherRec, otherReq)
	if otherRec.Code != http.StatusNotFound {
		t.Fatalf("cross-user resume status = %d, want 404; body=%s", otherRec.Code, otherRec.Body.String())
	}

	mustExec(t, fx.db, `UPDATE payment_orders SET status=? WHERE id=?`, store.PaymentOrderFulfilled, orderID)
	ownerReq := httptest.NewRequest(http.MethodPost, "/api/payments/orders/"+orderID+"/resume", nil)
	ownerReq = paymentAPIRequest(ownerReq, fx.user, map[string]string{"id": orderID})
	ownerRec := httptest.NewRecorder()
	resumePaymentOrderHandler(fx.d, ownerRec, ownerReq)
	if ownerRec.Code != http.StatusConflict || !strings.Contains(ownerRec.Body.String(), errPaymentOrderAlreadyPaid.Error()) {
		t.Fatalf("fulfilled-order resume = %d %s, want 409 %s", ownerRec.Code, ownerRec.Body.String(), errPaymentOrderAlreadyPaid)
	}
}

func TestResumePaymentOrderRejectsDisabledChannel(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, _ := createEPayCheckoutForTest(t, fx)
	mustExec(t, fx.db, `UPDATE payment_channels SET enabled=0 WHERE id=?`, fx.channel.ID)

	req := httptest.NewRequest(http.MethodPost, "/api/payments/orders/"+orderID+"/resume", nil)
	req = paymentAPIRequest(req, fx.user, map[string]string{"id": orderID})
	rec := httptest.NewRecorder()
	resumePaymentOrderHandler(fx.d, rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errPaymentOrderNotResumable.Error()) {
		t.Fatalf("resume with disabled channel = %d %s, want 409 %s", rec.Code, rec.Body.String(), errPaymentOrderNotResumable)
	}
}

func TestPublicPaymentOrderResumeMetadata(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name        string
		order       store.PaymentOrder
		canResume   bool
		canRetry    bool
		mode        string
		wantExpires bool
	}{
		{name: "Stripe active", order: store.PaymentOrder{Provider: payment.ProviderStripe, Status: store.PaymentOrderProcessing, CheckoutExpiresAt: now + 60}, canResume: true, mode: payment.CheckoutResumeOriginalSession, wantExpires: true},
		{name: "Stripe create response unknown", order: store.PaymentOrder{Provider: payment.ProviderStripe, Status: store.PaymentOrderProcessing}, canResume: true, mode: payment.CheckoutResumeOriginalSession},
		{name: "Stripe expired", order: store.PaymentOrder{Provider: payment.ProviderStripe, Status: store.PaymentOrderProcessing, CheckoutExpiresAt: now - 1}, wantExpires: true},
		{name: "Waffo active", order: store.PaymentOrder{Provider: payment.ProviderWaffo, Status: store.PaymentOrderProcessing, CheckoutSessionID: "SES_1", CheckoutURL: "https://checkout.example.test/SES_1", CheckoutExpiresAt: now + 60}, canResume: true, mode: payment.CheckoutResumeOriginalSession, wantExpires: true},
		{name: "Waffo token-free URL missing", order: store.PaymentOrder{Provider: payment.ProviderWaffo, Status: store.PaymentOrderProcessing, CheckoutSessionID: "SES_1", CheckoutExpiresAt: now + 60}, wantExpires: true},
		{name: "EPay retry", order: store.PaymentOrder{Provider: payment.ProviderEPay, Status: store.PaymentOrderPending}, canRetry: true, mode: payment.CheckoutResumeRetrySubmission},
		{name: "terminal", order: store.PaymentOrder{Provider: payment.ProviderEPay, Status: store.PaymentOrderFailed}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			canResume, canRetry, mode, expires := publicPaymentOrderResumeMetadata(tc.order, now)
			if canResume != tc.canResume || canRetry != tc.canRetry || mode != tc.mode || (expires != nil) != tc.wantExpires {
				t.Fatalf("metadata = resume=%v retry=%v mode=%q expires=%v", canResume, canRetry, mode, expires)
			}
		})
	}
}

func TestPaidPreResumeReconciliationFulfillsStripeAndWaffoAcrossCurrencies(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	stripeConfig, err := json.Marshal(payment.StripeConfig{
		SecretKey: "sk_test_resume_paid", WebhookSecret: "whsec_resume_paid",
	})
	if err != nil {
		t.Fatalf("marshal Stripe config: %v", err)
	}
	stripeChannel, err := store.CreatePaymentChannel(context.Background(), fx.db, store.PaymentChannel{
		ID: "paych_stripe_resume_paid", Name: "Stripe resume paid", Provider: payment.ProviderStripe,
		Config: stripeConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Stripe channel: %v", err)
	}
	stripeMethod, err := store.CreatePaymentMethod(context.Background(), fx.db, store.PaymentMethod{
		ID: "paym_stripe_resume_paid", ChannelID: stripeChannel.ID, Name: "Stripe card",
		Type: payment.ProviderStripe, ProviderMethodConfig: json.RawMessage(`{}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create Stripe method: %v", err)
	}
	stripeOrder, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: fx.user.ID, PaymentMethodID: stripeMethod.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
	})
	if err != nil {
		t.Fatalf("create Stripe order: %v", err)
	}
	const stripeSessionID = "cs_test_resume_paid"
	stripeOrder, err = store.MarkPaymentOrderCheckoutStarted(
		context.Background(), fx.db, stripeOrder.ID, stripeSessionID, stripeSessionID, time.Now().Add(time.Hour).Unix(), "",
	)
	if err != nil {
		t.Fatalf("mark Stripe order processing: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/payments/orders/"+stripeOrder.ID+"/resume", nil)
	stripeResult, err := applyPaymentResumeReconciliationEvent(req, fx.d, *stripeOrder, payment.ProviderEvent{
		ID: "reconcile:" + stripeSessionID + ":paid", Type: "checkout.session.reconciled", Status: payment.EventPaid,
		OrderID: stripeOrder.ID, ProviderOrderID: stripeSessionID,
		AmountMinor: stripeOrder.AmountMinor, PaidAmountMinor: stripeOrder.AmountMinor, Currency: stripeOrder.Currency,
	})
	if err != nil || stripeResult.Status != store.PaymentOrderFulfilled {
		t.Fatalf("Stripe paid pre-resume reconciliation = %+v, %v", stripeResult, err)
	}
	var creditsAfterStripe float64
	if err := fx.db.QueryRowContext(context.Background(), `SELECT credits_permanent FROM users WHERE id=?`, fx.user.ID).Scan(&creditsAfterStripe); err != nil {
		t.Fatalf("read credits after Stripe reconciliation: %v", err)
	}
	if creditsAfterStripe != 25+fx.pkg.Credits {
		t.Fatalf("credits after Stripe reconciliation = %v, want %v", creditsAfterStripe, 25+fx.pkg.Credits)
	}

	waffoOrder, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: fx.user.ID, PaymentMethodID: fx.method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
	})
	if err != nil {
		t.Fatalf("create deferred Waffo order fixture: %v", err)
	}
	mustExec(t, fx.db, `UPDATE payment_orders SET provider=?, status=? WHERE id=?`, payment.ProviderWaffo, store.PaymentOrderProcessing, waffoOrder.ID)
	waffoOrder.Provider = payment.ProviderWaffo
	waffoOrder.Status = store.PaymentOrderProcessing
	waffoResult, err := applyPaymentResumeReconciliationEvent(req, fx.d, *waffoOrder, payment.ProviderEvent{
		ID: "reconcile:waffo:paid", Type: "payment.reconciled", Status: payment.EventPaid,
		OrderID: waffoOrder.ID, AmountMinor: waffoOrder.ProviderAmountMinor,
		PaidAmountMinor: waffoOrder.ProviderAmountMinor, Currency: waffoOrder.ProviderCurrency,
		UserID: payment.WaffoBuyerIdentity(fx.user.ID),
	})
	if err != nil || waffoResult.Status != store.PaymentOrderFulfilled {
		t.Fatalf("same-currency Waffo paid pre-resume reconciliation = %+v, %v", waffoResult, err)
	}
	var creditsAfterWaffo float64
	if err := fx.db.QueryRowContext(context.Background(), `SELECT credits_permanent FROM users WHERE id=?`, fx.user.ID).Scan(&creditsAfterWaffo); err != nil {
		t.Fatalf("read credits after same-currency Waffo reconciliation: %v", err)
	}
	if creditsAfterWaffo != creditsAfterStripe+fx.pkg.Credits {
		t.Fatalf("credits after same-currency Waffo reconciliation = %v, want %v", creditsAfterWaffo, creditsAfterStripe+fx.pkg.Credits)
	}

	crossCurrencyOrder, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: fx.user.ID, PaymentMethodID: fx.method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
	})
	if err != nil {
		t.Fatalf("create cross-currency Waffo order fixture: %v", err)
	}
	mustExec(t, fx.db,
		`UPDATE payment_orders SET provider=?, status=?, provider_amount_minor=?, provider_currency=?, conversion_rate=? WHERE id=?`,
		payment.ProviderWaffo, store.PaymentOrderProcessing, crossCurrencyOrder.AmountMinor*7, "CNY", "7", crossCurrencyOrder.ID,
	)
	crossCurrencyOrder.Provider = payment.ProviderWaffo
	crossCurrencyOrder.Status = store.PaymentOrderProcessing
	crossCurrencyOrder.ProviderAmountMinor = crossCurrencyOrder.AmountMinor * 7
	crossCurrencyOrder.ProviderCurrency = "CNY"
	crossCurrencyOrder.ConversionRate = "7"
	crossCurrencyResult, err := applyPaymentResumeReconciliationEvent(req, fx.d, *crossCurrencyOrder, payment.ProviderEvent{
		ID: "reconcile:waffo-cross-currency:paid", Type: "payment.reconciled", Status: payment.EventPaid,
		OrderID: crossCurrencyOrder.ID, AmountMinor: crossCurrencyOrder.ProviderAmountMinor,
		PaidAmountMinor: crossCurrencyOrder.ProviderAmountMinor, Currency: crossCurrencyOrder.ProviderCurrency,
		UserID: payment.WaffoBuyerIdentity(fx.user.ID),
	})
	if err != nil || crossCurrencyResult.Status != store.PaymentOrderFulfilled {
		t.Fatalf("cross-currency Waffo paid pre-resume reconciliation = %+v, %v", crossCurrencyResult, err)
	}
	var creditsAfterCrossCurrency float64
	if err := fx.db.QueryRowContext(context.Background(), `SELECT credits_permanent FROM users WHERE id=?`, fx.user.ID).Scan(&creditsAfterCrossCurrency); err != nil {
		t.Fatalf("read credits after cross-currency Waffo reconciliation: %v", err)
	}
	if creditsAfterCrossCurrency != creditsAfterWaffo+fx.pkg.Credits {
		t.Fatalf("credits after cross-currency Waffo reconciliation = %v, want %v", creditsAfterCrossCurrency, creditsAfterWaffo+fx.pkg.Credits)
	}
	if crossCurrencyResult.PaidAmountMinor != crossCurrencyOrder.ProviderAmountMinor ||
		crossCurrencyResult.Currency != crossCurrencyOrder.Currency {
		t.Fatalf("cross-currency Waffo financial snapshots = %+v", crossCurrencyResult)
	}
	displayAmount, displayCurrency := paymentOrderDisplayAmountCurrency(*crossCurrencyResult)
	if displayAmount != crossCurrencyOrder.ProviderAmountMinor || displayCurrency != crossCurrencyOrder.ProviderCurrency {
		t.Fatalf("cross-currency Waffo display = %d %s", displayAmount, displayCurrency)
	}
}

func TestAdminPermanentPaymentOrderDeletionRemovesUserHistoryAndEvents(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, _ := createEPayCheckoutForTest(t, fx)

	pendingReq := httptest.NewRequest(http.MethodDelete, "/api/admin/payment-orders/"+orderID, nil)
	pendingReq = paymentAPIRequest(pendingReq, nil, map[string]string{"id": orderID})
	pendingRec := httptest.NewRecorder()
	deletePaymentOrderAdmin(fx.d, pendingRec, pendingReq)
	if pendingRec.Code != http.StatusConflict || !strings.Contains(pendingRec.Body.String(), store.ErrPaymentOrderNotDeletable.Error()) {
		t.Fatalf("delete processing order = %d %s, want 409", pendingRec.Code, pendingRec.Body.String())
	}

	mustExec(t, fx.db, `UPDATE payment_orders SET status=? WHERE id=?`, store.PaymentOrderFailed, orderID)
	mustExec(t, fx.db,
		`INSERT INTO payment_events(id, provider, channel_id, event_id, order_id) VALUES(?,?,?,?,?)`,
		"pe_api_delete", payment.ProviderEPay, fx.channel.ID, "provider-api-delete", orderID,
	)
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/admin/payment-orders/"+orderID, nil)
	deleteReq = paymentAPIRequest(deleteReq, nil, map[string]string{"id": orderID})
	deleteRec := httptest.NewRecorder()
	deletePaymentOrderAdmin(fx.d, deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete terminal order = %d %s, want 200", deleteRec.Code, deleteRec.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/payments/orders/"+orderID, nil)
	detailReq = paymentAPIRequest(detailReq, fx.user, map[string]string{"id": orderID})
	detailRec := httptest.NewRecorder()
	getPaymentOrderHandler(fx.d, detailRec, detailReq)
	if detailRec.Code != http.StatusNotFound {
		t.Fatalf("deleted user order detail = %d, want 404; body=%s", detailRec.Code, detailRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/payments/orders", nil)
	listReq = paymentAPIRequest(listReq, fx.user, nil)
	listRec := httptest.NewRecorder()
	listPaymentOrdersForUserHandler(fx.d, listRec, listReq)
	var list struct {
		Orders []publicPaymentOrderListItem `json:"orders"`
		Total  int                          `json:"total"`
	}
	if listRec.Code != http.StatusOK || json.Unmarshal(listRec.Body.Bytes(), &list) != nil {
		t.Fatalf("list user orders after deletion = %d %s", listRec.Code, listRec.Body.String())
	}
	if list.Total != 0 || len(list.Orders) != 0 {
		t.Fatalf("user history after deletion = total %d orders %+v", list.Total, list.Orders)
	}
	var eventCount int
	if err := fx.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM payment_events WHERE order_id=?`, orderID).Scan(&eventCount); err != nil {
		t.Fatalf("count deleted order events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("deleted order still has %d events", eventCount)
	}
}

func TestAdminPermanentDeletionRequiresGatewayConfirmationForEPayManualClose(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, _ := createEPayCheckoutForTest(t, fx)
	closed, err := store.CancelPaymentOrderByAdmin(context.Background(), fx.db, orderID, "verified only locally")
	if err != nil {
		t.Fatalf("manually close EPay order: %v", err)
	}
	response := adminPaymentOrderJSON(*closed)
	if !response.CanDelete || !response.DeleteNeedsGatewayConfirmation {
		t.Fatalf("recoverable EPay manual-close deletion policy = %+v", response)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/payment-orders/"+orderID, nil)
	req = paymentAPIRequest(req, nil, map[string]string{"id": orderID})
	rec := httptest.NewRecorder()
	deletePaymentOrderAdmin(fx.d, rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.ErrPaymentOrderDeleteNeedsAck.Error()) {
		t.Fatalf("delete recoverable EPay manual-close order without confirmation = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if _, err := store.GetPaymentOrder(context.Background(), fx.db, orderID); err != nil {
		t.Fatalf("recoverable EPay manual-close order was removed: %v", err)
	}

	confirmedReq := httptest.NewRequest(http.MethodDelete, "/api/admin/payment-orders/"+orderID+"?gateway_final_acknowledged=true", nil)
	confirmedReq = paymentAPIRequest(confirmedReq, nil, map[string]string{"id": orderID})
	confirmedRec := httptest.NewRecorder()
	deletePaymentOrderAdmin(fx.d, confirmedRec, confirmedReq)
	if confirmedRec.Code != http.StatusOK {
		t.Fatalf("delete gateway-confirmed EPay manual-close order = %d %s, want 200", confirmedRec.Code, confirmedRec.Body.String())
	}
	if _, err := store.GetPaymentOrder(context.Background(), fx.db, orderID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("gateway-confirmed EPay manual-close order still exists: %v", err)
	}
}

func TestPaymentResumeAndDeleteRoutesRequireAuthentication(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	fx.d.Cache = cache.NewMemory()
	router := NewRouter(fx.d)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/payments/orders/po_test/resume"},
		{http.MethodDelete, "/api/admin/payment-orders/po_test"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401; body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestPaymentOrderDeleteRouteRequiresAdminRole(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	c := cache.NewMemory()
	fx.d.Cache = c
	fx.d.Auth = authsvc.New("payment-delete-admin-role-secret", time.Hour, 24*time.Hour, c)
	user, err := store.FindUserByID(context.Background(), fx.db, fx.user.ID)
	if err != nil {
		t.Fatalf("find payment user: %v", err)
	}
	token, _, err := fx.d.Auth.IssueAccess(user.ID, user.Role, user.TokenVer)
	if err != nil {
		t.Fatalf("issue payment user token: %v", err)
	}
	c.Set("seen:"+user.ID, "1", time.Minute)
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/admin/payment-orders/:id", requireAdmin(fx.d, deletePaymentOrderAdmin))
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/payment-orders/po_test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ordinary-user payment order delete status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}
