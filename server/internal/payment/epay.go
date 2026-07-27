package payment

import (
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/url"
	"path"
	"sort"
	"strings"
)

type EPayConfig struct {
	GatewayURL  string `json:"gateway_url"`
	MerchantID  string `json:"merchant_id"`
	MerchantKey string `json:"merchant_key"`
	Currency    string `json:"currency"`
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
	return nil
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
		return CheckoutAction{}, errors.New("EPay channel currency does not match the settlement currency")
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
		"out_trade_no": req.OrderID,
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
	currency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	amount, err := ParseMinorAmount(params["money"], currency)
	if err != nil {
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
		AmountMinor:     amount,
		PaidAmountMinor: amount,
		Currency:        currency,
		MethodType:      strings.TrimSpace(params["type"]),
	}, nil
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
