package main

import (
	"context"
	"encoding/hex"

	"github.com/getAlby/lndhub.go/lnd"
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
