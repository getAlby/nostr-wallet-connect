package main

import (
	"time"

	"gorm.io/gorm"
)

const (
	NIP_47_INFO_EVENT_KIND       = 13194
	NIP_47_REQUEST_KIND          = 23194
	NIP_47_RESPONSE_KIND         = 23195
	NIP_47_PAY_INVOICE_METHOD    = "pay_invoice"
	NIP_47_ERROR_INTERNAL        = "INTERNAL"
	NIP_47_ERROR_NOT_IMPLEMENTED = "NOT_IMPLEMENTED"
	NIP_47_ERROR_UNAUTHORIZED    = "UNAUTHORIZED"
	NIP_47_CAPABILITIES          = "pay_invoice"
)

type AlbyMe struct {
	Identifier       string `json:"identifier"`
	NPub             string `json:"nostr_pubkey"`
	LightningAddress string `json:"lightning_address"`
	Email            string `json:"email"`
}

type User struct {
	ID               uint   `gorm:"primaryKey"`
	AlbyIdentifier   string `gorm:"uniqueIndex" validate:"required"`
	AccessToken      string `validate:"required"`
	RefreshToken     string `validate:"required"`
	Email            string
	Expiry           time.Time
	LightningAddress string
	Apps             []App
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
	TipNWC      bool
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

type ErrorResponse struct {
	Error   bool   `json:"error"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Identity struct {
	gorm.Model
	Privkey string
}

type Nip47Request struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type Nip47Response struct {
	Error      *Nip47Error `json:"error,omitempty"`
	Result     interface{} `json:"result,omitempty"`
	ResultType string      `json:"result_type,omitempty"`
}

type Nip47Error struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type Nip47PayParams struct {
	Invoice string `json:"invoice"`
}
type Nip47PayResponse struct {
	Preimage string `json:"preimage"`
}
