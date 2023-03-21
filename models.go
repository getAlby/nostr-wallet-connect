package main

import (
	"time"

	"gorm.io/gorm"
)

const (
	NIP_47_REQUEST_KIND          = 23194
	NIP_47_SUCCESS_RESPONSE_KIND = 23195
	NIP_47_ERROR_RESPONSE_KIND   = 23196
)

type AlbyMe struct {
	Identifier string `json:"identifier"`
	NPub       string `json:"nostr_pubkey"`
}

type User struct {
	gorm.Model
	NostrPubkey  string
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

type PayRequest struct {
	Invoice string `json:"invoice"`
}

type PayResponse struct {
	Preimage    string `json:"payment_preimage"`
	PaymentHash string `json:"payment_hash"`
}
