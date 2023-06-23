package main

import (
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	echologrus "github.com/davrux/echo-logrus/v4"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/nbd-wtf/go-nostr"
	"github.com/sirupsen/logrus"
	ddEcho "gopkg.in/DataDog/dd-trace-go.v1/contrib/labstack/echo.v4"
	"gorm.io/gorm"
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
	user, err := svc.GetUser(c)
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "about.html", map[string]interface{}{
		"User": user,
	})
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

	lastEvents := make(map[uint]NostrEvent)
	eventsCounts := make(map[uint]int64)
	for _, app := range apps {
		var lastEvent NostrEvent
		var eventsCount int64
		svc.db.Where("app_id = ?", app.ID).Order("id desc").Limit(1).Find(&lastEvent)
		svc.db.Model(&NostrEvent{}).Where("app_id = ?", app.ID).Count(&eventsCount)
		lastEvents[app.ID] = lastEvent
		eventsCounts[app.ID] = eventsCount
	}

	return c.Render(http.StatusOK, "apps/index.html", map[string]interface{}{
		"Apps":         apps,
		"User":         user,
		"LastEvents":   lastEvents,
		"EventsCounts": eventsCounts,
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

	appPermission := AppPermission{}
	svc.db.Where("app_id = ? AND request_method = ?", app.ID, NIP_47_PAY_INVOICE_METHOD).First(&appPermission)

	renewsIn := ""
	budgetUsage := int64(0)
	maxAmount := appPermission.MaxAmount
	if maxAmount > 0 {
		budgetUsage = svc.GetBudgetUsage(&appPermission)
		endOfBudget := GetEndOfBudget(appPermission.BudgetRenewal, app.CreatedAt)
		renewsIn = getEndOfBudgetString(endOfBudget)

	}

	return c.Render(http.StatusOK, "apps/show.html", map[string]interface{}{
		"App":           app,
		"AppPermission": appPermission,
		"User":          user,
		"LastEvent":     lastEvent,
		"EventsCount":   eventsCount,
		"BudgetUsage":   budgetUsage,
		"RenewsIn":      renewsIn,
	})
}

func getEndOfBudgetString(endOfBudget time.Time) (result string) {
	if endOfBudget.IsZero() {
		return "--"
	}
	endOfBudgetDuration := endOfBudget.Sub(time.Now())

	//less than a day
	if endOfBudgetDuration.Hours() < 24 {
		hours := int(endOfBudgetDuration.Hours())
		minutes := int(endOfBudgetDuration.Minutes()) % 60
		return fmt.Sprintf("%d hours and %d minutes", hours, minutes)
	}
	//less than a month
	if endOfBudgetDuration.Hours() < 24*30 {
		days := int(endOfBudgetDuration.Hours() / 24)
		return fmt.Sprintf("%d days", days)
	}
	//more than a month
	months := int(endOfBudgetDuration.Hours() / 24 / 30)
	days := int(endOfBudgetDuration.Hours()/24) % 30
	if days > 0 {
		return fmt.Sprintf("%d months %d days", months, days)
	}
	return fmt.Sprintf("%d months", months)
}

func (svc *Service) AppsNewHandler(c echo.Context) error {
	appName := c.QueryParam("c") // c - for client
	pubkey := c.QueryParam("pubkey")
	returnTo := c.QueryParam("return_to")
	maxAmount := c.QueryParam("max_amount")
	budgetRenewal := strings.ToLower(c.QueryParam("budget_renewal"))
	expiresAt := c.QueryParam("expires_at") // YYYY-MM-DD or MM/DD/YYYY
	disabled := c.QueryParam("editable") == "false"
	budgetEnabled := maxAmount != "" || budgetRenewal != ""

	user, err := svc.GetUser(c)
	if err != nil {
		return err
	}
	if user == nil {
		sess, _ := session.Get("alby_nostr_wallet_connect", c)
		sess.Values["return_to"] = c.Path() + "?" + c.QueryString()
		sess.Save(c.Request(), c.Response())
		return c.Redirect(302, fmt.Sprintf("/%s/auth", strings.ToLower(svc.cfg.LNBackendType)))
	}

	return c.Render(http.StatusOK, "apps/new.html", map[string]interface{}{
		"User":          user,
		"Name":          appName,
		"Pubkey":        pubkey,
		"ReturnTo":      returnTo,
		"MaxAmount":     maxAmount,
		"BudgetRenewal": budgetRenewal,
		"ExpiresAt":     expiresAt,
		"BudgetEnabled": budgetEnabled,
		"Disabled":      disabled,
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
		//validate public key
		decoded, err := hex.DecodeString(pairingPublicKey)
		if err != nil || len(decoded) != 32 {
			svc.Logger.Errorf("Invalid public key format: %s", pairingPublicKey)
			return c.Redirect(302, "/apps")
		}
	}

	backend := "alby"
	var lnbitsadminkey = ""
	var lnbitshost = ""
	if svc.cfg.LNBackendType != AlbyBackendType {
		backend = "lnd"
		if c.FormValue("backend") != "" {
			backend = c.FormValue("backend")
		}
		if backend == "lnbits" {
			if c.FormValue("lnbitsadminkey") != "" {
				lnbitsadminkey = c.FormValue("lnbitsadminkey")
			}
			//use local instance from config if not overwritten
			if c.FormValue("lnbitshost") != "" {
				lnbitshost = c.FormValue("lnbitshost")
			}
		}
	}

	app := App{Name: name, NostrPubkey: pairingPublicKey, Backend: backend, BackendOptionsLNBitsKey: lnbitsadminkey, BackendOptionsLNBitsHost: lnbitshost}

	maxAmount, _ := strconv.Atoi(c.FormValue("MaxAmount"))
	budgetRenewal := c.FormValue("BudgetRenewal")
	expiresAt, _ := time.Parse("2006-01-02", c.FormValue("ExpiresAt"))
	if !expiresAt.IsZero() {
		expiresAt = time.Date(expiresAt.Year(), expiresAt.Month(), expiresAt.Day(), 23, 59, 59, 0, expiresAt.Location())
	}

	err = svc.db.Transaction(func(tx *gorm.DB) error {
		err = tx.Model(&user).Association("Apps").Append(&app)
		if err != nil {
			return err
		}

		if maxAmount > 0 || !expiresAt.IsZero() {
			appPermission := AppPermission{
				App:           app,
				RequestMethod: NIP_47_PAY_INVOICE_METHOD,
				MaxAmount:     maxAmount,
				BudgetRenewal: budgetRenewal,
				ExpiresAt:     expiresAt,
			}

			err = tx.Create(&appPermission).Error
			if err != nil {
				return err
			}
		}

		// commit transaction
		return nil
	})

	if err != nil {
		svc.Logger.WithFields(logrus.Fields{
			"pairingPublicKey": pairingPublicKey,
			"name":             name,
		}).Errorf("Failed to save app: %v", err)
		return c.Redirect(302, "/apps")
	}

	if c.FormValue("returnTo") != "" {
		returnToUrl, err := url.Parse(c.FormValue("returnTo"))
		if err == nil {
			query := returnToUrl.Query()
			query.Add("relay", svc.cfg.Relay)
			query.Add("pubkey", svc.cfg.IdentityPubkey)
			if user.LightningAddress != "" {
				query.Add("lud16", user.LightningAddress)
			}
			returnToUrl.RawQuery = query.Encode()
			return c.Redirect(302, returnToUrl.String())
		}
	}

	var lud16 string
	if user.LightningAddress != "" {
		lud16 = fmt.Sprintf("&lud16=%s", user.LightningAddress)
	}
	pairingUri := template.URL(fmt.Sprintf("nostrwalletconnect://%s?relay=%s&secret=%s%s", svc.cfg.IdentityPubkey, svc.cfg.Relay, pairingSecretKey, lud16))
	return c.Render(http.StatusOK, "apps/create.html", map[string]interface{}{
		"User":          user,
		"PairingUri":    pairingUri,
		"PairingSecret": pairingSecretKey,
		"Pubkey":        pairingPublicKey,
		"Name":          name,
		"Backend":       svc.cfg.LNBackendType,
	})
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
