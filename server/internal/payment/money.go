package payment

import (
	"errors"
	"fmt"
	"math/big"
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

// NormalizeConversionRate validates and canonicalizes a positive decimal
// exchange rate without converting it through float64. Scientific notation is
// intentionally rejected so the persisted order snapshot stays human-readable
// and round-trips identically through JSON and both supported databases.
func NormalizeConversionRate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return "", errors.New("payment conversion rate must be a positive decimal")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return "", errors.New("payment conversion rate must be a positive decimal")
	}
	for _, part := range parts {
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return "", errors.New("payment conversion rate must be a positive decimal")
			}
		}
	}
	integer := strings.TrimLeft(parts[0], "0")
	if integer == "" {
		integer = "0"
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = strings.TrimRight(parts[1], "0")
	}
	if integer == "0" && fraction == "" {
		return "", errors.New("payment conversion rate must be greater than zero")
	}
	if fraction == "" {
		return integer, nil
	}
	return integer + "." + fraction, nil
}

// ConvertMinorAmount converts a positive amount between currencies using a
// rate expressed as target-major-units per source-major-unit. It applies each
// currency's ISO exponent and rounds half-up to the target's smallest unit.
func ConvertMinorAmount(amountMinor int64, sourceCurrency, targetCurrency, rate string) (int64, string, error) {
	if amountMinor <= 0 {
		return 0, "", errors.New("payment amount must be positive")
	}
	normalizedRate, err := NormalizeConversionRate(rate)
	if err != nil {
		return 0, "", err
	}

	parts := strings.Split(normalizedRate, ".")
	rateDigits := parts[0]
	fractionDigits := 0
	if len(parts) == 2 {
		rateDigits += parts[1]
		fractionDigits = len(parts[1])
	}
	rateNumerator, ok := new(big.Int).SetString(rateDigits, 10)
	if !ok || rateNumerator.Sign() <= 0 {
		return 0, "", errors.New("payment conversion rate must be a positive decimal")
	}
	rateDenominator := pow10Big(fractionDigits)

	scaled := new(big.Int).SetInt64(amountMinor)
	scaled.Mul(scaled, rateNumerator)
	scaled.Mul(scaled, pow10Big(CurrencyExponent(targetCurrency)))
	divisor := new(big.Int).Mul(rateDenominator, pow10Big(CurrencyExponent(sourceCurrency)))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled, divisor, remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(divisor) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if quotient.Sign() <= 0 {
		return 0, "", errors.New("converted payment amount rounds to zero")
	}
	if !quotient.IsInt64() {
		return 0, "", errors.New("converted payment amount is out of range")
	}
	return quotient.Int64(), normalizedRate, nil
}

func pow10Big(exponent int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
}

// ValidateMajorAmount performs currency-independent syntax validation for an
// EPay callback. The currency exponent is applied later from the immutable
// order snapshot, not from mutable channel configuration.
func ValidateMajorAmount(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return errors.New("invalid payment amount")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return errors.New("invalid payment amount")
	}
	for _, part := range parts {
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return errors.New("invalid payment amount")
			}
		}
	}
	return nil
}
