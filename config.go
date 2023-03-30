package main

const (
	AlbyBackendType = "ALBY"
	LNDBackendType  = "LND"
)

type Config struct {
	NostrSecretKey   string `envconfig:"NOSTR_PRIVKEY" required:"true"`
	CookieSecret     string `envconfig:"COOKIE_SECRET" required:"true"`
	ClientPubkey     string `envconfig:"CLIENT_NOSTR_PUBKEY"`
	Relay            string `envconfig:"RELAY" required:"true"`
	LNBackendType    string `envconfig:"LN_BACKEND_TYPE" default:"ALBY"`
	LNDAddress       string `envconfig:"LND_ADDRESS"`
	LNDCertFile      string `envconfig:"LND_CERT_FILE"`
	LNDMacaroonFile  string `envconfig:"LND_MACAROON_FILE"`
	AlbyAPIURL       string `envconfig:"ALBY_API_URL" default:"https://api.getalby.com"`
	AlbyClientId     string `envconfig:"ALBY_CLIENT_ID"`
	AlbyClientSecret string `envconfig:"ALBY_CLIENT_SECRET"`
	OAuthRedirectUrl string `envconfig:"OAUTH_REDIRECT_URL"`
	OAuthAuthUrl     string `envconfig:"OAUTH_AUTH_URL" default:"https://getalby.com/oauth"`
	OAuthTokenUrl    string `envconfig:"OAUTH_TOKEN_URL" default:"https://api.getalby.com/oauth/token"`
	OAuthServerPort  string `envconfig:"OAUTH_SERVER_PORT" default:"8080"`
	DatabaseUri      string `envconfig:"DATABASE_URI" default:"nostr-wallet-connect.db"`
	IdentityPubkey   string
}
