package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/storage"
	"github.com/builtly/builtly/internal/tenant"
)

// StorageAvatars adapts storage.Service to AvatarUploader.
type StorageAvatars struct {
	Storage *storage.Service
}

// BeginAvatar reserves a pending avatar PUT.
func (a StorageAvatars) BeginAvatar(ctx context.Context, app tenant.App, userID uuid.UUID, filename, contentType string, sizeBytes int64) (AvatarUploadBegin, error) {
	res, err := a.Storage.CreateAvatar(ctx, app, userID, storage.CreateInput{
		Filename: filename, ContentType: contentType, SizeBytes: sizeBytes,
	})
	if err != nil {
		return AvatarUploadBegin{}, err
	}
	out := AvatarUploadBegin{
		FileID: res.File.ID, Filename: res.File.Filename,
		ContentType: res.File.ContentType, SizeBytes: res.File.SizeBytes,
	}
	out.Upload.Method = res.Upload.Method
	out.Upload.URL = res.Upload.URL
	out.Upload.Headers = res.Upload.Headers
	out.Upload.ExpiresAt = res.Upload.ExpiresAt
	return out, nil
}

// FinishAvatar completes the upload and returns the public avatar URL.
func (a StorageAvatars) FinishAvatar(ctx context.Context, app tenant.App, userID, fileID uuid.UUID, bind func(context.Context, db.Querier, string) error) (string, error) {
	return a.Storage.CompleteAvatar(ctx, app, userID, fileID, bind)
}
