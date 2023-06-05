package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

type AlbyOAuthService struct {
	cfg       *Config
	oauthConf *oauth2.Config
	db        *gorm.DB
	Logger    *logrus.Logger
}

func NewAlbyOauthService(svc *Service, e *echo.Echo) (result *AlbyOAuthService, err error) {
	conf := &oauth2.Config{
		ClientID:     svc.cfg.AlbyClientId,
		ClientSecret: svc.cfg.AlbyClientSecret,
		//Todo: do we really need all these permissions?
		Scopes: []string{"account:read", "payments:send", "invoices:read", "transactions:read", "invoices:create"},
		Endpoint: oauth2.Endpoint{
			TokenURL:  svc.cfg.OAuthTokenUrl,
			AuthURL:   svc.cfg.OAuthAuthUrl,
			AuthStyle: 2, // use HTTP Basic Authorization https://pkg.go.dev/golang.org/x/oauth2#AuthStyle
		},
		RedirectURL: svc.cfg.OAuthRedirectUrl,
	}

	albySvc := &AlbyOAuthService{
		cfg:       svc.cfg,
		oauthConf: conf,
		db:        svc.db,
		Logger:    svc.Logger,
	}

	e.GET("/alby/auth", albySvc.AuthHandler)
	e.GET("/alby/callback", albySvc.CallbackHandler)
	e.POST("/alby/admin/balance_event", svc.BalanceHandler)

	return albySvc, err
}

func (svc *AlbyOAuthService) SendPaymentSync(ctx context.Context, senderPubkey, payReq string) (preimage string, err error) {
	app := App{}
	err = svc.db.Preload("User").First(&app, &App{
		NostrPubkey: senderPubkey,
	}).Error
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"senderPubkey": senderPubkey,
			"bolt11":       payReq,
		}).Errorf("App not found: %v", err)
		return "", err
	}
	svc.Logger.WithFields(logrus.Fields{
		"senderPubkey": senderPubkey,
		"bolt11":       payReq,
		"appId":        app.ID,
		"userId":       app.User.ID,
	}).Info("Processing payment request")
	user := app.User
	tok, err := svc.oauthConf.TokenSource(ctx, &oauth2.Token{
		AccessToken:  user.AccessToken,
		RefreshToken: user.RefreshToken,
		Expiry:       user.Expiry,
	}).Token()
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"senderPubkey": senderPubkey,
			"bolt11":       payReq,
			"appId":        app.ID,
			"userId":       app.User.ID,
		}).Errorf("Token error: %v", err)
		return "", err
	}
	// we always update the user's token for future use
	// the oauth library handles the token refreshing
	user.AccessToken = tok.AccessToken
	user.RefreshToken = tok.RefreshToken
	user.Expiry = tok.Expiry // TODO; probably needs some calculation
	err = svc.db.Save(&user).Error
	if err != nil {
		svc.Logger.WithError(err).Error("Error saving user")
		return "", err
	}
	client := svc.oauthConf.Client(ctx, tok)

	body := bytes.NewBuffer([]byte{})
	payload := &PayRequest{
		Invoice: payReq,
	}
	err = json.NewEncoder(body).Encode(payload)

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/payments/bolt11", svc.cfg.AlbyAPIURL), body)
	if err != nil {
		svc.Logger.WithError(err).Error("Error creating request /payments/bolt11")
		return "", err
	}

	req.Header.Set("User-Agent", "NWC")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"senderPubkey": senderPubkey,
			"bolt11":       payReq,
			"appId":        app.ID,
			"userId":       app.User.ID,
		}).Errorf("Failed to pay invoice: %v", err)
		return "", err
	}

	if resp.StatusCode < 300 {
		responsePayload := &PayResponse{}
		err = json.NewDecoder(resp.Body).Decode(responsePayload)
		if err != nil {
			return "", err
		}
		svc.Logger.WithFields(logrus.Fields{
			"senderPubkey": senderPubkey,
			"bolt11":       payReq,
			"appId":        app.ID,
			"userId":       app.User.ID,
		}).Info("Payment successful")
		return responsePayload.Preimage, nil
	} else {
		errorPayload := &ErrorResponse{}
		err = json.NewDecoder(resp.Body).Decode(errorPayload)
		svc.Logger.WithFields(logrus.Fields{
			"senderPubkey":  senderPubkey,
			"bolt11":        payReq,
			"appId":         app.ID,
			"userId":        app.User.ID,
			"APIHttpStatus": resp.StatusCode,
		}).Errorf("Payment failed %s", string(errorPayload.Message))
		return "", errors.New(errorPayload.Message)
	}
}

func (svc *AlbyOAuthService) AuthHandler(c echo.Context) error {
	// clear current session
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	sess.Values["user_id"] = ""
	delete(sess.Values, "user_id")
	sess.Options = &sessions.Options{
		MaxAge: -1,
	}
	sess.Save(c.Request(), c.Response())

	url := svc.oauthConf.AuthCodeURL("")
	return c.Redirect(302, url)
}

func (svc *AlbyOAuthService) CallbackHandler(c echo.Context) error {
	code := c.QueryParam("code")
	tok, err := svc.oauthConf.Exchange(c.Request().Context(), code)
	if err != nil {
		svc.Logger.WithError(err).Error("Failed to exchange token")
		return err
	}
	client := svc.oauthConf.Client(c.Request().Context(), tok)

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/user/me", svc.cfg.AlbyAPIURL), nil)
	if err != nil {
		svc.Logger.WithError(err).Error("Error creating request /me")
		return err
	}

	req.Header.Set("User-Agent", "NWC")

	res, err := client.Do(req)
	if err != nil {
		svc.Logger.WithError(err).Error("Failed to fetch /me")
		return err
	}
	me := AlbyMe{}
	err = json.NewDecoder(res.Body).Decode(&me)
	if err != nil {
		svc.Logger.WithError(err).Error("Failed to decode API response")
		return err
	}

	user := User{}
	svc.db.FirstOrInit(&user, User{AlbyIdentifier: me.Identifier})
	user.AccessToken = tok.AccessToken
	user.RefreshToken = tok.RefreshToken
	user.Expiry = tok.Expiry // TODO; probably needs some calculation
	user.Email = me.Email
	user.LightningAddress = me.LightningAddress
	svc.db.Save(&user)

	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	sess.Values["user_id"] = user.ID
	sess.Save(c.Request(), c.Response())
	return c.Redirect(302, "/")
}
