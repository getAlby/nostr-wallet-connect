package main

import (
	"time"

	"gorm.io/gorm"
)

const (
	NIP_47_INFO_EVENT_KIND = 13194
	NIP_47_REQUEST_KIND    = 23194
	NIP_47_RESPONSE_KIND   = 23195
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
	ID          uint   `gorm:"primaryKey"`
	UserId      uint   `gorm:"index" validate:"required"`
	User        User   `gorm:"constraint:OnDelete:CASCADE"`
	Name        string `validate:"required"`
	Description string
	NostrPubkey string `gorm:"index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type NostrEvent struct {
	ID        uint   `gorm:"primaryKey"`
	AppId     uint   `gorm:"index" validate:"required"`
	App       App    `gorm:"constraint:OnDelete:CASCADE"`
	NostrId   string `gorm:"uniqueIndex" validate:"required"`
	ReplyId   string
	Content   string
	State     string
	RepliedAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Payment struct {
	ID             uint `gorm:"primaryKey"`
	AppId          uint `gorm:"index" validate:"required"`
	App            App  `gorm:"constraint:OnDelete:CASCADE"`
	NostrEventId   uint `gorm:"index" validate:"required"`
	NostrEvent     NostrEvent
	Amount         uint
	PaymentRequest string
	Preimage       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type PayRequest struct {
	Invoice string `json:"invoice"`
}

type PayResponse struct {
	Preimage    string `json:"payment_preimage"`
	PaymentHash string `json:"payment_hash"`
}
type Identity struct {
	gorm.Model
	Privkey string
}
