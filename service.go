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
	Logger      logrus.Logger
}

func (svc *Service) StartSubscription(ctx context.Context, sub *nostr.Subscription) error {
	for {
		select {
		case notice := <-sub.Relay.Notices:
			svc.Logger.Infof("Received a notice %s", notice)
		case conErr := <-sub.Relay.ConnectionError:
			return conErr
		case <-ctx.Done():
			svc.Logger.Info("Exiting subscription.")
			return nil
		case <-sub.EndOfStoredEvents:
			svc.Logger.Info("Received EOS")
			svc.ReceivedEOS = true
		case event := <-sub.Events:
			resp, err := svc.HandleEvent(ctx, event)
			if err != nil {
				svc.Logger.Error(err)
				continue
			}
			if resp != nil {
				nostrEvent := NostrEvent{}
				result := svc.db.Where("nostr_id = ?", event.ID).First(&nostrEvent)
				if result.Error != nil {
					svc.Logger.Error(result.Error)
					continue
				}
				status := sub.Relay.Publish(ctx, *resp)
				nostrEvent.State = "replied" // TODO: check if publish was successful
				nostrEvent.ReplyId = resp.ID
				svc.db.Save(&nostrEvent)
				svc.Logger.WithFields(logrus.Fields{
					"nostrEventId": nostrEvent.ID,
					"eventId":      event.ID,
					"status":       status,
					"replyEventId": resp.ID,
					"appId":        nostrEvent.AppId,
				}).Info("Published reply")
			}
		}
	}
}

func (svc *Service) HandleEvent(ctx context.Context, event *nostr.Event) (result *nostr.Event, err error) {
	//don't process historical events
	if !svc.ReceivedEOS {
		return nil, nil
	}
	svc.Logger.WithFields(logrus.Fields{
		"eventId":   event.ID,
		"eventKind": event.Kind,
	}).Info("Processing Event")

	// make sure we don't know the event, yet
	nostrEvent := NostrEvent{}
	findEventResult := svc.db.Where("nostr_id = ?", event.ID).Find(&nostrEvent)
	if findEventResult.RowsAffected != 0 {
		svc.Logger.WithFields(logrus.Fields{
			"eventId": event.ID,
		}).Warn("Event already processed")
		return nil, nil
	}
	app := App{}
	err = svc.db.Preload("User").First(&app, &App{
		NostrPubkey: event.PubKey,
	}).Error
	if err != nil {
		return nil, err
	}

	svc.Logger.WithFields(logrus.Fields{
		"eventId":   event.ID,
		"eventKind": event.Kind,
		"appId":     app.ID,
	}).Info("App found for nostr event")

	ss, err := nip04.ComputeSharedSecret(event.PubKey, svc.cfg.NostrSecretKey)
	if err != nil {
		return nil, err
	}
	//todo: define and handle connect requests
	bolt11, err := nip04.Decrypt(event.Content, ss)
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"eventId":   event.ID,
			"eventKind": event.Kind,
			"appId":     app.ID,
		}).Errorf("Failed to decrypt content: %v", err)
		return nil, err
	}

	nostrEvent = NostrEvent{App: app, NostrId: event.ID, Content: event.Content, State: "received"}
	insertNostrEventResult := svc.db.Create(&nostrEvent)
	if insertNostrEventResult.Error != nil {
		svc.Logger.WithFields(logrus.Fields{
			"eventId":   event.ID,
			"eventKind": event.Kind,
			"appId":     app.ID,
		}).Errorf("Failed to save nostr event: %v", err)
		return nil, insertNostrEventResult.Error
	}

	paymentRequest, err := decodepay.Decodepay(bolt11)
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"eventId":   event.ID,
			"eventKind": event.Kind,
			"appId":     app.ID,
			"bolt11":    bolt11,
		}).Errorf("Failed to decode bolt11 invoice: %v", err)
		return nil, err
	}

	if !svc.hasPermission(&app, event, &paymentRequest) {
		return svc.createResponse(NIP_47_ERROR_RESPONSE_KIND, event, "no permission", ss)
	}

	payment := Payment{App: app, NostrEvent: nostrEvent, PaymentRequest: bolt11, Amount: uint(paymentRequest.MSatoshi / 1000)}
	insertPaymentResult := svc.db.Create(&payment)
	if insertPaymentResult.Error != nil {
		return nil, insertPaymentResult.Error
	}

	svc.Logger.WithFields(logrus.Fields{
		"eventId":   event.ID,
		"eventKind": event.Kind,
		"appId":     app.ID,
		"bolt11":    bolt11,
	}).Info("Sending payment")

	preimage, err := svc.lnClient.SendPaymentSync(ctx, event.PubKey, bolt11)
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"eventId":   event.ID,
			"eventKind": event.Kind,
			"appId":     app.ID,
			"bolt11":    bolt11,
		}).Info("Failed to send payment: %v", err)
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

func (svc *Service) hasPermission(app *App, event *nostr.Event, paymentRequest *decodepay.Bolt11) bool {
	appPermission := AppPermission{}
	findPermissionResult := svc.db.Find(&appPermission, &AppPermission{
		AppId:     app.ID,
		NostrKind: event.Kind,
	})
	if findPermissionResult.RowsAffected == 0 {
		return false
	}
	maxAmount := appPermission.MaxAmount
	if maxAmount != 0 {
		var result SumResult
		svc.db.Table("payments").Select("SUM(amount) as sum").Where("app_id = ? AND preimage IS NOT NULL AND created_at > ?", app.ID, time.Now().AddDate(0, -1, 0)).Scan(&result)
		if int64(result.Sum)+paymentRequest.MSatoshi/1000 > int64(maxAmount) {
			return false
		}
	}
	maxAmoutPerTransaction := appPermission.MaxAmoutPerTransaction
	if maxAmoutPerTransaction != 0 {
		if paymentRequest.MSatoshi/1000 > int64(maxAmoutPerTransaction) {
			return false
		}
	}
	return true
}
