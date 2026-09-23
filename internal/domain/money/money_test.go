package money_test

import (
	"math"
	"testing"

	"github.com/felipecristiano/desafio/internal/domain/money"
)

func TestNewFromExternalString_Valid(t *testing.T) {
	cases := []struct {
		input    string
		currency money.Currency
		wantAmt  int64
	}{
		{"0.00", money.BRL, 0},
		{"1.00", money.BRL, 100},
		{"25.00", money.BRL, 2500},
		{"25.50", money.BRL, 2550},
		{"25.01", money.BRL, 2501},
		{"100.00", money.BRL, 10000},
		{"1000.00", money.BRL, 100000},
		{"9999999999.99", money.BRL, 999999999999},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			m, err := money.NewFromExternalString(tc.input, tc.currency)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.Amount() != tc.wantAmt {
				t.Errorf("amount: got %d, want %d", m.Amount(), tc.wantAmt)
			}
			if m.Currency() != tc.currency {
				t.Errorf("currency: got %s, want %s", m.Currency(), tc.currency)
			}
		})
	}
}

func TestNewFromExternalString_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"nan", "NaN"},
		{"nan_lower", "nan"},
		{"infinity", "Infinity"},
		{"inf", "Inf"},
		{"neg_inf", "-Inf"},
		{"scientific", "1e5"},
		{"scientific_upper", "1E5"},
		{"negative", "-1.00"},
		{"negative_zero", "-0.00"},
		{"no_decimal", "25"},
		{"one_decimal", "25.5"},
		{"three_decimal", "25.500"},
		{"empty_int", ".50"},
		{"empty_dec", "25."},
		{"alpha_int", "abc.00"},
		{"alpha_dec", "25.ab"},
		{"two_dots", "25.0.0"},
		{"positive_sign", "+25.00"},
		{"space", "25 .00"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := money.NewFromExternalString(tc.input, money.BRL)
			if err == nil {
				t.Errorf("expected error for input %q, got nil", tc.input)
			}
		})
	}
}

func TestNewFromExternalString_InvalidCurrency(t *testing.T) {
	_, err := money.NewFromExternalString("10.00", "XYZ")
	if err == nil {
		t.Fatal("expected error for unsupported currency")
	}
}

// ─── Overflow ────────────────────────────────────────────────────────────────

func TestNewFromExternalString_Overflow(t *testing.T) {
	// MaxInt64 = 9223372036854775807 centavos → "92233720368547758.07"
	_, err := money.NewFromExternalString("99999999999999999.99", money.BRL)
	if err == nil {
		t.Error("expected overflow error")
	}
}

func TestNewFromInt64_Rehydration(t *testing.T) {
	m := money.NewFromInt64(2500, money.BRL)
	if m.Amount() != 2500 {
		t.Errorf("got %d, want 2500", m.Amount())
	}
	if m.String() != "25.00" {
		t.Errorf("got %s, want 25.00", m.String())
	}
}

func TestNewFromInt64_Negative(t *testing.T) {
	m := money.NewFromInt64(-100, money.BRL)
	if !m.IsNegative() {
		t.Error("expected negative")
	}
	if m.String() != "-1.00" {
		t.Errorf("got %s, want -1.00", m.String())
	}
}


func TestZero(t *testing.T) {
	m := money.Zero(money.BRL)
	if !m.IsZero() {
		t.Error("expected zero")
	}
	if m.String() != "0.00" {
		t.Errorf("got %s, want 0.00", m.String())
	}
}


func TestString(t *testing.T) {
	cases := []struct {
		amount int64
		want   string
	}{
		{0, "0.00"},
		{100, "1.00"},
		{2500, "25.00"},
		{2501, "25.01"},
		{2550, "25.50"},
		{-100, "-1.00"},
		{-2501, "-25.01"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			m := money.NewFromInt64(tc.amount, money.BRL)
			if m.String() != tc.want {
				t.Errorf("got %s, want %s", m.String(), tc.want)
			}
		})
	}
}


func TestAdd_Success(t *testing.T) {
	a := money.NewFromInt64(1000, money.BRL) // 10.00
	b := money.NewFromInt64(500, money.BRL)  // 5.00
	c, err := a.Add(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Amount() != 1500 {
		t.Errorf("got %d, want 1500", c.Amount())
	}
}

func TestAdd_CurrencyMismatch(t *testing.T) {
	a := money.NewFromInt64(1000, money.BRL)
	b := money.NewFromInt64(500, money.USD)
	_, err := a.Add(b)
	if err == nil {
		t.Error("expected currency mismatch error")
	}
}

func TestAdd_Overflow(t *testing.T) {
	a := money.NewFromInt64(math.MaxInt64, money.BRL)
	b := money.NewFromInt64(1, money.BRL)
	_, err := a.Add(b)
	if err == nil {
		t.Error("expected overflow error")
	}
}


func TestSub_Success(t *testing.T) {
	a := money.NewFromInt64(1000, money.BRL)
	b := money.NewFromInt64(300, money.BRL)
	c, err := a.Sub(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Amount() != 700 {
		t.Errorf("got %d, want 700", c.Amount())
	}
}

func TestSub_ResultNegative(t *testing.T) {
	a := money.NewFromInt64(300, money.BRL)
	b := money.NewFromInt64(1000, money.BRL)
	c, err := a.Sub(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Amount() != -700 {
		t.Errorf("got %d, want -700", c.Amount())
	}
	if !c.IsNegative() {
		t.Error("expected negative result")
	}
}

func TestSub_CurrencyMismatch(t *testing.T) {
	a := money.NewFromInt64(1000, money.BRL)
	b := money.NewFromInt64(500, money.USD)
	_, err := a.Sub(b)
	if err == nil {
		t.Error("expected currency mismatch error")
	}
}

func TestNeg_Success(t *testing.T) {
	m := money.NewFromInt64(500, money.BRL)
	n, err := m.Neg()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.Amount() != -500 {
		t.Errorf("got %d, want -500", n.Amount())
	}
}

func TestNeg_MinInt64Overflow(t *testing.T) {
	m := money.NewFromInt64(math.MinInt64, money.BRL)
	_, err := m.Neg()
	if err == nil {
		t.Error("expected overflow error for MinInt64")
	}
}


func TestComparisons(t *testing.T) {
	a := money.NewFromInt64(1000, money.BRL)
	b := money.NewFromInt64(500, money.BRL)
	c := money.NewFromInt64(1000, money.BRL)

	gt, _ := a.GreaterThan(b)
	if !gt {
		t.Error("1000 > 500 should be true")
	}

	lt, _ := b.LessThan(a)
	if !lt {
		t.Error("500 < 1000 should be true")
	}

	if !a.Equal(c) {
		t.Error("1000 == 1000 should be true")
	}

	gte, _ := a.GreaterThanOrEqual(c)
	if !gte {
		t.Error("1000 >= 1000 should be true")
	}
}

func TestComparisons_CurrencyMismatch(t *testing.T) {
	a := money.NewFromInt64(1000, money.BRL)
	b := money.NewFromInt64(500, money.USD)

	_, err := a.GreaterThan(b)
	if err == nil {
		t.Error("expected currency mismatch in GreaterThan")
	}
	_, err = a.LessThan(b)
	if err == nil {
		t.Error("expected currency mismatch in LessThan")
	}
}


func TestPredicates(t *testing.T) {
	zero := money.Zero(money.BRL)
	pos := money.NewFromInt64(1, money.BRL)
	neg := money.NewFromInt64(-1, money.BRL)

	if !zero.IsZero() || zero.IsPositive() || zero.IsNegative() {
		t.Error("zero predicates failed")
	}
	if pos.IsZero() || !pos.IsPositive() || pos.IsNegative() {
		t.Error("positive predicates failed")
	}
	if neg.IsZero() || neg.IsPositive() || !neg.IsNegative() {
		t.Error("negative predicates failed")
	}
}
