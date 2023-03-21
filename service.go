package main

import (
	"context"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/sirupsen/logrus"
)

type Service struct {
	cfg            *Config
	lnClient       LNClient
	IdentityPubkey string
	ReceivedEOS    bool
}

func (svc *Service) StartSubscription(ctx context.Context, sub *nostr.Subscription) {
	for {
		select {
		case <-ctx.Done():
			logrus.Info("Exiting subscription.")
			return
		case <-sub.EndOfStoredEvents:
			logrus.Info("Received EOS")
			svc.ReceivedEOS = true
		case event := <-sub.Events:
			resp, err := svc.HandleEvent(ctx, event)
			if err != nil {
				logrus.Error(err)
				continue
			}
			if resp != nil {
				status := sub.Relay.Publish(ctx, *resp)
				logrus.Infof("published event, status %v", status)
			}
		}
	}
}

func (svc *Service) HandleEvent(ctx context.Context, event *nostr.Event) (result *nostr.Event, err error) {
	//don't process historical events
	if !svc.ReceivedEOS {
		return nil, nil
	}
	ss, err := nip04.ComputeSharedSecret(event.PubKey, svc.cfg.NostrSecretKey)
	if err != nil {
		return nil, err
	}
	//todo: define and handle connect requests
	bolt11, err := nip04.Decrypt(event.Content, ss)
	if err != nil {
		return nil, err
	}
	preimage, err := svc.lnClient.SendPaymentSync(ctx, event.PubKey, bolt11)
	if err != nil {
		return svc.createResponse(NIP_47_ERROR_RESPONSE_KIND, event, err.Error(), ss)
	}
	return svc.createResponse(NIP_47_SUCCESS_RESPONSE_KIND, event, preimage, ss)
}

func (svc *Service) createResponse(kind int, initialEvent *nostr.Event, content string, ss []byte) (result *nostr.Event, err error) {
	msg, err := nip04.Encrypt(content, ss)
	if err != nil {
		return nil, err
	}
	resp := &nostr.Event{
		PubKey:    svc.IdentityPubkey,
		CreatedAt: time.Now(),
		Kind:      kind,
		Tags:      nostr.Tags{[]string{"p", initialEvent.PubKey}, []string{"e", initialEvent.ID}},
		Content:   msg,
	}
	err = resp.Sign(svc.cfg.NostrSecretKey)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
