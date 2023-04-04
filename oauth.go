package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"time"

	echologrus "github.com/davrux/echo-logrus/v4"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/nbd-wtf/go-nostr"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	ddEcho "gopkg.in/DataDog/dd-trace-go.v1/contrib/labstack/echo.v4"
	"gorm.io/gorm"
)

type TemplateRegistry struct {
	templates map[string]*template.Template
}

type AlbyOAuthService struct {
	cfg       *Config
	oauthConf *oauth2.Config
	db        *gorm.DB
	e         *echo.Echo
	Logger    *logrus.Logger
}

//go:embed public/*
var embeddedAssets embed.FS

//go:embed views/*
var embeddedViews embed.FS

// Implement e.Renderer interface
func (t *TemplateRegistry) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	tmpl, ok := t.templates[name]
	if !ok {
		err := errors.New("Template not found -> " + name)
		return err
	}
	return tmpl.ExecuteTemplate(w, "layout.html", data)
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

func NewAlbyOauthService(svc *Service) (result *AlbyOAuthService, err error) {
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

	e := echo.New()
	templates := make(map[string]*template.Template)
	templates["apps/index.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/index.html", "views/layout.html"))
	templates["apps/new.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/new.html", "views/layout.html"))
	templates["apps/show.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/show.html", "views/layout.html"))
	templates["apps/create.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/create.html", "views/layout.html"))
	templates["index.html"] = template.Must(template.ParseFS(embeddedViews, "views/index.html", "views/layout.html"))
	e.Renderer = &TemplateRegistry{
		templates: templates,
	}
	e.HideBanner = true
	e.Logger = echologrus.GetEchoLogger()
	e.Use(echologrus.Middleware())

	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(session.Middleware(sessions.NewCookieStore([]byte(svc.cfg.CookieSecret))))
	e.Use(ddEcho.Middleware(ddEcho.WithServiceName("nostr-wallet-connect")))

	assetSubdir, err := fs.Sub(embeddedAssets, "public")
	assetHandler := http.FileServer(http.FS(assetSubdir))
	e.GET("/public/*", echo.WrapHandler(http.StripPrefix("/public/", assetHandler)))
	e.GET("/", albySvc.IndexHandler)
	e.GET("/alby/auth", albySvc.AuthHandler)
	e.GET("/alby/callback", albySvc.CallbackHandler)
	e.GET("/apps", albySvc.AppsListHandler)
	e.GET("/apps/new", albySvc.AppsNewHandler)
	e.GET("/apps/:id", albySvc.AppsShowHandler)
	e.POST("/apps", albySvc.AppsCreateHandler)
	e.POST("/apps/delete/:id", albySvc.AppsDeleteHandler)
	e.GET("/logout", albySvc.LogoutHandler)
	albySvc.e = e
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
	svc.db.Save(&user)
	client := svc.oauthConf.Client(ctx, tok)

	body := bytes.NewBuffer([]byte{})
	payload := &PayRequest{
		Invoice: payReq,
	}
	err = json.NewEncoder(body).Encode(payload)
	resp, err := client.Post(fmt.Sprintf("%s/payments/bolt11", svc.cfg.AlbyAPIURL), "application/json", body)
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"senderPubkey": senderPubkey,
			"bolt11":       payReq,
			"appId":        app.ID,
			"userId":       app.User.ID,
		}).Errorf("Failed to pay invoice: %v", err)
		return "", err
	}
	//todo non-200 status code handling
	responsePayload := &PayResponse{}
	err = json.NewDecoder(resp.Body).Decode(responsePayload)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 300 {
		svc.Logger.WithFields(logrus.Fields{
			"senderPubkey": senderPubkey,
			"bolt11":       payReq,
			"appId":        app.ID,
			"userId":       app.User.ID,
		}).Info("Payment successful")
		return responsePayload.Preimage, nil
	} else {
		svc.Logger.WithFields(logrus.Fields{
			"senderPubkey": senderPubkey,
			"bolt11":       payReq,
			"appId":        app.ID,
			"userId":       app.User.ID,
		}).Errorf("Payment failed %v", err)
		return "", errors.New("Failed")
	}
}

func (svc *AlbyOAuthService) IndexHandler(c echo.Context) error {
	appName := c.QueryParam("c") // c - for client
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	if appName != "" {
		sess.Values["app_name"] = appName
		sess.Save(c.Request(), c.Response())
	}
	userID := sess.Values["user_id"]
	if userID != nil {
		if appName != "" {
			//auto-create app
			return c.Redirect(302, fmt.Sprintf("/apps/new?c=%s", appName))
		}
		//else, go to dashboard
		return c.Redirect(302, "/apps")
	}
	return c.Render(http.StatusOK, "index.html", map[string]interface{}{})
}

func (svc *AlbyOAuthService) LogoutHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	sess.Options = &sessions.Options{
		MaxAge: -1,
	}
	sess.Save(c.Request(), c.Response())
	return c.Redirect(302, "/")
}

func (svc *AlbyOAuthService) AppsListHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if userID == nil {
		return c.Redirect(302, "/")
	}

	user := User{}
	svc.db.Preload("Apps").First(&user, userID)
	apps := user.Apps
	return c.Render(http.StatusOK, "apps/index.html", map[string]interface{}{
		"Apps": apps,
		"User": user,
	})
}

func (svc *AlbyOAuthService) AppsShowHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if userID == nil {
		return c.Redirect(302, "/")
	}

	user := User{}
	svc.db.Preload("Apps").First(&user, userID)
	app := App{}
	svc.db.Where("user_id = ?", user.ID).First(&app, c.Param("id"))
	lastEvent := NostrEvent{}
	svc.db.Where("app_id = ?", app.ID).Order("id desc").Limit(1).Find(&lastEvent)
	var eventsCount int64
	svc.db.Model(&NostrEvent{}).Where("app_id = ?", app.ID).Count(&eventsCount)
	return c.Render(http.StatusOK, "apps/show.html", map[string]interface{}{
		"App":         app,
		"User":        user,
		"LastEvent":   lastEvent,
		"EventsCount": eventsCount,
	})
}

func (svc *AlbyOAuthService) AppsNewHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	name := c.QueryParam("c") // c - for client
	if userID == nil {
		return c.Redirect(302, "/?c="+name)
	}

	if name != "" {
		//auto-create app
		sk, pk, connectionString, err := svc.CreateApp(name, userID.(uint))
		if err != nil {
			svc.Logger.WithFields(logrus.Fields{
				"pairingPublicKey": pk,
				"name":             name,
			}).Errorf("Failed to save app: %v", err)
			return c.Redirect(302, "/apps")
		}
		//check user agent
		//return page based on user agent
		if checkMobile(c.Request().UserAgent()) {
			return c.Render(http.StatusOK, "apps/mobile_create.html", map[string]interface{}{
				"PairingUri":    connectionString,
				"PairingSecret": sk,
				"Pubkey":        pk,
				"Name":          name,
			})
		}
		return c.Render(http.StatusOK, "apps/create.html", map[string]interface{}{
			"PairingUri":    connectionString,
			"PairingSecret": sk,
			"Pubkey":        pk,
			"Name":          name,
		})
	}
	user := User{}
	svc.db.First(&user, userID)

	return c.Render(http.StatusOK, "apps/new.html", map[string]interface{}{
		"User": user,
	})
}

func checkMobile(userAgent string) bool {
	fmt.Println(userAgent)
	//todo
	return false
}

func (svc *AlbyOAuthService) CreateApp(name string, userId uint) (sk, pk, connectionString string, err error) {
	user := User{}
	err = svc.db.Preload("Apps").First(&user, userId).Error
	if err != nil {
		return
	}

	sk = nostr.GeneratePrivateKey()
	pk, err = nostr.GetPublicKey(sk)
	if err != nil {
		return
	}
	if name == "" {
		err = fmt.Errorf("Empty name is not allowed.")
		return
	}

	err = svc.db.Model(&user).Association("Apps").Append(&App{Name: name, NostrPubkey: pk})
	if err != nil {
		return
	}
	connectionString = fmt.Sprintf("nostrwalletconnect://%s?relay=%s&secret=%s", svc.cfg.IdentityPubkey, svc.cfg.Relay, sk)
	return
}

func (svc *AlbyOAuthService) AppsCreateHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if userID == nil {
		return c.Redirect(302, "/")
	}
	name := c.FormValue("name")
	sk, pk, connectionString, err := svc.CreateApp(name, userID.(uint))
	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"pairingPublicKey": pk,
			"name":             name,
		}).Errorf("Failed to save app: %v", err)
		return c.Redirect(302, "/apps")
	}
	return c.Render(http.StatusOK, "apps/create.html", map[string]interface{}{
		"PairingUri":    connectionString,
		"PairingSecret": sk,
		"Pubkey":        pk,
		"Name":          name,
	})
}

func (svc *AlbyOAuthService) AppsDeleteHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	userID := sess.Values["user_id"]
	if userID == nil {
		return c.Redirect(302, "/")
	}
	user := User{}
	svc.db.Preload("Apps").First(&user, userID)
	app := App{}
	svc.db.Where("user_id = ?", user.ID).First(&app, c.Param("id"))
	svc.db.Delete(&app)
	return c.Redirect(302, "/apps")
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
	res, err := client.Get(fmt.Sprintf("%s/user/me", svc.cfg.AlbyAPIURL))
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
	svc.db.Save(&user)

	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	sess.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 0, // TODO: how to session cookie?
	}
	sess.Values["user_id"] = user.ID
	sess.Save(c.Request(), c.Response())
	appName := sess.Values["app_name"].(string)
	if appName != "" {
		return c.Redirect(302, fmt.Sprintf("/apps/new?c=%s", appName))
	}
	return c.Redirect(302, "/apps")
}
