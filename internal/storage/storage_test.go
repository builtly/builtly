package storage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/billing/stripeapi"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/server"
	"github.com/builtly/builtly/internal/storage"
	"github.com/builtly/builtly/internal/tenant"
)

const storageAdminToken = "test-admin-token-at-least-32-chars!!"

type fakeObject struct {
	body        []byte
	contentType string
}

type storageHarness struct {
	srv     *server.Server
	tSvc    *tenant.Service
	iSvc    *integration.Service
	authSvc *auth.Service
	storage *storage.Service

	mu      sync.Mutex
	objects map[string]fakeObject // "/bucket/key"
}

func setupStorage(t *testing.T) *storageHarness {
	t.Helper()
	h := &storageHarness{objects: map[string]fakeObject{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		h.mu.Lock()
		defer h.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			h.objects[key] = fakeObject{body: body, contentType: r.Header.Get("Content-Type")}
			w.WriteHeader(200)
		case http.MethodHead:
			o, ok := h.objects[key]
			if !ok {
				http.Error(w, "missing", 404)
				return
			}
			w.Header().Set("Content-Length", itoa(len(o.body)))
			w.Header().Set("Content-Type", o.contentType)
			w.WriteHeader(200)
		case http.MethodDelete:
			delete(h.objects, key)
			w.WriteHeader(204)
		case http.MethodGet:
			o, ok := h.objects[key]
			if !ok {
				http.Error(w, "missing", 404)
				return
			}
			w.Header().Set("Content-Type", o.contentType)
			_, _ = w.Write(o.body)
		default:
			http.Error(w, "method", 405)
		}
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	database := dbtest.New(t, "sqlite")
	key := bytes.Repeat([]byte{3}, 32)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	jwtKey := bytes.Repeat([]byte{5}, 32)
	cfg := &config.Config{
		Env: "development", PublicURL: "https://api.example.com",
		AdminEnabled: true, AdminToken: storageAdminToken,
		JWTAccessTTL: time.Minute, JWTRefreshTTL: 24 * time.Hour,
		JWTSigningKeys: []config.KeyMaterial{{ID: "dev", Key: jwtKey}},
	}
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	tMW := tenant.NewMiddleware(tSvc, 600)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)
	iSvc := integration.NewService(database, iStore, box, env, cfg.PublicURL, resolver)
	userStore := auth.NewStore(database)
	creditSvc := credit.NewService(database, credit.NewStore(database))
	tokens := auth.NewTokenService(cfg.JWTSigningKeys, cfg.JWTAccessTTL)
	outboxStore := outbox.NewStore(database)
	oreg := outbox.NewRegistry()
	oreg.DefaultSubscriptions()
	pub := outbox.NewPublisher(outboxStore, box, oreg)
	authSvc := auth.NewService(database, userStore, tokens, creditSvc, pub, cfg.JWTRefreshTTL)

	r2 := storage.NewR2Client(upstream.Client())
	storSvc := storage.NewService(database, storage.NewStore(database), resolver, r2)
	// Point all R2 traffic at the fake by rewriting endpoint via Create after binding — we inject via connection settings.
	authSvc.SetFileMarker(storSvc)
	authSvc.SetAvatarUploader(auth.StorageAvatars{Storage: storSvc})

	stripeClient := stripeapi.New(http.DefaultClient)
	catSvc := catalog.NewService(catalog.NewStore(database), resolver, stripeClient)
	billSvc := billing.NewService(database, billing.NewStore(database), catSvc, creditSvc, pub, resolver, iSvc, iStore, box, userStore, tSvc, stripeClient, true)
	adminH := admin.NewHandler(database, tSvc, iSvc, resolver, admin.NewAuditStore(database), tStore, iStore, tMW, userStore, creditSvc, billing.NewStore(database), outboxStore, box, catSvc, stripeClient, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, database, log, server.Deps{
		Tenant: tSvc, TenantMW: tMW, Secrets: box, AdminAuth: admin.NewAuth(cfg), Admin: adminH,
		AuthMW: auth.NewMiddleware(tokens, tSvc), Credits: creditSvc,
		CreditUser: credit.NewUserHandler(creditSvc), AI: ai.NewHandler(creditSvc, userStore),
		Auth: auth.NewHandler(authSvc, nil), Catalog: catalog.NewHandler(catSvc), Billing: billing.NewHandler(billSvc),
		Storage: storage.NewHandler(storSvc),
	})
	h.srv, h.tSvc, h.iSvc, h.authSvc, h.storage = srv, tSvc, iSvc, authSvc, storSvc

	// Monkey-patch: after createR2Conn we'll set Endpoint on resolved config by using a test-only approach —
	// wrap by creating connection with account_id that we ignore; instead set Endpoint via Service internal.
	// Use r2 client endpoint override through connection: we store endpoint in settings for tests by temporarily
	// using Create with custom client — patch storage service R2 endpoint by recreating with endpoint in cfg.
	// Simplest: expose SetEndpoint via creating files with a custom R2Config in tests through admin + override.
	h.injectEndpoint(t, upstream.URL, storSvc, r2)
	return h
}

func (h *storageHarness) injectEndpoint(t *testing.T, endpoint string, svc *storage.Service, r2 *storage.R2Client) {
	t.Helper()
	// Replace client's HTTP and force endpoint by wrapping Presign through connection account_id unused —
	// storage uses cfg.Endpoint when set. We'll set it via a test helper on the service by creating files
	// after binding; use storage.TestSetEndpoint if needed.
	storage.SetTestEndpoint(svc, endpoint)
	_ = r2
}

func (h *storageHarness) createApp(t *testing.T) tenant.App {
	t.Helper()
	app, err := h.tSvc.Create(context.Background(), "acme", "Acme", tenant.Settings{
		AllowedOrigins: []string{"https://acme.com"},
		Auth:           tenant.AuthSettings{PasswordEnabled: true},
		Email:          tenant.EmailSettings{FromAddress: "n@acme.com", ActionBaseURL: "https://acme.com"},
		Storage: tenant.StorageSettings{
			MaxFileBytes: 1024, UserQuotaBytes: 2048,
			AllowedContentTypes: []string{"image/png", "application/pdf"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func (h *storageHarness) createR2Conn(t *testing.T) string {
	t.Helper()
	return h.createR2ConnWithPublic(t, "")
}

func (h *storageHarness) createR2ConnWithPublic(t *testing.T, publicBase string) string {
	t.Helper()
	settings := map[string]any{"account_id": "acct", "bucket": "bucket"}
	if publicBase != "" {
		settings["public_base_url"] = publicBase
	}
	body, _ := json.Marshal(map[string]any{
		"provider": "r2", "name": "r2",
		"settings": settings,
		"secrets":  map[string]any{"access_key_id": "AKID", "secret_access_key": "SECRETSECRETSECRETSECRETSECR"},
	})
	req := httptest.NewRequest("POST", "/api/v1/admin/connections", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+storageAdminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("r2 conn: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return out["id"].(string)
}

func (h *storageHarness) bindR2(t *testing.T, appID, connID string) {
	t.Helper()
	id, _ := uuid.Parse(appID)
	app, _ := h.tSvc.Get(context.Background(), id)
	cfgBody, _ := json.Marshal(map[string]any{
		"settings": app.Settings,
		"integrations": map[string]any{
			"r2": map[string]any{"connection_id": connID, "settings": map[string]any{"key_prefix": "acme"}},
		},
	})
	req := httptest.NewRequest("PUT", "/api/v1/admin/apps/"+appID+"/config", bytes.NewReader(cfgBody))
	req.Header.Set("Authorization", "Bearer "+storageAdminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("bind: %d %s", rr.Code, rr.Body.String())
	}
}

func (h *storageHarness) register(t *testing.T, app tenant.App, email string) (string, uuid.UUID) {
	t.Helper()
	resp, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: email, Password: "password123",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Tokens.AccessToken, resp.User.ID
}

func TestFilesUploadCompleteDownloadDelete(t *testing.T) {
	h := setupStorage(t)
	app := h.createApp(t)
	conn := h.createR2Conn(t)
	h.bindR2(t, app.ID.String(), conn)
	access, _ := h.register(t, app, "u@ex.com")

	body, _ := json.Marshal(map[string]any{
		"filename": "pic.png", "content_type": "image/png", "size_bytes": 4,
	})
	req := httptest.NewRequest("POST", "/api/v1/files", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	file := created["file"].(map[string]any)
	fileID := file["id"].(string)
	upload := created["upload"].(map[string]any)
	putURL := upload["url"].(string)
	headers := upload["headers"].(map[string]any)

	putReq, err := http.NewRequest(http.MethodPut, putURL, strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		putReq.Header.Set(k, v.(string))
	}
	putRes, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	putRes.Body.Close()
	if putRes.StatusCode != 200 {
		t.Fatalf("put: %d", putRes.StatusCode)
	}

	req = httptest.NewRequest("POST", "/api/v1/files/"+fileID+"/complete", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("complete: %d %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/v1/files/"+fileID, nil)
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("DELETE", "/api/v1/files/"+fileID, nil)
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 204 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
}

func TestFilesQuotaAndType(t *testing.T) {
	h := setupStorage(t)
	app := h.createApp(t)
	conn := h.createR2Conn(t)
	h.bindR2(t, app.ID.String(), conn)
	access, _ := h.register(t, app, "q@ex.com")

	body, _ := json.Marshal(map[string]any{
		"filename": "x.bin", "content_type": "application/octet-stream", "size_bytes": 10,
	})
	req := httptest.NewRequest("POST", "/api/v1/files", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 422 {
		t.Fatalf("want content_type_not_allowed, got %d %s", rr.Code, rr.Body.String())
	}

	body, _ = json.Marshal(map[string]any{
		"filename": "big.png", "content_type": "image/png", "size_bytes": 2048,
	})
	req = httptest.NewRequest("POST", "/api/v1/files", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 413 {
		t.Fatalf("want file_too_large, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestAvatarUploadToR2(t *testing.T) {
	h := setupStorage(t)
	app := h.createApp(t)
	conn := h.createR2ConnWithPublic(t, "https://cdn.example.com")
	h.bindR2(t, app.ID.String(), conn)
	access, _ := h.register(t, app, "avatar@ex.com")

	body, _ := json.Marshal(map[string]any{
		"filename": "me.png", "content_type": "image/png", "size_bytes": 4,
	})
	req := httptest.NewRequest("POST", "/api/v1/user/me/avatar", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create avatar: %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	file := created["file"].(map[string]any)
	fileID := file["id"].(string)
	upload := created["upload"].(map[string]any)
	putURL := upload["url"].(string)
	headers := upload["headers"].(map[string]any)

	putReq, err := http.NewRequest(http.MethodPut, putURL, strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		putReq.Header.Set(k, v.(string))
	}
	putRes, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	putRes.Body.Close()
	if putRes.StatusCode != 200 {
		t.Fatalf("put: %d", putRes.StatusCode)
	}

	body, _ = json.Marshal(map[string]any{"file_id": fileID})
	req = httptest.NewRequest("POST", "/api/v1/user/me/avatar/complete", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("complete avatar: %d %s", rr.Code, rr.Body.String())
	}
	var me map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &me)
	url, _ := me["avatar_url"].(string)
	if !strings.HasPrefix(url, "https://cdn.example.com/acme/u/") || !strings.Contains(url, "/avatar/") {
		t.Fatalf("avatar_url=%q", url)
	}
}

func TestCompleteAvatarRejectsNonAvatarFile(t *testing.T) {
	h := setupStorage(t)
	app := h.createApp(t)
	conn := h.createR2ConnWithPublic(t, "https://cdn.example.com")
	h.bindR2(t, app.ID.String(), conn)
	access, userID := h.register(t, app, "noavatar@ex.com")

	body, _ := json.Marshal(map[string]any{
		"filename": "pic.png", "content_type": "image/png", "size_bytes": 4,
	})
	req := httptest.NewRequest("POST", "/api/v1/files", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create file: %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	fileID := created["file"].(map[string]any)["id"].(string)

	body, _ = json.Marshal(map[string]any{"file_id": fileID})
	req = httptest.NewRequest("POST", "/api/v1/user/me/avatar/complete", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("want 400 for non-avatar file, got %d %s", rr.Code, rr.Body.String())
	}

	fid, err := uuid.Parse(fileID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := h.storage.Store().GetByID(context.Background(), nil, app.ID, userID, fid)
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != storage.StatusPending {
		t.Fatalf("file status should stay pending, got %q", f.Status)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
