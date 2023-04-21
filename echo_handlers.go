package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	echologrus "github.com/davrux/echo-logrus/v4"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/nbd-wtf/go-nostr"
	"github.com/sirupsen/logrus"
	ddEcho "gopkg.in/DataDog/dd-trace-go.v1/contrib/labstack/echo.v4"
)

func (svc *Service) RegisterSharedRoutes(e *echo.Echo) {

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

	assetSubdir, _ := fs.Sub(embeddedAssets, "public")
	assetHandler := http.FileServer(http.FS(assetSubdir))
	e.GET("/public/*", echo.WrapHandler(http.StripPrefix("/public/", assetHandler)))
	e.GET("/apps", svc.AppsListHandler)
	e.GET("/apps/new", svc.AppsNewHandler)
	e.GET("/apps/:id", svc.AppsShowHandler)
	e.POST("/apps", svc.AppsCreateHandler)
	e.POST("/apps/delete/:id", svc.AppsDeleteHandler)
	e.GET("/logout", svc.LogoutHandler)
	e.GET("/", svc.IndexHandler)
}

func (svc *Service) IndexHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	returnTo := sess.Values["return_to"]
	user, err := svc.GetUser(c)
	if err != nil {
		return err
	}
	if user != nil && returnTo != nil {
		delete(sess.Values, "return_to")
		sess.Save(c.Request(), c.Response())
		return c.Redirect(302, fmt.Sprintf("%s", returnTo))
	}
	if user != nil {
		return c.Redirect(302, "/apps")
	}
	return c.Render(http.StatusOK, "index.html", map[string]interface{}{})
}

func (svc *Service) AppsListHandler(c echo.Context) error {
	user, err := svc.GetUser(c)
	if err != nil {
		return err
	}
	if user == nil {
		return c.Redirect(302, "/")
	}

	apps := user.Apps
	return c.Render(http.StatusOK, "apps/index.html", map[string]interface{}{
		"Apps": apps,
		"User": user,
	})
}

func (svc *Service) AppsShowHandler(c echo.Context) error {
	user, err := svc.GetUser(c)
	if err != nil {
		return err
	}
	if user == nil {
		return c.Redirect(302, "/")
	}

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

func (svc *Service) AppsNewHandler(c echo.Context) error {
	appName := c.QueryParam("c") // c - for client
	user, err := svc.GetUser(c)
	if err != nil {
		return err
	}
	if user == nil {
		sess, _ := session.Get("alby_nostr_wallet_connect", c)
		sess.Values["return_to"] = c.Path() + "?" + c.QueryString()
		sess.Save(c.Request(), c.Response())
		return c.Redirect(302, "/")
	}
	return c.Render(http.StatusOK, "apps/new.html", map[string]interface{}{
		"User": user,
		"Name": appName,
	})
}

func (svc *Service) AppsCreateHandler(c echo.Context) error {
	user, err := svc.GetUser(c)
	if err != nil {
		return err
	}
	if user == nil {
		return c.Redirect(302, "/")
	}

	name := c.FormValue("name")
	var pairingPublicKey string
	var pairingSecretKey string
	if c.FormValue("pubkey") == "" {
		pairingSecretKey = nostr.GeneratePrivateKey()
		pairingPublicKey, _ = nostr.GetPublicKey(pairingSecretKey)
	} else {
		pairingPublicKey = c.FormValue("pubkey")
	}

	err = svc.db.Model(&user).Association("Apps").Append(&App{Name: name, NostrPubkey: pairingPublicKey})
	if err == nil {
		pairingUri := template.URL(fmt.Sprintf("nostrwalletconnect://%s?relay=%s&secret=%s", svc.cfg.IdentityPubkey, svc.cfg.Relay, pairingSecretKey))
		return c.Render(http.StatusOK, "apps/create.html", map[string]interface{}{
			"PairingUri":    pairingUri,
			"PairingSecret": pairingSecretKey,
			"Pubkey":        pairingPublicKey,
			"Name":          name,
		})
	} else {
		svc.Logger.WithFields(logrus.Fields{
			"pairingPublicKey": pairingPublicKey,
			"name":             name,
		}).Errorf("Failed to save app: %v", err)
		return c.Redirect(302, "/apps")
	}
}

func (svc *Service) AppsDeleteHandler(c echo.Context) error {
	user, err := svc.GetUser(c)
	if err != nil {
		return err
	}
	if user == nil {
		return c.Redirect(302, "/")
	}
	app := App{}
	svc.db.Where("user_id = ?", user.ID).First(&app, c.Param("id"))
	svc.db.Delete(&app)
	return c.Redirect(302, "/apps")
}

func (svc *Service) LogoutHandler(c echo.Context) error {
	sess, _ := session.Get("alby_nostr_wallet_connect", c)
	sess.Options = &sessions.Options{
		MaxAge: -1,
	}
	sess.Save(c.Request(), c.Response())
	return c.Redirect(302, "/")
}
