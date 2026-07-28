package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/payment"
	"aivory/server/internal/store"
)

const (
	testEPayMerchantID  = "merchant_api_test"
	testEPayMerchantKey = "epay_api_test_secret"
)

type paymentAPIFixture struct {
	db      *sql.DB
	d       Deps
	user    *store.User
	channel *store.PaymentChannel
	method  *store.PaymentMethod
	pkg     *store.CreditPackage
}

func newPaymentAPIFixture(t *testing.T) paymentAPIFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "payment-api.db"))
	t.Cleanup(func() { _ = db.Close() })

	if err := store.SetSetting(db, "settlement_currency", "USD"); err != nil {
		t.Fatalf("set settlement currency: %v", err)
	}
	user := &store.User{ID: "payment_user", Email: "buyer@example.test", GroupID: store.DefaultGroupID}
	mustExec(t, db,
		`INSERT INTO users(id,email,password_hash,group_id,credits_permanent) VALUES(?,?,?,?,?)`,
		user.ID, user.Email, "hash", user.GroupID, 25,
	)

	channelConfig, err := json.Marshal(payment.EPayConfig{
		GatewayURL:  "https://epay.example.test/gateway",
		MerchantID:  testEPayMerchantID,
		MerchantKey: testEPayMerchantKey,
		Currency:    "USD",
	})
	if err != nil {
		t.Fatalf("marshal EPay channel config: %v", err)
	}
	channel, err := store.CreatePaymentChannel(context.Background(), db, store.PaymentChannel{
		ID: "paych_epay_api", Name: "EPay API", Provider: payment.ProviderEPay,
		Config: channelConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create EPay channel: %v", err)
	}
	method, err := store.CreatePaymentMethod(context.Background(), db, store.PaymentMethod{
		ID: "paym_epay_api", ChannelID: channel.ID, Name: "Alipay", Type: payment.ProviderEPay,
		Icon: "wallet-cards", ProviderMethodConfig: json.RawMessage(`{"type":"alipay"}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create EPay method: %v", err)
	}
	pkg, err := store.CreateCreditPackage(context.Background(), db, store.CreditPackage{
		ID: "cp_payment_api", Name: "1,200 credits", Credits: 1200, PriceAmountMinor: 12345, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create credit package: %v", err)
	}
	return paymentAPIFixture{db: db, d: Deps{DB: db}, user: user, channel: channel, method: method, pkg: pkg}
}

func paymentAPIRequest(req *http.Request, user *store.User, pathValues map[string]string) *http.Request {
	ctx := req.Context()
	if user != nil {
		ctx = context.WithValue(ctx, userCtxKey{}, user)
	}
	if pathValues != nil {
		ctx = context.WithValue(ctx, pathCtxKey{}, pathValues)
	}
	return req.WithContext(ctx)
}

func TestPaymentCheckoutFailureDetailsPreservesWaffoCurrencyError(t *testing.T) {
	status, responseErr, failureCode, failureMessage := paymentCheckoutFailureDetails(
		fmt.Errorf("provider rejected checkout: %w", payment.ErrWaffoProductCurrencyUnsupported),
	)
	if status != http.StatusUnprocessableEntity ||
		!errors.Is(responseErr, payment.ErrWaffoProductCurrencyUnsupported) ||
		failureCode != payment.ErrWaffoProductCurrencyUnsupported.Error() || failureMessage == "" {
		t.Fatalf("Waffo checkout failure details = %d, %v, %q, %q", status, responseErr, failureCode, failureMessage)
	}

	status, responseErr, failureCode, failureMessage = paymentCheckoutFailureDetails(errors.New("provider unavailable"))
	if status != http.StatusBadGateway || responseErr.Error() != "payment_checkout_unavailable" ||
		failureCode != "provider_checkout_failed" || failureMessage == "" {
		t.Fatalf("generic checkout failure details = %d, %v, %q, %q", status, responseErr, failureCode, failureMessage)
	}
}

func createEPayCheckoutForTest(t *testing.T, fx paymentAPIFixture) (string, payment.CheckoutAction) {
	t.Helper()
	body := fmt.Sprintf(
		`{"payment_method_id":%q,"target_type":%q,"target_id":%q,"amount_minor":1,"currency":"JPY","credits":999999}`,
		fx.method.ID, store.PaymentProductCreditPackage, fx.pkg.ID,
	)
	req := httptest.NewRequest(http.MethodPost, "https://aivory.example.test/api/payments/checkout", strings.NewReader(body))
	req = paymentAPIRequest(req, fx.user, nil)
	rec := httptest.NewRecorder()
	createPaymentCheckoutHandler(fx.d, rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create checkout status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var response struct {
		OrderID string                 `json:"order_id"`
		Action  payment.CheckoutAction `json:"action"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode checkout response: %v (%s)", err, rec.Body.String())
	}
	if response.OrderID == "" {
		t.Fatalf("checkout response has no order id: %s", rec.Body.String())
	}
	return response.OrderID, response.Action
}

func signedEPayCallbackURL(channelID string, params map[string]string) string {
	params["sign_type"] = "MD5"
	params["sign"] = payment.EPaySign(params, testEPayMerchantKey)
	values := make(url.Values, len(params))
	for key, value := range params {
		values.Set(key, value)
	}
	return "/api/payments/webhooks/" + url.PathEscape(channelID) + "?" + values.Encode()
}

func serveEPayCallback(t *testing.T, fx paymentAPIFixture, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, signedEPayCallbackURL(fx.channel.ID, params), nil)
	req = paymentAPIRequest(req, nil, map[string]string{
		"provider": payment.ProviderEPay, "channelId": fx.channel.ID,
	})
	rec := httptest.NewRecorder()
	paymentWebhookHandler(fx.d, rec, req)
	return rec
}

func TestListPaymentMethodsPublicFiltersUnavailableAndInvalidMethods(t *testing.T) {
	fx := newPaymentAPIFixture(t)

	createChannelAndMethod := func(channelID, methodID, name, currency string, channelEnabled, methodEnabled bool, rawConfig json.RawMessage) {
		t.Helper()
		if rawConfig == nil {
			rawConfig, _ = json.Marshal(payment.EPayConfig{
				GatewayURL: "https://epay.example.test", MerchantID: "pid", MerchantKey: "key", Currency: currency,
			})
		}
		channel, err := store.CreatePaymentChannel(context.Background(), fx.db, store.PaymentChannel{
			ID: channelID, Name: name + " channel", Provider: payment.ProviderEPay,
			Config: rawConfig, Enabled: channelEnabled, SortOrder: 10,
		})
		if err != nil {
			t.Fatalf("create %s channel: %v", name, err)
		}
		if _, err := store.CreatePaymentMethod(context.Background(), fx.db, store.PaymentMethod{
			ID: methodID, ChannelID: channel.ID, Name: name, Type: payment.ProviderEPay,
			Icon: "landmark", ProviderMethodConfig: json.RawMessage(`{"type":"alipay"}`), Enabled: methodEnabled,
		}); err != nil {
			t.Fatalf("create %s method: %v", name, err)
		}
	}

	createChannelAndMethod("paych_currency_mismatch", "paym_currency_mismatch", "Wrong currency", "CNY", true, true, nil)
	validCrossCurrencyConfig, err := json.Marshal(payment.EPayConfig{
		GatewayURL: "https://epay.example.test", MerchantID: "pid-cross", MerchantKey: "key-cross", Currency: "CNY",
		ConversionRate: "7", ConversionRateBaseCurrency: "USD",
	})
	if err != nil {
		t.Fatalf("marshal valid cross-currency config: %v", err)
	}
	createChannelAndMethod("paych_currency_converted", "paym_currency_converted", "Converted currency", "CNY", true, true, validCrossCurrencyConfig)
	createChannelAndMethod("paych_disabled", "paym_disabled_channel", "Disabled channel", "USD", false, true, nil)
	createChannelAndMethod("paych_method_disabled", "paym_disabled", "Disabled method", "USD", true, false, nil)
	createChannelAndMethod(
		"paych_invalid", "paym_invalid", "Invalid config", "USD", true, true,
		json.RawMessage(`{"gateway_url":"javascript:alert(1)","merchant_id":"pid","merchant_key":"key","currency":"USD"}`),
	)
	if err := store.SetSetting(fx.db, "card_purchase_url", "https://cards.example.test/buy"); err != nil {
		t.Fatalf("set card purchase URL: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/payment-methods?target_type=credit_package", nil)
	listPaymentMethodsPublic(fx.d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list methods status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		Methods         []publicPaymentMethod `json:"methods"`
		CardPurchaseURL string                `json:"card_purchase_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode public payment methods: %v (%s)", err, rec.Body.String())
	}
	visibleIDs := map[string]bool{}
	for _, method := range response.Methods {
		visibleIDs[method.ID] = true
	}
	if len(response.Methods) != 2 || !visibleIDs[fx.method.ID] || !visibleIDs["paym_currency_converted"] ||
		visibleIDs["paym_currency_mismatch"] {
		t.Fatalf("public methods = %+v, want same-currency and configured cross-currency methods", response.Methods)
	}
	if response.CardPurchaseURL != "https://cards.example.test/buy" {
		t.Fatalf("card_purchase_url = %q", response.CardPurchaseURL)
	}

	if err := store.SetSetting(fx.db, "card_purchase_url", "javascript:alert(1)"); err != nil {
		t.Fatalf("set invalid card purchase URL: %v", err)
	}
	rec = httptest.NewRecorder()
	listPaymentMethodsPublic(fx.d, rec, httptest.NewRequest(http.MethodGet, "/api/payment-methods?target_type=user_group", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list methods with invalid card URL status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response with invalid card URL: %v", err)
	}
	if response.CardPurchaseURL != "" {
		t.Fatalf("unsafe card_purchase_url was exposed: %q", response.CardPurchaseURL)
	}
}

func TestTestPaymentChannelsAreRestrictedToAdministrators(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	testEnvironment := store.PaymentEnvironmentTest
	if _, err := store.UpdatePaymentChannel(context.Background(), fx.db, fx.channel.ID, store.PaymentChannelPatch{
		Environment: &testEnvironment,
	}); err != nil {
		t.Fatalf("mark channel as test: %v", err)
	}

	list := func(user *store.User) []publicPaymentMethod {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/payment-methods?target_type=credit_package", nil)
		req = paymentAPIRequest(req, user, nil)
		rec := httptest.NewRecorder()
		listPaymentMethodsPublic(fx.d, rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list test methods status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var response struct {
			Methods []publicPaymentMethod `json:"methods"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode test methods: %v", err)
		}
		return response.Methods
	}

	if methods := list(&store.User{ID: fx.user.ID, Role: "user"}); len(methods) != 0 {
		t.Fatalf("ordinary user saw test methods: %+v", methods)
	}
	if order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: fx.user.ID, PaymentMethodID: fx.method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
	}); order != nil || !errors.Is(err, store.ErrPaymentMethodUnavailable) {
		t.Fatalf("ordinary-user test checkout = %+v, %v; want nil/%v", order, err, store.ErrPaymentMethodUnavailable)
	}

	mustExec(t, fx.db, `UPDATE users SET role='admin' WHERE id=?`, fx.user.ID)
	if methods := list(&store.User{ID: fx.user.ID, Role: "admin"}); len(methods) != 1 || methods[0].ID != fx.method.ID {
		t.Fatalf("administrator test methods = %+v, want %q", methods, fx.method.ID)
	}
	if order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: fx.user.ID, PaymentMethodID: fx.method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
	}); err != nil || order == nil || order.Environment != store.PaymentEnvironmentTest {
		t.Fatalf("administrator test checkout = %+v, %v", order, err)
	}
}

func TestListPaymentMethodsPublicIgnoresLegacyPurchaseLinks(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	for key, value := range map[string]string{
		"card_purchase_url": "",
		"credit_buy_url":    "https://legacy.example.test/credits",
		"group_buy_url":     "https://legacy.example.test/groups",
	} {
		if err := store.SetSetting(fx.db, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	requestCardURL := func(targetType string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		requestURL := "/api/payment-methods?target_type=" + url.QueryEscape(targetType)
		listPaymentMethodsPublic(fx.d, rec, httptest.NewRequest(http.MethodGet, requestURL, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list methods for %s status = %d; body=%s", targetType, rec.Code, rec.Body.String())
		}
		var response struct {
			CardPurchaseURL string `json:"card_purchase_url"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode methods for %s: %v", targetType, err)
		}
		return response.CardPurchaseURL
	}

	if got := requestCardURL(store.PaymentProductCreditPackage); got != "" {
		t.Fatalf("legacy credit-package URL leaked into payment methods: %q", got)
	}
	if got := requestCardURL(store.PaymentProductUserGroup); got != "" {
		t.Fatalf("legacy user-group URL leaked into payment methods: %q", got)
	}

	if err := store.SetSetting(fx.db, "card_purchase_url", "/redeem-card"); err != nil {
		t.Fatalf("set global card purchase URL: %v", err)
	}
	for _, targetType := range []string{store.PaymentProductCreditPackage, store.PaymentProductUserGroup} {
		if got := requestCardURL(targetType); got != "/redeem-card" {
			t.Fatalf("global card URL for %s = %q", targetType, got)
		}
	}
}

func TestGetPaymentOrderOnlyReturnsAuthenticatedUsersOrder(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
		UserID: fx.user.ID, PaymentMethodID: fx.method.ID,
		ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
	})
	if err != nil {
		t.Fatalf("create payment order: %v", err)
	}

	ownerRec := httptest.NewRecorder()
	ownerReq := httptest.NewRequest(http.MethodGet, "/api/payments/orders/"+order.ID, nil)
	ownerReq = paymentAPIRequest(ownerReq, fx.user, map[string]string{"id": order.ID})
	getPaymentOrderHandler(fx.d, ownerRec, ownerReq)
	if ownerRec.Code != http.StatusOK {
		t.Fatalf("owner lookup status = %d, want %d; body=%s", ownerRec.Code, http.StatusOK, ownerRec.Body.String())
	}
	var visible publicPaymentOrder
	if err := json.Unmarshal(ownerRec.Body.Bytes(), &visible); err != nil {
		t.Fatalf("decode owner order: %v", err)
	}
	if visible.ID != order.ID || visible.AmountMinor != fx.pkg.PriceAmountMinor || visible.Currency != "USD" ||
		visible.Provider != payment.ProviderEPay || visible.MethodName != fx.method.Name || visible.MethodType != fx.method.Type {
		t.Fatalf("owner order response = %+v", visible)
	}

	otherRec := httptest.NewRecorder()
	otherReq := httptest.NewRequest(http.MethodGet, "/api/payments/orders/"+order.ID, nil)
	otherReq = paymentAPIRequest(otherReq, &store.User{ID: "another_user"}, map[string]string{"id": order.ID})
	getPaymentOrderHandler(fx.d, otherRec, otherReq)
	if otherRec.Code != http.StatusNotFound {
		t.Fatalf("other user lookup status = %d, want %d; body=%s", otherRec.Code, http.StatusNotFound, otherRec.Body.String())
	}
}

func TestListPaymentOrdersForUserIsolatedPaginatedOrderedAndSafe(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	otherUser := &store.User{ID: "payment_other_user", Email: "other-buyer@example.test", GroupID: store.DefaultGroupID}
	mustExec(t, fx.db,
		`INSERT INTO users(id,email,password_hash,group_id) VALUES(?,?,?,?)`,
		otherUser.ID, otherUser.Email, "hash", otherUser.GroupID,
	)
	createOrder := func(userID string) *store.PaymentOrder {
		t.Helper()
		order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
			UserID: userID, PaymentMethodID: fx.method.ID,
			ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
		})
		if err != nil {
			t.Fatalf("create order for %s: %v", userID, err)
		}
		return order
	}

	pending := createOrder(fx.user.ID)
	processing := createOrder(fx.user.ID)
	paid := createOrder(fx.user.ID)
	cancelled := createOrder(fx.user.ID)
	other := createOrder(otherUser.ID)
	mustExec(t, fx.db, `UPDATE payment_orders SET status=?, created_at=?, updated_at=? WHERE id=?`, store.PaymentOrderPending, 100, 100, pending.ID)
	mustExec(t, fx.db, `UPDATE payment_orders SET status=?, created_at=?, updated_at=? WHERE id=?`, store.PaymentOrderProcessing, 150, 150, processing.ID)
	mustExec(t, fx.db,
		`UPDATE payment_orders SET status=?, paid_at=?, created_at=?, updated_at=?, method_config=?, failure_message=? WHERE id=?`,
		store.PaymentOrderFulfilled, 201, 200, 201, `{"merchant_key":"do-not-leak"}`, "private decline detail", paid.ID,
	)
	mustExec(t, fx.db, `UPDATE payment_orders SET status=?, created_at=?, updated_at=? WHERE id=?`, store.PaymentOrderCancelled, 300, 300, cancelled.ID)
	mustExec(t, fx.db, `UPDATE payment_orders SET status=?, created_at=?, updated_at=? WHERE id=?`, store.PaymentOrderFulfilled, 400, 400, other.ID)

	requestPage := func(user *store.User, query string) (*httptest.ResponseRecorder, struct {
		Orders []publicPaymentOrderListItem `json:"orders"`
		Total  int                          `json:"total"`
		Limit  int                          `json:"limit"`
		Offset int                          `json:"offset"`
	}) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/payments/orders"+query, nil)
		req = paymentAPIRequest(req, user, nil)
		rec := httptest.NewRecorder()
		listPaymentOrdersForUserHandler(fx.d, rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list payment orders status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var response struct {
			Orders []publicPaymentOrderListItem `json:"orders"`
			Total  int                          `json:"total"`
			Limit  int                          `json:"limit"`
			Offset int                          `json:"offset"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode payment-order list: %v (%s)", err, rec.Body.String())
		}
		return rec, response
	}

	firstRec, first := requestPage(fx.user, "?limit=2&offset=0")
	if first.Total != 4 || first.Limit != 2 || first.Offset != 0 || len(first.Orders) != 2 {
		t.Fatalf("first page metadata = total %d limit %d offset %d orders %d", first.Total, first.Limit, first.Offset, len(first.Orders))
	}
	if first.Orders[0].ID != cancelled.ID || first.Orders[0].Status != "expired" ||
		first.Orders[1].ID != paid.ID || first.Orders[1].Status != "paid" || first.Orders[1].PaidAt != 201 {
		t.Fatalf("first page order/status mapping = %+v", first.Orders)
	}
	_, second := requestPage(fx.user, "?limit=2&offset=2")
	if len(second.Orders) != 2 || second.Orders[0].ID != processing.ID || second.Orders[0].Status != store.PaymentOrderProcessing ||
		second.Orders[1].ID != pending.ID || second.Orders[1].Status != store.PaymentOrderPending {
		t.Fatalf("second page order/status mapping = %+v", second.Orders)
	}
	_, otherResponse := requestPage(otherUser, "")
	if otherResponse.Total != 1 || otherResponse.Limit != 10 || otherResponse.Offset != 0 ||
		len(otherResponse.Orders) != 1 || otherResponse.Orders[0].ID != other.ID {
		t.Fatalf("other user's isolated list = %+v", otherResponse)
	}

	var raw struct {
		Orders []map[string]json.RawMessage `json:"orders"`
		Total  json.RawMessage              `json:"total"`
		Limit  json.RawMessage              `json:"limit"`
		Offset json.RawMessage              `json:"offset"`
	}
	if err := json.Unmarshal(firstRec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw payment-order list: %v", err)
	}
	expectedFields := map[string]bool{
		"id": true, "status": true, "provider": true, "method_name": true, "method_type": true,
		"target_type": true, "target_name": true, "amount_minor": true, "currency": true,
		"billing_cycle": true, "created_at": true, "paid_at": true,
	}
	for index, item := range raw.Orders {
		if len(item) != len(expectedFields) {
			t.Fatalf("order %d fields = %v, want exactly %v", index, item, expectedFields)
		}
		for field := range expectedFields {
			if _, ok := item[field]; !ok {
				t.Fatalf("order %d missing safe field %q: %v", index, field, item)
			}
		}
	}
	responseBody := firstRec.Body.String()
	for _, forbidden := range []string{
		"method_config", "channel_id", "provider_order_id", "failure_message", "failure_reason",
		"user_id", "user_email", "merchant_key", "do-not-leak", "private decline detail",
		fx.user.Email, otherUser.Email,
	} {
		if strings.Contains(responseBody, forbidden) {
			t.Fatalf("payment-order list leaked %q: %s", forbidden, responseBody)
		}
	}
}

func TestListPaymentOrdersForUserRejectsInvalidPagination(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	for _, query := range []string{
		"?limit=0", "?limit=-1", "?limit=51", "?limit=invalid",
		"?offset=-1", "?offset=invalid", "?limit=10&offset=999999999999999999999999",
	} {
		t.Run(query, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/payments/orders"+query, nil)
			req = paymentAPIRequest(req, fx.user, nil)
			rec := httptest.NewRecorder()
			listPaymentOrdersForUserHandler(fx.d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid pagination %q status = %d, want %d; body=%s", query, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestPaymentOrderListRouteRequiresAuthentication(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	rec := httptest.NewRecorder()
	NewRouter(fx.d).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/payments/orders", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated payment-order list status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestEPayCheckoutUsesDatabaseProductSnapshot(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, action := createEPayCheckoutForTest(t, fx)

	if action.Type != payment.ActionFormPost || action.URL != "https://epay.example.test/gateway/submit.php" {
		t.Fatalf("checkout action = %+v", action)
	}
	if action.Fields["out_trade_no"] != orderID || action.Fields["money"] != "123.45" ||
		action.Fields["name"] != fx.pkg.Name || action.Fields["type"] != "alipay" {
		t.Fatalf("EPay checkout fields = %+v", action.Fields)
	}
	if action.Fields["notify_url"] != "https://aivory.example.test/api/payments/webhooks/"+fx.channel.ID {
		t.Fatalf("EPay notify URL = %q", action.Fields["notify_url"])
	}
	if got, want := action.Fields["sign"], payment.EPaySign(action.Fields, testEPayMerchantKey); got != want {
		t.Fatalf("EPay form signature = %q, want %q", got, want)
	}

	order, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil {
		t.Fatalf("get checkout order: %v", err)
	}
	if order.AmountMinor != fx.pkg.PriceAmountMinor || order.Currency != "USD" || order.Credits != fx.pkg.Credits ||
		order.ProductName != fx.pkg.Name || order.Status != store.PaymentOrderProcessing {
		t.Fatalf("persisted checkout snapshot = %+v", order)
	}
}

func TestEPayCrossCurrencyCheckoutWebhookAndHistoryUseOrderSnapshots(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	mustExec(t, fx.db, `UPDATE credit_packages SET price_amount_minor=? WHERE id=?`, 4000, fx.pkg.ID)
	fx.pkg.PriceAmountMinor = 4000

	channelConfig, err := json.Marshal(payment.EPayConfig{
		GatewayURL: "https://epay.example.test/gateway", MerchantID: testEPayMerchantID,
		MerchantKey: testEPayMerchantKey, Currency: "CNY",
		ConversionRate: "7", ConversionRateBaseCurrency: "USD",
	})
	if err != nil {
		t.Fatalf("marshal cross-currency EPay config: %v", err)
	}
	rawChannelConfig := json.RawMessage(channelConfig)
	if _, err := store.UpdatePaymentChannel(context.Background(), fx.db, fx.channel.ID, store.PaymentChannelPatch{Config: &rawChannelConfig}); err != nil {
		t.Fatalf("configure cross-currency EPay channel: %v", err)
	}

	orderID, action := createEPayCheckoutForTest(t, fx)
	if got := action.Fields["money"]; got != "280.00" {
		t.Fatalf("cross-currency checkout money = %q, want 280.00", got)
	}
	order, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil {
		t.Fatalf("get cross-currency order: %v", err)
	}
	if order.AmountMinor != 4000 || order.Currency != "USD" ||
		order.ProviderAmountMinor != 28000 || order.ProviderCurrency != "CNY" || order.ConversionRate != "7" {
		t.Fatalf("cross-currency order snapshot = %+v", order)
	}

	// Simulate a later channel-rate edit outside the guarded admin API. Existing
	// orders must still validate against the immutable provider-side snapshot.
	changedConfig, err := json.Marshal(payment.EPayConfig{
		GatewayURL: "https://epay.example.test/gateway", MerchantID: testEPayMerchantID,
		MerchantKey: testEPayMerchantKey, Currency: "CNY",
		ConversionRate: "8", ConversionRateBaseCurrency: "USD",
	})
	if err != nil {
		t.Fatalf("marshal changed EPay rate: %v", err)
	}
	mustExec(t, fx.db, `UPDATE payment_channels SET config=? WHERE id=?`, string(changedConfig), fx.channel.ID)

	bad := serveEPayCallback(t, fx, map[string]string{
		"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
		"trade_no": "epay_cross_currency_bad_amount", "trade_status": "TRADE_SUCCESS", "money": "279.99",
	})
	assertRejectedEPayFulfillment(t, fx, orderID, bad)

	paid := serveEPayCallback(t, fx, map[string]string{
		"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
		"trade_no": "epay_cross_currency_paid", "trade_status": "TRADE_SUCCESS", "money": "280.00",
	})
	if paid.Code != http.StatusOK || strings.TrimSpace(paid.Body.String()) != "success" {
		t.Fatalf("cross-currency callback = status %d body %q", paid.Code, paid.Body.String())
	}
	order, err = store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil {
		t.Fatalf("get fulfilled cross-currency order: %v", err)
	}
	if order.Status != store.PaymentOrderFulfilled || order.PaidAmountMinor != 4000 || order.AmountMinor != 4000 ||
		order.Currency != "USD" || order.ProviderAmountMinor != 28000 || order.ProviderCurrency != "CNY" ||
		order.ConversionRate != "7" {
		t.Fatalf("fulfilled cross-currency order = %+v", order)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/payments/orders", nil)
	req = paymentAPIRequest(req, fx.user, nil)
	rec := httptest.NewRecorder()
	listPaymentOrdersForUserHandler(fx.d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list cross-currency payment history status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var history struct {
		Orders []publicPaymentOrderListItem `json:"orders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode cross-currency payment history: %v", err)
	}
	if len(history.Orders) != 1 || history.Orders[0].ID != orderID || history.Orders[0].AmountMinor != 4000 ||
		history.Orders[0].Currency != "USD" || history.Orders[0].Status != "paid" {
		t.Fatalf("cross-currency payment history = %+v", history.Orders)
	}
}

func TestPaymentCheckoutUsesAllowedFrontendOriginForReturnURL(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	fx.d.Config.AllowedOrigins = []string{"https://app.example.test"}
	body := fmt.Sprintf(
		`{"payment_method_id":%q,"target_type":%q,"target_id":%q}`,
		fx.method.ID, store.PaymentProductCreditPackage, fx.pkg.ID,
	)
	req := httptest.NewRequest(http.MethodPost, "https://api.example.test/api/payments/checkout", strings.NewReader(body))
	req.Header.Set("Origin", "https://app.example.test")
	req = paymentAPIRequest(req, fx.user, nil)
	rec := httptest.NewRecorder()
	createPaymentCheckoutHandler(fx.d, rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create split-origin checkout status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		OrderID string                 `json:"order_id"`
		Action  payment.CheckoutAction `json:"action"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode split-origin checkout: %v", err)
	}
	returnURL, err := url.Parse(response.Action.Fields["return_url"])
	if err != nil {
		t.Fatalf("parse EPay return URL: %v", err)
	}
	if returnURL.Scheme != "https" || returnURL.Host != "app.example.test" || returnURL.Path != "/subscription" ||
		returnURL.Query().Get("order") != response.OrderID || returnURL.Query().Get("payment") != "return" {
		t.Fatalf("split-origin return URL = %q", returnURL.String())
	}
	if got := response.Action.Fields["notify_url"]; got != "https://api.example.test/api/payments/webhooks/"+fx.channel.ID {
		t.Fatalf("split-origin notify URL = %q", got)
	}
}

func TestPaymentReturnBaseURLRejectsUntrustedOrMalformedOrigin(t *testing.T) {
	d := Deps{}
	d.Config.AllowedOrigins = []string{"https://app.example.test", "https://configured.example.test/path"}
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{name: "allowed frontend", origin: "https://app.example.test", want: "https://app.example.test"},
		{name: "untrusted frontend", origin: "https://evil.example.test", want: "https://api.example.test"},
		{name: "configured value with path is not an origin", origin: "https://configured.example.test/path", want: "https://api.example.test"},
		{name: "credentialed origin", origin: "https://user@example.test", want: "https://api.example.test"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://api.example.test/api/payments/checkout", nil)
			req.Header.Set("Origin", tc.origin)
			if got := paymentReturnBaseURL(d, req); got != tc.want {
				t.Fatalf("paymentReturnBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPaymentDetachedContextSurvivesClientCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/payments/checkout", nil).WithContext(parent)
	cancelParent()
	ctx, cancel := paymentDetachedContext(req, time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatalf("detached payment context ended with client context: %v", ctx.Err())
	default:
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("detached payment context has no bounded deadline")
	}
}

func TestPaymentFormValuesMergesQueryAndPostAndRejectsDuplicates(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		body      string
		want      map[string]string
		wantError bool
	}{
		{name: "get query", method: http.MethodGet, target: "/callback?a=1&b=2", want: map[string]string{"a": "1", "b": "2"}},
		{name: "post query and form", method: http.MethodPost, target: "/callback?a=1", body: "b=2", want: map[string]string{"a": "1", "b": "2"}},
		{name: "duplicate across query and form", method: http.MethodPost, target: "/callback?a=1", body: "a=1", wantError: true},
		{name: "duplicate query", method: http.MethodGet, target: "/callback?a=1&a=2", wantError: true},
		{name: "duplicate form", method: http.MethodPost, target: "/callback", body: "a=1&a=2", wantError: true},
		{name: "empty", method: http.MethodPost, target: "/callback", wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			if tc.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			got, err := paymentFormValues(httptest.NewRecorder(), req)
			if tc.wantError {
				if err == nil {
					t.Fatalf("paymentFormValues() = %#v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("paymentFormValues() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("paymentFormValues() = %#v, want %#v", got, tc.want)
			}
			for key, want := range tc.want {
				if got[key] != want {
					t.Fatalf("paymentFormValues()[%q] = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestEPayWebhookFulfillsCreditOrderExactlyOnce(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, _ := createEPayCheckoutForTest(t, fx)
	params := map[string]string{
		"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
		"trade_no": "epay_trade_success", "trade_status": "TRADE_SUCCESS", "money": "123.45",
	}

	for attempt := 1; attempt <= 2; attempt++ {
		rec := serveEPayCallback(t, fx, cloneStringMap(params))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
			t.Fatalf("callback attempt %d = status %d body %q", attempt, rec.Code, rec.Body.String())
		}
	}

	var credits float64
	if err := fx.db.QueryRow(`SELECT credits_permanent FROM users WHERE id=?`, fx.user.ID).Scan(&credits); err != nil {
		t.Fatalf("query permanent credits: %v", err)
	}
	if credits != 25+fx.pkg.Credits {
		t.Fatalf("permanent credits = %v, want %v after duplicate callback", credits, 25+fx.pkg.Credits)
	}
	order, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil {
		t.Fatalf("get fulfilled order: %v", err)
	}
	if order.Status != store.PaymentOrderFulfilled || order.ProviderOrderID != "epay_trade_success" ||
		order.PaidAt == 0 || order.FulfilledAt == 0 {
		t.Fatalf("fulfilled order = %+v", order)
	}
	var eventCount int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=?`, orderID).Scan(&eventCount); err != nil {
		t.Fatalf("count payment events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("payment event count = %d, want 1 for duplicate callback", eventCount)
	}
}

func TestEPayWebhookFulfillsManuallyClosedOrderExactlyOnce(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, _ := createEPayCheckoutForTest(t, fx)
	closed, err := store.CancelPaymentOrderByAdmin(context.Background(), fx.db, orderID, "")
	if err != nil || closed.Status != store.PaymentOrderCancelled || closed.FailureCode != "admin_manual_close" {
		t.Fatalf("manually close EPay order = %+v, %v", closed, err)
	}
	params := map[string]string{
		"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
		"trade_no": "epay_trade_after_manual_close", "trade_status": "TRADE_SUCCESS", "money": "123.45",
	}

	for attempt := 1; attempt <= 2; attempt++ {
		rec := serveEPayCallback(t, fx, cloneStringMap(params))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
			t.Fatalf("callback after manual close attempt %d = status %d body %q", attempt, rec.Code, rec.Body.String())
		}
	}

	var credits float64
	if err := fx.db.QueryRow(`SELECT credits_permanent FROM users WHERE id=?`, fx.user.ID).Scan(&credits); err != nil {
		t.Fatalf("query permanent credits after manual-close recovery: %v", err)
	}
	if credits != 25+fx.pkg.Credits {
		t.Fatalf("permanent credits after manual-close recovery = %v, want %v", credits, 25+fx.pkg.Credits)
	}
	fulfilled, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil {
		t.Fatalf("get recovered EPay order: %v", err)
	}
	if fulfilled.Status != store.PaymentOrderFulfilled || fulfilled.ProviderOrderID != "epay_trade_after_manual_close" ||
		fulfilled.FailureCode != "" || fulfilled.FailureMessage != "" || fulfilled.PaidAt == 0 || fulfilled.FulfilledAt == 0 {
		t.Fatalf("recovered EPay order = %+v", fulfilled)
	}
	events, err := store.ListPaymentEventsForOrder(context.Background(), fx.db, orderID)
	if err != nil || len(events) != 2 {
		t.Fatalf("manual-close webhook events = %+v, %v", events, err)
	}
	eventTypes := make(map[string]bool, len(events))
	for _, paymentEvent := range events {
		eventTypes[paymentEvent.EventType] = paymentEvent.ProcessedAt > 0
	}
	if !eventTypes["admin.manual_close"] || !eventTypes["payment_notification"] {
		t.Fatalf("manual-close webhook event types = %+v", events)
	}
}

func TestEPayWebhookAcceptsExistingOrderAfterChannelIsDisabled(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	orderID, _ := createEPayCheckoutForTest(t, fx)
	disabled := false
	if _, err := store.UpdatePaymentChannel(context.Background(), fx.db, fx.channel.ID, store.PaymentChannelPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable payment channel: %v", err)
	}

	rec := serveEPayCallback(t, fx, map[string]string{
		"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
		"trade_no": "epay_disabled_channel_trade", "trade_status": "TRADE_SUCCESS", "money": "123.45",
	})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
		t.Fatalf("disabled-channel callback = status %d body %q", rec.Code, rec.Body.String())
	}
	order, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil {
		t.Fatalf("get fulfilled order: %v", err)
	}
	if order.Status != store.PaymentOrderFulfilled {
		t.Fatalf("disabled-channel callback order status = %q", order.Status)
	}
}

func TestEPayWebhookRejectsAmountAndIdentityMismatchAndUsesOrderCurrencySnapshot(t *testing.T) {
	t.Run("amount", func(t *testing.T) {
		fx := newPaymentAPIFixture(t)
		orderID, _ := createEPayCheckoutForTest(t, fx)
		rec := serveEPayCallback(t, fx, map[string]string{
			"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
			"trade_no": "epay_trade_bad_amount", "trade_status": "TRADE_SUCCESS", "money": "0.01",
		})
		assertRejectedEPayFulfillment(t, fx, orderID, rec)
	})

	t.Run("channel currency changed after checkout", func(t *testing.T) {
		fx := newPaymentAPIFixture(t)
		orderID, _ := createEPayCheckoutForTest(t, fx)
		changedConfig, err := json.Marshal(payment.EPayConfig{
			GatewayURL:  "https://epay.example.test/gateway",
			MerchantID:  testEPayMerchantID,
			MerchantKey: testEPayMerchantKey,
			Currency:    "EUR",
		})
		if err != nil {
			t.Fatalf("marshal changed channel config: %v", err)
		}
		mustExec(t, fx.db, `UPDATE payment_channels SET config=? WHERE id=?`, string(changedConfig), fx.channel.ID)
		rec := serveEPayCallback(t, fx, map[string]string{
			"pid": testEPayMerchantID, "type": "alipay", "out_trade_no": orderID,
			"trade_no": "epay_trade_snapshot_currency", "trade_status": "TRADE_SUCCESS", "money": "123.45",
		})
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
			t.Fatalf("snapshot-currency callback = status %d body %q", rec.Code, rec.Body.String())
		}
		order, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
		if err != nil {
			t.Fatalf("get snapshot-currency order: %v", err)
		}
		if order.Status != store.PaymentOrderFulfilled || order.Currency != "USD" || order.ProviderCurrency != "USD" {
			t.Fatalf("snapshot-currency order = %+v", order)
		}
	})

	t.Run("merchant", func(t *testing.T) {
		fx := newPaymentAPIFixture(t)
		orderID, _ := createEPayCheckoutForTest(t, fx)
		rec := serveEPayCallback(t, fx, map[string]string{
			"pid": "another-merchant", "type": "alipay", "out_trade_no": orderID,
			"trade_no": "epay_trade_wrong_merchant", "trade_status": "TRADE_SUCCESS", "money": "123.45",
		})
		assertRejectedEPayFulfillment(t, fx, orderID, rec)
	})

	t.Run("payment method", func(t *testing.T) {
		fx := newPaymentAPIFixture(t)
		orderID, _ := createEPayCheckoutForTest(t, fx)
		rec := serveEPayCallback(t, fx, map[string]string{
			"pid": testEPayMerchantID, "type": "wxpay", "out_trade_no": orderID,
			"trade_no": "epay_trade_wrong_method", "trade_status": "TRADE_SUCCESS", "money": "123.45",
		})
		assertRejectedEPayFulfillment(t, fx, orderID, rec)
	})
}

func TestPaymentURLValidation(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"https://payments.example.test/buy", "http://localhost:8080/cards", "/buy-cards", "/buy-cards?from=subscription",
	} {
		if !validPaymentHTTPURL(value) {
			t.Errorf("validPaymentHTTPURL(%q) = false", value)
		}
	}
	for _, value := range []string{
		"", "//evil.example", `/\\evil.example`, "relative/path", "javascript:alert(1)",
		"https://user:password@payments.example.test/buy", "https://payments.example.test/\nheader",
	} {
		if validPaymentHTTPURL(value) {
			t.Errorf("validPaymentHTTPURL(%q) = true", value)
		}
	}
	if validAbsolutePaymentHTTPURL("/relative-checkout") {
		t.Error("provider checkout accepted a relative URL")
	}
}

func TestValidateProviderEventRequiresWaffoBuyerIdentity(t *testing.T) {
	order := store.PaymentOrder{
		Provider: payment.ProviderWaffo, ChannelID: "paych_waffo", UserID: "user_123",
		AmountMinor: 1999, Currency: "USD", MethodConfig: json.RawMessage(`{}`),
	}
	event := payment.ProviderEvent{
		AmountMinor: 1999, Currency: "USD", MethodType: "card",
		UserID: payment.WaffoBuyerIdentity(order.UserID),
	}
	if err := validateProviderEvent(order, payment.ProviderWaffo, order.ChannelID, event); err != nil {
		t.Fatalf("valid Waffo buyer identity was rejected: %v", err)
	}

	for _, identity := range []string{"", payment.WaffoBuyerIdentity("another_user")} {
		event.UserID = identity
		if err := validateProviderEvent(order, payment.ProviderWaffo, order.ChannelID, event); err == nil {
			t.Fatalf("invalid Waffo buyer identity %q was accepted", identity)
		}
	}
}

func TestMarkPaymentCheckoutStartedHandlesFulfilledWebhookRace(t *testing.T) {
	tests := []struct {
		name                    string
		checkoutProviderOrderID string
		wantErr                 error
	}{
		{name: "provider ID omitted by checkout action"},
		{name: "matching provider ID", checkoutProviderOrderID: "epay_fast_trade"},
		{name: "conflicting provider ID", checkoutProviderOrderID: "different_trade", wantErr: store.ErrPaymentProviderOrderMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPaymentAPIFixture(t)
			order, err := store.CreatePaymentOrder(context.Background(), fx.db, store.PaymentOrderCreateInput{
				UserID: fx.user.ID, PaymentMethodID: fx.method.ID,
				ProductType: store.PaymentProductCreditPackage, ProductID: fx.pkg.ID,
			})
			if err != nil {
				t.Fatalf("create payment order: %v", err)
			}
			amount := order.AmountMinor
			if _, err := store.FulfillPaymentOrder(context.Background(), fx.db, store.PaymentFulfillmentInput{
				PaymentEventInput: store.PaymentEventInput{
					Provider: order.Provider, ChannelID: order.ChannelID, EventID: "epay_fast_event",
					OrderID: order.ID, EventType: "payment_notification",
				},
				ProviderOrderID: "epay_fast_trade", AmountMinor: &amount, Currency: order.Currency,
			}); err != nil {
				t.Fatalf("fulfill raced payment order: %v", err)
			}

			err = markPaymentCheckoutStarted(context.Background(), fx.d, order.ID, tc.checkoutProviderOrderID, "", 0)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("fulfilled checkout race returned error: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("fulfilled checkout race error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func assertRejectedEPayFulfillment(t *testing.T, fx paymentAPIFixture, orderID string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "fail" {
		t.Fatalf("rejected EPay callback = status %d body %q", rec.Code, rec.Body.String())
	}
	var credits float64
	if err := fx.db.QueryRow(`SELECT credits_permanent FROM users WHERE id=?`, fx.user.ID).Scan(&credits); err != nil {
		t.Fatalf("query permanent credits: %v", err)
	}
	if credits != 25 {
		t.Fatalf("permanent credits changed after rejected callback: %v", credits)
	}
	order, err := store.GetPaymentOrder(context.Background(), fx.db, orderID)
	if err != nil {
		t.Fatalf("get rejected callback order: %v", err)
	}
	if order.Status != store.PaymentOrderProcessing {
		t.Fatalf("order status after rejected callback = %q, want %q", order.Status, store.PaymentOrderProcessing)
	}
	var eventCount int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=?`, orderID).Scan(&eventCount); err != nil {
		t.Fatalf("count rejected callback events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("rejected callback persisted %d events, want 0", eventCount)
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
