# nostr-wallet-connect

This service allows you to control your Lightning node or wallet over Nostr.

[Draft NIP](https://github.com/getAlby/nips/blob/master/47.md)
## Configuration parameters
For self-hosting with your own node, set the following parameters:

- `NOSTR_PRIVKEY`: the private key of this service. Should be a securely randomly generated 32 byte hex string.
- `CLIENT_NOSTR_PUBKEY`: if set, this service will only listen to events authored by this public key. You can set this to your own nostr public key.
- `RELAY`: default: "wss://relay.getalby.com/v1"
- `LN_BACKEND_TYPE`: should be set to `LND`
- `LND_ADDRESS`: the LND gRPC address, eg. `localhost:10009`
- `LND_CERT_FILE`: the location where LND's `tls.cert` file can be found
- `LND_MACAROON_FILE`:  the location where LND's `admin.macaroon` file can be found
- `COOKIE_SECRET`: a randomly generated secret string.
- `DATABASE_URI`: a postgres connection string or sqlite filename. Default: nostr-wallet-connect.db (sqlite)

## Installation
`cp .env.example .env`

add `NOSTR_PRIVKEY`, `ALBY_CLIENT_SECRET`, `ALBY_CLIENT_ID` to .env

## Development
`go run .`

To build the CSS run:

`npx tailwindcss -i ./views/application.css -o ./public/css/application.css --watch`
