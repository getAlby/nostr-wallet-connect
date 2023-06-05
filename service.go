package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"math"

	"github.com/fiatjaf/go-lnurl"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
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
	Logger      *logrus.Logger
}

func (svc *Service) GetUser(c echo.Context) (user *User, err error) {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if svc.cfg.LNBackendType == LNDBackendType {
		//if we self-host, there is always only one user
		userID = 1
	}
	if userID == nil {
		return nil, nil
	}
	user = &User{}
	err = svc.db.Preload("Apps").First(&user, userID).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return
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
			go func() {
				resp, err := svc.HandleEvent(ctx, event)
				if err != nil {
					svc.Logger.Error(err)
				}
				if resp != nil {
					status := sub.Relay.Publish(ctx, *resp)
					nostrEvent := NostrEvent{}
					result := svc.db.Where("nostr_id = ?", event.ID).First(&nostrEvent)
					if result.Error != nil {
						svc.Logger.Error(result.Error)
						return
					}
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
			}()
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
		ss, err := nip04.ComputeSharedSecret(event.PubKey, svc.cfg.NostrSecretKey)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.createResponse(event, Nip47Response{
			Error: &Nip47Error{
				Code:    NIP_47_ERROR_UNAUTHORIZED,
				Message: "The public key does not have a wallet connected.",
			},
		}, ss)
		return resp, err
	}

	svc.Logger.WithFields(logrus.Fields{
		"eventId":   event.ID,
		"eventKind": event.Kind,
		"appId":     app.ID,
	}).Info("App found for nostr event")

	//to be extra safe, decrypt using the key found from the app
	ss, err := nip04.ComputeSharedSecret(app.NostrPubkey, svc.cfg.NostrSecretKey)
	if err != nil {
		return nil, err
	}
	payload, err := nip04.Decrypt(event.Content, ss)
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
		}).Errorf("Failed to save nostr event: %v", insertNostrEventResult.Error)
		return nil, insertNostrEventResult.Error
	}

	var bolt11 string
	if strings.HasPrefix(payload, "ln") {
		//legacy
		bolt11 = payload
	} else {
		payParams := &Nip47PayParams{}
		nip47Request := &Nip47Request{
			Params: payParams,
		}
		err = json.Unmarshal([]byte(payload), nip47Request)
		if err != nil {
			return nil, err
		}
		if nip47Request.Method != NIP_47_PAY_INVOICE_METHOD {
			return svc.createResponse(event, Nip47Response{Error: &Nip47Error{
				Code:    NIP_47_ERROR_NOT_IMPLEMENTED,
				Message: fmt.Sprintf("Unknown method: %s", nip47Request.Method),
			}}, ss)
		}
		bolt11 = payParams.Invoice
	}
	paymentRequest, err := decodepay.Decodepay(bolt11)
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"eventId":   event.ID,
			"eventKind": event.Kind,
			"appId":     app.ID,
			"bolt11":    bolt11,
		}).Errorf("Failed to decode bolt11 invoice: %v", err)
		//todo: create & send response
		return nil, err
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
		}).Infof("Failed to send payment: %v", err)
		nostrEvent.State = "error"
		svc.db.Save(&nostrEvent)
		return svc.createResponse(event, Nip47Response{
			Error: &Nip47Error{
				Code:    NIP_47_ERROR_INTERNAL,
				Message: fmt.Sprintf("Something went wrong while paying invoice: %s", err.Error()),
			},
		}, ss)
	}
	payment.Preimage = preimage
	nostrEvent.State = "executed"
	svc.db.Save(&nostrEvent)
	svc.db.Save(&payment)
	
	if (app.TipNWC) {
		// TODO: this should just be done once, not for every payment
		_, params, err := lnurl.HandleLNURL(svc.cfg.TipLightningAddress)
		
		if err != nil {
			svc.Logger.Error("Failed to request LNURL")
		} else if (params.LNURLKind() == "lnurl-pay") {
			payParams := params.(lnurl.LNURLPayParams)

			paymentSats := math.Ceil(float64(paymentRequest.MSatoshi) / 1000)
			tipSats := math.Ceil(paymentSats / 100)
			
			payValues, err := payParams.Call(int64(tipSats * 1000), "1% Tip", nil)
			if err != nil {
				svc.Logger.Error("Failed to request tip invoice")
			} else {
				preimage, err := svc.lnClient.SendPaymentSync(ctx, event.PubKey, payValues.PR)
				if err != nil {
					svc.Logger.Error("Failed to send tip", err)
				} else {
					svc.Logger.Info("Sent tip: ", preimage)
				}
			}
		} else {
			svc.Logger.Error("Unexpected LNURL kind")
		}
	}


	return svc.createResponse(event, Nip47Response{
		ResultType: NIP_47_PAY_INVOICE_METHOD,
		Result: Nip47PayResponse{
			Preimage: preimage,
		},
	}, ss)
}

func (svc *Service) createResponse(initialEvent *nostr.Event, content interface{}, ss []byte) (result *nostr.Event, err error) {
	payloadBytes, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	msg, err := nip04.Encrypt(string(payloadBytes), ss)
	if err != nil {
		return nil, err
	}
	resp := &nostr.Event{
		PubKey:    svc.cfg.IdentityPubkey,
		CreatedAt: time.Now(),
		Kind:      NIP_47_RESPONSE_KIND,
		Tags:      nostr.Tags{[]string{"p", initialEvent.PubKey}, []string{"e", initialEvent.ID}},
		Content:   msg,
	}
	err = resp.Sign(svc.cfg.NostrSecretKey)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (svc *Service) PublishNip47Info(ctx context.Context, relay *nostr.Relay) error {
	ev := &nostr.Event{}
	ev.Kind = NIP_47_INFO_EVENT_KIND
	ev.Content = NIP_47_CAPABILITIES
	ev.CreatedAt = time.Now()
	ev.PubKey = svc.cfg.IdentityPubkey
	err := ev.Sign(svc.cfg.NostrSecretKey)
	if err != nil {
		return err
	}
	status := relay.Publish(ctx, *ev)
	if status != nostr.PublishStatusSucceeded {
		return fmt.Errorf("Nostr publish not succesful: %s", status)
	}
	return nil
}
