package main

import (
	"context"
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

const testDB = "test.db"

func TestHandleEvent(t *testing.T) {
	ctx := context.TODO()
	svc, _ := createTestService(t)
	//test not yet receivedEOS
	res, err := svc.HandleEvent(ctx, &nostr.Event{
		Kind: NIP_47_REQUEST_KIND,
	})
	assert.Nil(t, res)
	assert.Nil(t, err)
	//now signal that we are ready to receive events
	svc.ReceivedEOS = true

	senderPrivkey := nostr.GeneratePrivateKey()
	senderPubkey, err := nostr.GetPublicKey(senderPrivkey)
	assert.NoError(t, err)
	//test lnbc.. payload without having an app registered
	ss, err := nip04.ComputeSharedSecret(svc.cfg.IdentityPubkey, senderPrivkey)
	assert.NoError(t, err)
	payload, err := nip04.Encrypt("lnbc1234", ss)
	assert.NoError(t, err)
	res, err = svc.HandleEvent(ctx, &nostr.Event{
		Kind:    NIP_47_REQUEST_KIND,
		PubKey:  senderPubkey,
		Content: payload,
	})
	assert.Error(t, err)
	//TODO: implement & check response
	//test new payload
	//test malformed payload
	//test wrong method
	//test LN error
	//cleanup
	os.Remove(testDB)
}

func createTestService(t *testing.T) (svc *Service, ln *MockLn) {
	db, err := gorm.Open(sqlite.Open(testDB), &gorm.Config{})
	assert.NoError(t, err)
	err = db.AutoMigrate(&User{}, &App{}, &NostrEvent{}, &Payment{}, &Identity{})
	assert.NoError(t, err)
	ln = &MockLn{}
	sk := nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	assert.NoError(t, err)
	return &Service{
		cfg: &Config{
			NostrSecretKey: sk,
			IdentityPubkey: pk,
		},
		db:          db,
		lnClient:    ln,
		ReceivedEOS: false,
		Logger:      &logrus.Logger{},
	}, ln
}

type MockLn struct {
}

func (mln *MockLn) SendPaymentSync(ctx context.Context, senderPubkey string, payReq string) (preimage string, err error) {
	//todo more advanced behaviour
	return "123preimage", nil
}
