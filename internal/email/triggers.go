package email

// System triggers. Security triggers always send; the binding only overrides copy.
// Welcome is optional and has no built-in template.
const (
	TriggerVerifyEmail   = "auth.verify_email"
	TriggerResetPassword = "auth.reset_password"
	TriggerEmailChanged  = "auth.email_changed"
	TriggerWelcome       = "auth.welcome"
)

const (
	KindSystem = "system"
	KindCustom = "custom"
)

// Injected variable names. Clients cannot set these.
const (
	VarAppName        = "APP_NAME"
	VarEmail          = "EMAIL"
	VarSupportAddress = "SUPPORT_ADDRESS"
	VarActionURL      = "ACTION_URL"
	VarExpiry         = "EXPIRY"
)

// Trigger describes one Builtly-owned send.
type Trigger struct {
	Key      string   `json:"trigger"`
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	Builtin  string   `json:"builtin,omitempty"`
	Injected []string `json:"injected"`
}

// Triggers is the closed set of system behaviors that can bind a template.
func Triggers() []Trigger {
	return []Trigger{
		{Key: TriggerVerifyEmail, Label: "Verify email", Required: true, Builtin: "verify_email", Injected: securityVars()},
		{Key: TriggerResetPassword, Label: "Reset password", Required: true, Builtin: "reset_password", Injected: securityVars()},
		{Key: TriggerEmailChanged, Label: "Confirm email change", Required: true, Builtin: "email_changed", Injected: securityVars()},
		{Key: TriggerWelcome, Label: "Welcome", Required: false, Injected: welcomeVars()},
	}
}

func securityVars() []string {
	return []string{VarActionURL, VarAppName, VarEmail, VarExpiry, VarSupportAddress}
}

func welcomeVars() []string {
	return []string{VarAppName, VarEmail, VarSupportAddress}
}

// LookupTrigger returns the trigger definition.
func LookupTrigger(key string) (Trigger, bool) {
	for _, t := range Triggers() {
		if t.Key == key {
			return t, true
		}
	}
	return Trigger{}, false
}

// TriggerForBuiltin maps an embedded template name to its system trigger.
func TriggerForBuiltin(name string) string {
	switch name {
	case "verify_email":
		return TriggerVerifyEmail
	case "reset_password":
		return TriggerResetPassword
	case "email_changed":
		return TriggerEmailChanged
	default:
		return ""
	}
}

// BuiltinForTrigger maps a trigger back to the embedded template, if any.
func BuiltinForTrigger(trigger string) string {
	t, ok := LookupTrigger(trigger)
	if !ok {
		return ""
	}
	return t.Builtin
}

func injectedSet(trigger string) map[string]struct{} {
	t, ok := LookupTrigger(trigger)
	if !ok {
		return map[string]struct{}{
			VarAppName: {}, VarEmail: {}, VarSupportAddress: {},
		}
	}
	out := make(map[string]struct{}, len(t.Injected))
	for _, k := range t.Injected {
		out[k] = struct{}{}
	}
	return out
}
