package main

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strings"

	echologrus "github.com/davrux/echo-logrus/v4"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/nbd-wtf/go-nostr"
	"github.com/sirupsen/logrus"
	ddEcho "gopkg.in/DataDog/dd-trace-go.v1/contrib/labstack/echo.v4"
)

//go:embed public/*
var embeddedAssets embed.FS

//go:embed views/*
var embeddedViews embed.FS

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

func (svc *Service) RegisterSharedRoutes(e *echo.Echo) {

	templates := make(map[string]*template.Template)
	templates["apps/index.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/index.html", "views/layout.html"))
	templates["apps/new.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/new.html", "views/layout.html"))
	templates["apps/show.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/show.html", "views/layout.html"))
	templates["apps/create.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/create.html", "views/layout.html"))
	templates["apps/new_with_pubkey.html"] = template.Must(template.ParseFS(embeddedViews, "views/apps/new_with_pubkey.html", "views/layout.html"))
	templates["alby/index.html"] = template.Must(template.ParseFS(embeddedViews, "views/backends/alby/index.html", "views/layout.html"))
	templates["about.html"] = template.Must(template.ParseFS(embeddedViews, "views/about.html", "views/layout.html"))
	templates["lnd/index.html"] = template.Must(template.ParseFS(embeddedViews, "views/backends/lnd/index.html", "views/layout.html"))
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
	e.GET("/about", svc.AboutHandler)
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
	return c.Render(http.StatusOK, fmt.Sprintf("%s/index.html", strings.ToLower(svc.cfg.LNBackendType)), map[string]interface{}{})
}

func (svc *Service) AboutHandler(c echo.Context) error {
	return c.Render(http.StatusOK, "about.html", map[string]interface{}{})
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
	pubkey := c.QueryParam("pubkey")
	referrer := c.Request().Header.Get("Referrer")
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
	var template string
	if pubkey != "" {
		template = "apps/new_with_pubkey.html"
	} else {
		template = "apps/new.html"
	}
	return c.Render(http.StatusOK, template, map[string]interface{}{
		"User":     user,
		"Name":     appName,
		"Pubkey":   pubkey,
		"Referrer": referrer,
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
