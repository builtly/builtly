package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/billing/playstore"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/secrets"
)

// VoidedPoller runs the Google voided-purchases poll.
type VoidedPoller struct {
	svc *Service
}

// NewVoidedPoller constructs a poller.
func NewVoidedPoller(svc *Service) *VoidedPoller { return &VoidedPoller{svc: svc} }

// Run loops until ctx is cancelled.
func (p *VoidedPoller) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	p.Tick(ctx) // run once at start
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Tick(ctx)
		}
	}
}

// Tick polls all google_play connections once.
func (p *VoidedPoller) Tick(ctx context.Context) {
	conns, err := p.svc.iStore.ListConnections(ctx, nil, integration.ProviderGooglePlay)
	if err != nil {
		p.svc.iap.log.Error("voided list connections", "err", err)
		return
	}
	// Also env default if present
	if c, _, ok := p.svc.integ.Env().Get(integration.ProviderGooglePlay); ok {
		conns = append(conns, c)
	}
	seen := map[string]bool{}
	for _, c := range conns {
		id := c.ID
		if id == "" {
			id = integration.DefaultConnectionID
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := p.pollConnection(ctx, id); err != nil {
			p.svc.iap.log.Error("voided poll failed", "connection_id", id, "err", err)
		}
	}
}

func (p *VoidedPoller) pollConnection(ctx context.Context, connectionID string) error {
	cfg, err := p.loadGoogle(ctx, connectionID)
	if err != nil {
		return err
	}
	appID, err := p.svc.resolver.AppForConnection(ctx, integration.ProviderGooglePlay, connectionID)
	if err != nil {
		// not bound — skip
		return nil
	}
	key := "google_voided:" + connectionID
	start := time.Now().UTC().Add(-30 * 24 * time.Hour).UnixMilli()
	if v, ok, err := p.svc.iap.jobs.Get(ctx, key); err == nil && ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			// 1h overlap
			start = n - 3600_000
		}
	}
	var maxVoided int64 = start
	page := ""
	for {
		items, next, err := p.svc.iap.google.ListVoidedPurchases(ctx, cfg, start, page)
		if err != nil {
			return err
		}
		for _, item := range items {
			vt, _ := strconv.ParseInt(item.VoidedTimeMillis, 10, 64)
			if vt > maxVoided {
				maxVoided = vt
			}
			ref := googleProviderRef(item.PurchaseToken)
			purchase, err := p.svc.store.GetPurchaseByProviderRef(ctx, nil, appID, ProviderGoogle, ref)
			if err != nil {
				continue
			}
			key := "refund:google:" + ref
			_ = p.svc.db.WithTx(ctx, func(tx *sqlx.Tx) error {
				cur, err := p.svc.store.GetPurchaseByID(ctx, tx, purchase.AppID, purchase.ID)
				if err != nil {
					return err
				}
				return p.svc.applyRefund(ctx, tx, cur, cur.Credits, key, "google_voided")
			})
		}
		if next == "" {
			break
		}
		page = next
	}
	return p.svc.iap.jobs.Set(ctx, key, strconv.FormatInt(maxVoided, 10))
}

func (p *VoidedPoller) loadGoogle(ctx context.Context, connectionID string) (playstore.Config, error) {
	var settings map[string]any
	var plain []byte
	if connectionID == integration.DefaultConnectionID {
		c, sec, ok := p.svc.integ.Env().Get(integration.ProviderGooglePlay)
		if !ok {
			return playstore.Config{}, integration.ErrNotConfigured
		}
		plain = sec
		_ = json.Unmarshal(c.Settings, &settings)
	} else {
		c, err := p.svc.iStore.GetConnection(ctx, nil, connectionID)
		if err != nil {
			return playstore.Config{}, err
		}
		plain, err = p.svc.box.Open(c.SecretsEnc, c.KeyVersion, secrets.ConnectionAAD(c.ID, c.Provider))
		if err != nil {
			return playstore.Config{}, err
		}
		_ = json.Unmarshal(c.Settings, &settings)
	}
	var sec integration.GooglePlaySecrets
	if err := json.Unmarshal(plain, &sec); err != nil {
		return playstore.Config{}, err
	}
	pkg, _ := settings["package_name"].(string)
	if pkg == "" {
		return playstore.Config{}, fmt.Errorf("missing package_name")
	}
	return playstore.Config{PackageName: pkg, ServiceAccountJSON: []byte(sec.ServiceAccountJSON)}, nil
}
