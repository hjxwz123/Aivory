package payment

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var zeroDecimalCurrencies = map[string]struct{}{
	"BIF": {}, "CLP": {}, "DJF": {}, "GNF": {}, "ISK": {}, "JPY": {},
	"KMF": {}, "KRW": {}, "PYG": {}, "RWF": {}, "UGX": {}, "UYI": {},
	"VND": {}, "VUV": {}, "XAF": {}, "XOF": {}, "XPF": {},
}

var threeDecimalCurrencies = map[string]struct{}{
	"BHD": {}, "IQD": {}, "JOD": {}, "KWD": {}, "LYD": {}, "OMR": {}, "TND": {},
}

var fourDecimalCurrencies = map[string]struct{}{
	"CLF": {}, "UYW": {},
}

func CurrencyExponent(currency string) int {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if _, ok := zeroDecimalCurrencies[currency]; ok {
		return 0
	}
	if _, ok := threeDecimalCurrencies[currency]; ok {
		return 3
	}
	if _, ok := fourDecimalCurrencies[currency]; ok {
		return 4
	}
	return 2
}

func FormatMinorAmount(amount int64, currency string) (string, error) {
	if amount < 0 {
		return "", errors.New("payment amount must not be negative")
	}
	exponent := CurrencyExponent(currency)
	if exponent == 0 {
		return strconv.FormatInt(amount, 10), nil
	}
	power := int64(1)
	for i := 0; i < exponent; i++ {
		power *= 10
	}
	return fmt.Sprintf("%d.%0*d", amount/power, exponent, amount%power), nil
}

func ParseMinorAmount(value, currency string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, errors.New("invalid payment amount")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, errors.New("invalid payment amount")
	}
	for _, part := range parts {
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return 0, errors.New("invalid payment amount")
			}
		}
	}
	exponent := CurrencyExponent(currency)
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > exponent {
		return 0, errors.New("payment amount has too many decimal places")
	}
	fraction += strings.Repeat("0", exponent-len(fraction))
	digits := strings.TrimLeft(parts[0]+fraction, "0")
	if digits == "" {
		return 0, nil
	}
	amount, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, errors.New("payment amount is out of range")
	}
	return amount, nil
}
