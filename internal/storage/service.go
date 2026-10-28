package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/tenant"
)

// Service owns file upload/download lifecycle.
type Service struct {
	db           *db.DB
	store        *Store
	resolver     *integration.Resolver
	r2           *R2Client
	now          func() time.Time
	testEndpoint string // tests only
}

// NewService constructs a storage Service.
func NewService(database *db.DB, store *Store, resolver *integration.Resolver, r2 *R2Client) *Service {
	return &Service{db: database, store: store, resolver: resolver, r2: r2, now: time.Now}
}

// Store exposes the file store (auth delete hook).
func (s *Service) Store() *Store { return s.store }

// CreateInput is POST /files.
type CreateInput struct {
	Filename    string
	ContentType string
	SizeBytes   int64
}

// CreateResult is the create response.
type CreateResult struct {
	File   File
	Upload struct {
		Method    string            `json:"method"`
		URL       string            `json:"url"`
		Headers   map[string]string `json:"headers"`
		ExpiresAt time.Time         `json:"expires_at"`
	}
}

// Create reserves a pending file and returns a presigned PUT.
func (s *Service) Create(ctx context.Context, app tenant.App, userID uuid.UUID, in CreateInput) (CreateResult, error) {
	in.Filename = strings.TrimSpace(in.Filename)
	in.ContentType = strings.TrimSpace(strings.ToLower(in.ContentType))
	if in.Filename == "" || in.ContentType == "" || in.SizeBytes <= 0 {
		return CreateResult{}, ErrValidationFailed
	}
	if path.Base(in.Filename) != in.Filename || strings.Contains(in.Filename, "..") {
		return CreateResult{}, ErrValidationFailed.WithDetails(map[string]any{"reason": "invalid filename"})
	}
	st := app.Settings.Storage
	if in.SizeBytes > st.MaxFileBytes {
		return CreateResult{}, ErrFileTooLarge
	}
	if !allowedType(st.AllowedContentTypes, in.ContentType) {
		return CreateResult{}, ErrContentTypeNotAllowed
	}
	cfg, err := s.r2Config(ctx, app.ID)
	if err != nil {
		return CreateResult{}, err
	}
	used, err := s.store.SumUsageBytes(ctx, nil, app.ID, userID)
	if err != nil {
		return CreateResult{}, err
	}
	if used+in.SizeBytes > st.UserQuotaBytes {
		return CreateResult{}, ErrQuotaExceeded
	}

	now := db.Time(s.now().UTC())
	fileID := uuid.Must(uuid.NewV7())
	key := cfg.KeyPrefix + "/u/" + userID.String() + "/" + fileID.String()
	f := File{
		ID: fileID, AppID: app.ID, UserID: userID, ConnectionID: cfg.connectionID,
		Bucket: cfg.Bucket, ObjectKey: key, Filename: in.Filename, ContentType: in.ContentType,
		SizeBytes: in.SizeBytes, Status: StatusPending, CreatedAt: now,
	}
	if err := s.store.Insert(ctx, nil, f); err != nil {
		return CreateResult{}, err
	}
	url, headers, exp, err := s.r2.PresignPUT(cfg.R2Config, key, in.ContentType, 15*time.Minute, s.now().UTC())
	if err != nil {
		return CreateResult{}, ErrUpstream.WithDetails(map[string]any{"reason": err.Error()})
	}
	var out CreateResult
	out.File = f
	out.Upload.Method = "PUT"
	out.Upload.URL = url
	out.Upload.Headers = headers
	out.Upload.ExpiresAt = exp
	return out, nil
}

// Complete verifies the object and marks uploaded.
func (s *Service) Complete(ctx context.Context, app tenant.App, userID, fileID uuid.UUID) (File, error) {
	f, err := s.store.GetByID(ctx, nil, app.ID, userID, fileID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return File{}, ErrFileNotFound
		}
		return File{}, err
	}
	if f.Status == StatusUploaded {
		return f, nil
	}
	if f.Status != StatusPending {
		return File{}, ErrFileNotFound
	}
	cfg, err := s.r2Config(ctx, app.ID)
	if err != nil {
		return File{}, err
	}
	meta, err := s.r2.HeadObject(ctx, cfg.R2Config, f.ObjectKey)
	if err != nil {
		if errors.Is(err, errObjectNotFound) {
			_ = s.store.HardDelete(ctx, nil, app.ID, f.ID)
			return File{}, ErrUploadMismatch
		}
		return File{}, ErrUpstream.WithDetails(map[string]any{"reason": err.Error()})
	}
	ct := strings.ToLower(strings.TrimSpace(meta.ContentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if meta.ContentLength != f.SizeBytes || (ct != "" && ct != f.ContentType) {
		_ = s.r2.DeleteObject(ctx, cfg.R2Config, f.ObjectKey)
		_ = s.store.HardDelete(ctx, nil, app.ID, f.ID)
		return File{}, ErrUploadMismatch
	}
	now := db.Time(s.now().UTC())
	if err := s.store.MarkUploaded(ctx, nil, app.ID, f.ID, now); err != nil {
		return File{}, err
	}
	f.Status = StatusUploaded
	f.UploadedAt = db.NullTime{Time: now, Valid: true}
	return f, nil
}

// Get returns metadata + download URL.
func (s *Service) Get(ctx context.Context, app tenant.App, userID, fileID uuid.UUID) (File, string, time.Time, error) {
	f, err := s.store.GetByID(ctx, nil, app.ID, userID, fileID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return File{}, "", time.Time{}, ErrFileNotFound
		}
		return File{}, "", time.Time{}, err
	}
	if f.Status != StatusUploaded {
		return File{}, "", time.Time{}, ErrFileNotFound
	}
	cfg, err := s.r2Config(ctx, app.ID)
	if err != nil {
		return File{}, "", time.Time{}, err
	}
	url, exp, err := s.r2.PresignGET(cfg.R2Config, f.ObjectKey, f.Filename, 5*time.Minute, s.now().UTC())
	if err != nil {
		return File{}, "", time.Time{}, ErrUpstream.WithDetails(map[string]any{"reason": err.Error()})
	}
	return f, url, exp, nil
}

// List returns uploaded files.
func (s *Service) List(ctx context.Context, appID, userID uuid.UUID, limit int, cursor string) ([]File, string, error) {
	var after db.Time
	var afterID uuid.UUID
	if cursor != "" {
		parts := strings.SplitN(cursor, ",", 2)
		if len(parts) != 2 {
			return nil, "", ErrValidationFailed
		}
		t, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			return nil, "", ErrValidationFailed
		}
		after = db.Time(t)
		afterID, err = uuid.Parse(parts[1])
		if err != nil {
			return nil, "", ErrValidationFailed
		}
	}
	rows, err := s.store.ListUploaded(ctx, nil, appID, userID, limit, after, afterID)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) == limit && limit > 0 {
		last := rows[len(rows)-1]
		next = time.Time(last.CreatedAt).UTC().Format(time.RFC3339Nano) + "," + last.ID.String()
	}
	return rows, next, nil
}

// Delete marks deleted and removes the R2 object.
func (s *Service) Delete(ctx context.Context, app tenant.App, userID, fileID uuid.UUID) error {
	f, err := s.store.GetByID(ctx, nil, app.ID, userID, fileID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return ErrFileNotFound
		}
		return err
	}
	if f.Status == StatusDeleted {
		return nil
	}
	now := db.Time(s.now().UTC())
	if err := s.store.MarkDeleted(ctx, nil, app.ID, f.ID, now); err != nil {
		return err
	}
	cfg, err := s.r2Config(ctx, app.ID)
	if err != nil {
		return nil // marked; cleanup will retry object delete when configured
	}
	if err := s.r2.DeleteObject(ctx, cfg.R2Config, f.ObjectKey); err == nil {
		_ = s.store.HardDelete(ctx, nil, app.ID, f.ID)
	}
	return nil
}

// MarkDeletedForUser implements the auth account-deletion hook.
func (s *Service) MarkDeletedForUser(ctx context.Context, tx *sqlx.Tx, appID, userID uuid.UUID) error {
	return s.store.MarkDeletedForUser(ctx, tx, appID, userID, db.Time(s.now().UTC()))
}

type r2Resolved struct {
	R2Config
	connectionID string
}

func (s *Service) r2Config(ctx context.Context, appID uuid.UUID) (r2Resolved, error) {
	resolved, err := s.resolver.For(ctx, appID, integration.ProviderR2)
	if err == integration.ErrNotConfigured {
		return r2Resolved{}, ErrIntegrationNotConfigured
	}
	if err != nil {
		return r2Resolved{}, err
	}
	var settings map[string]any
	_ = json.Unmarshal(resolved.Settings, &settings)
	var sec integration.R2Secrets
	if err := json.Unmarshal(resolved.Secrets, &sec); err != nil {
		return r2Resolved{}, ErrIntegrationNotConfigured
	}
	prefix := appID.String()
	if len(resolved.BindingSettings) > 0 {
		var bs map[string]any
		_ = json.Unmarshal(resolved.BindingSettings, &bs)
		if p, ok := bs["key_prefix"].(string); ok && strings.TrimSpace(p) != "" {
			prefix = strings.TrimSpace(p)
		}
	}
	account, _ := settings["account_id"].(string)
	bucket, _ := settings["bucket"].(string)
	if account == "" || bucket == "" || sec.AccessKeyID == "" || sec.SecretAccessKey == "" {
		return r2Resolved{}, ErrIntegrationNotConfigured
	}
	return r2Resolved{
		R2Config: R2Config{
			AccountID: account, Bucket: bucket,
			AccessKeyID: sec.AccessKeyID, SecretAccessKey: sec.SecretAccessKey,
			KeyPrefix: prefix, Endpoint: s.testEndpoint,
		},
		connectionID: resolved.ConnectionID,
	}, nil
}

func allowedType(allow []string, ct string) bool {
	for _, a := range allow {
		if strings.EqualFold(strings.TrimSpace(a), ct) {
			return true
		}
	}
	return false
}
