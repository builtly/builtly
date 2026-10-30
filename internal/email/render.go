package email

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

var placeholderRe = regexp.MustCompile(`\{\{\{\s*([A-Z][A-Z0-9_]{0,31})\s*\}\}\}`)

// Placeholders returns unique {{{KEY}}} names in order of first appearance.
func Placeholders(parts ...string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, part := range parts {
		for _, m := range placeholderRe.FindAllStringSubmatch(part, -1) {
			if _, ok := seen[m[1]]; ok {
				continue
			}
			seen[m[1]] = struct{}{}
			out = append(out, m[1])
		}
	}
	return out
}

// RenderStored substitutes variables into a stored template.
// html values are escaped; subject and text are not.
func RenderStored(subject, htmlBody, text string, vars map[string]string) (string, string, string, error) {
	subj, err := substitute(subject, vars, false)
	if err != nil {
		return "", "", "", err
	}
	if strings.ContainsAny(subj, "\r\n") {
		return "", "", "", fmt.Errorf("email: subject contains a newline")
	}
	body, err := substitute(htmlBody, vars, true)
	if err != nil {
		return "", "", "", err
	}
	plain, err := substitute(text, vars, false)
	if err != nil {
		return "", "", "", err
	}
	return subj, body, plain, nil
}

func substitute(s string, vars map[string]string, escapeHTML bool) (string, error) {
	var missing string
	out := placeholderRe.ReplaceAllStringFunc(s, func(match string) string {
		m := placeholderRe.FindStringSubmatch(match)
		if len(m) < 2 {
			return match
		}
		v, ok := vars[m[1]]
		if !ok {
			if missing == "" {
				missing = m[1]
			}
			return match
		}
		v = strings.ReplaceAll(strings.ReplaceAll(v, "\r", " "), "\n", " ")
		if escapeHTML {
			return html.EscapeString(v)
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("email: missing variable %s", missing)
	}
	return out, nil
}

var tagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// PlainFromHTML is the text alternative derived from an HTML template.
func PlainFromHTML(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

func formatValue(v any) (string, error) {
	switch n := v.(type) {
	case string:
		return n, nil
	case float64:
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10), nil
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case json.Number:
		return n.String(), nil
	default:
		return "", fmt.Errorf("email: variable must be a string or number")
	}
}
