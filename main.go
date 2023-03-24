package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"

	"github.com/getAlby/lndhub.go/lnd"
	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/sirupsen/logrus"
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

	logrus.Infof("Starting nostr-wallet-connect. My npub is %s", npub)
	svc := Service{
		cfg: cfg,
		db:  db,
	}
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
			logrus.Fatal(err)
		}
		info, err := lndClient.GetInfo(ctx, &lnrpc.GetInfoRequest{})
		if err != nil {
			logrus.Fatal(err)
		}
		logrus.Infof("Connected to LND - alias %s", info.Alias)
		svc.lnClient = &LNDWrapper{lndClient}
	case AlbyBackendType:
		oauthService, err := NewAlbyOauthService(&svc)
		if err != nil {
			logrus.Fatal(err)
		}
		wg.Add(1)
		go func() {
			oauthService.Start(ctx)
			logrus.Info("OAuth server exited")
			wg.Done()
		}()
		svc.lnClient = oauthService
	}

	//Start infinite loop which will be only broken by canceling ctx (SIGINT)
	//TODO: we can start this loop for multiple relays
	for {
		logrus.Info("Connecting to the relay")
		relay, err := nostr.RelayConnect(ctx, cfg.Relay)
		if err != nil {
			logrus.Fatal(err)
		}
		logrus.Info("Subscribing to events")
		sub := relay.Subscribe(ctx, svc.createFilters())
		err = svc.StartSubscription(ctx, sub)
		if err != nil {
			//err being non-nil means that we have an error on the websocket error channel. In this case we just try to reconnect.
			logrus.WithError(err).Error("Got an error from the relay. Reconnecting...")
			continue
		}
		//err being nil means that the context was canceled and we should exit the program.
		break
	}
	logrus.Info("Graceful shutdown completed. Goodbye.")
}

func (svc *Service) createFilters() nostr.Filters {
	filter := nostr.Filter{
		Tags:  nostr.TagMap{"p": []string{svc.cfg.IdentityPubkey}},
		Kinds: []int{NIP_47_REQUEST_KIND},
	}
	if svc.cfg.ClientPubkey != "" {
		filter.Authors = []string{svc.cfg.ClientPubkey}
	}
	return []nostr.Filter{filter}
}
