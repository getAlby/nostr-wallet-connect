package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type AlbyOAuthService struct {
	cfg       *Config
	oauthConf *oauth2.Config
	db        *gorm.DB
	e         *echo.Echo
}

func (svc *AlbyOAuthService) Start(ctx context.Context) (err error) {
	// Start server
	go func() {
		if err := svc.e.Start(fmt.Sprintf(":%v", svc.cfg.OAuthServerPort)); err != nil && err != http.ErrServerClosed {
			svc.e.Logger.Fatal("shutting down the server")
		}
	}()
	<-ctx.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return svc.e.Shutdown(ctx)
}

func NewAlbyOauthService(cfg *Config) (result *AlbyOAuthService, err error) {
	conf := &oauth2.Config{
		ClientID:     cfg.AlbyClientId,
		ClientSecret: cfg.AlbyClientSecret,
		//Todo: do we really need all these permissions?
		Scopes: []string{"account:read", "payments:send", "invoices:read", "transactions:read", "invoices:create"},
		Endpoint: oauth2.Endpoint{
			TokenURL: cfg.OAuthTokenUrl,
			AuthURL:  cfg.OAuthAuthUrl,
		},
		RedirectURL: cfg.OAuthRedirectUrl,
	}
	//todo: postgres db
	db, err := gorm.Open(postgres.Open(cfg.DatabaseUri), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	// Migrate the schema
	err = db.AutoMigrate(&User{})
	if err != nil {
		return nil, err
	}

	svc := &AlbyOAuthService{
		cfg:       cfg,
		oauthConf: conf,
		db:        db,
	}

	e := echo.New()
	e.GET("/alby/auth", svc.AuthHandler)
	e.GET("/alby/callback", svc.CallbackHandler)
	svc.e = e
	return svc, err
}

func (svc *AlbyOAuthService) SendPaymentSync(ctx context.Context, senderPubkey, payReq string) (preimage string, err error) {
	user := &User{}
	err = svc.db.Find(user, &User{
		NostrPubkey: senderPubkey,
	}).Error
	if err != nil {
		return "", err
	}
	client := svc.oauthConf.Client(ctx, &oauth2.Token{
		AccessToken:  user.AccessToken,
		RefreshToken: user.RefreshToken,
		Expiry:       user.Expiry,
	})
	body := bytes.NewBuffer([]byte{})
	payload := &PayRequest{
		Invoice: payReq,
	}
	err = json.NewEncoder(body).Encode(payload)
	resp, err := client.Post(fmt.Sprintf("%s/payments/bolt11", svc.cfg.AlbyAPIURL), "application/json", body)
	if err != nil {
		return "", err
	}
	//todo non-200 status code handling
	responsePayload := &PayResponse{}
	err = json.NewDecoder(resp.Body).Decode(responsePayload)
	if err != nil {
		return "", err
	}
	logrus.Infof("Sent payment with hash %s", responsePayload.PaymentHash)
	return responsePayload.Preimage, nil
}

func (svc *AlbyOAuthService) AuthHandler(c echo.Context) error {
	url := svc.oauthConf.AuthCodeURL("")
	return c.Redirect(http.StatusMovedPermanently, url)
}

func (svc *AlbyOAuthService) CallbackHandler(c echo.Context) error {
	code := c.QueryParam("code")
	tok, err := svc.oauthConf.Exchange(c.Request().Context(), code)
	if err != nil {
		svc.e.Logger.Error(err)
		return err
	}
	client := svc.oauthConf.Client(c.Request().Context(), tok)
	res, err := client.Get(fmt.Sprintf("%s/user/me", svc.cfg.AlbyAPIURL))
	if err != nil {
		svc.e.Logger.Error(err)
		return err
	}
	me := AlbyMe{}
	err = json.NewDecoder(res.Body).Decode(&me)
	if err != nil {
		svc.e.Logger.Error(err)
		return err
	}
	_, pubkey, err := nip19.Decode(me.NPub)
	if err != nil {
		svc.e.Logger.Error(err)
		return err
	}
	err = svc.db.Create(&User{
		NostrPubkey:  pubkey.(string),
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
	}).Error
	if err != nil {
		svc.e.Logger.Error(err)
		return err
	}
	return c.String(http.StatusOK, me.NPub)

}
