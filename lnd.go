package main

import (
	"context"
	"encoding/hex"

	"github.com/getAlby/lndhub.go/lnd"
	"github.com/labstack/echo/v4"
	"github.com/lightningnetwork/lnd/lnrpc"
)

type LNClient interface {
	SendPaymentSync(ctx context.Context, senderPubkey, payReq string) (preimage string, err error)
}

// wrap it again :sweat_smile:
// todo: drop dependency on lndhub package
type LNDWrapper struct {
	client *lnd.LNDWrapper
}

func (lnd *LNDWrapper) SendPaymentSync(ctx context.Context, senderPubkey, payReq string) (preimage string, err error) {
	resp, err := lnd.client.SendPaymentSync(ctx, &lnrpc.SendRequest{PaymentRequest: payReq})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(resp.PaymentPreimage), nil
}

func NewLNDService(ctx context.Context, svc *Service, e *echo.Echo) (result *LNDWrapper, err error) {
	lndClient, err := lnd.NewLNDclient(lnd.LNDoptions{
		Address:      svc.cfg.LNDAddress,
		CertFile:     svc.cfg.LNDCertFile,
		MacaroonFile: svc.cfg.LNDMacaroonFile,
	})
	if err != nil {
		return nil, err
	}
	info, err := lndClient.GetInfo(ctx, &lnrpc.GetInfoRequest{})
	if err != nil {
		return nil, err
	}
	//add default user to db
	user := &User{}
	err = svc.db.FirstOrInit(user, User{AlbyIdentifier: "lnd"}).Error
	if err != nil {
		return nil, err
	}
	err = svc.db.Save(user).Error
	if err != nil {
		return nil, err
	}

	svc.Logger.Infof("Connected to LND - alias %s", info.Alias)

	return &LNDWrapper{lndClient}, nil
}
