package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/payment"
	"aivory/server/internal/store"
)

const paymentWebhookBodyLimit int64 = 1 << 20

var errPaymentProviderReferencePending = errors.New("payment provider reference is not mapped yet")

const (
	paymentCheckoutProviderTimeout = 45 * time.Second
	paymentCheckoutStateTimeout    = 10 * time.Second
)

func paymentCheckoutFailureDetails(err error) (int, error, string, string) {
	if errors.Is(err, payment.ErrWaffoProductCurrencyUnsupported) {
		return http.StatusUnprocessableEntity, payment.ErrWaffoProductCurrencyUnsupported,
			payment.ErrWaffoProductCurrencyUnsupported.Error(),
			"Waffo product does not support the configured checkout currency"
	}
	return http.StatusBadGateway, errors.New("payment_checkout_unavailable"),
		"provider_checkout_failed", "Payment provider could not create checkout"
}

type publicPaymentMethod struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Provider string `json:"provider"`
}

type publicPaymentOrder struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Provider       string `json:"provider"`
	MethodName     string `json:"method_name"`
	MethodType     string `json:"method_type"`
	TargetType     string `json:"target_type"`
	TargetName     string `json:"target_name"`
	BillingCycle   string `json:"billing_cycle"`
	AmountMinor    int64  `json:"amount_minor"`
	TaxAmountMinor int64  `json:"tax_amount_minor,omitempty"`
	Currency       string `json:"currency"`
	FailureReason  string `json:"failure_reason,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	PaidAt         int64  `json:"paid_at,omitempty"`
	FulfilledAt    int64  `json:"fulfilled_at,omitempty"`
}

type publicPaymentOrderListItem struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Provider       string `json:"provider"`
	MethodName     string `json:"method_name"`
	MethodType     string `json:"method_type"`
	TargetType     string `json:"target_type"`
	TargetName     string `json:"target_name"`
	AmountMinor    int64  `json:"amount_minor"`
	TaxAmountMinor int64  `json:"tax_amount_minor,omitempty"`
	Currency       string `json:"currency"`
	BillingCycle   string `json:"billing_cycle"`
	CreatedAt      int64  `json:"created_at"`
	PaidAt         int64  `json:"paid_at"`
}

func listPaymentMethodsPublic(d Deps, w http.ResponseWriter, r *http.Request) {
	targetType := strings.TrimSpace(r.URL.Query().Get("target_type"))
	if targetType != store.PaymentProductCreditPackage && targetType != store.PaymentProductUserGroup {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	methods, err := store.ListEnabledPaymentMethods(r.Context(), d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	currency := globalSettlementCurrency(d)
	user := authUser(r)
	visible := make([]publicPaymentMethod, 0, len(methods))
	for _, method := range methods {
		channel, channelErr := store.GetPaymentChannel(r.Context(), d.DB, method.ChannelID)
		if channelErr != nil || channel.Provider != method.Provider || !channel.Enabled {
			continue
		}
		if channel.Environment == store.PaymentEnvironmentTest && user.Role != "admin" {
			continue
		}
		storedMethod, methodErr := store.GetPaymentMethod(r.Context(), d.DB, method.ID)
		if methodErr != nil || !storedMethod.Enabled || storedMethod.ChannelID != channel.ID {
			continue
		}
		if !paymentChannelSupportsCurrency(channel.Provider, channel.Config, currency) {
			continue
		}
		if _, gatewayErr := paymentGateway(channel.Provider, channel.Config, storedMethod.ProviderMethodConfig); gatewayErr != nil {
			continue
		}
		visible = append(visible, publicPaymentMethod{
			ID: method.ID, Name: method.Name, Icon: method.Icon, Provider: method.Provider,
		})
	}
	// The new payment dialog is controlled only by card_purchase_url. Legacy
	// group_buy_url/credit_buy_url values must not silently create a card option.
	cardURL := strings.TrimSpace(globalSettingStr(d, "card_purchase_url"))
	if cardURL != "" && !validPaymentHTTPURL(cardURL) {
		cardURL = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"methods": visible, "card_purchase_url": cardURL,
	})
}

func createPaymentCheckoutHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		PaymentMethodID string `json:"payment_method_id"`
		TargetType      string `json:"target_type"`
		TargetID        string `json:"target_id"`
		BillingCycle    string `json:"billing_cycle"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	req.PaymentMethodID = strings.TrimSpace(req.PaymentMethodID)
	req.TargetType = strings.TrimSpace(req.TargetType)
	req.TargetID = strings.TrimSpace(req.TargetID)
	req.BillingCycle = strings.TrimSpace(req.BillingCycle)
	if req.PaymentMethodID == "" || req.TargetID == "" || len(req.PaymentMethodID) > 100 || len(req.TargetID) > 100 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	order, err := store.CreatePaymentOrder(r.Context(), d.DB, store.PaymentOrderCreateInput{
		UserID: authUser(r).ID, PaymentMethodID: req.PaymentMethodID,
		ProductType: req.TargetType, ProductID: req.TargetID, BillingCycle: req.BillingCycle,
	})
	if errors.Is(err, store.ErrInvalidPaymentProduct) {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if errors.Is(err, store.ErrPaymentMethodUnavailable) || errors.Is(err, store.ErrPaymentProductUnavailable) ||
		errors.Is(err, store.ErrPaymentUserUnavailable) ||
		errors.Is(err, store.ErrPaymentUserGroupPermanent) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	orderStateCtx, orderStateCancel := paymentDetachedContext(r, paymentCheckoutStateTimeout)
	channel, err := store.GetPaymentChannel(orderStateCtx, d.DB, order.ChannelID)
	if err != nil || channel.Provider != order.Provider || !channel.Enabled {
		_, _ = store.MarkPaymentOrderFailed(orderStateCtx, d.DB, order.ID, "channel_unavailable", "Payment channel is unavailable")
		orderStateCancel()
		writeError(w, http.StatusConflict, store.ErrPaymentMethodUnavailable)
		return
	}
	gateway, err := paymentGateway(order.Provider, channel.Config, order.MethodConfig)
	if err != nil {
		_, _ = store.MarkPaymentOrderFailed(orderStateCtx, d.DB, order.ID, "channel_invalid", "Payment channel configuration is invalid")
		orderStateCancel()
		writeError(w, http.StatusConflict, store.ErrPaymentMethodUnavailable)
		return
	}
	orderStateCancel()
	baseURL := paymentReturnBaseURL(d, r)
	returnQuery := url.Values{"payment": {"return"}, "order": {order.ID}}
	cancelQuery := url.Values{"payment": {"cancel"}, "order": {order.ID}}
	taxCategory := payment.TaxCategoryDigitalGoods
	if order.ProductType == store.PaymentProductUserGroup {
		taxCategory = payment.TaxCategorySaaS
	}
	providerCtx, providerCancel := paymentDetachedContext(r, paymentCheckoutProviderTimeout)
	action, err := createPaymentCheckout(providerCtx, gateway, payment.CheckoutRequest{
		OrderID: order.ID, Name: order.ProductName, AmountMinor: order.ProviderAmountMinor, Currency: order.ProviderCurrency,
		TaxCategory: taxCategory,
		UserID:      order.UserID, UserEmail: order.UserEmail,
		NotifyURL:  paymentAbsoluteWebhookURL(r, order.ChannelID),
		SuccessURL: baseURL + "/subscription?" + returnQuery.Encode(),
		CancelURL:  baseURL + "/subscription?" + cancelQuery.Encode(),
	})
	providerCancel()
	if err != nil {
		stateCtx, stateCancel := paymentDetachedContext(r, paymentCheckoutStateTimeout)
		responseStatus, responseErr, failureCode, failureMessage := paymentCheckoutFailureDetails(err)
		if payment.IsCheckoutStateUnknown(err) {
			_, _ = store.MarkPaymentOrderProcessing(stateCtx, d.DB, order.ID, "")
		} else {
			_, _ = store.MarkPaymentOrderFailed(stateCtx, d.DB, order.ID, failureCode, failureMessage)
		}
		stateCancel()
		slog.Error("payment checkout creation failed", "provider", order.Provider, "channel_id", order.ChannelID, "order_id", order.ID, "err", err)
		writeError(w, responseStatus, responseErr)
		return
	}
	if !validAbsolutePaymentHTTPURL(action.URL) || (action.Type != payment.ActionRedirect && action.Type != payment.ActionFormPost) {
		stateCtx, stateCancel := paymentDetachedContext(r, paymentCheckoutStateTimeout)
		_, _ = store.MarkPaymentOrderFailed(stateCtx, d.DB, order.ID, "provider_checkout_invalid", "Payment provider returned an invalid checkout action")
		stateCancel()
		writeError(w, http.StatusBadGateway, errors.New("payment_checkout_unavailable"))
		return
	}
	stateCtx, stateCancel := paymentDetachedContext(r, paymentCheckoutStateTimeout)
	err = markPaymentCheckoutStarted(stateCtx, d, order.ID, action.ProviderOrderID, action.SessionID, action.ExpiresAt)
	stateCancel()
	if err != nil {
		slog.Error("payment checkout state update failed", "provider", order.Provider, "channel_id", order.ChannelID, "order_id", order.ID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	action.ProviderOrderID = ""
	action.SessionID = ""
	action.ExpiresAt = 0
	writeJSON(w, http.StatusCreated, map[string]any{"order_id": order.ID, "action": action})
}

func paymentDetachedContext(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
}

func markPaymentCheckoutStarted(ctx context.Context, d Deps, orderID, providerOrderID, sessionID string, expiresAt int64) error {
	order, err := store.MarkPaymentOrderCheckoutStarted(ctx, d.DB, orderID, providerOrderID, sessionID, expiresAt)
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrPaymentOrderNotMutable) || order == nil || order.Status != store.PaymentOrderFulfilled {
		return err
	}
	// A provider can deliver its webhook before CreateCheckout returns. Treat
	// that fulfilled order as success, but never hide a conflicting provider ID.
	if providerOrderID != "" && order.ProviderOrderID != providerOrderID {
		return store.ErrPaymentProviderOrderMismatch
	}
	return nil
}

func getPaymentOrderHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	order, err := store.GetPaymentOrderForUser(r.Context(), d.DB, pathParam(r, "id"), authUser(r).ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, publicPaymentOrderResponse(*order))
}

func listPaymentOrdersForUserHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := 10
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		limit = parsed
	}
	offset := 0
	if raw := query.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		offset = parsed
	}

	filter := store.PaymentOrderFilter{
		UserID: authUser(r).ID,
		Limit:  limit,
		Offset: offset,
	}
	orders, err := store.ListPaymentOrders(r.Context(), d.DB, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	total, err := store.CountPaymentOrders(r.Context(), d.DB, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	response := make([]publicPaymentOrderListItem, 0, len(orders))
	for _, order := range orders {
		response = append(response, publicPaymentOrderListResponse(order))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"orders": response,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func paymentWebhookHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	channelID := strings.TrimSpace(pathParam(r, "channelId"))
	channel, err := store.GetPaymentChannel(r.Context(), d.DB, channelID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	provider, err := normalizePaymentProvider(channel.Provider)
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	event, err := verifyProviderWebhook(w, r, provider, channel.Config)
	if err != nil {
		slog.Warn("payment webhook verification failed", "provider", provider, "channel_id", channelID, "err", err)
		writePaymentWebhookResponse(w, provider, channel.Config, false, webhookFailureStatus(provider, true))
		return
	}
	if event.Status == payment.EventIgnored {
		slog.Info("payment webhook event ignored", "provider", provider, "channel_id", channelID, "event_type", event.Type)
		writePaymentWebhookResponse(w, provider, channel.Config, true, http.StatusOK)
		return
	}
	if event.ID == "" || (event.OrderID == "" && event.ProviderPaymentID == "") {
		writePaymentWebhookResponse(w, provider, channel.Config, false, webhookFailureStatus(provider, true))
		return
	}
	appliedOrder, err := applyProviderEvent(r, d, provider, channelID, event)
	if err != nil {
		slog.Error("payment webhook processing failed", "provider", provider, "channel_id", channelID, "order_id", event.OrderID, "event_id", event.ID, "err", err)
		writePaymentWebhookResponse(w, provider, channel.Config, false, webhookFailureStatus(provider, isPermanentPaymentEventError(err)))
		return
	}
	if appliedOrder != nil && appliedOrder.UserID != "" {
		invalidateAuthUser(d, appliedOrder.UserID)
	}
	writePaymentWebhookResponse(w, provider, channel.Config, true, http.StatusOK)
}

func verifyProviderWebhook(w http.ResponseWriter, r *http.Request, provider string, rawConfig json.RawMessage) (payment.ProviderEvent, error) {
	switch provider {
	case payment.ProviderStripe:
		body, err := readPaymentWebhookBody(w, r)
		if err != nil {
			return payment.ProviderEvent{}, err
		}
		var cfg payment.StripeConfig
		if json.Unmarshal(rawConfig, &cfg) != nil || payment.ValidateStripeConfig(cfg) != nil {
			return payment.ProviderEvent{}, errors.New("invalid Stripe channel configuration")
		}
		return payment.VerifyStripeEvent(body, r.Header.Get("Stripe-Signature"), cfg)
	case payment.ProviderEPay:
		var cfg payment.EPayConfig
		if json.Unmarshal(rawConfig, &cfg) != nil || payment.ValidateEPayConfig(cfg) != nil {
			return payment.ProviderEvent{}, errors.New("invalid EPay channel configuration")
		}
		params, err := paymentFormValues(w, r)
		if err != nil {
			return payment.ProviderEvent{}, err
		}
		return payment.VerifyEPayEvent(params, cfg)
	case payment.ProviderWaffo:
		body, err := readPaymentWebhookBody(w, r)
		if err != nil {
			return payment.ProviderEvent{}, err
		}
		var cfg payment.WaffoConfig
		if json.Unmarshal(rawConfig, &cfg) != nil || payment.ValidateWaffoConfig(cfg) != nil {
			return payment.ProviderEvent{}, errors.New("invalid Waffo channel configuration")
		}
		return payment.VerifyWaffoEvent(body, r.Header.Get("X-Waffo-Signature"), cfg)
	default:
		return payment.ProviderEvent{}, errors.New("unsupported payment provider")
	}
}

func applyProviderEvent(r *http.Request, d Deps, provider, channelID string, event payment.ProviderEvent) (*store.PaymentOrder, error) {
	order, err := paymentOrderForProviderEvent(r, d, provider, channelID, event)
	if err != nil {
		return nil, err
	}
	event.OrderID = order.ID
	event, err = normalizeProviderEventForOrder(*order, provider, event)
	if err != nil {
		return nil, err
	}
	if err := validateProviderEvent(*order, provider, channelID, event); err != nil {
		return nil, err
	}
	eventInput := store.PaymentEventInput{
		Provider: provider, ChannelID: channelID, EventID: event.ID, OrderID: event.OrderID, EventType: event.Type,
	}
	if event.Status == payment.EventPaid {
		amount := event.AmountMinor
		paidAmount := event.PaidAmountMinor
		if paidAmount == 0 {
			paidAmount = amount
		}
		taxAmount := event.TaxAmountMinor
		currency := event.Currency
		if provider == payment.ProviderEPay {
			// EPay's signed amount is in the provider currency. Entitlements and
			// user-visible history remain denominated in the settlement snapshot.
			amount = order.AmountMinor
			paidAmount = order.AmountMinor
			taxAmount = 0
			currency = order.Currency
		}
		result, err := store.FulfillPaymentOrder(r.Context(), d.DB, store.PaymentFulfillmentInput{
			PaymentEventInput: eventInput, ProviderOrderID: event.ProviderOrderID, ProviderPaymentID: event.ProviderPaymentID,
			AmountMinor: &amount, PaidAmountMinor: &paidAmount, TaxAmountMinor: &taxAmount, Currency: currency,
		})
		if err != nil {
			return nil, err
		}
		if result.Applied {
			return &result.Order, nil
		}
		return nil, nil
	}
	eventRow, _, err := store.RecordPaymentEvent(r.Context(), d.DB, eventInput)
	if err != nil {
		return nil, err
	}
	terminal := order.Status == store.PaymentOrderFulfilled || order.Status == store.PaymentOrderExpired ||
		order.Status == store.PaymentOrderCancelled || order.Status == store.PaymentOrderFailed
	if !terminal {
		switch event.Status {
		case payment.EventProcessing:
			order, err = store.MarkPaymentOrderProcessing(r.Context(), d.DB, order.ID, event.ProviderOrderID)
		case payment.EventFailed:
			message := strings.TrimSpace(event.FailureReason)
			if message == "" || len(message) > 500 {
				message = "Payment provider reported a failed payment"
			}
			order, err = store.MarkPaymentOrderFailed(r.Context(), d.DB, order.ID, "provider_payment_failed", message)
		case payment.EventExpired:
			order, err = store.MarkPaymentOrderExpired(r.Context(), d.DB, order.ID, event.ProviderOrderID)
		default:
			err = errors.New("unsupported payment event status")
		}
		if err != nil {
			return nil, err
		}
	}
	if err := store.MarkPaymentEventProcessed(r.Context(), d.DB, eventRow.ID); err != nil {
		return nil, err
	}
	return nil, nil
}

func normalizeProviderEventForOrder(order store.PaymentOrder, provider string, event payment.ProviderEvent) (payment.ProviderEvent, error) {
	if provider != payment.ProviderEPay {
		return event, nil
	}
	amount, err := payment.ParseMinorAmount(event.AmountMajor, order.ProviderCurrency)
	if err != nil {
		return event, err
	}
	event.AmountMinor = amount
	event.PaidAmountMinor = amount
	event.TaxAmountMinor = 0
	event.Currency = order.ProviderCurrency
	return event, nil
}

func paymentOrderForProviderEvent(r *http.Request, d Deps, provider, channelID string, event payment.ProviderEvent) (*store.PaymentOrder, error) {
	if strings.TrimSpace(event.OrderID) != "" {
		return store.GetPaymentOrder(r.Context(), d.DB, event.OrderID)
	}
	order, err := store.GetPaymentOrderByProviderPaymentID(
		r.Context(), d.DB, provider, channelID, event.ProviderPaymentID,
	)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", errPaymentProviderReferencePending, event.ProviderPaymentID)
	}
	return order, err
}

func validateProviderEvent(order store.PaymentOrder, provider, channelID string, event payment.ProviderEvent) error {
	if order.Provider != provider || order.ChannelID != channelID {
		return store.ErrPaymentEventConflict
	}
	expectedAmount := order.AmountMinor
	expectedCurrency := order.Currency
	if provider == payment.ProviderEPay {
		expectedAmount = order.ProviderAmountMinor
		expectedCurrency = order.ProviderCurrency
	}
	if expectedAmount != event.AmountMinor {
		return store.ErrPaymentAmountMismatch
	}
	if !strings.EqualFold(expectedCurrency, event.Currency) {
		return store.ErrPaymentCurrencyMismatch
	}
	if order.ProviderOrderID != "" && event.ProviderOrderID != "" && order.ProviderOrderID != event.ProviderOrderID {
		return store.ErrPaymentProviderOrderMismatch
	}
	if order.ProviderPaymentID != "" && event.ProviderPaymentID != "" && order.ProviderPaymentID != event.ProviderPaymentID {
		return store.ErrPaymentProviderOrderMismatch
	}
	switch provider {
	case payment.ProviderEPay:
		var method payment.EPayMethodConfig
		if json.Unmarshal(order.MethodConfig, &method) != nil || strings.TrimSpace(method.Type) == "" || method.Type != event.MethodType {
			return fmt.Errorf("%w: EPay payment method does not match the order", store.ErrInvalidPaymentEvent)
		}
	case payment.ProviderWaffo:
		if order.UserID != "" && strings.TrimSpace(event.UserID) != payment.WaffoBuyerIdentity(order.UserID) {
			return fmt.Errorf("%w: Waffo buyer identity does not match the order", store.ErrInvalidPaymentEvent)
		}
	}
	return nil
}

func readPaymentWebhookBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, paymentWebhookBodyLimit)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("empty payment webhook body")
	}
	return body, nil
}

func paymentFormValues(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	sources := []url.Values{r.URL.Query()}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, paymentWebhookBodyLimit)
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		sources = append(sources, r.PostForm)
	}
	params := map[string]string{}
	for _, values := range sources {
		for key, items := range values {
			if key == "" || len(items) != 1 {
				return nil, errors.New("duplicate or invalid EPay webhook parameter")
			}
			if _, exists := params[key]; exists {
				return nil, errors.New("duplicate or invalid EPay webhook parameter")
			}
			params[key] = items[0]
		}
	}
	if len(params) == 0 {
		return nil, errors.New("empty EPay webhook parameters")
	}
	return params, nil
}

// paymentReturnBaseURL keeps provider callbacks on the API origin while
// returning the buyer to a separately hosted frontend when its Origin is in
// the server's CORS allowlist. An untrusted or malformed Origin is ignored.
func paymentReturnBaseURL(d Deps, r *http.Request) string {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if validPaymentReturnOrigin(origin) {
		for _, allowed := range d.Config.AllowedOrigins {
			if strings.EqualFold(origin, strings.TrimRight(strings.TrimSpace(allowed), "/")) {
				return origin
			}
		}
	}
	return strings.TrimRight(externalBaseURL(r), "/")
}

func validPaymentReturnOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host != "" && parsed.User == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") &&
		(parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" && parsed.Fragment == ""
}

func writePaymentWebhookResponse(w http.ResponseWriter, provider string, rawConfig json.RawMessage, success bool, status int) {
	switch provider {
	case payment.ProviderEPay:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if success {
			_, _ = io.WriteString(w, "success")
		} else {
			_, _ = io.WriteString(w, "fail")
		}
	default:
		if success {
			writeJSON(w, http.StatusOK, map[string]bool{"received": true})
		} else {
			writeJSON(w, status, map[string]string{"error": "payment webhook rejected"})
		}
	}
}

func webhookFailureStatus(provider string, permanent bool) int {
	if provider == payment.ProviderEPay {
		return http.StatusOK
	}
	if permanent {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func isPermanentPaymentEventError(err error) bool {
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrPaymentEventConflict) ||
		errors.Is(err, store.ErrPaymentProviderOrderMismatch) || errors.Is(err, store.ErrPaymentAmountMismatch) ||
		errors.Is(err, store.ErrPaymentCurrencyMismatch) || errors.Is(err, store.ErrInvalidPaymentEvent) ||
		errors.Is(err, store.ErrPaymentOrderNotFulfillable) || errors.Is(err, store.ErrPaymentProviderOrderConflict)
}

func publicPaymentOrderResponse(order store.PaymentOrder) publicPaymentOrder {
	return publicPaymentOrder{
		ID: order.ID, Status: publicPaymentOrderStatus(order.Status),
		Provider: order.Provider, MethodName: order.MethodName, MethodType: order.MethodType,
		TargetType: order.ProductType, TargetName: order.ProductName,
		BillingCycle: order.BillingCycle, AmountMinor: paymentOrderDisplayAmount(order), TaxAmountMinor: order.TaxAmountMinor, Currency: order.Currency,
		FailureReason: order.FailureMessage, CreatedAt: order.CreatedAt, PaidAt: order.PaidAt, FulfilledAt: order.FulfilledAt,
	}
}

func publicPaymentOrderListResponse(order store.PaymentOrder) publicPaymentOrderListItem {
	return publicPaymentOrderListItem{
		ID: order.ID, Status: publicPaymentOrderStatus(order.Status), Provider: order.Provider,
		MethodName: order.MethodName, MethodType: order.MethodType,
		TargetType: order.ProductType, TargetName: order.ProductName,
		AmountMinor: paymentOrderDisplayAmount(order), TaxAmountMinor: order.TaxAmountMinor, Currency: order.Currency, BillingCycle: order.BillingCycle,
		CreatedAt: order.CreatedAt, PaidAt: order.PaidAt,
	}
}

func paymentOrderDisplayAmount(order store.PaymentOrder) int64 {
	if order.PaidAmountMinor > 0 {
		return order.PaidAmountMinor
	}
	return order.AmountMinor
}

func publicPaymentOrderStatus(status string) string {
	switch status {
	case store.PaymentOrderFulfilled:
		return "paid"
	case store.PaymentOrderCancelled:
		return "expired"
	default:
		return status
	}
}

func paymentAbsoluteWebhookURL(r *http.Request, channelID string) string {
	return paymentWebhookURL(r, channelID)
}
