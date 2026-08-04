package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	paymentcore "aivory/server/internal/payment"
)

func openPaymentsTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "payments.db"))
	if err != nil {
		t.Fatalf("open payment test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate payment test db: %v", err)
	}
	return db, context.Background()
}

func createPaymentsTestMethod(t *testing.T, ctx context.Context, db *sql.DB) (*PaymentChannel, *PaymentMethod) {
	t.Helper()
	channel, err := CreatePaymentChannel(ctx, db, PaymentChannel{
		Name: "Stripe primary", Provider: "Stripe", Config: json.RawMessage(`{"secret":"sk_test"}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create payment channel: %v", err)
	}
	method, err := CreatePaymentMethod(ctx, db, PaymentMethod{
		ChannelID:            channel.ID,
		Name:                 "Card",
		Type:                 "stripe",
		Icon:                 "credit-card",
		ProviderMethodConfig: json.RawMessage(`{}`),
		Enabled:              true,
	})
	if err != nil {
		t.Fatalf("create payment method: %v", err)
	}
	return channel, method
}

func createPaymentsTestUser(t *testing.T, db *sql.DB, id, email string) {
	t.Helper()
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES(?,?,?,?)`, id, email, "hash", "user")
}

func TestPaymentChannelAndMethodCRUD(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	channelConfig := json.RawMessage("  {\"api_key\":\"secret\",\"nested\": {\"value\":1}}  ")
	channel, err := CreatePaymentChannel(ctx, db, PaymentChannel{
		Name: "  EPay Main  ", Provider: " EPay ", Config: channelConfig, Enabled: true, SortOrder: 4,
	})
	if err != nil {
		t.Fatalf("CreatePaymentChannel: %v", err)
	}
	if channel.Name != "EPay Main" || channel.Provider != "epay" || string(channel.Config) != string(channelConfig) {
		t.Fatalf("channel normalization/config mismatch: %+v config=%q", channel, channel.Config)
	}

	methodConfig := json.RawMessage(" {\"pay_type\":\"alipay\", \"pay_name\":\"ALIPAY\"} ")
	method, err := CreatePaymentMethod(ctx, db, PaymentMethod{
		ChannelID:            channel.ID,
		Name:                 "  Alipay  ",
		Type:                 " EPay ",
		Icon:                 "  wallet-cards  ",
		ProviderMethodConfig: methodConfig,
		Enabled:              true,
		SortOrder:            7,
	})
	if err != nil {
		t.Fatalf("CreatePaymentMethod: %v", err)
	}
	if method.Name != "Alipay" || method.Type != "epay" || method.Icon != "wallet-cards" ||
		string(method.ProviderMethodConfig) != string(methodConfig) {
		t.Fatalf("method normalization/config mismatch: %+v config=%q", method, method.ProviderMethodConfig)
	}

	second, err := CreatePaymentMethod(ctx, db, PaymentMethod{
		ChannelID: channel.ID, Name: "WeChat Pay", Type: "epay", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create second method: %v", err)
	}
	if err := ReorderPaymentMethods(ctx, db, []string{second.ID, method.ID}); err != nil {
		t.Fatalf("ReorderPaymentMethods: %v", err)
	}
	methods, err := ListPaymentMethods(ctx, db, channel.ID)
	if err != nil {
		t.Fatalf("ListPaymentMethods: %v", err)
	}
	if len(methods) != 2 || methods[0].ID != second.ID || methods[1].ID != method.ID {
		t.Fatalf("reordered methods = %+v", methods)
	}
	if count, err := CountPaymentMethodsByChannel(ctx, db, channel.ID); err != nil || count != 2 {
		t.Fatalf("CountPaymentMethodsByChannel = %d, %v", count, err)
	}

	visible, err := ListEnabledPaymentMethods(ctx, db)
	if err != nil {
		t.Fatalf("ListEnabledPaymentMethods: %v", err)
	}
	if len(visible) != 2 || visible[1].Icon != "wallet-cards" || visible[1].Provider != "epay" {
		t.Fatalf("enabled methods = %+v", visible)
	}
	if err := DeletePaymentChannel(ctx, db, channel.ID); !errors.Is(err, ErrPaymentChannelHasMethods) {
		t.Fatalf("delete channel with methods error = %v", err)
	}

	disabled := false
	if _, err := UpdatePaymentChannel(ctx, db, channel.ID, PaymentChannelPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable channel: %v", err)
	}
	visible, err = ListEnabledPaymentMethods(ctx, db)
	if err != nil || len(visible) != 0 {
		t.Fatalf("methods from disabled channel = %+v, %v", visible, err)
	}

	if err := DeletePaymentMethod(ctx, db, method.ID); err != nil {
		t.Fatalf("delete method: %v", err)
	}
	if err := DeletePaymentMethod(ctx, db, second.ID); err != nil {
		t.Fatalf("delete second method: %v", err)
	}
	if err := DeletePaymentChannel(ctx, db, channel.ID); err != nil {
		t.Fatalf("delete unbound channel: %v", err)
	}
}

func TestPaymentOrderSnapshotsFiltersAndTerminalUpdates(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_order", "Buyer@Example.com")
	if err := SetSetting(db, "settlement_currency", "JPY"); err != nil {
		t.Fatalf("set settlement currency: %v", err)
	}
	creditPackage, err := CreateCreditPackage(ctx, db, CreditPackage{
		Name: "Starter", Credits: 1200, PriceAmountMinor: 980, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create credit package: %v", err)
	}
	group, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "Pro", MonthlyPriceAmountMinor: 1200, YearlyPriceAmountMinor: 12000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create user group: %v", err)
	}
	channel, method := createPaymentsTestMethod(t, ctx, db)

	creditOrder, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_order", PaymentMethodID: method.ID,
		ProductType: PaymentProductCreditPackage, ProductID: creditPackage.ID,
	})
	if err != nil {
		t.Fatalf("create credit order: %v", err)
	}
	if !strings.HasPrefix(creditOrder.ID, "po_") || len(creditOrder.ID) != 25 {
		t.Fatalf("order id %q is not the 128-bit external-safe shape", creditOrder.ID)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(creditOrder.ID, "po_"))
	if err != nil || len(decoded) != 16 {
		t.Fatalf("decode order id entropy: bytes=%d err=%v", len(decoded), err)
	}
	if creditOrder.UserEmail != "Buyer@Example.com" || creditOrder.ChannelName != "Stripe primary" ||
		creditOrder.Currency != "JPY" || creditOrder.AmountMinor != 980 || creditOrder.Credits != 1200 ||
		string(creditOrder.MethodConfig) != string(method.ProviderMethodConfig) {
		t.Fatalf("credit order snapshots = %+v method_config=%q", creditOrder, creditOrder.MethodConfig)
	}

	newPackageName := "Changed package"
	newCredits := 9999.0
	newPrice := int64(7777)
	if _, err := UpdateCreditPackage(ctx, db, creditPackage.ID, CreditPackagePatch{
		Name: &newPackageName, Credits: &newCredits, PriceAmountMinor: &newPrice,
	}); err != nil {
		t.Fatalf("change source package: %v", err)
	}
	newMethodName := "Changed method"
	newMethodConfig := json.RawMessage(`{"legacy":"changed"}`)
	if _, err := UpdatePaymentMethod(ctx, db, method.ID, PaymentMethodPatch{
		Name: &newMethodName, ProviderMethodConfig: &newMethodConfig,
	}); err != nil {
		t.Fatalf("change source method: %v", err)
	}
	newChannelName := "Changed channel"
	if _, err := UpdatePaymentChannel(ctx, db, channel.ID, PaymentChannelPatch{Name: &newChannelName}); err != nil {
		t.Fatalf("change source channel: %v", err)
	}
	stored, err := GetPaymentOrder(ctx, db, creditOrder.ID)
	if err != nil {
		t.Fatalf("get stored order: %v", err)
	}
	if stored.ProductName != "Starter" || stored.MethodName != "Card" || stored.ChannelName != "Stripe primary" ||
		stored.AmountMinor != 980 || stored.Credits != 1200 || string(stored.MethodConfig) != string(method.ProviderMethodConfig) {
		t.Fatalf("order snapshot changed with source records: %+v config=%q", stored, stored.MethodConfig)
	}

	groupOrder, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_order", PaymentMethodID: method.ID,
		ProductType: PaymentProductUserGroup, ProductID: group.ID, BillingCycle: PaymentBillingYearly,
	})
	if err != nil {
		t.Fatalf("create group order: %v", err)
	}
	if groupOrder.AmountMinor != 12000 || groupOrder.UserGroupID != group.ID || groupOrder.BillingCycle != PaymentBillingYearly {
		t.Fatalf("group snapshots = %+v", groupOrder)
	}
	if err := DeletePaymentChannel(ctx, db, channel.ID); !errors.Is(err, ErrPaymentChannelHasPending) {
		t.Fatalf("delete channel with pending orders error = %v", err)
	}
	if err := DeleteUserGroup(ctx, db, group.ID); !errors.Is(err, ErrPaymentOrdersPendingForGroup) {
		t.Fatalf("delete group with pending order error = %v", err)
	}

	creditOrder, err = MarkPaymentOrderProcessing(ctx, db, creditOrder.ID, "pi_credit")
	if err != nil || creditOrder.Status != PaymentOrderProcessing {
		t.Fatalf("mark credit order processing = %+v, %v", creditOrder, err)
	}
	if pending, err := CountPendingPaymentOrdersByChannel(ctx, db, channel.ID); err != nil || pending != 2 {
		t.Fatalf("pending channel orders = %d, %v", pending, err)
	}
	creditOrder, err = MarkPaymentOrderFailed(ctx, db, creditOrder.ID, "declined", "card declined")
	if err != nil || creditOrder.Status != PaymentOrderFailed || creditOrder.FailureCode != "declined" {
		t.Fatalf("mark order failed = %+v, %v", creditOrder, err)
	}
	failureEvent, created, err := RecordPaymentEvent(ctx, db, PaymentEventInput{
		Provider: "stripe", ChannelID: channel.ID, EventID: "evt_failed", OrderID: creditOrder.ID,
		EventType: "payment_intent.payment_failed",
	})
	if err != nil || !created {
		t.Fatalf("record failure event = %+v, created=%v err=%v", failureEvent, created, err)
	}
	if err := MarkPaymentEventProcessed(ctx, db, failureEvent.ID); err != nil {
		t.Fatalf("mark failure event processed: %v", err)
	}
	if err := MarkPaymentEventProcessed(ctx, db, failureEvent.ID); err != nil {
		t.Fatalf("repeat mark failure event processed: %v", err)
	}
	failureEvent, err = GetPaymentEvent(ctx, db, failureEvent.ID)
	if err != nil || failureEvent.ProcessedAt == 0 {
		t.Fatalf("processed failure event = %+v, %v", failureEvent, err)
	}
	groupOrder, err = MarkPaymentOrderProcessing(ctx, db, groupOrder.ID, "sess_group")
	if err != nil {
		t.Fatalf("mark group order processing: %v", err)
	}
	if _, err := MarkPaymentOrderExpired(ctx, db, groupOrder.ID, "wrong_session"); !errors.Is(err, ErrPaymentProviderOrderMismatch) {
		t.Fatalf("expire with mismatched provider id error = %v", err)
	}
	groupOrder, err = MarkPaymentOrderExpired(ctx, db, groupOrder.ID, "sess_group")
	if err != nil || groupOrder.Status != PaymentOrderExpired {
		t.Fatalf("expire group order = %+v, %v", groupOrder, err)
	}
	if pending, err := CountPendingPaymentOrdersByChannel(ctx, db, channel.ID); err != nil || pending != 0 {
		t.Fatalf("terminal orders still counted pending = %d, %v", pending, err)
	}
	if err := DeleteUserGroup(ctx, db, group.ID); err != nil {
		t.Fatalf("delete group after terminal order: %v", err)
	}

	orders, err := ListPaymentOrders(ctx, db, PaymentOrderFilter{
		Provider: "STRIPE", Search: "buyer@example", Limit: 20,
	})
	if err != nil || len(orders) != 2 {
		t.Fatalf("filtered orders = %+v, %v", orders, err)
	}
	if count, err := CountPaymentOrders(ctx, db, PaymentOrderFilter{Search: creditOrder.ID}); err != nil || count != 1 {
		t.Fatalf("CountPaymentOrders by local order id = %d, %v", count, err)
	}
	if count, err := CountPaymentOrders(ctx, db, PaymentOrderFilter{Search: "PI_CREDIT"}); err != nil || count != 1 {
		t.Fatalf("CountPaymentOrders by provider order id = %d, %v", count, err)
	}

	if err := DeleteUser(ctx, db, "u_order"); err != nil {
		t.Fatalf("delete order owner: %v", err)
	}
	stored, err = GetPaymentOrder(ctx, db, creditOrder.ID)
	if err != nil {
		t.Fatalf("order disappeared with user: %v", err)
	}
	if stored.UserID != "" || stored.UserEmail != "Buyer@Example.com" {
		t.Fatalf("deleted-user order identity snapshot = %+v", stored)
	}
}

func TestEPayPaymentOrderSnapshotsCrossCurrencyConversion(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_epay_conversion", "conversion@example.test")
	if err := SetSetting(db, "settlement_currency", "USD"); err != nil {
		t.Fatalf("set settlement currency: %v", err)
	}
	pkg, err := CreateCreditPackage(ctx, db, CreditPackage{
		Name: "USD conversion package", Credits: 400, PriceAmountMinor: 4000, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create conversion package: %v", err)
	}
	channelConfig, err := json.Marshal(paymentcore.EPayConfig{
		GatewayURL: "https://epay.example.test", MerchantID: "conversion-merchant", MerchantKey: "conversion-secret",
		Currency: "CNY", ConversionRate: "7", ConversionRateBaseCurrency: "USD",
	})
	if err != nil {
		t.Fatalf("marshal EPay conversion config: %v", err)
	}
	channel, err := CreatePaymentChannel(ctx, db, PaymentChannel{
		Name: "Converted EPay", Provider: paymentcore.ProviderEPay, Config: channelConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create EPay conversion channel: %v", err)
	}
	method, err := CreatePaymentMethod(ctx, db, PaymentMethod{
		ChannelID: channel.ID, Name: "Converted Alipay", Type: paymentcore.ProviderEPay,
		ProviderMethodConfig: json.RawMessage(`{"type":"alipay"}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create EPay conversion method: %v", err)
	}

	order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_epay_conversion", PaymentMethodID: method.ID,
		ProductType: PaymentProductCreditPackage, ProductID: pkg.ID,
	})
	if err != nil {
		t.Fatalf("create cross-currency EPay order: %v", err)
	}
	if order.AmountMinor != 4000 || order.Currency != "USD" ||
		order.ProviderAmountMinor != 28000 || order.ProviderCurrency != "CNY" || order.ConversionRate != "7" {
		t.Fatalf("cross-currency EPay snapshots = %+v", order)
	}

	changedConfig, err := json.Marshal(paymentcore.EPayConfig{
		GatewayURL: "https://epay.example.test", MerchantID: "conversion-merchant", MerchantKey: "conversion-secret",
		Currency: "CNY", ConversionRate: "8", ConversionRateBaseCurrency: "USD",
	})
	if err != nil {
		t.Fatalf("marshal changed EPay conversion config: %v", err)
	}
	exec(t, db, `UPDATE payment_channels SET config=? WHERE id=?`, string(changedConfig), channel.ID)
	stored, err := GetPaymentOrder(ctx, db, order.ID)
	if err != nil {
		t.Fatalf("get cross-currency EPay order: %v", err)
	}
	if stored.ProviderAmountMinor != 28000 || stored.ProviderCurrency != "CNY" || stored.ConversionRate != "7" {
		t.Fatalf("EPay order snapshots changed with channel rate: %+v", stored)
	}

	if err := SetSetting(db, "settlement_currency", "EUR"); err != nil {
		t.Fatalf("change settlement currency: %v", err)
	}
	if _, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_epay_conversion", PaymentMethodID: method.ID,
		ProductType: PaymentProductCreditPackage, ProductID: pkg.ID,
	}); !errors.Is(err, ErrPaymentMethodUnavailable) {
		t.Fatalf("order with stale rate base error = %v, want %v", err, ErrPaymentMethodUnavailable)
	}
	if err := SetSetting(db, "settlement_currency", "USD"); err != nil {
		t.Fatalf("restore settlement currency: %v", err)
	}

	for _, tc := range []struct {
		name string
		cfg  paymentcore.EPayConfig
	}{
		{
			name: "missing rate",
			cfg: paymentcore.EPayConfig{
				GatewayURL: "https://epay.example.test", MerchantID: "missing-rate", MerchantKey: "secret",
				Currency: "CNY", ConversionRateBaseCurrency: "USD",
			},
		},
		{
			name: "wrong base",
			cfg: paymentcore.EPayConfig{
				GatewayURL: "https://epay.example.test", MerchantID: "wrong-base", MerchantKey: "secret",
				Currency: "CNY", ConversionRate: "7", ConversionRateBaseCurrency: "EUR",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, marshalErr := json.Marshal(tc.cfg)
			if marshalErr != nil {
				t.Fatalf("marshal invalid EPay config: %v", marshalErr)
			}
			invalidChannel, createErr := CreatePaymentChannel(ctx, db, PaymentChannel{
				Name: "Invalid EPay " + tc.name, Provider: paymentcore.ProviderEPay, Config: raw, Enabled: true,
			})
			if createErr != nil {
				t.Fatalf("create invalid EPay channel fixture: %v", createErr)
			}
			invalidMethod, createErr := CreatePaymentMethod(ctx, db, PaymentMethod{
				ChannelID: invalidChannel.ID, Name: "Invalid " + tc.name, Type: paymentcore.ProviderEPay,
				ProviderMethodConfig: json.RawMessage(`{"type":"alipay"}`), Enabled: true,
			})
			if createErr != nil {
				t.Fatalf("create invalid EPay method fixture: %v", createErr)
			}
			if _, createErr = CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
				UserID: "u_epay_conversion", PaymentMethodID: invalidMethod.ID,
				ProductType: PaymentProductCreditPackage, ProductID: pkg.ID,
			}); !errors.Is(createErr, ErrPaymentMethodUnavailable) {
				t.Fatalf("invalid EPay conversion order error = %v, want %v", createErr, ErrPaymentMethodUnavailable)
			}
		})
	}
}

func TestEPayPaymentOrderAttemptReusesOutstandingReference(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_epay_attempts", "attempts@example.test")
	pkg, err := CreateCreditPackage(ctx, db, CreditPackage{
		Name: "Attempt package", Credits: 75, PriceAmountMinor: 12345, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create attempt package: %v", err)
	}
	channelConfig, err := json.Marshal(paymentcore.EPayConfig{
		GatewayURL: "https://epay.example.test", MerchantID: "attempt-merchant", MerchantKey: "attempt-secret", Currency: "USD",
	})
	if err != nil {
		t.Fatalf("marshal EPay config: %v", err)
	}
	channel, err := CreatePaymentChannel(ctx, db, PaymentChannel{
		Name: "Attempt EPay", Provider: paymentcore.ProviderEPay, Config: channelConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create EPay channel: %v", err)
	}
	method, err := CreatePaymentMethod(ctx, db, PaymentMethod{
		ChannelID: channel.ID, Name: "Attempt Alipay", Type: paymentcore.ProviderEPay,
		ProviderMethodConfig: json.RawMessage(`{"type":"alipay"}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create EPay method: %v", err)
	}
	order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_epay_attempts", PaymentMethodID: method.ID,
		ProductType: PaymentProductCreditPackage, ProductID: pkg.ID,
	})
	if err != nil {
		t.Fatalf("create EPay order: %v", err)
	}
	initial, err := CreatePaymentOrderAttempt(ctx, db, order.ID, order.ID)
	if err != nil {
		t.Fatalf("create initial attempt: %v", err)
	}
	retry, err := CreatePaymentOrderAttempt(ctx, db, order.ID, "")
	if err != nil {
		t.Fatalf("reuse retry attempt: %v", err)
	}
	explicitRetry, err := CreatePaymentOrderAttempt(ctx, db, order.ID, "pa_should_not_be_issued")
	if err != nil {
		t.Fatalf("reuse attempt with explicit candidate: %v", err)
	}
	if initial.MerchantOrderID != order.ID || retry.MerchantOrderID != initial.MerchantOrderID ||
		explicitRetry.MerchantOrderID != initial.MerchantOrderID || retry.OrderID != order.ID {
		t.Fatalf("EPay outstanding attempt was not reused: initial=%+v retry=%+v explicit=%+v", initial, retry, explicitRetry)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_order_attempts WHERE order_id=?`, order.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("outstanding attempt count = %d, %v; want 1", count, err)
	}
	exec(t, db,
		`INSERT INTO payment_order_attempts(merchant_order_id, order_id, provider, channel_id, status)
		 VALUES(?, ?, ?, ?, ?)`,
		"pa_legacy_duplicate", order.ID, paymentcore.ProviderEPay, channel.ID, PaymentOrderAttemptIssued,
	)
	if _, err := CreatePaymentOrderAttempt(ctx, db, order.ID, ""); !errors.Is(err, ErrPaymentOrderNotMutable) {
		t.Fatalf("resume legacy order with multiple issued attempts error = %v, want %v", err, ErrPaymentOrderNotMutable)
	}
	exec(t, db, `DELETE FROM payment_order_attempts WHERE merchant_order_id=?`, "pa_legacy_duplicate")

	exec(t, db, `UPDATE payment_order_attempts SET status='unknown' WHERE merchant_order_id=?`, initial.MerchantOrderID)
	if _, err := CreatePaymentOrderAttempt(ctx, db, order.ID, ""); !errors.Is(err, ErrPaymentOrderNotMutable) {
		t.Fatalf("replace ambiguous attempt error = %v, want %v", err, ErrPaymentOrderNotMutable)
	}
	exec(t, db, `UPDATE payment_order_attempts SET status='expired' WHERE merchant_order_id=?`, initial.MerchantOrderID)
	if _, err := CreatePaymentOrderAttempt(ctx, db, order.ID, ""); !errors.Is(err, ErrPaymentOrderNotMutable) {
		t.Fatalf("replace locally terminal attempt error = %v, want %v", err, ErrPaymentOrderNotMutable)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_order_attempts WHERE order_id=?`, order.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("attempt count after rejected replacement = %d, %v; want 1", count, err)
	}

	// Restore the fixture to the only state accepted by a verified payment.
	exec(t, db, `UPDATE payment_order_attempts SET status=? WHERE merchant_order_id=?`, PaymentOrderAttemptIssued, initial.MerchantOrderID)
	amount := order.AmountMinor
	result, err := FulfillPaymentOrder(ctx, db, PaymentFulfillmentInput{
		PaymentEventInput: PaymentEventInput{
			Provider: paymentcore.ProviderEPay, ChannelID: channel.ID,
			EventID: "epay-attempt-success", OrderID: order.ID, EventType: "payment_notification",
		},
		MerchantOrderID: initial.MerchantOrderID, ProviderOrderID: "epay-provider-success",
		AmountMinor: &amount, PaidAmountMinor: &amount, Currency: order.Currency,
	})
	if err != nil || !result.Applied || result.Order.Status != PaymentOrderFulfilled {
		t.Fatalf("reused attempt fulfillment = %+v, %v", result, err)
	}

	var credits float64
	if err := db.QueryRowContext(ctx, `SELECT credits_permanent FROM users WHERE id=?`, order.UserID).Scan(&credits); err != nil {
		t.Fatalf("read fulfilled credits: %v", err)
	}
	if credits != pkg.Credits {
		t.Fatalf("credits after fulfillment = %v, want %v", credits, pkg.Credits)
	}
	if _, err := CreatePaymentOrderAttempt(ctx, db, order.ID, ""); !errors.Is(err, ErrPaymentOrderNotMutable) {
		t.Fatalf("create attempt for fulfilled order error = %v, want %v", err, ErrPaymentOrderNotMutable)
	}
}

func TestCreatePaymentOrderRejectsPermanentUserGroupAndAllowsFiniteRenewal(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_group_renewal", "renewal@example.com")
	group, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "Renewable Pro", MonthlyPriceAmountMinor: 1800, YearlyPriceAmountMinor: 18000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create renewable user group: %v", err)
	}
	_, method := createPaymentsTestMethod(t, ctx, db)
	exec(t, db,
		`UPDATE users SET group_id=?, group_expires_at=0, previous_group_id='' WHERE id=?`,
		group.ID, "u_group_renewal",
	)

	cycles := []string{PaymentBillingMonthly, PaymentBillingYearly}
	for _, cycle := range cycles {
		order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
			UserID: "u_group_renewal", PaymentMethodID: method.ID,
			ProductType: PaymentProductUserGroup, ProductID: group.ID, BillingCycle: cycle,
		})
		if order != nil || !errors.Is(err, ErrPaymentUserGroupPermanent) {
			t.Fatalf("permanent-group %s order = %+v, err = %v; want nil, %v",
				cycle, order, err, ErrPaymentUserGroupPermanent)
		}
	}
	var orderCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM payment_orders WHERE user_id=?`, "u_group_renewal").Scan(&orderCount); err != nil {
		t.Fatalf("count rejected permanent-group orders: %v", err)
	}
	if orderCount != 0 {
		t.Fatalf("permanent-group rejection created %d orders, want 0", orderCount)
	}
	temporary, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "Temporary higher tier", MonthlyPriceAmountMinor: 2400, YearlyPriceAmountMinor: 24000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create temporary current group: %v", err)
	}
	exec(t, db,
		`UPDATE users SET group_id=?, group_expires_at=?, previous_group_id=? WHERE id=?`,
		temporary.ID, time.Now().UTC().AddDate(0, 1, 0).Unix(), group.ID, "u_group_renewal",
	)
	for _, cycle := range cycles {
		order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
			UserID: "u_group_renewal", PaymentMethodID: method.ID,
			ProductType: PaymentProductUserGroup, ProductID: group.ID, BillingCycle: cycle,
		})
		if order != nil || !errors.Is(err, ErrPaymentUserGroupPermanent) {
			t.Fatalf("permanent baseline %s order = %+v, err = %v; want nil, %v",
				cycle, order, err, ErrPaymentUserGroupPermanent)
		}
	}

	finiteExpiry := time.Now().UTC().AddDate(0, 2, 0).Unix()
	exec(t, db,
		`UPDATE users SET group_id=?, group_expires_at=?, previous_group_id='' WHERE id=?`,
		group.ID, finiteExpiry, "u_group_renewal",
	)
	for _, cycle := range cycles {
		order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
			UserID: "u_group_renewal", PaymentMethodID: method.ID,
			ProductType: PaymentProductUserGroup, ProductID: group.ID, BillingCycle: cycle,
		})
		if err != nil {
			t.Fatalf("create finite-group %s renewal: %v", cycle, err)
		}
		wantAmount := group.MonthlyPriceAmountMinor
		if cycle == PaymentBillingYearly {
			wantAmount = group.YearlyPriceAmountMinor
		}
		if order.UserGroupID != group.ID || order.BillingCycle != cycle || order.AmountMinor != wantAmount {
			t.Fatalf("finite-group %s renewal snapshot = %+v", cycle, order)
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM payment_orders WHERE user_id=?`, "u_group_renewal").Scan(&orderCount); err != nil {
		t.Fatalf("count finite-group renewal orders: %v", err)
	}
	if orderCount != len(cycles) {
		t.Fatalf("finite-group renewal order count = %d, want %d", orderCount, len(cycles))
	}
}

func TestPendingPaymentBlocksUserDeletionAndInactiveUserCheckout(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_payment_delete", "payment-delete@example.com")
	pkg, err := CreateCreditPackage(ctx, db, CreditPackage{
		Name: "Deletion guard package", Credits: 100, PriceAmountMinor: 500, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create deletion guard package: %v", err)
	}
	_, method := createPaymentsTestMethod(t, ctx, db)
	order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_payment_delete", PaymentMethodID: method.ID,
		ProductType: PaymentProductCreditPackage, ProductID: pkg.ID,
	})
	if err != nil {
		t.Fatalf("create pending payment before deletion: %v", err)
	}
	if changed, err := MarkUserDeleting(ctx, db, "u_payment_delete"); changed || !errors.Is(err, ErrPaymentOrdersPendingForUser) {
		t.Fatalf("MarkUserDeleting with pending payment = changed %v err %v, want false/%v",
			changed, err, ErrPaymentOrdersPendingForUser)
	}
	if err := DeleteUser(ctx, db, "u_payment_delete"); !errors.Is(err, ErrPaymentOrdersPendingForUser) {
		t.Fatalf("DeleteUser with pending payment error = %v, want %v", err, ErrPaymentOrdersPendingForUser)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM users WHERE id=?`, "u_payment_delete").Scan(&status); err != nil || status != "active" {
		t.Fatalf("payment deletion guard left user status %q, err=%v", status, err)
	}
	if _, err := MarkPaymentOrderFailed(ctx, db, order.ID, "cancelled_for_test", "terminal"); err != nil {
		t.Fatalf("make guarded payment terminal: %v", err)
	}
	exec(t, db, `UPDATE users SET status='blocked' WHERE id=?`, "u_payment_delete")
	if order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_payment_delete", PaymentMethodID: method.ID,
		ProductType: PaymentProductCreditPackage, ProductID: pkg.ID,
	}); order != nil || !errors.Is(err, ErrPaymentUserUnavailable) {
		t.Fatalf("inactive-user checkout = %+v, %v; want nil/%v", order, err, ErrPaymentUserUnavailable)
	}
}

func TestManualCloseOrderProtectsLateFulfillmentDependencies(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	const userID = "u_manual_close_dependencies"
	createPaymentsTestUser(t, db, userID, "manual-close-dependencies@example.test")
	group, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "Manual close target", MonthlyPriceAmountMinor: 1800,
		YearlyPriceAmountMinor: 18000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create target group: %v", err)
	}
	channelConfig, err := json.Marshal(paymentcore.EPayConfig{
		GatewayURL: "https://epay.example.test", MerchantID: "manual-close-merchant",
		MerchantKey: "manual-close-secret", Currency: "USD",
	})
	if err != nil {
		t.Fatalf("marshal EPay config: %v", err)
	}
	channel, err := CreatePaymentChannel(ctx, db, PaymentChannel{
		Name: "Manual close EPay", Provider: paymentcore.ProviderEPay, Config: channelConfig, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create EPay channel: %v", err)
	}
	method, err := CreatePaymentMethod(ctx, db, PaymentMethod{
		ChannelID: channel.ID, Name: "Manual close Alipay", Type: paymentcore.ProviderEPay,
		ProviderMethodConfig: json.RawMessage(`{"type":"alipay"}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create EPay method: %v", err)
	}
	order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: userID, PaymentMethodID: method.ID, ProductType: PaymentProductUserGroup,
		ProductID: group.ID, BillingCycle: PaymentBillingMonthly,
	})
	if err != nil {
		t.Fatalf("create EPay group order: %v", err)
	}
	attempt, err := CreatePaymentOrderAttempt(ctx, db, order.ID, order.ID)
	if err != nil {
		t.Fatalf("create EPay attempt: %v", err)
	}
	closed, err := CancelPaymentOrderByAdmin(ctx, db, order.ID, "locally closed")
	if err != nil || closed.Status != PaymentOrderCancelled || closed.FailureCode != "admin_manual_close" {
		t.Fatalf("manual close order = %+v, %v", closed, err)
	}

	if count, err := CountPendingPaymentOrdersByChannel(ctx, db, channel.ID); err != nil || count != 1 {
		t.Fatalf("recoverable channel order count = %d, %v; want 1", count, err)
	}
	if pending, err := HasPendingPaymentOrdersForUserGroup(ctx, db, group.ID); err != nil || !pending {
		t.Fatalf("recoverable group order guard = %v, %v; want true", pending, err)
	}
	if pending, err := HasPendingPaymentOrdersForUser(ctx, db, userID); err != nil || !pending {
		t.Fatalf("recoverable user order guard = %v, %v; want true", pending, err)
	}
	changedConfig := json.RawMessage(`{"gateway_url":"https://changed.invalid","merchant_id":"changed","merchant_key":"changed","currency":"USD"}`)
	if _, err := UpdatePaymentChannel(ctx, db, channel.ID, PaymentChannelPatch{Config: &changedConfig}); !errors.Is(err, ErrPaymentChannelHasPending) {
		t.Fatalf("change channel with recoverable order error = %v, want %v", err, ErrPaymentChannelHasPending)
	}
	if err := DeletePaymentChannel(ctx, db, channel.ID); !errors.Is(err, ErrPaymentChannelHasPending) {
		t.Fatalf("delete channel with recoverable order error = %v, want %v", err, ErrPaymentChannelHasPending)
	}
	if err := DeletePaymentMethod(ctx, db, method.ID); !errors.Is(err, ErrPaymentMethodHasPending) {
		t.Fatalf("delete method with recoverable order error = %v, want %v", err, ErrPaymentMethodHasPending)
	}
	if err := DeleteUserGroup(ctx, db, group.ID); !errors.Is(err, ErrPaymentOrdersPendingForGroup) {
		t.Fatalf("delete group with recoverable order error = %v, want %v", err, ErrPaymentOrdersPendingForGroup)
	}
	if changed, err := MarkUserDeleting(ctx, db, userID); changed || !errors.Is(err, ErrPaymentOrdersPendingForUser) {
		t.Fatalf("mark user deleting with recoverable order = %v, %v; want false/%v", changed, err, ErrPaymentOrdersPendingForUser)
	}
	if err := DeleteUser(ctx, db, userID); !errors.Is(err, ErrPaymentOrdersPendingForUser) {
		t.Fatalf("delete user with recoverable order error = %v, want %v", err, ErrPaymentOrdersPendingForUser)
	}

	amount := order.AmountMinor
	fulfilled, err := FulfillPaymentOrder(ctx, db, PaymentFulfillmentInput{
		PaymentEventInput: PaymentEventInput{
			Provider: paymentcore.ProviderEPay, ChannelID: channel.ID,
			EventID: "manual-close-late-payment", OrderID: order.ID, EventType: "payment_notification",
		},
		MerchantOrderID: attempt.MerchantOrderID, ProviderOrderID: "manual-close-late-trade",
		AmountMinor: &amount, PaidAmountMinor: &amount, Currency: order.Currency,
	})
	if err != nil || !fulfilled.Applied || fulfilled.Order.Status != PaymentOrderFulfilled {
		t.Fatalf("late fulfillment after protected dependencies = %+v, %v", fulfilled, err)
	}
	var currentGroup string
	if err := db.QueryRowContext(ctx, `SELECT group_id FROM users WHERE id=?`, userID).Scan(&currentGroup); err != nil || currentGroup != group.ID {
		t.Fatalf("late fulfillment user group = %q, %v; want %q", currentGroup, err, group.ID)
	}
}

func TestUserGroupPaymentPreservesRestoredPermanentBaseline(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_restored_baseline", "restored-baseline@example.com")
	permanent, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "Permanent baseline", MonthlyPriceAmountMinor: 1500, YearlyPriceAmountMinor: 15000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create permanent baseline: %v", err)
	}
	temporary, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "Expired temporary", MonthlyPriceAmountMinor: 2500, YearlyPriceAmountMinor: 25000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create temporary group: %v", err)
	}
	channel, method := createPaymentsTestMethod(t, ctx, db)
	// Model an order created by an older release before permanent-baseline
	// ownership was checked during checkout.
	order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_restored_baseline", PaymentMethodID: method.ID,
		ProductType: PaymentProductUserGroup, ProductID: permanent.ID, BillingCycle: PaymentBillingMonthly,
	})
	if err != nil {
		t.Fatalf("create historical pending baseline order: %v", err)
	}
	if _, err := MarkPaymentOrderProcessing(ctx, db, order.ID, "pi_restored_baseline"); err != nil {
		t.Fatalf("mark historical baseline order processing: %v", err)
	}
	exec(t, db,
		`UPDATE users SET group_id=?, group_expires_at=?, previous_group_id=? WHERE id=?`,
		temporary.ID, time.Now().Unix()-1, permanent.ID, "u_restored_baseline",
	)
	amount := order.AmountMinor
	result, err := FulfillPaymentOrder(ctx, db, PaymentFulfillmentInput{
		PaymentEventInput: PaymentEventInput{
			Provider: channel.Provider, ChannelID: channel.ID, EventID: "evt_restored_baseline", OrderID: order.ID,
			EventType: "checkout.session.completed",
		},
		ProviderOrderID: "pi_restored_baseline", AmountMinor: &amount, Currency: order.Currency,
	})
	if err != nil || !result.Applied {
		t.Fatalf("fulfill historical baseline order = %+v, %v", result, err)
	}
	user, err := FindUserByID(ctx, db, "u_restored_baseline")
	if err != nil {
		t.Fatalf("read restored baseline user: %v", err)
	}
	if user.GroupID != permanent.ID || user.GroupExpiresAt != 0 || user.PreviousGroupID != "" {
		t.Fatalf("restored baseline after fulfillment = group %q expiry %d previous %q, want %q permanent",
			user.GroupID, user.GroupExpiresAt, user.PreviousGroupID, permanent.ID)
	}
}

func TestCreditPaymentFulfillmentIsStrictlyIdempotent(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_credit", "credit@example.com")
	creditPackage, err := CreateCreditPackage(ctx, db, CreditPackage{
		Name: "Credits", Credits: 2500, PriceAmountMinor: 499, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create package: %v", err)
	}
	channel, method := createPaymentsTestMethod(t, ctx, db)
	createOrder := func() *PaymentOrder {
		order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
			UserID: "u_credit", PaymentMethodID: method.ID,
			ProductType: PaymentProductCreditPackage, ProductID: creditPackage.ID,
		})
		if err != nil {
			t.Fatalf("create payment order: %v", err)
		}
		return order
	}
	order := createOrder()
	if _, err := MarkPaymentOrderProcessing(ctx, db, order.ID, "pi_one"); err != nil {
		t.Fatalf("mark processing: %v", err)
	}
	amount := order.AmountMinor
	paidAmount := amount + 50
	taxAmount := int64(50)
	input := PaymentFulfillmentInput{
		PaymentEventInput: PaymentEventInput{
			Provider: "stripe", ChannelID: channel.ID, EventID: "evt_one", OrderID: order.ID,
			EventType: "checkout.session.completed",
		},
		ProviderOrderID: "pi_one", AmountMinor: &amount, PaidAmountMinor: &paidAmount, TaxAmountMinor: &taxAmount, Currency: order.Currency,
	}
	result, err := FulfillPaymentOrder(ctx, db, input)
	if err != nil || !result.Applied || result.DuplicateEvent || result.Order.Status != PaymentOrderFulfilled {
		t.Fatalf("first fulfillment = %+v, %v", result, err)
	}
	if result.Order.PaidAmountMinor != paidAmount || result.Order.TaxAmountMinor != taxAmount {
		t.Fatalf("settled payment totals = paid %d tax %d, want %d/%d",
			result.Order.PaidAmountMinor, result.Order.TaxAmountMinor, paidAmount, taxAmount)
	}
	result, err = FulfillPaymentOrder(ctx, db, input)
	if err != nil || result.Applied || !result.DuplicateEvent {
		t.Fatalf("duplicate event fulfillment = %+v, %v", result, err)
	}
	input.EventID = "evt_two"
	result, err = FulfillPaymentOrder(ctx, db, input)
	if err != nil || result.Applied || result.DuplicateEvent {
		t.Fatalf("new event for fulfilled order = %+v, %v", result, err)
	}
	var balance float64
	if err := db.QueryRowContext(ctx, `SELECT credits_permanent FROM users WHERE id='u_credit'`).Scan(&balance); err != nil {
		t.Fatalf("read credits: %v", err)
	}
	if balance != 2500 {
		t.Fatalf("credits after repeated success events = %v, want 2500", balance)
	}
	events, err := ListPaymentEventsForOrder(ctx, db, order.ID)
	if err != nil || len(events) != 2 || events[0].ProcessedAt == 0 || events[1].ProcessedAt == 0 {
		t.Fatalf("recorded fulfillment events = %+v, %v", events, err)
	}

	second := createOrder()
	collision := input
	collision.OrderID = second.ID
	collision.EventID = "evt_one"
	collision.ProviderOrderID = "pi_two"
	secondAmount := second.AmountMinor
	collision.AmountMinor = &secondAmount
	if _, err := FulfillPaymentOrder(ctx, db, collision); !errors.Is(err, ErrPaymentEventConflict) {
		t.Fatalf("event id collision error = %v", err)
	}
	mismatch := collision
	mismatch.EventID = "evt_mismatch"
	wrongAmount := second.AmountMinor + 1
	mismatch.AmountMinor = &wrongAmount
	if _, err := FulfillPaymentOrder(ctx, db, mismatch); !errors.Is(err, ErrPaymentAmountMismatch) {
		t.Fatalf("amount mismatch error = %v", err)
	}
	secondEvents, err := ListPaymentEventsForOrder(ctx, db, second.ID)
	if err != nil || len(secondEvents) != 0 {
		t.Fatalf("failed fulfillment left events = %+v, %v", secondEvents, err)
	}
	mismatch.EventID = "evt_recovered"
	mismatch.AmountMinor = &secondAmount
	result, err = FulfillPaymentOrder(ctx, db, mismatch)
	if err != nil || !result.Applied {
		t.Fatalf("successful webhook after rejected mismatch = %+v, %v", result, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT credits_permanent FROM users WHERE id='u_credit'`).Scan(&balance); err != nil {
		t.Fatalf("read recovered credits: %v", err)
	}
	if balance != 5000 {
		t.Fatalf("credits after second distinct order = %v, want 5000", balance)
	}

	failed := createOrder()
	if _, err := MarkPaymentOrderFailed(ctx, db, failed.ID, "async_failed", "terminal"); err != nil {
		t.Fatalf("mark third order failed: %v", err)
	}
	failedAmount := failed.AmountMinor
	if _, err := FulfillPaymentOrder(ctx, db, PaymentFulfillmentInput{
		PaymentEventInput: PaymentEventInput{
			Provider: "stripe", ChannelID: channel.ID, EventID: "evt_after_failed", OrderID: failed.ID,
			EventType: "checkout.session.completed",
		},
		AmountMinor: &failedAmount, Currency: failed.Currency,
	}); !errors.Is(err, ErrPaymentOrderNotFulfillable) {
		t.Fatalf("fulfilled terminal failed order, err=%v", err)
	}
	if events, err := ListPaymentEventsForOrder(ctx, db, failed.ID); err != nil || len(events) != 0 {
		t.Fatalf("failed-order fulfillment left event = %+v, %v", events, err)
	}

	manuallyClosed := createOrder()
	if _, err := CancelPaymentOrderByAdmin(ctx, db, manuallyClosed.ID, ""); err != nil {
		t.Fatalf("manually close payment without reason: %v", err)
	}
	manualAmount := manuallyClosed.AmountMinor
	manualInput := PaymentFulfillmentInput{
		PaymentEventInput: PaymentEventInput{
			Provider: channel.Provider, ChannelID: channel.ID, EventID: "evt_after_manual_close", OrderID: manuallyClosed.ID,
			EventType: "checkout.session.completed",
		},
		ProviderOrderID: "pi_after_manual_close", AmountMinor: &manualAmount, Currency: manuallyClosed.Currency,
	}
	result, err = FulfillPaymentOrder(ctx, db, manualInput)
	if err != nil || !result.Applied || result.Order.Status != PaymentOrderFulfilled {
		t.Fatalf("verified payment after manual close = %+v, %v", result, err)
	}
	result, err = FulfillPaymentOrder(ctx, db, manualInput)
	if err != nil || result.Applied || !result.DuplicateEvent {
		t.Fatalf("duplicate payment after manual close = %+v, %v", result, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT credits_permanent FROM users WHERE id='u_credit'`).Scan(&balance); err != nil {
		t.Fatalf("read credits after manual-close recovery: %v", err)
	}
	if balance != 7500 {
		t.Fatalf("credits after manual-close recovery = %v, want 7500", balance)
	}
	events, err = ListPaymentEventsForOrder(ctx, db, manuallyClosed.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("manual-close recovery events = %+v, %v", events, err)
	}
	eventTypes := make(map[string]bool, len(events))
	for _, paymentEvent := range events {
		eventTypes[paymentEvent.EventType] = paymentEvent.ProcessedAt > 0
	}
	if !eventTypes["admin.manual_close"] || !eventTypes["checkout.session.completed"] {
		t.Fatalf("manual-close recovery event types = %+v", events)
	}

	providerCancelled := createOrder()
	if _, err := MarkPaymentOrderCancelled(ctx, db, providerCancelled.ID, "provider_cancelled", "terminal"); err != nil {
		t.Fatalf("mark provider-cancelled order: %v", err)
	}
	providerCancelledAmount := providerCancelled.AmountMinor
	if _, err := FulfillPaymentOrder(ctx, db, PaymentFulfillmentInput{
		PaymentEventInput: PaymentEventInput{
			Provider: channel.Provider, ChannelID: channel.ID, EventID: "evt_after_provider_cancel", OrderID: providerCancelled.ID,
			EventType: "checkout.session.completed",
		},
		AmountMinor: &providerCancelledAmount, Currency: providerCancelled.Currency,
	}); !errors.Is(err, ErrPaymentOrderNotFulfillable) {
		t.Fatalf("fulfilled provider-cancelled order, err=%v", err)
	}
	if events, err := ListPaymentEventsForOrder(ctx, db, providerCancelled.ID); err != nil || len(events) != 0 {
		t.Fatalf("provider-cancelled fulfillment left event = %+v, %v", events, err)
	}
}

func TestUserGroupPaymentUsesCalendarRenewal(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_group", "group@example.com")
	if _, err := CreateUserGroup(ctx, db, UserGroup{ID: DefaultGroupID, Name: "Free", IsPublic: true}); err != nil {
		t.Fatalf("create default placeholder group: %v", err)
	}
	pro, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "Pro", MonthlyPriceAmountMinor: 1000, YearlyPriceAmountMinor: 10000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create pro: %v", err)
	}
	vip, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "VIP", MonthlyPriceAmountMinor: 2000, YearlyPriceAmountMinor: 20000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create vip: %v", err)
	}
	channel, method := createPaymentsTestMethod(t, ctx, db)
	createGroupOrder := func(groupID, cycle string) *PaymentOrder {
		order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
			UserID: "u_group", PaymentMethodID: method.ID,
			ProductType: PaymentProductUserGroup, ProductID: groupID, BillingCycle: cycle,
		})
		if err != nil {
			t.Fatalf("create %s group order: %v", cycle, err)
		}
		return order
	}
	fulfill := func(order *PaymentOrder, eventID string) {
		amount := order.AmountMinor
		if _, err := FulfillPaymentOrder(ctx, db, PaymentFulfillmentInput{
			PaymentEventInput: PaymentEventInput{
				Provider: "stripe", ChannelID: channel.ID, EventID: eventID, OrderID: order.ID,
				EventType: "checkout.session.completed",
			},
			ProviderOrderID: "pi_" + eventID, AmountMinor: &amount, Currency: order.Currency,
		}); err != nil {
			t.Fatalf("fulfill %s: %v", eventID, err)
		}
	}

	base := time.Date(2030, time.January, 31, 12, 0, 0, 0, time.UTC).Unix()
	exec(t, db, `UPDATE users SET group_id=?, group_expires_at=?, previous_group_id='', credit_cycle_anchor=12345 WHERE id='u_group'`, pro.ID, base)
	monthly := createGroupOrder(pro.ID, PaymentBillingMonthly)
	fulfill(monthly, "monthly")
	wantMonthly := time.Unix(base, 0).UTC().AddDate(0, 1, 0).Unix()
	var groupID, previousGroup string
	var expiry int64
	var creditAnchor int64
	var tokenVersion int
	if err := db.QueryRowContext(ctx,
		`SELECT group_id, group_expires_at, previous_group_id, credit_cycle_anchor, token_ver FROM users WHERE id='u_group'`,
	).Scan(&groupID, &expiry, &previousGroup, &creditAnchor, &tokenVersion); err != nil {
		t.Fatalf("read monthly membership: %v", err)
	}
	if groupID != pro.ID || expiry != wantMonthly || creditAnchor != 12345 || tokenVersion != 1 {
		t.Fatalf("monthly membership = group %q expiry %d anchor %d token %d, want %q/%d/12345/1", groupID, expiry, creditAnchor, tokenVersion, pro.ID, wantMonthly)
	}

	yearly := createGroupOrder(pro.ID, PaymentBillingYearly)
	fulfill(yearly, "yearly")
	wantYearly := time.Unix(wantMonthly, 0).UTC().AddDate(1, 0, 0).Unix()
	if err := db.QueryRowContext(ctx,
		`SELECT group_id, group_expires_at, previous_group_id, credit_cycle_anchor, token_ver FROM users WHERE id='u_group'`,
	).Scan(&groupID, &expiry, &previousGroup, &creditAnchor, &tokenVersion); err != nil {
		t.Fatalf("read yearly membership: %v", err)
	}
	if groupID != pro.ID || expiry != wantYearly || creditAnchor != 12345 || tokenVersion != 2 {
		t.Fatalf("yearly membership = group %q expiry %d anchor %d token %d, want %q/%d/12345/2", groupID, expiry, creditAnchor, tokenVersion, pro.ID, wantYearly)
	}

	before := time.Now().UTC()
	vipOrder := createGroupOrder(vip.ID, PaymentBillingMonthly)
	fulfill(vipOrder, "vip")
	after := time.Now().UTC()
	if err := db.QueryRowContext(ctx,
		`SELECT group_id, group_expires_at, previous_group_id, credit_cycle_anchor FROM users WHERE id='u_group'`,
	).Scan(&groupID, &expiry, &previousGroup, &creditAnchor); err != nil {
		t.Fatalf("read switched membership: %v", err)
	}
	low := before.AddDate(0, 1, 0).Unix()
	high := after.AddDate(0, 1, 0).Unix()
	if groupID != vip.ID || previousGroup != "" || expiry < low || expiry > high || creditAnchor < before.Unix() || creditAnchor > after.Unix() {
		t.Fatalf("switched membership = group %q prev %q expiry %d anchor %d, want %q/%q in %d..%d with fresh anchor",
			groupID, previousGroup, expiry, creditAnchor, vip.ID, "", low, high)
	}

	// Replacing finite Pro with finite VIP must not restore Pro as a permanent
	// group after VIP expires.
	exec(t, db, `UPDATE users SET group_expires_at=? WHERE id='u_group'`, time.Now().Unix()-1)
	expiredUser, err := FindUserByID(ctx, db, "u_group")
	if err != nil {
		t.Fatalf("expire switched finite membership: %v", err)
	}
	if expiredUser.GroupID != DefaultGroupID || expiredUser.GroupExpiresAt != 0 || expiredUser.PreviousGroupID != "" {
		t.Fatalf("expired switched membership = group %q expiry %d prev %q, want default permanent",
			expiredUser.GroupID, expiredUser.GroupExpiresAt, expiredUser.PreviousGroupID)
	}

	// A permanent non-default group is a real baseline and must be restored
	// after a temporary paid upgrade expires.
	exec(t, db, `UPDATE users SET group_id=?, group_expires_at=0, previous_group_id='' WHERE id='u_group'`, pro.ID)
	temporaryUpgrade := createGroupOrder(vip.ID, PaymentBillingMonthly)
	fulfill(temporaryUpgrade, "vip-permanent-baseline")
	if err := db.QueryRowContext(ctx,
		`SELECT group_id, group_expires_at, previous_group_id FROM users WHERE id='u_group'`,
	).Scan(&groupID, &expiry, &previousGroup); err != nil {
		t.Fatalf("read temporary upgrade over permanent baseline: %v", err)
	}
	if groupID != vip.ID || expiry <= time.Now().Unix() || previousGroup != pro.ID {
		t.Fatalf("temporary upgrade = group %q expiry %d prev %q, want %q/finite/%q",
			groupID, expiry, previousGroup, vip.ID, pro.ID)
	}
	exec(t, db, `UPDATE users SET group_expires_at=? WHERE id='u_group'`, time.Now().Unix()-1)
	restoredUser, err := FindUserByID(ctx, db, "u_group")
	if err != nil {
		t.Fatalf("expire temporary upgrade: %v", err)
	}
	if restoredUser.GroupID != pro.ID || restoredUser.GroupExpiresAt != 0 || restoredUser.PreviousGroupID != "" {
		t.Fatalf("restored permanent baseline = group %q expiry %d prev %q, want %q permanent",
			restoredUser.GroupID, restoredUser.GroupExpiresAt, restoredUser.PreviousGroupID, pro.ID)
	}

	exec(t, db, `UPDATE users SET group_id=?, group_expires_at=0, previous_group_id='' WHERE id='u_group'`, vip.ID)
	permanent, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_group", PaymentMethodID: method.ID,
		ProductType: PaymentProductUserGroup, ProductID: vip.ID, BillingCycle: PaymentBillingMonthly,
	})
	if permanent != nil || !errors.Is(err, ErrPaymentUserGroupPermanent) {
		t.Fatalf("permanent same-group order = %+v, err = %v; want nil, %v",
			permanent, err, ErrPaymentUserGroupPermanent)
	}
	if err := db.QueryRowContext(ctx, `SELECT group_expires_at FROM users WHERE id='u_group'`).Scan(&expiry); err != nil {
		t.Fatalf("read permanent membership: %v", err)
	}
	if expiry != 0 {
		t.Fatalf("rejected same-group purchase changed permanent membership to %d", expiry)
	}
}

func TestPaymentProviderSnapshotMigrationBackfillsLegacyOrders(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	for _, column := range []string{"provider_amount_minor", "provider_currency", "conversion_rate", "checkout_url"} {
		if _, err := db.ExecContext(ctx, `ALTER TABLE payment_orders DROP COLUMN `+column); err != nil {
			t.Fatalf("drop provider snapshot column %s: %v", column, err)
		}
	}
	exec(t, db,
		`INSERT INTO payment_orders(
		   id, user_email, provider, channel_id, channel_name, method_id, method_name, method_type,
		   product_type, product_id, product_name, amount_minor, currency
		 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"po_legacy_provider_snapshot", "legacy@example.test", paymentcore.ProviderEPay,
		"paych_legacy", "Legacy EPay", "paym_legacy", "Legacy Alipay", paymentcore.ProviderEPay,
		PaymentProductCreditPackage, "cp_legacy", "Legacy package", 4321, "USD",
	)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy provider snapshots: %v", err)
	}

	order, err := GetPaymentOrder(ctx, db, "po_legacy_provider_snapshot")
	if err != nil {
		t.Fatalf("get migrated legacy payment order: %v", err)
	}
	if order.ProviderAmountMinor != 4321 || order.ProviderCurrency != "USD" || order.ConversionRate != "" {
		t.Fatalf("migrated legacy provider snapshots = %+v", order)
	}
	if order.CheckoutURL != "" {
		t.Fatalf("migrated legacy checkout URL = %q, want empty", order.CheckoutURL)
	}
	for _, column := range []string{"provider_amount_minor", "provider_currency", "conversion_rate", "checkout_url"} {
		if _, err := db.ExecContext(ctx, `SELECT `+column+` FROM payment_orders WHERE 1=0`); err != nil {
			t.Fatalf("provider snapshot column %s missing after migration: %v", column, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`SELECT merchant_order_id, order_id, provider, channel_id, provider_order_id, status, paid_at
		   FROM payment_order_attempts WHERE 1=0`,
	); err != nil {
		t.Fatalf("payment attempt table missing after migration: %v", err)
	}
	for _, fragment := range []string{
		"provider_amount_minor BIGINT", "provider_currency TEXT", "conversion_rate   TEXT", "checkout_url      TEXT",
		"CREATE TABLE IF NOT EXISTS payment_order_attempts", "merchant_order_id TEXT PRIMARY KEY",
	} {
		if !strings.Contains(schemaPGSQL, fragment) {
			t.Fatalf("PostgreSQL schema missing %q", fragment)
		}
	}
}

func TestDeletePaymentOrderRequiresTerminalStatusAndCascadesEvents(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_delete_orders", "delete-orders@example.test")
	packageRecord, err := CreateCreditPackage(ctx, db, CreditPackage{
		Name: "Deletion package", Credits: 10, PriceAmountMinor: 100, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create credit package: %v", err)
	}
	_, method := createPaymentsTestMethod(t, ctx, db)
	createOrder := func(t *testing.T, status string) *PaymentOrder {
		t.Helper()
		order, createErr := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
			UserID: "u_delete_orders", PaymentMethodID: method.ID,
			ProductType: PaymentProductCreditPackage, ProductID: packageRecord.ID,
		})
		if createErr != nil {
			t.Fatalf("create %s order: %v", status, createErr)
		}
		if status != PaymentOrderPending {
			exec(t, db, `UPDATE payment_orders SET status=? WHERE id=?`, status, order.ID)
			order.Status = status
		}
		return order
	}

	for _, status := range []string{PaymentOrderPending, PaymentOrderProcessing} {
		order := createOrder(t, status)
		if err := DeletePaymentOrder(ctx, db, order.ID, false); !errors.Is(err, ErrPaymentOrderNotDeletable) {
			t.Fatalf("delete %s order error = %v, want %v", status, err, ErrPaymentOrderNotDeletable)
		}
		if _, err := GetPaymentOrder(ctx, db, order.ID); err != nil {
			t.Fatalf("protected %s order disappeared: %v", status, err)
		}
	}

	manualClose := createOrder(t, PaymentOrderCancelled)
	exec(t, db, `UPDATE payment_orders SET failure_code='admin_manual_close' WHERE id=?`, manualClose.ID)
	if err := DeletePaymentOrder(ctx, db, manualClose.ID, false); !errors.Is(err, ErrPaymentOrderDeleteNeedsAck) {
		t.Fatalf("delete recoverable manual-close order error = %v, want %v", err, ErrPaymentOrderDeleteNeedsAck)
	}
	protected, err := GetPaymentOrder(ctx, db, manualClose.ID)
	if err != nil {
		t.Fatalf("recoverable manual-close order deletion state = %+v, %v", protected, err)
	}
	canDelete, needsGatewayConfirmation := PaymentOrderDeletePolicy(*protected)
	if !canDelete || !needsGatewayConfirmation {
		t.Fatalf("recoverable manual-close order deletion policy = %v/%v", canDelete, needsGatewayConfirmation)
	}
	if err := DeletePaymentOrder(ctx, db, manualClose.ID, true); err != nil {
		t.Fatalf("delete gateway-confirmed manual-close order: %v", err)
	}
	if _, err := GetPaymentOrder(ctx, db, manualClose.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gateway-confirmed manual-close order still exists: %v", err)
	}

	for index, status := range []string{PaymentOrderFulfilled, PaymentOrderFailed, PaymentOrderExpired, PaymentOrderCancelled} {
		order := createOrder(t, status)
		eventID := fmt.Sprintf("pe_delete_%d", index)
		attemptID := fmt.Sprintf("pa_delete_%d", index)
		exec(t, db,
			`INSERT INTO payment_order_attempts(merchant_order_id, order_id, provider, channel_id) VALUES(?,?,?,?)`,
			attemptID, order.ID, order.Provider, order.ChannelID,
		)
		exec(t, db,
			`INSERT INTO payment_events(id, provider, channel_id, event_id, order_id) VALUES(?,?,?,?,?)`,
			eventID, order.Provider, order.ChannelID, "provider-event-"+eventID, order.ID,
		)
		if err := DeletePaymentOrder(ctx, db, order.ID, false); err != nil {
			t.Fatalf("delete %s order: %v", status, err)
		}
		if _, err := GetPaymentOrder(ctx, db, order.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get deleted %s order error = %v, want %v", status, err, ErrNotFound)
		}
		var eventCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_events WHERE order_id=?`, order.ID).Scan(&eventCount); err != nil {
			t.Fatalf("count events for deleted %s order: %v", status, err)
		}
		if eventCount != 0 {
			t.Fatalf("events remaining for deleted %s order = %d", status, eventCount)
		}
		var attemptCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_order_attempts WHERE order_id=?`, order.ID).Scan(&attemptCount); err != nil {
			t.Fatalf("count attempts for deleted %s order: %v", status, err)
		}
		if attemptCount != 0 {
			t.Fatalf("attempts remaining for deleted %s order = %d", status, attemptCount)
		}
	}

	if err := DeletePaymentOrder(ctx, db, "po_missing", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing order error = %v, want %v", err, ErrNotFound)
	}
}

func TestUserGroupPaymentRejectsCorruptBillingCycleSnapshot(t *testing.T) {
	db, ctx := openPaymentsTestDB(t)
	createPaymentsTestUser(t, db, "u_group_corrupt", "group-corrupt@example.com")
	group, err := CreateUserGroup(ctx, db, UserGroup{
		Name: "Corrupt cycle target", MonthlyPriceAmountMinor: 1000, YearlyPriceAmountMinor: 10000, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create target group: %v", err)
	}
	channel, method := createPaymentsTestMethod(t, ctx, db)
	existingExpiry := time.Now().UTC().AddDate(1, 0, 0).Unix()
	exec(t, db,
		`UPDATE users SET group_id=?, group_expires_at=?, previous_group_id='', token_ver=7 WHERE id=?`,
		group.ID, existingExpiry, "u_group_corrupt",
	)
	order, err := CreatePaymentOrder(ctx, db, PaymentOrderCreateInput{
		UserID: "u_group_corrupt", PaymentMethodID: method.ID,
		ProductType: PaymentProductUserGroup, ProductID: group.ID, BillingCycle: PaymentBillingMonthly,
	})
	if err != nil {
		t.Fatalf("create group order: %v", err)
	}
	order, err = MarkPaymentOrderProcessing(ctx, db, order.ID, "pi_corrupt_cycle")
	if err != nil {
		t.Fatalf("mark corrupt-cycle order processing: %v", err)
	}
	exec(t, db, `UPDATE payment_orders SET billing_cycle='weekly' WHERE id=?`, order.ID)

	amount := order.AmountMinor
	_, err = FulfillPaymentOrder(ctx, db, PaymentFulfillmentInput{
		PaymentEventInput: PaymentEventInput{
			Provider: order.Provider, ChannelID: channel.ID, EventID: "evt_corrupt_cycle", OrderID: order.ID,
			EventType: "checkout.session.completed",
		},
		ProviderOrderID: order.ProviderOrderID, AmountMinor: &amount, Currency: order.Currency,
	})
	if !errors.Is(err, ErrInvalidPaymentProduct) {
		t.Fatalf("corrupt billing cycle fulfillment error = %v, want %v", err, ErrInvalidPaymentProduct)
	}

	var groupID, previousGroup string
	var expiry int64
	var tokenVersion int
	if err := db.QueryRowContext(ctx,
		`SELECT group_id, group_expires_at, previous_group_id, token_ver FROM users WHERE id=?`,
		"u_group_corrupt",
	).Scan(&groupID, &expiry, &previousGroup, &tokenVersion); err != nil {
		t.Fatalf("read membership after rejected fulfillment: %v", err)
	}
	if groupID != group.ID || expiry != existingExpiry || previousGroup != "" || tokenVersion != 7 {
		t.Fatalf("membership changed after rejected fulfillment: group=%q expiry=%d previous=%q token=%d",
			groupID, expiry, previousGroup, tokenVersion)
	}
	events, err := ListPaymentEventsForOrder(ctx, db, order.ID)
	if err != nil {
		t.Fatalf("list rejected fulfillment events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("rejected corrupt snapshot persisted events: %+v", events)
	}
	stored, err := GetPaymentOrder(ctx, db, order.ID)
	if err != nil {
		t.Fatalf("get rejected corrupt order: %v", err)
	}
	if stored.Status != PaymentOrderProcessing || stored.PaidAt != 0 || stored.FulfilledAt != 0 {
		t.Fatalf("rejected corrupt order changed state: %+v", stored)
	}
}

func TestPaymentBackupScopes(t *testing.T) {
	full := map[string]bool{}
	for _, table := range BackupTableOrder() {
		full[table] = true
	}
	config := map[string]bool{}
	for _, table := range ConfigTableOrder() {
		config[table] = true
	}
	for _, table := range []string{"payment_channels", "payment_methods", "payment_orders", "payment_order_attempts", "payment_events"} {
		if !full[table] {
			t.Errorf("full backup omits %s", table)
		}
	}
	for _, table := range []string{"payment_channels", "payment_methods"} {
		if !config[table] {
			t.Errorf("config backup omits %s", table)
		}
	}
	for _, table := range []string{"payment_orders", "payment_order_attempts", "payment_events"} {
		if config[table] {
			t.Errorf("config backup contains financial data table %s", table)
		}
	}
}
