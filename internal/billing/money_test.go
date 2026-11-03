package billing

import "testing"

func TestMinorFromMilliunits(t *testing.T) {
	cases := []struct {
		milli int64
		cur   string
		want  int64
	}{
		{9990, "USD", 999},
		{990, "usd", 99},
		{120000, "jpy", 120},
		{1000000, "JPY", 1000},
		{1234, "KWD", 1234},
		{5, "usd", 1},
		{4, "usd", 0},
		{0, "usd", 0},
	}
	for _, tc := range cases {
		if got := minorFromMilliunits(tc.milli, tc.cur); got != tc.want {
			t.Errorf("minorFromMilliunits(%d, %s) = %d, want %d", tc.milli, tc.cur, got, tc.want)
		}
	}
}

func TestMinorFromGoogleUnits(t *testing.T) {
	got, cur, ok := minorFromGoogleUnits("9", 990_000_000, "USD")
	if !ok || got != 999 || cur != "usd" {
		t.Fatalf("usd: %d %s ok=%v", got, cur, ok)
	}
	got, cur, ok = minorFromGoogleUnits("120", 0, "jpy")
	if !ok || got != 120 || cur != "jpy" {
		t.Fatalf("jpy: %d %s ok=%v", got, cur, ok)
	}
	got, cur, ok = minorFromGoogleUnits("1", 234_000_000, "KWD")
	if !ok || got != 1234 || cur != "kwd" {
		t.Fatalf("kwd: %d %s ok=%v", got, cur, ok)
	}
	if _, _, ok = minorFromGoogleUnits("9", 0, " "); ok {
		t.Fatal("blank currency should fail")
	}
	if _, _, ok = minorFromGoogleUnits("", 0, "USD"); ok {
		t.Fatal("empty units and zero nanos should fail")
	}
	got, cur, ok = minorFromGoogleUnits("", 990_000_000, "USD")
	if !ok || got != 99 || cur != "usd" {
		t.Fatalf("sub-dollar nanos: %d %s ok=%v", got, cur, ok)
	}
}

func TestScaleMilliunits(t *testing.T) {
	got, ok := scaleMilliunits(9990, 2)
	if !ok || got != 19980 {
		t.Fatalf("qty 2: %d ok=%v", got, ok)
	}
	got, ok = scaleMilliunits(9990, 0)
	if !ok || got != 9990 {
		t.Fatalf("missing qty: %d ok=%v", got, ok)
	}
}
