package payment

import (
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
)

type EPayConfig struct {
	GatewayURL                 string      `json:"gateway_url"`
	MerchantID                 string      `json:"merchant_id"`
	MerchantKey                string      `json:"merchant_key"`
	Currency                   string      `json:"currency"`
	ConversionRate             json.Number `json:"conversion_rate,omitempty"`
	ConversionRateBaseCurrency string      `json:"conversion_rate_base_currency,omitempty"`
}

type EPayMethodConfig struct {
	Type string `json:"type"`
}

type EPayGateway struct {
	Config EPayConfig
	Method EPayMethodConfig
}

func ValidateEPayConfig(cfg EPayConfig) error {
	if _, err := validateGatewayURL(cfg.GatewayURL); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.MerchantID) == "" || strings.TrimSpace(cfg.MerchantKey) == "" {
		return errors.New("EPay merchant credentials are incomplete")
	}
	currency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if len(currency) != 3 {
		return errors.New("EPay currency must be a three-letter code")
	}
	for _, ch := range currency {
		if ch < 'A' || ch > 'Z' {
			return errors.New("EPay currency must be a three-letter code")
		}
	}
	if rate := strings.TrimSpace(cfg.ConversionRate.String()); rate != "" {
		if _, err := NormalizeConversionRate(rate); err != nil {
			return err
		}
	}
	if base := strings.ToUpper(strings.TrimSpace(cfg.ConversionRateBaseCurrency)); base != "" {
		if !validCurrencyCode(base) {
			return errors.New("EPay conversion-rate base currency must be a three-letter code")
		}
	}
	return nil
}

// ValidateEPaySettlementConfig verifies that a cross-currency channel has a
// positive rate bound to the current settlement currency. Same-currency
// channels intentionally need neither a rate nor a base currency.
func ValidateEPaySettlementConfig(cfg EPayConfig, settlementCurrency string) error {
	if err := ValidateEPayConfig(cfg); err != nil {
		return err
	}
	settlementCurrency = strings.ToUpper(strings.TrimSpace(settlementCurrency))
	if !validCurrencyCode(settlementCurrency) {
		return errors.New("invalid settlement currency")
	}
	providerCurrency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if providerCurrency == settlementCurrency {
		return nil
	}
	if strings.ToUpper(strings.TrimSpace(cfg.ConversionRateBaseCurrency)) != settlementCurrency {
		return errors.New("EPay conversion-rate base currency must match the settlement currency")
	}
	if _, err := NormalizeConversionRate(cfg.ConversionRate.String()); err != nil {
		return err
	}
	return nil
}

// EPayProviderAmount returns the immutable provider-side checkout snapshot.
// The rate is target/provider major units per source/settlement major unit.
func EPayProviderAmount(amountMinor int64, settlementCurrency string, cfg EPayConfig) (int64, string, string, error) {
	if err := ValidateEPaySettlementConfig(cfg, settlementCurrency); err != nil {
		return 0, "", "", err
	}
	settlementCurrency = strings.ToUpper(strings.TrimSpace(settlementCurrency))
	providerCurrency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
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

func (g EPayGateway) CreateCheckout(_ context.Context, req CheckoutRequest) (CheckoutAction, error) {
	if err := ValidateEPayConfig(g.Config); err != nil {
		return CheckoutAction{}, err
	}
	base, _ := validateGatewayURL(g.Config.GatewayURL)
	method := strings.TrimSpace(g.Method.Type)
	if method == "" {
		return CheckoutAction{}, errors.New("EPay payment type is required")
	}
	if !strings.EqualFold(strings.TrimSpace(g.Config.Currency), req.Currency) {
		return CheckoutAction{}, errors.New("EPay channel currency does not match the order provider currency")
	}
	merchantOrderID := strings.TrimSpace(req.MerchantOrderID)
	if merchantOrderID == "" {
		merchantOrderID = strings.TrimSpace(req.OrderID)
	}
	if merchantOrderID == "" {
		return CheckoutAction{}, errors.New("EPay merchant order reference is required")
	}
	money, err := FormatMinorAmount(req.AmountMinor, req.Currency)
	if err != nil {
		return CheckoutAction{}, err
	}
	if !strings.EqualFold(path.Base(strings.TrimRight(base.Path, "/")), "submit.php") {
		base.Path = path.Join(base.Path, "submit.php")
	} else {
		base.Path = strings.TrimRight(base.Path, "/")
	}
	base.RawPath = ""
	fields := map[string]string{
		"pid":          strings.TrimSpace(g.Config.MerchantID),
		"type":         method,
		"out_trade_no": merchantOrderID,
		"notify_url":   req.NotifyURL,
		"return_url":   req.SuccessURL,
		"name":         req.Name,
		"money":        money,
		"device":       "pc",
		"sign_type":    "MD5",
	}
	fields["sign"] = EPaySign(fields, g.Config.MerchantKey)
	return CheckoutAction{Type: ActionFormPost, URL: base.String(), Fields: fields}, nil
}

// ResumeCheckout re-signs the one outstanding provider-facing merchant order
// reference. The EPay-compatible protocol has no portable session retrieval or
// cancellation API, so minting a new reference on every resume could leave
// multiple independently chargeable forms alive. Callers present this as a
// retry submission, not as restoration of a provider-hosted session.
func (g EPayGateway) ResumeCheckout(ctx context.Context, req CheckoutResumeRequest) (CheckoutAction, error) {
	merchantOrderID := strings.TrimSpace(req.MerchantOrderID)
	if strings.TrimSpace(req.OrderID) == "" || merchantOrderID == "" {
		return CheckoutAction{}, fmt.Errorf("%w: EPay retry requires an outstanding merchant order reference", ErrCheckoutNotResumable)
	}
	action, err := g.CreateCheckout(ctx, req.CheckoutRequest)
	if err != nil {
		return CheckoutAction{}, err
	}
	action.ResumeMode = CheckoutResumeRetrySubmission
	return action, nil
}

func EPaySign(params map[string]string, key string) string {
	keys := make([]string, 0, len(params))
	for k, value := range params {
		if k == "sign" || k == "sign_type" || value == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var signed strings.Builder
	for i, k := range keys {
		if i > 0 {
			signed.WriteByte('&')
		}
		signed.WriteString(k)
		signed.WriteByte('=')
		signed.WriteString(params[k])
	}
	signed.WriteString(key)
	digest := md5.Sum([]byte(signed.String()))
	return hex.EncodeToString(digest[:])
}

func VerifyEPayEvent(params map[string]string, cfg EPayConfig) (ProviderEvent, error) {
	received := strings.ToLower(strings.TrimSpace(params["sign"]))
	expected := EPaySign(params, cfg.MerchantKey)
	if received == "" || subtle.ConstantTimeCompare([]byte(received), []byte(expected)) != 1 {
		return ProviderEvent{}, errors.New("invalid EPay signature")
	}
	if params["pid"] != strings.TrimSpace(cfg.MerchantID) {
		return ProviderEvent{}, errors.New("EPay merchant does not match the channel")
	}
	amountMajor := strings.TrimSpace(params["money"])
	if err := ValidateMajorAmount(amountMajor); err != nil {
		return ProviderEvent{}, err
	}
	status := EventIgnored
	if params["trade_status"] == "TRADE_SUCCESS" {
		status = EventPaid
	}
	providerOrderID := strings.TrimSpace(params["trade_no"])
	eventID := providerOrderID + ":" + strings.TrimSpace(params["trade_status"])
	if providerOrderID == "" {
		eventID = strings.TrimSpace(params["out_trade_no"]) + ":" + strings.TrimSpace(params["trade_status"])
	}
	return ProviderEvent{
		ID:              eventID,
		Type:            "payment_notification",
		Status:          status,
		OrderID:         strings.TrimSpace(params["out_trade_no"]),
		ProviderOrderID: providerOrderID,
		AmountMajor:     amountMajor,
		MethodType:      strings.TrimSpace(params["type"]),
	}, nil
}

func validCurrencyCode(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, ch := range currency {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}

func validateGatewayURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, errors.New("invalid payment gateway URL")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}
