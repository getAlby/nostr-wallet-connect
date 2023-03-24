package main

import (
	"context"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Service struct {
	cfg         *Config
	db          *gorm.DB
	lnClient    LNClient
	ReceivedEOS bool
}

func (svc *Service) StartSubscription(ctx context.Context, sub *nostr.Subscription) error {
	for {
		select {
		case notice := <-sub.Relay.Notices:
			logrus.Infof("Received a notice %s", notice)
		case conErr := <-sub.Relay.ConnectionError:
			return conErr
		case <-ctx.Done():
			logrus.Info("Exiting subscription.")
			return nil
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
				nostrEvent := NostrEvent{}
				result := svc.db.Where("nostr_id = ?", event.ID).First(&nostrEvent)
				if result.Error != nil {
					logrus.Error(result.Error)
					continue
				}
				status := sub.Relay.Publish(ctx, *resp)
				nostrEvent.State = "replied" // TODO: check if publish was successful
				nostrEvent.ReplyId = resp.ID
				svc.db.Save(&nostrEvent)
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
	// make sure we don't know the event, yet
	nostrEvent := NostrEvent{}
	findEventResult := svc.db.Where("nostr_id = ?", event.ID).Find(&nostrEvent)
	if findEventResult.RowsAffected != 0 {
		logrus.Warnf("Event %s already processed", event.ID)
		return nil, nil
	}
	app := App{}
	err = svc.db.Preload("User").Find(&app, &App{
		NostrPubkey: event.PubKey,
	}).Error
	if err != nil {
		return nil, err
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

	nostrEvent = NostrEvent{App: app, NostrId: event.ID, Content: event.Content, State: "received"}
	insertNostrEventResult := svc.db.Create(&nostrEvent)
	if insertNostrEventResult.Error != nil {
		return nil, insertNostrEventResult.Error
	}

	paymentRequest, err := decodepay.Decodepay(bolt11)
	if err != nil {
		return nil, err
	}
	payment := Payment{App: app, NostrEvent: nostrEvent, PaymentRequest: bolt11, Amount: uint(paymentRequest.MSatoshi / 1000)}
	insertPaymentResult := svc.db.Create(&payment)
	if insertPaymentResult.Error != nil {
		return nil, insertPaymentResult.Error
	}

	preimage, err := svc.lnClient.SendPaymentSync(ctx, event.PubKey, bolt11)
	if err != nil {
		nostrEvent.State = "error"
		svc.db.Save(&nostrEvent)
		return svc.createResponse(NIP_47_ERROR_RESPONSE_KIND, event, err.Error(), ss)
	}
	payment.Preimage = preimage
	nostrEvent.State = "executed"
	svc.db.Save(&nostrEvent)
	svc.db.Save(&payment)
	return svc.createResponse(NIP_47_SUCCESS_RESPONSE_KIND, event, preimage, ss)
}

func (svc *Service) createResponse(kind int, initialEvent *nostr.Event, content string, ss []byte) (result *nostr.Event, err error) {
	msg, err := nip04.Encrypt(content, ss)
	if err != nil {
		return nil, err
	}
	resp := &nostr.Event{
		PubKey:    svc.cfg.IdentityPubkey,
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
