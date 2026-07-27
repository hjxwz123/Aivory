package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	paymentcore "aivory/server/internal/payment"
	"aivory/server/internal/store"
)

const paymentSecretMask = "••••••"

func paymentSensitiveConfigKey(key string) bool {
	switch key {
	case "secret_key", "webhook_secret", "merchant_key", "private_key":
		return true
	default:
		return false
	}
}

func normalizePaymentProvider(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	switch provider {
	case paymentcore.ProviderStripe, paymentcore.ProviderEPay, paymentcore.ProviderWaffo:
		return provider, nil
	default:
		return "", errors.New("unsupported payment provider")
	}
}

func detectedPaymentEnvironment(provider string, raw json.RawMessage) string {
	switch provider {
	case paymentcore.ProviderStripe:
		var cfg paymentcore.StripeConfig
		if json.Unmarshal(raw, &cfg) != nil {
			return ""
		}
		key := strings.ToLower(strings.TrimSpace(cfg.SecretKey))
		switch {
		case strings.HasPrefix(key, "sk_test_"), strings.HasPrefix(key, "rk_test_"), strings.HasPrefix(key, "rkcs_test_"):
			return store.PaymentEnvironmentTest
		case strings.HasPrefix(key, "sk_live_"), strings.HasPrefix(key, "rk_live_"), strings.HasPrefix(key, "rkcs_live_"):
			return store.PaymentEnvironmentLive
		}
	case paymentcore.ProviderWaffo:
		var cfg paymentcore.WaffoConfig
		if json.Unmarshal(raw, &cfg) != nil {
			return ""
		}
		if strings.EqualFold(strings.TrimSpace(cfg.Mode), "test") {
			return store.PaymentEnvironmentTest
		}
		if strings.EqualFold(strings.TrimSpace(cfg.Mode), "prod") {
			return store.PaymentEnvironmentLive
		}
	}
	return ""
}

func normalizePaymentEnvironment(provider string, raw json.RawMessage, requested string) (string, error) {
	environment := strings.ToLower(strings.TrimSpace(requested))
	detected := detectedPaymentEnvironment(provider, raw)
	if environment == "" {
		environment = detected
		if environment == "" {
			environment = store.PaymentEnvironmentLive
		}
	}
	if environment != store.PaymentEnvironmentLive && environment != store.PaymentEnvironmentTest {
		return "", errors.New("payment environment must be live or test")
	}
	if detected != "" && detected != environment {
		return "", errors.New("payment environment does not match the provider credentials")
	}
	return environment, nil
}

func paymentConfigObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("payment configuration must be a JSON object")
	}
	return object, nil
}

func mergePaymentChannelConfig(existing, incoming json.RawMessage) (json.RawMessage, error) {
	next, err := paymentConfigObject(incoming)
	if err != nil {
		return nil, err
	}
	merged := map[string]json.RawMessage{}
	if len(existing) > 0 {
		current, err := paymentConfigObject(existing)
		if err != nil {
			return nil, err
		}
		for key, value := range current {
			merged[key] = value
		}
	}
	for key, value := range next {
		var text string
		if json.Unmarshal(value, &text) == nil && paymentSensitiveConfigKey(key) {
			if text == paymentSecretMask {
				if _, ok := merged[key]; !ok {
					return nil, errors.New("masked payment secret has no saved value")
				}
				continue
			}
			// Editing an unrelated field should not force an administrator to
			// re-enter credentials. Empty sensitive inputs preserve saved values.
			if strings.TrimSpace(text) == "" {
				if _, ok := merged[key]; ok {
					continue
				}
			}
		}
		merged[key] = value
	}
	return json.Marshal(merged)
}

func normalizePaymentChannelConfig(provider string, raw json.RawMessage) (json.RawMessage, error) {
	return normalizePaymentChannelConfigForState(provider, raw, true)
}

func normalizePaymentChannelConfigForState(provider string, raw json.RawMessage, enabled bool) (json.RawMessage, error) {
	if _, err := paymentConfigObject(raw); err != nil {
		return nil, err
	}
	switch provider {
	case paymentcore.ProviderStripe:
		var cfg paymentcore.StripeConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, errors.New("invalid Stripe configuration")
		}
		cfg.SecretKey = strings.TrimSpace(cfg.SecretKey)
		cfg.WebhookSecret = strings.TrimSpace(cfg.WebhookSecret)
		validate := paymentcore.ValidateStripeSetupConfig
		if enabled {
			validate = paymentcore.ValidateStripeConfig
		}
		if err := validate(cfg); err != nil {
			return nil, err
		}
		return json.Marshal(cfg)
	case paymentcore.ProviderEPay:
		var cfg paymentcore.EPayConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, errors.New("invalid EPay configuration")
		}
		cfg.GatewayURL = strings.TrimSpace(cfg.GatewayURL)
		cfg.MerchantID = strings.TrimSpace(cfg.MerchantID)
		cfg.MerchantKey = strings.TrimSpace(cfg.MerchantKey)
		cfg.Currency = strings.ToUpper(strings.TrimSpace(cfg.Currency))
		if err := paymentcore.ValidateEPayConfig(cfg); err != nil {
			return nil, err
		}
		return json.Marshal(cfg)
	case paymentcore.ProviderWaffo:
		var cfg paymentcore.WaffoConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, errors.New("invalid Waffo configuration")
		}
		cfg.MerchantID = strings.TrimSpace(cfg.MerchantID)
		cfg.PrivateKey = strings.TrimSpace(cfg.PrivateKey)
		cfg.StoreID = strings.TrimSpace(cfg.StoreID)
		cfg.ProductID = strings.TrimSpace(cfg.ProductID)
		cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
		cfg.WebhookPublicKey = strings.TrimSpace(cfg.WebhookPublicKey)
		if err := paymentcore.ValidateWaffoConfig(cfg); err != nil {
			return nil, err
		}
		return json.Marshal(cfg)
	default:
		return nil, errors.New("unsupported payment provider")
	}
}

func maskedPaymentChannelConfig(provider string, raw json.RawMessage) json.RawMessage {
	object, err := paymentConfigObject(raw)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	secretKeys := map[string]bool{}
	switch provider {
	case paymentcore.ProviderStripe:
		secretKeys["secret_key"] = true
		secretKeys["webhook_secret"] = true
	case paymentcore.ProviderEPay:
		secretKeys["merchant_key"] = true
	case paymentcore.ProviderWaffo:
		secretKeys["private_key"] = true
	}
	mask, _ := json.Marshal(paymentSecretMask)
	for key := range secretKeys {
		if value, ok := object[key]; ok && string(value) != `""` && string(value) != "null" {
			object[key] = mask
		}
	}
	result, err := json.Marshal(object)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return result
}

func validPaymentMethodIdentifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func normalizePaymentMethodConfig(provider string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if _, err := paymentConfigObject(raw); err != nil {
		return nil, err
	}
	switch provider {
	case paymentcore.ProviderStripe:
		// Stripe dynamically selects eligible local methods from the Dashboard,
		// currency, buyer location, amount and device. Legacy
		// payment_method_types values are intentionally discarded.
		return json.Marshal(paymentcore.StripeMethodConfig{})
	case paymentcore.ProviderEPay:
		var cfg paymentcore.EPayMethodConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, errors.New("invalid EPay payment-method configuration")
		}
		cfg.Type = strings.TrimSpace(cfg.Type)
		if !validPaymentMethodIdentifier(cfg.Type) {
			return nil, errors.New("EPay payment type is required")
		}
		return json.Marshal(cfg)
	case paymentcore.ProviderWaffo:
		if _, err := paymentConfigObject(raw); err != nil {
			return nil, errors.New("invalid Waffo payment-method configuration")
		}
		return json.Marshal(paymentcore.WaffoMethodConfig{})
	default:
		return nil, errors.New("unsupported payment provider")
	}
}

func paymentChannelSupportsCurrency(provider string, config json.RawMessage, currency string) bool {
	if provider != paymentcore.ProviderEPay {
		return true
	}
	var cfg paymentcore.EPayConfig
	return json.Unmarshal(config, &cfg) == nil && strings.EqualFold(strings.TrimSpace(cfg.Currency), currency)
}

func paymentGateway(provider string, channelConfig, methodConfig json.RawMessage) (paymentcore.CheckoutCreator, error) {
	channelConfig, err := normalizePaymentChannelConfig(provider, channelConfig)
	if err != nil {
		return nil, err
	}
	methodConfig, err = normalizePaymentMethodConfig(provider, methodConfig)
	if err != nil {
		return nil, err
	}
	switch provider {
	case paymentcore.ProviderStripe:
		var cfg paymentcore.StripeConfig
		_ = json.Unmarshal(channelConfig, &cfg)
		return paymentcore.StripeGateway{Config: cfg}, nil
	case paymentcore.ProviderEPay:
		var cfg paymentcore.EPayConfig
		var method paymentcore.EPayMethodConfig
		_ = json.Unmarshal(channelConfig, &cfg)
		_ = json.Unmarshal(methodConfig, &method)
		return paymentcore.EPayGateway{Config: cfg, Method: method}, nil
	case paymentcore.ProviderWaffo:
		var cfg paymentcore.WaffoConfig
		var method paymentcore.WaffoMethodConfig
		_ = json.Unmarshal(channelConfig, &cfg)
		_ = json.Unmarshal(methodConfig, &method)
		return paymentcore.WaffoGateway{Config: cfg, Method: method}, nil
	default:
		return nil, errors.New("unsupported payment provider")
	}
}

func validPaymentHTTPURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		if strings.ContainsAny(raw, "\\\r\n\x00") {
			return false
		}
		parsed, err := url.ParseRequestURI(raw)
		return err == nil && !parsed.IsAbs() && parsed.Host == "" && parsed.User == nil
	}
	return validAbsolutePaymentHTTPURL(raw)
}

func validAbsolutePaymentHTTPURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Host != "" && parsed.User == nil &&
		(parsed.Scheme == "https" || parsed.Scheme == "http")
}

func createPaymentCheckout(ctx context.Context, gateway paymentcore.CheckoutCreator, request paymentcore.CheckoutRequest) (paymentcore.CheckoutAction, error) {
	return gateway.CreateCheckout(ctx, request)
}
