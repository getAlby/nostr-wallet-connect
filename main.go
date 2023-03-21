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
		logrus.Fatal(err)
	}
	identityPubkey, err := nostr.GetPublicKey(cfg.NostrSecretKey)
	if err != nil {
		log.Fatalf("Error converting nostr privkey to pubkey: %v", err)
	}
	npub, err := nip19.EncodePublicKey(identityPubkey)
	if err != nil {
		log.Fatalf("Error converting nostr privkey to pubkey: %v", err)
	}

	logrus.Infof("Starting nostr-wallet-connect. My npub is %s", npub)

	svc := Service{
		IdentityPubkey: identityPubkey,
		cfg:            cfg,
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
		oauthService, err := NewAlbyOauthService(cfg)
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
	var filters nostr.Filters
	filters = []nostr.Filter{{
		Tags:  nostr.TagMap{"p": []string{svc.IdentityPubkey}},
		Kinds: []int{NIP_47_REQUEST_KIND},
	}}
	logrus.Info(filters)
	if svc.cfg.ClientPubkey != "" {
		filters = append(filters, nostr.Filter{
			Authors: []string{svc.cfg.ClientPubkey},
		})
	}
	sub := relay.Subscribe(ctx, filters)
	logrus.Info("listening to events")
	wg.Add(1)
	go func() {
		svc.StartSubscription(ctx, sub)
		wg.Done()
	}()
	wg.Wait()
	logrus.Info("Graceful shutdown completed. Goodbye.")
}
