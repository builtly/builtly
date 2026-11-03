package billing

import (
	"math"
	"strconv"
	"strings"
)

// Purchases store money the same way Stripe's amount_total does, which is what
// Activity formats: ISO-4217 minor units, lowercase currency. Zero-decimal
// currencies use major units; three-decimal currencies use 1/1000; the rest use cents.

func currencyExponent(currency string) int {
	switch normalizeCurrency(currency) {
	case "bif", "clp", "djf", "gnf", "jpy", "kmf", "krw", "mga", "pyg", "rwf", "ugx", "vnd", "vuv", "xaf", "xof", "xpf":
		return 0
	case "bhd", "jod", "kwd", "omr", "tnd":
		return 3
	default:
		return 2
	}
}

func normalizeCurrency(currency string) string {
	return strings.ToLower(strings.TrimSpace(currency))
}

// minorFromMilliunits converts an App Store price (major units × 1000) into minor units.
func minorFromMilliunits(milli int64, currency string) int64 {
	switch currencyExponent(currency) {
	case 0:
		return divRound(milli, 1000)
	case 3:
		return milli
	default:
		return divRound(milli, 10)
	}
}

// minorFromDecimal converts a Google Money value (whole units + nanos) into minor units.
func minorFromDecimal(units, nanos int64, currency string) (int64, bool) {
	exp := currencyExponent(currency)
	scale := int64(1)
	for i := 0; i < exp; i++ {
		if scale > math.MaxInt64/10 {
			return 0, false
		}
		scale *= 10
	}
	major, ok := mul(units, scale)
	if !ok {
		return 0, false
	}
	nanoDiv := int64(1)
	for i := 0; i < 9-exp; i++ {
		nanoDiv *= 10
	}
	frac := nanos
	if nanoDiv > 1 {
		frac = divRound(nanos, nanoDiv)
	}
	sum, ok := add(major, frac)
	if !ok {
		return 0, false
	}
	return sum, true
}

// minorFromGoogleUnits parses a Play Money units string plus nanos.
func minorFromGoogleUnits(units string, nanos int64, currency string) (int64, string, bool) {
	cur := normalizeCurrency(currency)
	if cur == "" {
		return 0, "", false
	}
	trimmed := strings.TrimSpace(units)
	if trimmed == "" && nanos == 0 {
		return 0, "", false
	}
	whole := int64(0)
	if trimmed != "" {
		n, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return 0, "", false
		}
		whole = n
	}
	minor, ok := minorFromDecimal(whole, nanos, cur)
	if !ok {
		return 0, "", false
	}
	return minor, cur, true
}

func scaleMilliunits(price, qty int64) (int64, bool) {
	if qty <= 0 {
		qty = 1
	}
	if qty == 1 || price == 0 {
		return price, true
	}
	return mul(price, qty)
}

func divRound(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	if n >= 0 {
		return (n + d/2) / d
	}
	return (n - d/2) / d
}

func mul(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > 0 && b > 0 && a > math.MaxInt64/b {
		return 0, false
	}
	if a < 0 && b < 0 && a < math.MaxInt64/b {
		return 0, false
	}
	if a > 0 && b < 0 && b < math.MinInt64/a {
		return 0, false
	}
	if a < 0 && b > 0 && a < math.MinInt64/b {
		return 0, false
	}
	return a * b, true
}

func add(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}
