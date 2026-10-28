package email

import (
	"bytes"
	"embed"
	"fmt"
	htmltmpl "html/template"
	texttmpl "text/template"
)

//go:embed templates/*
var templateFS embed.FS

// TemplateData is passed to email templates.
type TemplateData struct {
	AppName        string
	ActionURL      string
	Expiry         string
	SupportAddress string
}

// Templates renders embedded transactional email templates.
type Templates struct {
	html map[string]*htmltmpl.Template
	text map[string]*texttmpl.Template
}

// LoadTemplates parses embedded templates.
func LoadTemplates() (*Templates, error) {
	names := []string{"verify_email", "reset_password", "email_changed"}
	t := &Templates{
		html: map[string]*htmltmpl.Template{},
		text: map[string]*texttmpl.Template{},
	}
	for _, name := range names {
		h, err := htmltmpl.ParseFS(templateFS, "templates/"+name+".html.tmpl")
		if err != nil {
			return nil, fmt.Errorf("email: html %s: %w", name, err)
		}
		x, err := texttmpl.ParseFS(templateFS, "templates/"+name+".txt.tmpl")
		if err != nil {
			return nil, fmt.Errorf("email: text %s: %w", name, err)
		}
		t.html[name] = h
		t.text[name] = x
	}
	return t, nil
}

// Render returns subject, html, text for a template name.
func (t *Templates) Render(name string, data TemplateData) (subject, html, text string, err error) {
	ht, ok := t.html[name]
	if !ok {
		return "", "", "", fmt.Errorf("email: unknown template %q", name)
	}
	tt := t.text[name]
	var hb, tb bytes.Buffer
	if err := ht.Execute(&hb, data); err != nil {
		return "", "", "", err
	}
	if err := tt.Execute(&tb, data); err != nil {
		return "", "", "", err
	}
	switch name {
	case "verify_email":
		subject = "Verify your email"
	case "reset_password":
		subject = "Reset your password"
	case "email_changed":
		subject = "Confirm your new email"
	default:
		subject = name
	}
	return subject, hb.String(), tb.String(), nil
}
