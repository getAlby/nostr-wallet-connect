package main

import (
	"context"
	"os"
	"os/signal"
	"sync"

	echologrus "github.com/davrux/echo-logrus/v4"
	"github.com/getAlby/lndhub.go/lnd"
	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	log "github.com/sirupsen/logrus"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {

	// Load config from environment variables / .env file
	godotenv.Load(".env")
	cfg := &Config{}
	err := envconfig.Process("", cfg)
	if err != nil {
		log.Fatalf("Error loading environment variables: %v", err)
	}
	relay, err := nostr.RelayConnect(context.Background(), cfg.Relay)
	if err != nil {
		log.Fatal(err)
	}
	identityPubkey, err := nostr.GetPublicKey(cfg.NostrSecretKey)
	if err != nil {
		log.Fatalf("Error converting nostr privkey to pubkey: %v", err)
	}
	cfg.IdentityPubkey = identityPubkey
	npub, err := nip19.EncodePublicKey(identityPubkey)
	if err != nil {
		log.Fatalf("Error converting nostr privkey to pubkey: %v", err)
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseUri), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to open DB %v", err)
	}
	// Migrate the schema
	err = db.AutoMigrate(&User{}, &App{}, &NostrEvent{}, &Payment{})
	if err != nil {
		log.Fatalf("Failed migrate DB %v", err)
	}

	log.Infof("Starting nostr-wallet-connect. My npub is %s", npub)
	svc := Service{
		cfg: cfg,
		db:  db,
	}
	echologrus.Logger = log.New()
	echologrus.Logger.SetFormatter(&log.JSONFormatter{})
	echologrus.Logger.SetOutput(os.Stdout)
	echologrus.Logger.SetLevel(log.InfoLevel)
	svc.Logger = *echologrus.Logger

	ctx := context.Background()
	ctx, _ = signal.NotifyContext(ctx, os.Interrupt)
	var wg sync.WaitGroup
	switch cfg.LNBackendType {
	case LNDBackendType:
		lndClient, err := lnd.NewLNDclient(lnd.LNDoptions{
			Address:      cfg.LNDAddress,
			CertFile:     cfg.LNDCertFile,
			MacaroonFile: cfg.LNDMacaroonFile,
		})
		if err != nil {
			log.Fatal(err)
		}
		info, err := lndClient.GetInfo(ctx, &lnrpc.GetInfoRequest{})
		if err != nil {
			log.Fatal(err)
		}
		log.Infof("Connected to LND - alias %s", info.Alias)
		svc.lnClient = &LNDWrapper{lndClient}
	case AlbyBackendType:
		oauthService, err := NewAlbyOauthService(&svc)
		if err != nil {
			log.Fatal(err)
		}
		wg.Add(1)
		go func() {
			oauthService.Start(ctx)
			log.Info("OAuth server exited")
			wg.Done()
		}()
		svc.lnClient = oauthService
	}
	var filters nostr.Filters
	filter := nostr.Filter{
		Tags:  nostr.TagMap{"p": []string{svc.cfg.IdentityPubkey}},
		Kinds: []int{NIP_47_REQUEST_KIND},
	}
	if svc.cfg.ClientPubkey != "" {
		filter.Authors = []string{svc.cfg.ClientPubkey}
	}
	filters = []nostr.Filter{filter}
	log.Info(filters)

	sub := relay.Subscribe(ctx, filters)
	log.Info("listening to events")
	wg.Add(1)
	go func() {
		svc.StartSubscription(ctx, sub)
		wg.Done()
	}()
	wg.Wait()
	log.Info("Graceful shutdown completed. Goodbye.")
}
