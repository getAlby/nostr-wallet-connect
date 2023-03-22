package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"html/template"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type TemplateRegistry struct {
	templates map[string]*template.Template
}

// Implement e.Renderer interface
func (t *TemplateRegistry) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	tmpl, ok := t.templates[name]
	if !ok {
		err := errors.New("Template not found -> " + name)
		return err
	}
	return tmpl.ExecuteTemplate(w, "layout.html", data)
}

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
	err = db.AutoMigrate(&User{}, &App{}, &AppPermission{})
	if err != nil {
		return nil, err
	}

	svc := &AlbyOAuthService{
		cfg:       cfg,
		oauthConf: conf,
		db:        db,
	}

	e := echo.New()
	templates := make(map[string]*template.Template)
	templates["apps/index.html"] = template.Must(template.ParseFiles("views/apps/index.html", "views/layout.html"))
	templates["apps/new.html"] = template.Must(template.ParseFiles("views/apps/new.html", "views/layout.html"))
	templates["index.html"] = template.Must(template.ParseFiles("views/index.html", "views/layout.html"))
	e.Renderer = &TemplateRegistry{
		templates: templates,
	}
	e.Use(session.Middleware(sessions.NewCookieStore([]byte("secret"))))
	e.GET("/", svc.IndexHandler)
	e.GET("/alby/auth", svc.AuthHandler)
	e.GET("/alby/callback", svc.CallbackHandler)
	e.GET("/apps", svc.AppsListHandler)
	e.GET("/apps/new", svc.AppsNewHandler)
	e.POST("/apps", svc.AppsCreateHandler)
	e.POST("/apps/delete/:id", svc.AppsDeleteHandler)
	e.GET("/logout", svc.LogoutHandler)
	svc.e = e
	return svc, err
}

func (svc *AlbyOAuthService) SendPaymentSync(ctx context.Context, senderPubkey, payReq string) (preimage string, err error) {
	logrus.Infof("Processing payment request %s from %s", payReq, senderPubkey)
	app := &App{}
	err = svc.db.Preload("User").Find(app, &App{
		NostrPubkey: senderPubkey,
	}).Error
	if err != nil {
		return "", err
	}
	user := app.User
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
	if resp.StatusCode < 300 {
		logrus.Infof("Sent payment with hash %s preimage %s", responsePayload.PaymentHash, responsePayload.Preimage)
		return responsePayload.Preimage, nil
	} else {
		return "", errors.New("Failed")
	}
}

func (svc *AlbyOAuthService) IndexHandler(c echo.Context) error {
	return c.Render(http.StatusOK, "index.html", map[string]interface{}{})
}

func (svc *AlbyOAuthService) LogoutHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	sess.Values["user_id"] = ""
	delete(sess.Values, "user_id")
	sess.Options = &sessions.Options{
		MaxAge: -1,
	}
	sess.Save(c.Request(), c.Response())
	return c.Redirect(http.StatusMovedPermanently, "/")
}

func (svc *AlbyOAuthService) AppsListHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if userID == nil {
		return c.Redirect(http.StatusMovedPermanently, "/alby/auth")
	}

	user := &User{}
	svc.db.Preload("Apps").First(&user, userID)
	apps := user.Apps
	return c.Render(http.StatusOK, "apps/index.html", map[string]interface{}{
		"Apps": apps,
	})
}

func (svc *AlbyOAuthService) AppsNewHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if userID == nil {
		return c.Redirect(http.StatusMovedPermanently, "/alby/auth")
	}

	return c.Render(http.StatusOK, "apps/new.html", map[string]interface{}{})
}

func (svc *AlbyOAuthService) AppsCreateHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if userID == nil {
		return c.Redirect(http.StatusMovedPermanently, "/alby/auth")
	}
	user := &User{}
	svc.db.Preload("Apps").First(&user, userID)

	svc.db.Model(&user).Association("Apps").Append(&App{Name: c.FormValue("name"), NostrPubkey: c.FormValue("pubkey")})
	return c.Redirect(http.StatusMovedPermanently, "/apps")
}

func (svc *AlbyOAuthService) AppsDeleteHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if userID == nil {
		return c.Redirect(http.StatusMovedPermanently, "/alby/auth")
	}
	user := &User{}
	svc.db.Preload("Apps").First(&user, userID)
	app := &App{}
	svc.db.Where("user_id = ?", user.ID).First(&app, c.Param("id"))
	svc.db.Delete(&app)
	return c.Redirect(http.StatusMovedPermanently, "/apps")
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

	user := &User{}
	svc.db.FirstOrInit(&user, User{AlbyIdentifier: me.Identifier})
	user.AccessToken = tok.AccessToken
	user.RefreshToken = tok.RefreshToken
	user.Expiry = tok.Expiry // TODO; probably needs some calculation
	svc.db.Save(&user)

	app := &App{}
	svc.db.FirstOrInit(&app, App{UserId: user.ID, NostrPubkey: pubkey.(string)})
	app.Name = "General"
	svc.db.Save(&app)

	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	sess.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 0, // TODO: how to session cookie?
	}
	sess.Values["user_id"] = user.ID
	sess.Save(c.Request(), c.Response())
	return c.Redirect(http.StatusMovedPermanently, "/apps")
}
