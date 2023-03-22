package main

import (
	"time"
)

const (
	NIP_47_REQUEST_KIND          = 23194
	NIP_47_SUCCESS_RESPONSE_KIND = 23195
	NIP_47_ERROR_RESPONSE_KIND   = 23196
)

type AlbyMe struct {
	Identifier       string `json:"identifier"`
	NPub             string `json:"nostr_pubkey"`
	LightningAddress string `json:"lightning_address"`
}

type User struct {
	ID             uint   `gorm:"primaryKey"`
	AlbyIdentifier string `gorm:"uniqueIndex" validate:"required"`
	AccessToken    string `validate:"required"`
	RefreshToken   string `validate:"required"`
	Expiry         time.Time
	Apps           []App
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type App struct {
	ID          uint `gorm:"primaryKey"`
	UserId      uint `gorm:"index" validate:"required"`
	User        User
	Name        string `validate:"required"`
	Description string
	NostrPubkey string `gorm:"index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type AppPermission struct {
	ID        uint `gorm:"primaryKey"`
	AppId     uint `gorm:"index" validate:"required"`
	App       App
	NostrKind int64 `gorm:"index" validate:"required"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type PayRequest struct {
	Invoice string `json:"invoice"`
}

type PayResponse struct {
	Preimage    string `json:"payment_preimage"`
	PaymentHash string `json:"payment_hash"`
}
