package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

const waffoCheckoutTimeout = 30 * time.Second

type WaffoConfig struct {
	MerchantID                 string      `json:"merchant_id"`
	PrivateKey                 string      `json:"private_key"`
	StoreID                    string      `json:"store_id"`
	ProductID                  string      `json:"product_id"`
	Currency                   string      `json:"currency,omitempty"`
	ConversionRate             json.Number `json:"conversion_rate,omitempty"`
	ConversionRateBaseCurrency string      `json:"conversion_rate_base_currency,omitempty"`
	Mode                       string      `json:"mode"`
	WebhookPublicKey           string      `json:"webhook_public_key,omitempty"`
}

type WaffoMethodConfig struct{}

type WaffoGateway struct {
	Config     WaffoConfig
	Method     WaffoMethodConfig
	BaseURL    string
	HTTPClient *http.Client
}

type WaffoReconciler struct {
	Config     WaffoConfig
	BaseURL    string
	HTTPClient *http.Client
}

func ValidateWaffoConfig(cfg WaffoConfig) error {
	cfg = normalizeWaffoConfig(cfg)
	if cfg.Mode != string(pancake.EnvironmentTest) && cfg.Mode != string(pancake.EnvironmentProd) {
		return errors.New("Waffo Pancake mode must be test or prod")
	}
	if !validWaffoShortID(cfg.StoreID, "STO") {
		return errors.New("invalid Waffo Pancake store ID")
	}
	if !validWaffoShortID(cfg.ProductID, "PROD") {
		return errors.New("invalid Waffo Pancake product ID")
	}
	// Currency was added after the first Waffo integration. An empty value is
	// retained for legacy records and resolved to the settlement currency by
	// ValidateWaffoSettlementConfig.
	if cfg.Currency != "" && !validCurrencyCode(cfg.Currency) {
		return errors.New("Waffo Pancake currency must be a three-letter code")
	}
	if rate := strings.TrimSpace(cfg.ConversionRate.String()); rate != "" {
		if _, err := NormalizeConversionRate(rate); err != nil {
			return err
		}
	}
	if cfg.ConversionRateBaseCurrency != "" && !validCurrencyCode(cfg.ConversionRateBaseCurrency) {
		return errors.New("Waffo Pancake conversion-rate base currency must be a three-letter code")
	}
	_, err := newWaffoClient(cfg, "", nil)
	if err != nil {
		return fmt.Errorf("invalid Waffo Pancake channel configuration: %w", err)
	}
	return nil
}

// ValidateWaffoSettlementConfig verifies that the Waffo product currency can
// be derived from the application's settlement currency. Waffo requires the
// checkout currency to exist in the selected product's prices map even when a
// runtime priceSnapshot overrides the amount.
func ValidateWaffoSettlementConfig(cfg WaffoConfig, settlementCurrency string) error {
	if err := ValidateWaffoConfig(cfg); err != nil {
		return err
	}
	settlementCurrency = strings.ToUpper(strings.TrimSpace(settlementCurrency))
	if !validCurrencyCode(settlementCurrency) {
		return errors.New("invalid settlement currency")
	}
	providerCurrency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if providerCurrency == "" {
		providerCurrency = settlementCurrency
	}
	if providerCurrency == settlementCurrency {
		return nil
	}
	if strings.ToUpper(strings.TrimSpace(cfg.ConversionRateBaseCurrency)) != settlementCurrency {
		return errors.New("Waffo Pancake conversion-rate base currency must match the settlement currency")
	}
	if _, err := NormalizeConversionRate(cfg.ConversionRate.String()); err != nil {
		return err
	}
	return nil
}

// WaffoProviderAmount returns the immutable provider-side amount, currency and
// exchange-rate snapshots used by checkout and webhook verification.
func WaffoProviderAmount(amountMinor int64, settlementCurrency string, cfg WaffoConfig) (int64, string, string, error) {
	if err := ValidateWaffoSettlementConfig(cfg, settlementCurrency); err != nil {
		return 0, "", "", err
	}
	settlementCurrency = strings.ToUpper(strings.TrimSpace(settlementCurrency))
	providerCurrency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if providerCurrency == "" {
		providerCurrency = settlementCurrency
	}
	if providerCurrency == settlementCurrency {
		if amountMinor <= 0 {
			return 0, "", "", errors.New("payment amount must be positive")
		}
		return amountMinor, providerCurrency, "", nil
	}
	converted, rate, err := ConvertMinorAmount(amountMinor, settlementCurrency, providerCurrency, cfg.ConversionRate.String())
	if err != nil {
		return 0, "", "", err
	}
	return converted, providerCurrency, rate, nil
}

func (g WaffoGateway) CreateCheckout(ctx context.Context, req CheckoutRequest) (CheckoutAction, error) {
	cfg := normalizeWaffoConfig(g.Config)
	if err := ValidateWaffoConfig(cfg); err != nil {
		return CheckoutAction{}, err
	}
	if strings.TrimSpace(req.OrderID) == "" || len(req.OrderID) > 128 {
		return CheckoutAction{}, errors.New("invalid payment order ID for Waffo Pancake")
	}
	if strings.TrimSpace(req.UserID) == "" {
		return CheckoutAction{}, errors.New("Waffo Pancake buyer identity is required")
	}
	if !validRedirectURL(req.SuccessURL) {
		return CheckoutAction{}, errors.New("invalid Waffo Pancake success URL")
	}
	requestCurrency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if cfg.Currency != "" && cfg.Currency != requestCurrency {
		return CheckoutAction{}, errors.New("Waffo Pancake channel currency does not match the order provider currency")
	}
	amount, err := FormatMinorAmount(req.AmountMinor, req.Currency)
	if err != nil {
		return CheckoutAction{}, err
	}
	taxCategory := pancake.TaxCategory(strings.TrimSpace(req.TaxCategory))
	if taxCategory != pancake.TaxCategoryDigitalGoods && taxCategory != pancake.TaxCategorySaaS {
		return CheckoutAction{}, errors.New("invalid Waffo Pancake tax category")
	}
	client, err := newWaffoClient(cfg, g.BaseURL, g.HTTPClient)
	if err != nil {
		return CheckoutAction{}, err
	}
	orderID := strings.TrimSpace(req.OrderID)
	successURL := strings.TrimSpace(req.SuccessURL)
	params := pancake.AuthenticatedCheckoutParams{
		CreateCheckoutSessionParams: pancake.CreateCheckoutSessionParams{
			ProductID: cfg.ProductID,
			Currency:  requestCurrency,
			PriceSnapshot: &pancake.PriceInfo{
				Amount:      amount,
				TaxCategory: taxCategory,
			},
			SuccessURL: &successURL,
			Metadata: map[string]string{
				"aivory_order_id": orderID,
			},
			OrderMerchantExternalID: &orderID,
		},
		BuyerIdentity: WaffoBuyerIdentity(req.UserID),
	}
	if email := strings.TrimSpace(req.UserEmail); email != "" {
		params.BuyerEmail = &email
	}
	session, err := client.Checkout.Authenticated.Create(ctx, params)
	if err != nil {
		if waffoProductCurrencyUnsupported(err) {
			return CheckoutAction{}, fmt.Errorf("%w: %v", ErrWaffoProductCurrencyUnsupported, err)
		}
		return CheckoutAction{}, fmt.Errorf("create Waffo Pancake checkout session: %w", err)
	}
	if session == nil || strings.TrimSpace(session.SessionID) == "" || !validRedirectURL(session.CheckoutURL) {
		return CheckoutAction{}, errors.New("Waffo Pancake returned an invalid checkout session")
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(session.ExpiresAt))
	if err != nil || expiresAt.Unix() <= 0 {
		return CheckoutAction{}, errors.New("Waffo Pancake returned an invalid checkout expiration")
	}
	// Checkout returns a SES_* ID, while the webhook carries an ORD_* ID.
	// Persisting the session ID as provider_order_id would reject valid events.
	return CheckoutAction{
		Type: ActionRedirect, URL: session.CheckoutURL,
		SessionID: strings.TrimSpace(session.SessionID), ExpiresAt: expiresAt.Unix(),
	}, nil
}

type waffoReconcilePayment struct {
	ID                      string `json:"id"`
	OrderID                 string `json:"orderId"`
	Status                  string `json:"status"`
	OrderMerchantExternalID string `json:"orderMerchantExternalId"`
	SnapshotAmountDetails   struct {
		Subtotal  json.RawMessage `json:"subtotal"`
		TaxAmount json.RawMessage `json:"taxAmount"`
		Total     json.RawMessage `json:"total"`
		Currency  string          `json:"currency"`
	} `json:"snapshotAmountDetails"`
}

type waffoReconcileOrder struct {
	ID                      string `json:"id"`
	Status                  string `json:"status"`
	OrderMerchantExternalID string `json:"orderMerchantExternalId"`
}

func (r WaffoReconciler) Reconcile(ctx context.Context, req ReconcileRequest) (ProviderEvent, error) {
	cfg := normalizeWaffoConfig(r.Config)
	if err := ValidateWaffoConfig(cfg); err != nil {
		return ProviderEvent{}, err
	}
	orderID := strings.TrimSpace(req.OrderID)
	if orderID == "" || strings.TrimSpace(req.UserID) == "" {
		return ProviderEvent{}, errors.New("invalid Waffo Pancake reconciliation request")
	}
	client, err := newWaffoClient(cfg, r.BaseURL, r.HTTPClient)
	if err != nil {
		return ProviderEvent{}, err
	}
	type queryData struct {
		Payments []waffoReconcilePayment `json:"payments"`
		Orders   []waffoReconcileOrder   `json:"onetimeOrders"`
	}
	response, err := pancake.GraphQLQuery[queryData](ctx, client, pancake.GraphQLParams{
		Query: `query($storeId: String!, $ref: String!) {
			payments(filter: { orderMerchantExternalId: { eq: $ref } }) {
				id orderId status orderMerchantExternalId
				snapshotAmountDetails { subtotal taxAmount total currency }
			}
			onetimeOrders(storeId: $storeId, filter: { orderMerchantExternalId: { eq: $ref } }) {
				id status orderMerchantExternalId
			}
		}`,
		Variables: map[string]any{"storeId": cfg.StoreID, "ref": orderID},
	})
	if err != nil {
		return ProviderEvent{}, fmt.Errorf("query Waffo Pancake payment order: %w", err)
	}
	if len(response.Errors) > 0 {
		return ProviderEvent{}, fmt.Errorf("query Waffo Pancake payment order: %s", response.Errors[0].Message)
	}
	for _, candidate := range response.Data.Payments {
		if strings.TrimSpace(candidate.OrderMerchantExternalID) != orderID {
			return ProviderEvent{}, errors.New("Waffo Pancake payment reference does not match the order")
		}
		if strings.EqualFold(strings.TrimSpace(candidate.Status), string(pancake.PaymentStatusSucceeded)) {
			return waffoReconcilePaidEvent(orderID, req.UserID, candidate)
		}
	}

	var providerOrder *waffoReconcileOrder
	for index := range response.Data.Orders {
		candidate := &response.Data.Orders[index]
		if strings.TrimSpace(candidate.OrderMerchantExternalID) != orderID {
			return ProviderEvent{}, errors.New("Waffo Pancake order reference does not match the order")
		}
		providerOrder = candidate
		break
	}
	if providerOrder != nil {
		status := strings.ToLower(strings.TrimSpace(providerOrder.Status))
		switch status {
		case string(pancake.OnetimeOrderStatusCompleted):
			return ProviderEvent{}, errors.New("Waffo Pancake reports a completed order without a succeeded payment")
		case string(pancake.OnetimeOrderStatusCanceled):
			return waffoReconcileStateEvent(req, providerOrder.ID, EventExpired), nil
		case string(pancake.OnetimeOrderStatusPending):
			if req.Close {
				storeID := cfg.StoreID
				token, err := client.Auth.IssueSessionToken(ctx, pancake.IssueSessionTokenParams{
					BuyerIdentity: WaffoBuyerIdentity(req.UserID), StoreID: &storeID,
				})
				if err != nil {
					return ProviderEvent{}, fmt.Errorf("issue Waffo Pancake reconciliation session: %w", err)
				}
				cancelled, err := client.Customer(token.Token).CancelOnetimeOrder(ctx, pancake.CancelOnetimeOrderParams{OrderID: providerOrder.ID})
				if err != nil {
					return ProviderEvent{}, fmt.Errorf("cancel Waffo Pancake order: %w", err)
				}
				if cancelled == nil || !strings.EqualFold(strings.TrimSpace(cancelled.Status), string(pancake.OnetimeOrderStatusCanceled)) {
					return ProviderEvent{}, fmt.Errorf("%w: Waffo Pancake did not confirm cancellation", ErrCheckoutNotClosable)
				}
				return waffoReconcileStateEvent(req, providerOrder.ID, EventExpired), nil
			}
			return waffoReconcileStateEvent(req, providerOrder.ID, EventProcessing), nil
		default:
			return ProviderEvent{}, fmt.Errorf("unknown Waffo Pancake order status %q", status)
		}
	}

	if req.SessionExpiresAt > 0 && time.Now().Unix() >= req.SessionExpiresAt {
		return waffoReconcileStateEvent(req, "", EventExpired), nil
	}
	if req.Close {
		return ProviderEvent{}, fmt.Errorf("%w: Waffo Pancake checkout session has not expired", ErrCheckoutNotClosable)
	}
	return waffoReconcileStateEvent(req, "", EventProcessing), nil
}

func waffoReconcilePaidEvent(orderID, userID string, payment waffoReconcilePayment) (ProviderEvent, error) {
	currency := strings.ToUpper(strings.TrimSpace(payment.SnapshotAmountDetails.Currency))
	total, err := ParseMinorAmount(waffoGraphQLAmount(payment.SnapshotAmountDetails.Total), currency)
	if err != nil {
		return ProviderEvent{}, fmt.Errorf("invalid Waffo Pancake reconciled total: %w", err)
	}
	taxRaw := waffoGraphQLAmount(payment.SnapshotAmountDetails.TaxAmount)
	if taxRaw == "" {
		taxRaw = "0"
	}
	tax, err := ParseMinorAmount(taxRaw, currency)
	if err != nil {
		return ProviderEvent{}, fmt.Errorf("invalid Waffo Pancake reconciled tax: %w", err)
	}
	if total <= 0 || tax < 0 || tax > total {
		return ProviderEvent{}, errors.New("invalid Waffo Pancake reconciled payment amounts")
	}
	net := total - tax
	if rawSubtotal := waffoGraphQLAmount(payment.SnapshotAmountDetails.Subtotal); rawSubtotal != "" {
		subtotal, err := ParseMinorAmount(rawSubtotal, currency)
		if err != nil || subtotal != net {
			return ProviderEvent{}, errors.New("Waffo Pancake reconciled subtotal does not match total and tax")
		}
	}
	return ProviderEvent{
		ID: "reconcile:" + strings.TrimSpace(payment.ID) + ":paid", Type: "payment.reconciled", Status: EventPaid,
		OrderID: orderID, ProviderOrderID: strings.TrimSpace(payment.OrderID), ProviderPaymentID: strings.TrimSpace(payment.ID),
		AmountMinor: net, PaidAmountMinor: total, TaxAmountMinor: tax, Currency: currency,
		UserID: WaffoBuyerIdentity(userID),
	}, nil
}

func waffoReconcileStateEvent(req ReconcileRequest, providerOrderID, status string) ProviderEvent {
	eventKey := strings.TrimSpace(providerOrderID)
	if eventKey == "" {
		eventKey = strings.TrimSpace(req.OrderID)
	}
	return ProviderEvent{
		ID: "reconcile:" + eventKey + ":" + status, Type: "order.reconciled", Status: status,
		OrderID: strings.TrimSpace(req.OrderID), ProviderOrderID: strings.TrimSpace(providerOrderID),
		AmountMinor: req.AmountMinor, PaidAmountMinor: req.AmountMinor,
		Currency: strings.ToUpper(strings.TrimSpace(req.Currency)), UserID: WaffoBuyerIdentity(req.UserID),
	}
}

func waffoGraphQLAmount(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(raw))
}

func VerifyWaffoEvent(payload []byte, signature string, cfg WaffoConfig) (ProviderEvent, error) {
	cfg = normalizeWaffoConfig(cfg)
	if err := ValidateWaffoConfig(cfg); err != nil {
		return ProviderEvent{}, err
	}
	client, err := newWaffoClient(cfg, "", nil)
	if err != nil {
		return ProviderEvent{}, err
	}
	event, err := client.Webhooks.Verify(string(payload), signature, &pancake.VerifyWebhookOptions{
		Environment: pancake.Environment(cfg.Mode),
	})
	if err != nil {
		return ProviderEvent{}, fmt.Errorf("invalid Waffo Pancake webhook: %w", err)
	}
	if event.Mode != pancake.Environment(cfg.Mode) {
		return ProviderEvent{}, errors.New("Waffo Pancake webhook mode does not match the channel")
	}
	if strings.TrimSpace(event.StoreID) != cfg.StoreID {
		return ProviderEvent{}, errors.New("Waffo Pancake webhook store does not match the channel")
	}
	if event.EventType != string(pancake.WebhookEventTypeOrderCompleted) {
		return ProviderEvent{ID: strings.TrimSpace(event.ID), Type: event.EventType, Status: EventIgnored}, nil
	}
	if strings.TrimSpace(event.ID) == "" {
		return ProviderEvent{}, errors.New("Waffo Pancake webhook is missing its delivery ID")
	}
	var data pancake.WebhookEventData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return ProviderEvent{}, fmt.Errorf("decode Waffo Pancake webhook data: %w", err)
	}
	return waffoCompletedProviderEvent(event, data)
}

func waffoCompletedProviderEvent(event *pancake.WebhookEvent, data pancake.WebhookEventData) (ProviderEvent, error) {
	orderID := ""
	if data.OrderMerchantExternalID != nil {
		orderID = strings.TrimSpace(*data.OrderMerchantExternalID)
	}
	if orderID == "" {
		return ProviderEvent{}, errors.New("Waffo Pancake webhook is missing orderMerchantExternalId")
	}
	providerOrderID := strings.TrimSpace(data.OrderID)
	if providerOrderID == "" {
		return ProviderEvent{}, errors.New("Waffo Pancake webhook is missing its order ID")
	}
	identity := ""
	if data.MerchantProvidedBuyerIdentity != nil {
		identity = strings.TrimSpace(*data.MerchantProvidedBuyerIdentity)
	}
	if identity == "" {
		return ProviderEvent{}, errors.New("Waffo Pancake webhook is missing buyer identity")
	}
	if metadataOrderID := strings.TrimSpace(data.OrderMetadata["aivory_order_id"]); metadataOrderID != "" && metadataOrderID != orderID {
		return ProviderEvent{}, errors.New("Waffo Pancake webhook order references do not match")
	}
	if data.OrderStatus == nil || !strings.EqualFold(strings.TrimSpace(*data.OrderStatus), "completed") {
		return ProviderEvent{}, errors.New("Waffo Pancake order is not completed")
	}
	if data.PaymentStatus == nil || !strings.EqualFold(strings.TrimSpace(*data.PaymentStatus), "succeeded") {
		return ProviderEvent{}, errors.New("Waffo Pancake payment is not succeeded")
	}
	paymentID := ""
	if data.PaymentID != nil {
		paymentID = strings.TrimSpace(*data.PaymentID)
	}
	if paymentID == "" {
		return ProviderEvent{}, errors.New("Waffo Pancake webhook is missing its payment ID")
	}
	currency := strings.ToUpper(strings.TrimSpace(data.Currency))
	netAmount, paidAmount, taxAmount, err := parseWaffoAmounts(data, currency)
	if err != nil {
		return ProviderEvent{}, err
	}
	method := ""
	if data.PaymentMethod != nil {
		method = strings.TrimSpace(*data.PaymentMethod)
	}
	return ProviderEvent{
		ID:                strings.TrimSpace(event.ID),
		Type:              event.EventType,
		Status:            EventPaid,
		OrderID:           orderID,
		ProviderOrderID:   providerOrderID,
		ProviderPaymentID: paymentID,
		AmountMinor:       netAmount,
		PaidAmountMinor:   paidAmount,
		TaxAmountMinor:    taxAmount,
		Currency:          currency,
		MethodType:        method,
		UserID:            identity,
	}, nil
}

func parseWaffoAmounts(data pancake.WebhookEventData, currency string) (int64, int64, int64, error) {
	gross, err := ParseMinorAmount(strings.TrimSpace(data.Amount), currency)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid Waffo Pancake payment amount: %w", err)
	}
	tax, err := ParseMinorAmount(strings.TrimSpace(data.TaxAmount), currency)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid Waffo Pancake tax amount: %w", err)
	}
	if gross < tax {
		return 0, 0, 0, errors.New("Waffo Pancake tax amount exceeds the payment amount")
	}
	net := gross - tax
	if data.Subtotal != nil {
		subtotal, err := ParseMinorAmount(strings.TrimSpace(*data.Subtotal), currency)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid Waffo Pancake subtotal: %w", err)
		}
		if subtotal != net {
			return 0, 0, 0, errors.New("Waffo Pancake subtotal does not match the payment amount and tax")
		}
	}
	if data.Total != nil {
		total, err := ParseMinorAmount(strings.TrimSpace(*data.Total), currency)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid Waffo Pancake total: %w", err)
		}
		if total != gross {
			return 0, 0, 0, errors.New("Waffo Pancake total does not match the payment amount")
		}
	}
	return net, gross, tax, nil
}

func WaffoBuyerIdentity(userID string) string {
	return "aivory-user:" + strings.TrimSpace(userID)
}

func normalizeWaffoConfig(cfg WaffoConfig) WaffoConfig {
	cfg.MerchantID = strings.TrimSpace(cfg.MerchantID)
	cfg.PrivateKey = strings.TrimSpace(cfg.PrivateKey)
	cfg.StoreID = strings.TrimSpace(cfg.StoreID)
	cfg.ProductID = strings.TrimSpace(cfg.ProductID)
	cfg.Currency = strings.ToUpper(strings.TrimSpace(cfg.Currency))
	cfg.ConversionRateBaseCurrency = strings.ToUpper(strings.TrimSpace(cfg.ConversionRateBaseCurrency))
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	cfg.WebhookPublicKey = strings.TrimSpace(cfg.WebhookPublicKey)
	return cfg
}

func waffoProductCurrencyUnsupported(err error) bool {
	var providerErr *pancake.Error
	if !errors.As(err, &providerErr) || providerErr.Status != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(providerErr.Error()))
	return strings.HasPrefix(message, "currency ") &&
		(strings.Contains(message, " is not supported for this product") ||
			strings.Contains(message, " is not supported for onetime payments"))
}

func newWaffoClient(cfg WaffoConfig, baseURL string, httpClient *http.Client) (*pancake.Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: waffoCheckoutTimeout}
	}
	return pancake.New(pancake.Config{
		MerchantID: cfg.MerchantID,
		PrivateKey: cfg.PrivateKey,
		BaseURL:    strings.TrimSpace(baseURL),
		HTTPClient: httpClient,
		WebhookPublicKey: pancake.WebhookPublicKeys{
			Shared: cfg.WebhookPublicKey,
		},
	})
}

func validWaffoShortID(value, prefix string) bool {
	value = strings.TrimSpace(value)
	prefix += "_"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+22 {
		return false
	}
	for _, char := range value[len(prefix):] {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}
