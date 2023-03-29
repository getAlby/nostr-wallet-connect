package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/getAlby/lndhub.go/lnd"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/tidwall/sjson"
)

type LNClient interface {
	SendPaymentSync(ctx context.Context, senderPubkey, payReq string) (preimage string, err error)
}

// wrap it again :sweat_smile:
// todo: drop dependency on lndhub package
type LNDWrapper struct {
	client *lnd.LNDWrapper
}

type LNBitsWrapper struct {
	AdminKey string
	Host     string
	client   *LNClient
}

func (lnd *LNDWrapper) SendPaymentSync(ctx context.Context, senderPubkey, payReq string) (preimage string, err error) {
	resp, err := lnd.client.SendPaymentSync(ctx, &lnrpc.SendRequest{PaymentRequest: payReq})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(resp.PaymentPreimage), nil

}

func (lnbits *LNBitsWrapper) SendPaymentSync(ctx context.Context, senderPubkey, payReq string) (preimage string, err error) {

	httpclient := &http.Client{
		Timeout: 10 * time.Second,
	}
	body, _ := sjson.Set("{}", "out", true)
	body, _ = sjson.Set(body, "bolt11", payReq)

	req, err := http.NewRequest("POST",
		lnbits.Host+"/api/v1/payments",
		bytes.NewBufferString(body),
	)
	if err != nil {
		return "", err
	}

	req.Header.Set("X-Api-Key", lnbits.AdminKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpclient.Do(req)
	if err != nil {
		return
	}

	if resp.StatusCode >= 300 {
		body, _ := ioutil.ReadAll(resp.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return "", fmt.Errorf("call to lnbits failed (%d): %s", resp.StatusCode, text)
	}

	defer resp.Body.Close()
	responseData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Print(err.Error())
		return
	}
	var jsonMap map[string]interface{}
	err = json.Unmarshal([]byte(string(responseData)), &jsonMap)
	if err != nil {
		fmt.Print(err.Error())
		return
	}

	fmt.Print("PAYMENT_HASH" + jsonMap["payment_hash"].(string))
	return jsonMap["payment_hash"].(string), nil

}
