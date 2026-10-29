package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/affiliate"
	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/analytics"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/auth/provider"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/billing/appstore"
	"github.com/builtly/builtly/internal/billing/playstore"
	"github.com/builtly/builtly/internal/billing/stripeapi"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/jwks"
	"github.com/builtly/builtly/internal/platform/logx"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/referral"
	"github.com/builtly/builtly/internal/server"
	"github.com/builtly/builtly/internal/setup"
	"github.com/builtly/builtly/internal/storage"
	"github.com/builtly/builtly/internal/tenant"
	"github.com/builtly/builtly/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "migrate":
		os.Exit(runMigrate(os.Args[2:]))
	case "secrets":
		fmt.Fprintln(os.Stderr, "secrets: not implemented yet (M2+)")
		os.Exit(1)
	case "credits":
		os.Exit(runCredits(os.Args[2:]))
	case "version":
		fmt.Println(version.Version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `builtly %s

Usage:
  builtly serve
  builtly migrate up | status
  builtly secrets rotate
  builtly credits reconcile [--app <id>]
  builtly version
`, version.Version)
}

func load() (*config.Config, error) {
	return config.Load()
}

func openDB(cfg *config.Config) (*db.DB, error) {
	return db.Open(cfg.DBDriver, cfg.DBDSN, cfg.DBMaxOpenConns)
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	_ = fs.Parse(args)

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for {
		cfg, err := load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			return 1
		}
		log := logx.Setup(cfg.LogLevel, cfg.LogFormat)

		if !cfg.NeedsSetup {
			if probe, err := openDB(cfg); err != nil {
				log.Warn("database unreachable; entering setup", "err", err)
				cfg.MarkDBUnreachable(err)
			} else {
				_ = probe.Close()
			}
		}

		if cfg.NeedsSetup {
			done := make(chan struct{})
			var once sync.Once
			setupCtx, cancelSetup := context.WithCancel(runCtx)
			go func() {
				select {
				case <-done:
					cancelSetup()
				case <-setupCtx.Done():
				}
			}()
			err := setup.Run(setupCtx, cfg, log, func() {
				once.Do(func() { close(done) })
			})
			cancelSetup()
			if err != nil {
				log.Error("setup server stopped with error", "err", err)
				return 1
			}
			if runCtx.Err() != nil {
				return 0
			}
			log.Info("setup complete; starting application")
			continue
		}

		return runApp(runCtx, cfg, log)
	}
}

func runApp(runCtx context.Context, cfg *config.Config, log *slog.Logger) int {
	database, err := openDB(cfg)
	if err != nil {
		log.Error("db open failed", "err", err)
		return 1
	}
	defer database.Close()

	ctx := context.Background()
	if cfg.AutoMigrate {
		if err := database.MigrateUp(ctx); err != nil {
			log.Error("auto-migrate failed", "err", err)
			return 1
		}
	} else {
		pending, err := database.Pending(ctx)
		if err != nil {
			log.Error("migration check failed", "err", err)
			return 1
		}
		if len(pending) > 0 {
			log.Error("pending migrations; run builtly migrate up or set BUILTLY_AUTO_MIGRATE=true", "pending", pending)
			return 1
		}
	}

	box, err := secrets.NewBox(cfg.EncryptionKeys)
	if err != nil {
		log.Error("secrets box failed", "err", err)
		return 1
	}
	envDefaults := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	tMW := tenant.NewMiddleware(tSvc, cfg.CORSMaxAge)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, envDefaults)
	iSvc := integration.NewService(database, iStore, box, envDefaults, cfg.PublicURL, resolver)
	userStore := auth.NewStore(database)
	creditStore := credit.NewStore(database)
	creditSvc := credit.NewService(database, creditStore)
	tokens := auth.NewTokenService(cfg.JWTSigningKeys, cfg.JWTAccessTTL)
	authMW := auth.NewMiddleware(tokens, tSvc)
	aiHandler := ai.NewHandler(creditSvc, userStore)
	creditUser := credit.NewUserHandler(creditSvc)
	auditStore := admin.NewAuditStore(database)
	adminAuth := admin.NewAuth(cfg)
	outboxStore := outbox.NewStore(database)

	registry := outbox.NewRegistry()
	registry.DefaultSubscriptions()
	templates, err := email.LoadTemplates()
	if err != nil {
		log.Error("email templates failed", "err", err)
		return 1
	}
	mailSub := email.NewSubscriber(email.NewResendClient(nil), templates, resolver, tSvc)
	registry.SetHandler(outbox.SubEmail, mailSub.Handle)
	hc := &http.Client{Timeout: 10 * time.Second}
	jwksCache := jwks.New(hc)
	provReg := provider.NewRegistry(hc, jwksCache)
	registry.SetHandler(outbox.SubAppleRevoke, auth.NewAppleRevokeSubscriber(provReg, resolver, log).Handle)
	affReg := affiliate.NewRegistry(hc)
	registry.SetHandler(outbox.SubAnalytics, analytics.NewSubscriber(analytics.NewClient(hc), resolver).Handle)
	registry.SetHandler(outbox.SubAffiliate, affiliate.NewSubscriber(affReg, resolver, tSvc).Handle)
	publisher := outbox.NewPublisher(outboxStore, box, registry)
	dispatcher := outbox.NewDispatcher(outboxStore, box, registry, uuid.Must(uuid.NewV7()).String(), log)
	authSvc := auth.NewService(database, userStore, tokens, creditSvc, publisher, cfg.JWTRefreshTTL)
	authSvc.SetOAuth(auth.OAuthDeps{
		Providers: provReg, Resolver: resolver, Box: box,
		PublicURL: cfg.PublicURL, Tenants: tSvc,
	})
	authHandler := auth.NewHandler(authSvc, cfg.TrustedProxies)

	stripeClient := stripeapi.New(hc)
	catalogStore := catalog.NewStore(database)
	catalogSvc := catalog.NewService(catalogStore, resolver, stripeClient)
	billingStore := billing.NewStore(database)
	billingSvc := billing.NewService(
		database, billingStore, catalogSvc, creditSvc, publisher, resolver, iSvc, iStore, box, userStore, tSvc,
		stripeClient, cfg.Env == "development",
	)
	appleVerifier, err := appstore.NewVerifier()
	if err != nil {
		log.Error("apple jws verifier", "err", err)
		return 1
	}
	billingSvc.SetIAP(appstore.NewClient(hc, appleVerifier), playstore.New(hc), billing.NewJobStore(database), log)
	billingSvc.SetAffiliate(affReg)
	storageSvc := storage.NewService(database, storage.NewStore(database), resolver, storage.NewR2Client(hc))
	authSvc.SetFileMarker(storageSvc)
	authSvc.SetAvatarUploader(auth.StorageAvatars{Storage: storageSvc})
	registry.SetHandler(outbox.SubStoragePurge, storage.NewPurgeSubscriber(storageSvc, log).Handle)
	referralSvc := referral.NewService(
		database, referral.NewStore(database), creditSvc, publisher,
		auth.ReferralUsers{Store: userStore},
		billing.ReferralPurchases{Store: billingStore},
		tSvc,
	)
	authSvc.SetReferrals(referralSvc)
	registry.SetHandler(outbox.SubReferral, referral.NewSubscriber(referralSvc).Handle)
	adminHandler := admin.NewHandler(database, tSvc, iSvc, resolver, auditStore, tStore, iStore, tMW, userStore, creditSvc, billingStore, outboxStore, box, catalogSvc, stripeClient, referralSvc)

	srv := server.New(cfg, database, log, server.Deps{
		Tenant:           tSvc,
		TenantMW:         tMW,
		TenantStore:      tStore,
		Integration:      iSvc,
		IntegStore:       iStore,
		Resolver:         resolver,
		Secrets:          box,
		AdminAuth:        adminAuth,
		Admin:            adminHandler,
		AuthMW:           authMW,
		Credits:          creditSvc,
		CreditUser:       creditUser,
		AI:               aiHandler,
		OutboxDispatcher: dispatcher,
		Auth:             authHandler,
		Catalog:          catalog.NewHandler(catalogSvc),
		Billing:          billing.NewHandler(billingSvc),
		VoidedPoller:     billing.NewVoidedPoller(billingSvc),
		Storage:          storage.NewHandler(storageSvc),
		StorageCleaner:   storage.NewCleaner(storageSvc, log),
		Referral:         referral.NewHandler(referralSvc),
	})
	if err := srv.Run(runCtx); err != nil {
		log.Error("server stopped with error", "err", err)
		return 1
	}
	return 0
}

func runMigrate(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: builtly migrate up | status")
		return 2
	}
	cfg, err := load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	log := logx.Setup(cfg.LogLevel, cfg.LogFormat)
	database, err := openDB(cfg)
	if err != nil {
		log.Error("db open failed", "err", err)
		return 1
	}
	defer database.Close()

	ctx := context.Background()
	switch args[0] {
	case "up":
		if err := database.MigrateUp(ctx); err != nil {
			log.Error("migrate up failed", "err", err)
			return 1
		}
		log.Info("migrations applied")
		return 0
	case "status":
		applied, pending, err := database.Status(ctx)
		if err != nil {
			log.Error("migrate status failed", "err", err)
			return 1
		}
		fmt.Println("applied:")
		for _, v := range applied {
			fmt.Println(" ", v)
		}
		fmt.Println("pending:")
		for _, v := range pending {
			fmt.Println(" ", v)
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown migrate subcommand %q\n", args[0])
		return 2
	}
}

func runCredits(args []string) int {
	fs := flag.NewFlagSet("credits", flag.ExitOnError)
	var appFlag string
	fs.StringVar(&appFlag, "app", "", "limit reconcile to one app id")
	_ = fs.Parse(args)
	if fs.NArg() < 1 || fs.Arg(0) != "reconcile" {
		fmt.Fprintln(os.Stderr, "usage: builtly credits reconcile [--app <id>]")
		return 2
	}
	cfg, err := load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	log := logx.Setup(cfg.LogLevel, cfg.LogFormat)
	database, err := openDB(cfg)
	if err != nil {
		log.Error("db open failed", "err", err)
		return 1
	}
	defer database.Close()

	var appID *uuid.UUID
	if appFlag != "" {
		id, err := uuid.Parse(appFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --app: %v\n", err)
			return 2
		}
		appID = &id
	}
	svc := credit.NewService(database, credit.NewStore(database))
	mismatches, err := svc.Reconcile(context.Background(), appID)
	if err != nil {
		log.Error("reconcile failed", "err", err)
		return 1
	}
	if len(mismatches) == 0 {
		fmt.Println("ok: ledger invariants hold")
		return 0
	}
	for _, m := range mismatches {
		fmt.Printf("mismatch app=%s user=%s balance=%d sum=%d problem=%s\n",
			m.AppID, m.UserID, m.Balance, m.Sum, m.Problem)
	}
	return 1
}
