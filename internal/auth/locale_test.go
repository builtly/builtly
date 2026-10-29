package auth

import "testing"

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"en":              "en",
		"zh-CN":           "zh-CN",
		"en-US,en;q=0.9":  "en-US",
		"  fr-CA ;q=0.8 ": "fr-CA",
		"*":               "",
		"toolonglangtag":  "",
		"en!!":            "",
	}
	for in, want := range cases {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAttributionUTMSource(t *testing.T) {
	if got := attributionUTMSource([]byte(`{"utm_source":" twitter "}`)); got != "twitter" {
		t.Fatalf("got %q", got)
	}
	if got := attributionUTMSource([]byte(`{`)); got != "" {
		t.Fatalf("bad json got %q", got)
	}
}
