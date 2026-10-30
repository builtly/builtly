package email

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/tenant"
)

var aliasRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var varKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,31}$`)

var reservedVars = map[string]struct{}{
	VarAppName: {}, VarEmail: {}, VarSupportAddress: {}, VarActionURL: {}, VarExpiry: {},
}

// Service manages templates and enqueues sends. Delivery stays on the outbox subscriber.
type Service struct {
	db    *db.DB
	store *Store
	pub   *outbox.Publisher
}

// NewService constructs a Service.
func NewService(database *db.DB, store *Store, pub *outbox.Publisher) *Service {
	return &Service{db: database, store: store, pub: pub}
}

// Store returns the underlying store.
func (s *Service) Store() *Store { return s.store }

// List returns app templates.
func (s *Service) List(ctx context.Context, appID uuid.UUID) ([]Template, error) {
	return s.store.List(ctx, nil, appID)
}

// Get returns one template.
func (s *Service) Get(ctx context.Context, appID, id uuid.UUID) (Template, error) {
	t, err := s.store.Get(ctx, nil, appID, id)
	if errors.Is(err, db.ErrNotFound) {
		return Template{}, ErrTemplateNotFound
	}
	return t, err
}

// Create stores a template.
func (s *Service) Create(ctx context.Context, appID uuid.UUID, in Input) (Template, error) {
	in, err := validateInput(in)
	if err != nil {
		return Template{}, err
	}
	now := db.Now()
	t := Template{
		ID: uuid.Must(uuid.NewV7()), AppID: appID, Key: in.Key, Name: in.Name,
		Subject: in.Subject, HTML: in.HTML, Text: in.Text, Variables: in.Variables,
		Published: in.Published, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Insert(ctx, nil, t); err != nil {
		var uv db.ErrUniqueViolation
		if errors.As(err, &uv) {
			return Template{}, ErrTemplateKeyTaken
		}
		return Template{}, err
	}
	return t, nil
}

// Update replaces template fields.
func (s *Service) Update(ctx context.Context, appID, id uuid.UUID, in Input) (Template, error) {
	cur, err := s.Get(ctx, appID, id)
	if err != nil {
		return Template{}, err
	}
	in, err = validateInput(in)
	if err != nil {
		return Template{}, err
	}
	if cur.Published && !in.Published {
		if _, err := s.store.BindingForTemplate(ctx, nil, appID, id); err == nil {
			return Template{}, ErrTemplateUnpublished.WithDetails(map[string]any{
				"reason": "unpublish the template only after removing its trigger binding",
			})
		} else if !errors.Is(err, db.ErrNotFound) {
			return Template{}, err
		}
	}
	cur.Key, cur.Name, cur.Subject, cur.HTML, cur.Text = in.Key, in.Name, in.Subject, in.HTML, in.Text
	cur.Variables, cur.Published, cur.UpdatedAt = in.Variables, in.Published, db.Now()
	if err := s.store.Update(ctx, nil, cur); err != nil {
		var uv db.ErrUniqueViolation
		if errors.As(err, &uv) {
			return Template{}, ErrTemplateKeyTaken
		}
		if errors.Is(err, db.ErrNotFound) {
			return Template{}, ErrTemplateNotFound
		}
		return Template{}, err
	}
	return cur, nil
}

// Delete removes a template that is not bound.
func (s *Service) Delete(ctx context.Context, appID, id uuid.UUID) error {
	if _, err := s.Get(ctx, appID, id); err != nil {
		return err
	}
	if _, err := s.store.BindingForTemplate(ctx, nil, appID, id); err == nil {
		return ErrTemplateInUse
	} else if !errors.Is(err, db.ErrNotFound) {
		return err
	}
	if err := s.store.Delete(ctx, nil, appID, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return ErrTemplateNotFound
		}
		return err
	}
	return nil
}

// TriggerView is a system trigger plus the current binding.
type TriggerView struct {
	Trigger
	TemplateID *uuid.UUID `json:"template_id"`
}

// ListTriggers returns every system trigger and its binding, if any.
func (s *Service) ListTriggers(ctx context.Context, appID uuid.UUID) ([]TriggerView, error) {
	bindings, err := s.store.ListBindings(ctx, nil, appID)
	if err != nil {
		return nil, err
	}
	by := map[string]uuid.UUID{}
	for _, b := range bindings {
		by[b.Trigger] = b.TemplateID
	}
	out := make([]TriggerView, 0, len(Triggers()))
	for _, t := range Triggers() {
		v := TriggerView{Trigger: t}
		if id, ok := by[t.Key]; ok {
			v.TemplateID = &id
		}
		out = append(out, v)
	}
	return out, nil
}

// SetTrigger binds a published template, or clears the binding when templateID is nil.
func (s *Service) SetTrigger(ctx context.Context, appID uuid.UUID, trigger string, templateID *uuid.UUID) error {
	def, ok := LookupTrigger(trigger)
	if !ok {
		return ErrInvalidTrigger
	}
	if templateID == nil {
		return s.store.DeleteBinding(ctx, nil, appID, trigger)
	}
	t, err := s.Get(ctx, appID, *templateID)
	if err != nil {
		return err
	}
	if !t.Published {
		return ErrTemplateUnpublished
	}
	if err := validateForTrigger(def, t); err != nil {
		return err
	}
	return s.store.UpsertBinding(ctx, nil, Binding{
		AppID: appID, Trigger: trigger, TemplateID: t.ID, UpdatedAt: db.Now(),
	})
}

// PublishWelcomeTx enqueues a welcome mail when that trigger is bound. No binding is a no-op.
func (s *Service) PublishWelcomeTx(ctx context.Context, tx *sqlx.Tx, app tenant.App, to string) error {
	if s == nil || s.pub == nil || to == "" {
		return nil
	}
	b, err := s.store.GetBinding(ctx, tx, app.ID, TriggerWelcome)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	t, err := s.store.Get(ctx, tx, app.ID, b.TemplateID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && !t.Published) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := json.Marshal(Payload{
		Kind: KindSystem, Trigger: TriggerWelcome, To: to,
	})
	if err != nil {
		return err
	}
	appID := app.ID
	return s.pub.PublishTx(ctx, tx, outbox.Event{
		AppID: &appID, Type: outbox.EventEmailRequested, Payload: raw,
	})
}

// RequestSend enqueues one custom template to the signed-in user's address.
func (s *Service) RequestSend(ctx context.Context, app tenant.App, to, idem, templateKey string, vars map[string]any) (uuid.UUID, error) {
	if idem == "" || len(idem) > 200 || strings.ContainsAny(idem, "\r\n") {
		return uuid.Nil, ErrMissingIdempotency
	}
	t, err := s.store.GetByKey(ctx, nil, app.ID, templateKey)
	if errors.Is(err, db.ErrNotFound) {
		return uuid.Nil, ErrTemplateNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	if !t.Published {
		return uuid.Nil, ErrTemplateUnpublished
	}
	if _, err := s.store.BindingForTemplate(ctx, nil, app.ID, t.ID); err == nil {
		return uuid.Nil, ErrNotCustomTemplate
	} else if !errors.Is(err, db.ErrNotFound) {
		return uuid.Nil, err
	}
	if err := rejectSecurityPlaceholders(t); err != nil {
		return uuid.Nil, err
	}
	resolved, err := resolveVars(t, vars, map[string]string{
		VarAppName: app.Name, VarEmail: to, VarSupportAddress: app.Settings.Email.ReplyTo,
	})
	if err != nil {
		return uuid.Nil, err
	}
	if _, _, _, err := RenderStored(t.Subject, t.HTML, t.Text, resolved); err != nil {
		return uuid.Nil, ErrInvalidTemplate.WithDetails(map[string]any{"reason": err.Error()})
	}
	sum := hashVars(templateKey, vars)
	outID := uuid.Must(uuid.NewV7())
	raw, err := json.Marshal(Payload{
		Kind: KindCustom, TemplateKey: t.Key, To: to, Variables: vars,
	})
	if err != nil {
		return uuid.Nil, err
	}
	appID := app.ID
	err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := s.store.InsertSend(ctx, tx, Send{
			AppID: app.ID, IdempotencyKey: idem, TemplateKey: t.Key, VariablesHash: sum,
			OutboxID: outID, CreatedAt: db.Now(),
		}); err != nil {
			return err
		}
		return s.pub.PublishTx(ctx, tx, outbox.Event{
			ID: outID, AppID: &appID, Type: outbox.EventEmailRequested, Payload: raw,
		})
	})
	if err == nil {
		return outID, nil
	}
	var uv db.ErrUniqueViolation
	if !errors.As(err, &uv) {
		return uuid.Nil, err
	}
	existing, gerr := s.store.GetSend(ctx, nil, app.ID, idem)
	if gerr != nil {
		return uuid.Nil, err
	}
	if existing.TemplateKey != t.Key || existing.VariablesHash != sum {
		return uuid.Nil, ErrIdempotencyConflict
	}
	return existing.OutboxID, nil
}

func validateInput(in Input) (Input, error) {
	in.Key = strings.TrimSpace(in.Key)
	in.Name = strings.TrimSpace(in.Name)
	in.Subject = strings.TrimSpace(in.Subject)
	if !aliasRe.MatchString(in.Key) || isBuiltinName(in.Key) {
		return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "alias must be a lowercase slug and not a built-in name"})
	}
	if in.Name == "" || len(in.Name) > 80 || strings.ContainsAny(in.Name, "\r\n") {
		return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "name is required"})
	}
	if in.Subject == "" || len(in.Subject) > 200 || strings.ContainsAny(in.Subject, "\r\n") {
		return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "subject is required"})
	}
	if strings.TrimSpace(in.HTML) == "" || len(in.HTML) > 100_000 {
		return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "html is required"})
	}
	if strings.TrimSpace(in.Text) == "" {
		in.Text = PlainFromHTML(in.HTML)
	}
	if len(in.Text) > 50_000 {
		return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "text is too long"})
	}
	if len(in.Variables) > 20 {
		return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "at most 20 variables"})
	}
	seen := map[string]struct{}{}
	for _, v := range in.Variables {
		if !varKeyRe.MatchString(v.Key) {
			return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "variable key must be UPPER_SNAKE"})
		}
		if _, ok := reservedVars[v.Key]; ok {
			return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": v.Key + " is reserved"})
		}
		if _, ok := seen[v.Key]; ok {
			return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "duplicate variable " + v.Key})
		}
		seen[v.Key] = struct{}{}
		if v.Type != "string" && v.Type != "number" {
			return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "variable type must be string or number"})
		}
		if v.Fallback != nil {
			if err := checkFallback(v); err != nil {
				return in, err
			}
		}
	}
	for _, ph := range Placeholders(in.Subject, in.HTML, in.Text) {
		if _, reserved := reservedVars[ph]; reserved {
			continue
		}
		if _, ok := seen[ph]; !ok {
			return in, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "undeclared variable " + ph})
		}
	}
	return in, nil
}

func checkFallback(v Variable) error {
	var raw any
	if err := json.Unmarshal(*v.Fallback, &raw); err != nil {
		return ErrInvalidTemplate.WithDetails(map[string]any{"reason": "invalid fallback for " + v.Key})
	}
	switch v.Type {
	case "string":
		if _, ok := raw.(string); !ok {
			return ErrInvalidTemplate.WithDetails(map[string]any{"reason": v.Key + " fallback must be a string"})
		}
	case "number":
		if _, ok := raw.(float64); !ok {
			return ErrInvalidTemplate.WithDetails(map[string]any{"reason": v.Key + " fallback must be a number"})
		}
	}
	return nil
}

func validateForTrigger(def Trigger, t Template) error {
	injected := map[string]struct{}{}
	for _, k := range def.Injected {
		injected[k] = struct{}{}
	}
	if def.Required {
		if _, ok := injected[VarActionURL]; ok && !strings.Contains(t.HTML, "{{{ACTION_URL}}}") && !strings.Contains(t.Text, "{{{ACTION_URL}}}") {
			return ErrInvalidTemplate.WithDetails(map[string]any{"reason": "security templates must include {{{ACTION_URL}}}"})
		}
	}
	for _, ph := range Placeholders(t.Subject, t.HTML, t.Text) {
		if _, ok := injected[ph]; ok {
			continue
		}
		found := false
		for _, v := range t.Variables {
			if v.Key == ph {
				found = true
				if v.Fallback == nil {
					return ErrInvalidTemplate.WithDetails(map[string]any{"reason": ph + " needs a fallback when used by a system trigger"})
				}
			}
		}
		if !found {
			return ErrInvalidTemplate.WithDetails(map[string]any{"reason": "undeclared variable " + ph})
		}
	}
	return nil
}

func rejectSecurityPlaceholders(t Template) error {
	for _, ph := range Placeholders(t.Subject, t.HTML, t.Text) {
		if ph == VarActionURL || ph == VarExpiry {
			return ErrNotCustomTemplate.WithDetails(map[string]any{"reason": ph + " is only available to system triggers"})
		}
	}
	return nil
}

func resolveVars(t Template, provided map[string]any, injected map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range injected {
		out[k] = v
	}
	declared := map[string]Variable{}
	for _, v := range t.Variables {
		declared[v.Key] = v
	}
	for k := range provided {
		if _, ok := reservedVars[k]; ok {
			return nil, ErrInvalidTemplate.WithDetails(map[string]any{"reason": k + " is reserved"})
		}
		if _, ok := declared[k]; !ok {
			return nil, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "unknown variable " + k})
		}
	}
	for _, v := range t.Variables {
		raw, ok := provided[v.Key]
		if !ok {
			if v.Fallback == nil {
				return nil, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "missing variable " + v.Key})
			}
			var fb any
			if err := json.Unmarshal(*v.Fallback, &fb); err != nil {
				return nil, ErrInvalidTemplate.WithDetails(map[string]any{"reason": "invalid fallback for " + v.Key})
			}
			raw = fb
		}
		s, err := formatValue(raw)
		if err != nil || (v.Type == "number" && !isNumber(raw)) || (v.Type == "string" && !isString(raw)) {
			return nil, ErrInvalidTemplate.WithDetails(map[string]any{"reason": v.Key + " must be a " + v.Type})
		}
		out[v.Key] = s
	}
	return out, nil
}

func isString(v any) bool { _, ok := v.(string); return ok }
func isNumber(v any) bool {
	switch v.(type) {
	case float64, json.Number:
		return true
	default:
		return false
	}
}

func hashVars(key string, vars map[string]any) string {
	raw, _ := json.Marshal(struct {
		Key  string         `json:"key"`
		Vars map[string]any `json:"vars"`
	}{key, vars})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func isBuiltinName(key string) bool {
	return key == "verify_email" || key == "reset_password" || key == "email_changed"
}
