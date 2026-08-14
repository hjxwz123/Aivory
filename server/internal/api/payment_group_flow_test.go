package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/payment"
	"aivory/server/internal/store"
)

func TestEPayUserGroupCheckoutAndWebhookFlow(t *testing.T) {
	tests := []struct {
		name           string
		billingCycle   string
		amountMinor    int64
		formattedMoney string
		addYears       int
		addMonths      int
	}{
		{
			name:           "monthly",
			billingCycle:   store.PaymentBillingMonthly,
			amountMinor:    2199,
			formattedMoney: "21.99",
			addMonths:      1,
		},
		{
			name:           "yearly",
			billingCycle:   store.PaymentBillingYearly,
			amountMinor:    21990,
			formattedMoney: "219.90",
			addYears:       1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPaymentAPIFixture(t)
			group, err := store.CreateUserGroup(context.Background(), fx.db, store.UserGroup{
				ID:                      "ug_epay_" + tc.name,
				Name:                    "EPay Pro " + tc.name,
				Description:             "Purchasable EPay group",
				MonthlyPriceAmountMinor: 2199,
				YearlyPriceAmountMinor:  21990,
				IsPublic:                true,
			})
			if err != nil {
				t.Fatalf("create purchasable user group: %v", err)
			}

			body := fmt.Sprintf(
				`{"payment_method_id":%q,"target_type":%q,"target_id":%q,"billing_cycle":%q}`,
				fx.method.ID, store.PaymentProductUserGroup, group.ID, tc.billingCycle,
			)
			checkoutReq := httptest.NewRequest(
				http.MethodPost,
				"https://aivory.example.test/api/payments/checkout",
				strings.NewReader(body),
			)
			checkoutReq = paymentAPIRequest(checkoutReq, fx.user, nil)
			checkoutRec := httptest.NewRecorder()
			createPaymentCheckoutHandler(fx.d, checkoutRec, checkoutReq)
			if checkoutRec.Code != http.StatusCreated {
				t.Fatalf("create %s checkout status = %d, want %d; body=%s",
					tc.billingCycle, checkoutRec.Code, http.StatusCreated, checkoutRec.Body.String())
			}

			var checkout struct {
				OrderID string                 `json:"order_id"`
				Action  payment.CheckoutAction `json:"action"`
			}
			if err := json.Unmarshal(checkoutRec.Body.Bytes(), &checkout); err != nil {
				t.Fatalf("decode %s checkout: %v (%s)", tc.billingCycle, err, checkoutRec.Body.String())
			}
			if checkout.OrderID == "" {
				t.Fatalf("%s checkout response has no order id", tc.billingCycle)
			}
			if checkout.Action.Type != payment.ActionFormPost ||
				checkout.Action.URL != "https://epay.example.test/gateway/submit.php" {
				t.Fatalf("%s checkout action = %+v", tc.billingCycle, checkout.Action)
			}
			if checkout.Action.Fields["out_trade_no"] != checkout.OrderID ||
				checkout.Action.Fields["name"] != group.Name ||
				checkout.Action.Fields["money"] != tc.formattedMoney ||
				checkout.Action.Fields["type"] != "alipay" {
				t.Fatalf("%s EPay checkout fields = %+v", tc.billingCycle, checkout.Action.Fields)
			}

			order, err := store.GetPaymentOrder(context.Background(), fx.db, checkout.OrderID)
			if err != nil {
				t.Fatalf("get %s checkout order: %v", tc.billingCycle, err)
			}
			if order.ProductType != store.PaymentProductUserGroup || order.ProductID != group.ID ||
				order.ProductName != group.Name || order.UserGroupID != group.ID ||
				order.BillingCycle != tc.billingCycle || order.AmountMinor != tc.amountMinor ||
				order.Currency != "USD" || order.Status != store.PaymentOrderProcessing {
				t.Fatalf("%s checkout snapshot = %+v", tc.billingCycle, order)
			}

			callbackParams := map[string]string{
				"pid":          testEPayMerchantID,
				"type":         "alipay",
				"out_trade_no": checkout.OrderID,
				"trade_no":     "epay_group_" + tc.name,
				"trade_status": "TRADE_SUCCESS",
				"money":        tc.formattedMoney,
			}
			beforeCallback := time.Now().UTC().Unix()
			callbackRec := serveEPayCallback(t, fx, cloneStringMap(callbackParams))
			afterCallback := time.Now().UTC().Unix()
			if callbackRec.Code != http.StatusOK || strings.TrimSpace(callbackRec.Body.String()) != "success" {
				t.Fatalf("%s callback = status %d body %q", tc.billingCycle, callbackRec.Code, callbackRec.Body.String())
			}

			var groupID, previousGroupID string
			var groupExpiresAt int64
			var tokenVersion int
			if err := fx.db.QueryRow(
				`SELECT group_id, group_expires_at, previous_group_id, token_ver FROM users WHERE id=?`,
				fx.user.ID,
			).Scan(&groupID, &groupExpiresAt, &previousGroupID, &tokenVersion); err != nil {
				t.Fatalf("query %s fulfilled membership: %v", tc.billingCycle, err)
			}
			minimumExpiry := time.Unix(beforeCallback, 0).UTC().AddDate(tc.addYears, tc.addMonths, 0).Unix()
			maximumExpiry := time.Unix(afterCallback, 0).UTC().AddDate(tc.addYears, tc.addMonths, 0).Unix()
			if groupID != group.ID || previousGroupID != "" || tokenVersion != fx.user.TokenVer ||
				groupExpiresAt < minimumExpiry || groupExpiresAt > maximumExpiry {
				t.Fatalf(
					"%s fulfilled membership = group %q previous %q expiry %d token %d; want group %q, empty previous, expiry %d..%d, token %d",
					tc.billingCycle, groupID, previousGroupID, groupExpiresAt, tokenVersion,
					group.ID, minimumExpiry, maximumExpiry, fx.user.TokenVer,
				)
			}

			historyReq := httptest.NewRequest(http.MethodGet, "/api/payments/orders?limit=10&offset=0", nil)
			historyReq = paymentAPIRequest(historyReq, fx.user, nil)
			historyRec := httptest.NewRecorder()
			listPaymentOrdersForUserHandler(fx.d, historyRec, historyReq)
			if historyRec.Code != http.StatusOK {
				t.Fatalf("list %s payment history status = %d; body=%s",
					tc.billingCycle, historyRec.Code, historyRec.Body.String())
			}
			var history struct {
				Orders []publicPaymentOrderListItem `json:"orders"`
				Total  int                          `json:"total"`
			}
			if err := json.Unmarshal(historyRec.Body.Bytes(), &history); err != nil {
				t.Fatalf("decode %s payment history: %v (%s)", tc.billingCycle, err, historyRec.Body.String())
			}
			if history.Total != 1 || len(history.Orders) != 1 {
				t.Fatalf("%s payment history = total %d orders %+v", tc.billingCycle, history.Total, history.Orders)
			}
			historyOrder := history.Orders[0]
			if historyOrder.ID != checkout.OrderID || historyOrder.Status != "paid" ||
				historyOrder.Provider != payment.ProviderEPay || historyOrder.MethodName != fx.method.Name ||
				historyOrder.TargetType != store.PaymentProductUserGroup || historyOrder.TargetName != group.Name ||
				historyOrder.BillingCycle != tc.billingCycle || historyOrder.AmountMinor != tc.amountMinor ||
				historyOrder.Currency != "USD" || historyOrder.PaidAt == 0 {
				t.Fatalf("%s payment history order = %+v", tc.billingCycle, historyOrder)
			}

			duplicateRec := serveEPayCallback(t, fx, cloneStringMap(callbackParams))
			if duplicateRec.Code != http.StatusOK || strings.TrimSpace(duplicateRec.Body.String()) != "success" {
				t.Fatalf("duplicate %s callback = status %d body %q",
					tc.billingCycle, duplicateRec.Code, duplicateRec.Body.String())
			}
			var duplicateGroupID, duplicatePreviousGroupID string
			var duplicateExpiresAt int64
			var duplicateTokenVersion int
			if err := fx.db.QueryRow(
				`SELECT group_id, group_expires_at, previous_group_id, token_ver FROM users WHERE id=?`,
				fx.user.ID,
			).Scan(&duplicateGroupID, &duplicateExpiresAt, &duplicatePreviousGroupID, &duplicateTokenVersion); err != nil {
				t.Fatalf("query membership after duplicate %s callback: %v", tc.billingCycle, err)
			}
			if duplicateGroupID != groupID || duplicatePreviousGroupID != previousGroupID ||
				duplicateExpiresAt != groupExpiresAt || duplicateTokenVersion != tokenVersion {
				t.Fatalf(
					"duplicate %s callback changed membership from %q/%q/%d/%d to %q/%q/%d/%d",
					tc.billingCycle, groupID, previousGroupID, groupExpiresAt, tokenVersion,
					duplicateGroupID, duplicatePreviousGroupID, duplicateExpiresAt, duplicateTokenVersion,
				)
			}
			var eventCount int
			if err := fx.db.QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=?`, checkout.OrderID).Scan(&eventCount); err != nil {
				t.Fatalf("count %s payment events: %v", tc.billingCycle, err)
			}
			if eventCount != 1 {
				t.Fatalf("%s payment event count = %d, want 1 after duplicate callback", tc.billingCycle, eventCount)
			}
		})
	}
}

func TestPaymentCheckoutRejectsPermanentUserGroupAndAllowsFiniteRenewal(t *testing.T) {
	tests := []struct {
		billingCycle string
		amountMinor  int64
	}{
		{billingCycle: store.PaymentBillingMonthly, amountMinor: 3100},
		{billingCycle: store.PaymentBillingYearly, amountMinor: 31000},
	}

	for _, tc := range tests {
		t.Run(tc.billingCycle, func(t *testing.T) {
			fx := newPaymentAPIFixture(t)
			group, err := store.CreateUserGroup(context.Background(), fx.db, store.UserGroup{
				ID:                      "ug_permanent_" + tc.billingCycle,
				Name:                    "Permanent Pro " + tc.billingCycle,
				MonthlyPriceAmountMinor: 3100,
				YearlyPriceAmountMinor:  31000,
				IsPublic:                true,
			})
			if err != nil {
				t.Fatalf("create permanent-group checkout target: %v", err)
			}
			mustExec(t, fx.db,
				`UPDATE users SET group_id=?, group_expires_at=0, previous_group_id='' WHERE id=?`,
				group.ID, fx.user.ID,
			)

			checkout := func() *httptest.ResponseRecorder {
				t.Helper()
				body := fmt.Sprintf(
					`{"payment_method_id":%q,"target_type":%q,"target_id":%q,"billing_cycle":%q}`,
					fx.method.ID, store.PaymentProductUserGroup, group.ID, tc.billingCycle,
				)
				req := httptest.NewRequest(
					http.MethodPost,
					"https://aivory.example.test/api/payments/checkout",
					strings.NewReader(body),
				)
				req = paymentAPIRequest(req, fx.user, nil)
				rec := httptest.NewRecorder()
				createPaymentCheckoutHandler(fx.d, rec, req)
				return rec
			}

			permanentRec := checkout()
			if permanentRec.Code != http.StatusConflict {
				t.Fatalf("permanent-group %s checkout status = %d, want %d; body=%s",
					tc.billingCycle, permanentRec.Code, http.StatusConflict, permanentRec.Body.String())
			}
			var failure map[string]string
			if err := json.Unmarshal(permanentRec.Body.Bytes(), &failure); err != nil {
				t.Fatalf("decode permanent-group %s rejection: %v (%s)",
					tc.billingCycle, err, permanentRec.Body.String())
			}
			if failure["error"] != store.ErrPaymentUserGroupPermanent.Error() {
				t.Fatalf("permanent-group %s error = %q, want %q",
					tc.billingCycle, failure["error"], store.ErrPaymentUserGroupPermanent.Error())
			}
			var orderCount int
			if err := fx.db.QueryRow(`SELECT COUNT(*) FROM payment_orders WHERE user_id=?`, fx.user.ID).Scan(&orderCount); err != nil {
				t.Fatalf("count rejected permanent-group %s orders: %v", tc.billingCycle, err)
			}
			if orderCount != 0 {
				t.Fatalf("permanent-group %s checkout created %d orders, want 0", tc.billingCycle, orderCount)
			}

			finiteExpiry := time.Now().UTC().AddDate(0, 2, 0).Unix()
			mustExec(t, fx.db, `UPDATE users SET group_expires_at=? WHERE id=?`, finiteExpiry, fx.user.ID)
			finiteRec := checkout()
			if finiteRec.Code != http.StatusCreated {
				t.Fatalf("finite-group %s checkout status = %d, want %d; body=%s",
					tc.billingCycle, finiteRec.Code, http.StatusCreated, finiteRec.Body.String())
			}
			var success struct {
				OrderID string `json:"order_id"`
			}
			if err := json.Unmarshal(finiteRec.Body.Bytes(), &success); err != nil {
				t.Fatalf("decode finite-group %s checkout: %v (%s)", tc.billingCycle, err, finiteRec.Body.String())
			}
			order, err := store.GetPaymentOrder(context.Background(), fx.db, success.OrderID)
			if err != nil {
				t.Fatalf("get finite-group %s order: %v", tc.billingCycle, err)
			}
			if order.UserGroupID != group.ID || order.BillingCycle != tc.billingCycle ||
				order.AmountMinor != tc.amountMinor || order.Status != store.PaymentOrderProcessing {
				t.Fatalf("finite-group %s checkout order = %+v", tc.billingCycle, order)
			}
			if err := fx.db.QueryRow(`SELECT COUNT(*) FROM payment_orders WHERE user_id=?`, fx.user.ID).Scan(&orderCount); err != nil {
				t.Fatalf("count finite-group %s orders: %v", tc.billingCycle, err)
			}
			if orderCount != 1 {
				t.Fatalf("finite-group %s checkout order count = %d, want 1", tc.billingCycle, orderCount)
			}
		})
	}
}

func TestPaymentCheckoutAndResumeRejectNonPurchasableUserGroup(t *testing.T) {
	fx := newPaymentAPIFixture(t)
	group, err := store.CreateUserGroup(context.Background(), fx.db, store.UserGroup{
		ID:                      "ug_paused_purchase",
		Name:                    "Paused purchase",
		MonthlyPriceAmountMinor: 1200,
		YearlyPriceAmountMinor:  12000,
		IsPublic:                true,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	paused := false
	if _, err := store.UpdateUserGroup(context.Background(), fx.db, group.ID, store.UserGroupPatch{IsPurchasable: &paused}); err != nil {
		t.Fatalf("pause group purchase: %v", err)
	}
	checkout := func() *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"payment_method_id":%q,"target_type":%q,"target_id":%q,"billing_cycle":%q}`,
			fx.method.ID, store.PaymentProductUserGroup, group.ID, store.PaymentBillingMonthly)
		req := httptest.NewRequest(http.MethodPost, "https://aivory.example.test/api/payments/checkout", strings.NewReader(body))
		req = paymentAPIRequest(req, fx.user, nil)
		rec := httptest.NewRecorder()
		createPaymentCheckoutHandler(fx.d, rec, req)
		return rec
	}

	blocked := checkout()
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), store.ErrPaymentUserGroupNotPurchasable.Error()) {
		t.Fatalf("paused-group checkout = status %d body=%s, want 409/%q", blocked.Code, blocked.Body.String(), store.ErrPaymentUserGroupNotPurchasable)
	}
	var count int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM payment_orders WHERE user_id=?`, fx.user.ID).Scan(&count); err != nil {
		t.Fatalf("count orders after blocked checkout: %v", err)
	}
	if count != 0 {
		t.Fatalf("blocked checkout created %d orders, want 0", count)
	}

	enabled := true
	if _, err := store.UpdateUserGroup(context.Background(), fx.db, group.ID, store.UserGroupPatch{IsPurchasable: &enabled}); err != nil {
		t.Fatalf("resume group purchase: %v", err)
	}
	created := checkout()
	if created.Code != http.StatusCreated {
		t.Fatalf("enabled-group checkout = status %d, want %d; body=%s", created.Code, http.StatusCreated, created.Body.String())
	}
	var response struct {
		OrderID string `json:"order_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil || response.OrderID == "" {
		t.Fatalf("decode created checkout: order=%+v err=%v body=%s", response, err, created.Body.String())
	}
	if _, err := store.UpdateUserGroup(context.Background(), fx.db, group.ID, store.UserGroupPatch{IsPurchasable: &paused}); err != nil {
		t.Fatalf("pause group before resume: %v", err)
	}
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/payments/orders/"+response.OrderID+"/resume", nil)
	resumeReq = paymentAPIRequest(resumeReq, fx.user, map[string]string{"id": response.OrderID})
	resumeRec := httptest.NewRecorder()
	resumePaymentOrderHandler(fx.d, resumeRec, resumeReq)
	if resumeRec.Code != http.StatusConflict || !strings.Contains(resumeRec.Body.String(), store.ErrPaymentUserGroupNotPurchasable.Error()) {
		t.Fatalf("paused-group resume = status %d body=%s, want 409/%q", resumeRec.Code, resumeRec.Body.String(), store.ErrPaymentUserGroupNotPurchasable)
	}
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM payment_order_attempts WHERE order_id=?`, response.OrderID).Scan(&count); err != nil {
		t.Fatalf("count attempts after blocked resume: %v", err)
	}
	if count != 1 {
		t.Fatalf("blocked resume created %d payment attempts, want only original attempt", count)
	}
}
