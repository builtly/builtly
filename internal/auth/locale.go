package auth

import (
	"encoding/json"
	"strings"
)

// NormalizeLocale trims and validates a BCP 47-ish locale tag (e.g. en, zh-CN).
// Empty or invalid input yields "".
func NormalizeLocale(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Accept-Language: take first range, drop ;q=
	if i := strings.IndexAny(raw, ",;"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return ""
	}
	parts := strings.Split(raw, "-")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	lang := strings.ToLower(parts[0])
	if len(lang) < 2 || len(lang) > 8 {
		return ""
	}
	for _, c := range lang {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	out := lang
	for _, p := range parts[1:] {
		if p == "" || len(p) > 8 {
			return ""
		}
		for _, c := range p {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				return ""
			}
		}
		out += "-" + p
	}
	if len(out) > 35 {
		return ""
	}
	return out
}

func attributionUTMSource(attr []byte) string {
	if len(attr) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(attr, &m); err != nil {
		return ""
	}
	v, _ := m["utm_source"].(string)
	return strings.TrimSpace(v)
}
