package money

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

type Currency string

const (
	BRL Currency = "BRL"
	USD Currency = "USD"
	EUR Currency = "EUR"
)

var validCurrencies = map[Currency]bool{
	BRL: true,
	USD: true,
	EUR: true,
}

type Money struct {
	amount   int64
	currency Currency
}

func NewFromExternalString(s string, c Currency) (Money, error) {
	if err := validateCurrency(c); err != nil {
		return Money{}, err
	}
	amount, err := parseDecimalString(s, false)
	if err != nil {
		return Money{}, err
	}
	return Money{amount: amount, currency: c}, nil
}

func NewFromInt64(amount int64, c Currency) Money {
	return Money{amount: amount, currency: c}
}

func Zero(c Currency) Money {
	return Money{amount: 0, currency: c}
}

func (m Money) Amount() int64 { return m.amount }

func (m Money) Currency() Currency { return m.currency }

func (m Money) IsZero() bool { return m.amount == 0 }

func (m Money) IsPositive() bool { return m.amount > 0 }

func (m Money) IsNegative() bool { return m.amount < 0 }

func (m Money) Equal(other Money) bool {
	return m.amount == other.amount && m.currency == other.currency
}

func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("money: currency mismatch in Add: %s != %s", m.currency, other.currency)
	}
	// overflow
	if other.amount > 0 && m.amount > math.MaxInt64-other.amount {
		return Money{}, fmt.Errorf("money: Add overflow: %d + %d", m.amount, other.amount)
	}
	if other.amount < 0 && m.amount < math.MinInt64-other.amount {
		return Money{}, fmt.Errorf("money: Add underflow: %d + %d", m.amount, other.amount)
	}
	return Money{amount: m.amount + other.amount, currency: m.currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("money: currency mismatch in Sub: %s != %s", m.currency, other.currency)
	}
	neg, err := other.Neg()
	if err != nil {
		return Money{}, fmt.Errorf("money: Sub neg: %w", err)
	}
	return m.Add(neg)
}

func (m Money) Neg() (Money, error) {
	if m.amount == math.MinInt64 {
		return Money{}, fmt.Errorf("money: Neg overflow for MinInt64")
	}
	return Money{amount: -m.amount, currency: m.currency}, nil
}

func (m Money) GreaterThan(other Money) (bool, error) {
	if m.currency != other.currency {
		return false, fmt.Errorf("money: currency mismatch in GreaterThan: %s != %s", m.currency, other.currency)
	}
	return m.amount > other.amount, nil
}

func (m Money) LessThan(other Money) (bool, error) {
	if m.currency != other.currency {
		return false, fmt.Errorf("money: currency mismatch in LessThan: %s != %s", m.currency, other.currency)
	}
	return m.amount < other.amount, nil
}

func (m Money) GreaterThanOrEqual(other Money) (bool, error) {
	gt, err := m.GreaterThan(other)
	if err != nil {
		return false, err
	}
	return gt || m.Equal(other), nil
}

func (m Money) String() string {
	abs := m.amount
	sign := ""
	if abs < 0 {
		sign = "-"
		if abs == math.MinInt64 {
			minVal := int64(math.MinInt64)
			intP := -(minVal / 100)
			decP := -(minVal % 100)
			return fmt.Sprintf("-%d.%02d", intP, decP)
		}
		abs = -abs
	}
	intPart := abs / 100
	decPart := abs % 100
	return fmt.Sprintf("%s%d.%02d", sign, intPart, decPart)
}

func validateCurrency(c Currency) error {
	if !validCurrencies[c] {
		return fmt.Errorf("money: unsupported currency %q", c)
	}
	return nil
}

func parseDecimalString(s string, allowNegative bool) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("money: empty amount string")
	}

	lower := strings.ToLower(s)
	switch lower {
	case "nan", "inf", "+inf", "-inf", "infinity", "+infinity", "-infinity":
		return 0, fmt.Errorf("money: invalid amount %q", s)
	}

	if strings.ContainsAny(s, "eE") {
		return 0, fmt.Errorf("money: scientific notation not allowed: %q", s)
	}

	if strings.HasPrefix(s, "-") && !allowNegative {
		return 0, fmt.Errorf("money: negative values not allowed in external inputs: %q", s)
	}

	if strings.HasPrefix(s, "+") {
		return 0, fmt.Errorf("money: explicit positive sign not allowed: %q", s)
	}

	dotCount := strings.Count(s, ".")
	if dotCount != 1 {
		return 0, fmt.Errorf("money: must have exactly one decimal point: %q", s)
	}
	parts := strings.SplitN(s, ".", 2)
	intStr, decStr := parts[0], parts[1]

	if len(decStr) != 2 || !isAllDigits(decStr) {
		return 0, fmt.Errorf("money: decimal part must be exactly 2 digits, got %q in %q", decStr, s)
	}

	negative := false
	if strings.HasPrefix(intStr, "-") {
		negative = true
		intStr = intStr[1:]
	}
	if intStr == "" || !isAllDigits(intStr) {
		return 0, fmt.Errorf("money: invalid integer part %q in %q", intStr, s)
	}

	intVal, err := strconv.ParseInt(intStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("money: integer part overflow in %q: %w", s, err)
	}
	decVal, _ := strconv.ParseInt(decStr, 10, 64)

	const maxCents = math.MaxInt64
	if intVal > (maxCents-decVal)/100 {
		return 0, fmt.Errorf("money: amount overflow: %q", s)
	}

	result := intVal*100 + decVal
	if negative {
		result = -result
	}
	return result, nil
}

func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
