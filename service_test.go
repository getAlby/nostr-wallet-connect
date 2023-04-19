package main

import (
	"context"
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/nbd-wtf/go-nostr"
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
	//test lnbc.. payload
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
	ln = &MockLn{}
	return &Service{
		cfg:         &Config{},
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
