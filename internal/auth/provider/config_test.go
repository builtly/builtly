package provider

import (
	"testing"

	"github.com/builtly/builtly/internal/integration"
)

func TestParseConfigGooglePlatformClientIDs(t *testing.T) {
	settings := []byte(`{
		"client_id":"web.apps.googleusercontent.com",
		"ios_client_id":"ios.apps.googleusercontent.com",
		"android_client_id":"android.apps.googleusercontent.com",
		"native_client_ids":["ios.apps.googleusercontent.com","legacy.apps.googleusercontent.com"]
	}`)
	secrets := []byte(`{"client_secret":"sec"}`)
	cfg, err := ParseConfig(integration.ProviderGoogleOAuth, settings, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "web.apps.googleusercontent.com" {
		t.Fatalf("web client_id: %q", cfg.ClientID)
	}
	if cfg.ClientSecret != "sec" {
		t.Fatalf("secret: %q", cfg.ClientSecret)
	}
	want := []string{
		"ios.apps.googleusercontent.com",
		"android.apps.googleusercontent.com",
		"legacy.apps.googleusercontent.com",
	}
	if len(cfg.NativeClientIDs) != len(want) {
		t.Fatalf("native=%v want %v", cfg.NativeClientIDs, want)
	}
	for i := range want {
		if cfg.NativeClientIDs[i] != want[i] {
			t.Fatalf("native[%d]=%q want %q", i, cfg.NativeClientIDs[i], want[i])
		}
	}
}
