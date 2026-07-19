package models

import "time"

type Source string

const (
	SourceWeb      Source = "web"
	SourceTelegram Source = "telegram"
)

type Service string

const (
	ServiceVisaOnly       Service = "visa_only"
	ServiceApartmentOnly  Service = "apartment_only"
	ServiceFullRelocation Service = "full_relocation"
)

// Lead is what we collect from the landing form or the Telegram intake bot.
// The zero value of a field means "unknown" — the intake channel decides
// which fields are required. The API layer enforces required fields for web
// leads; the Telegram bot fills them as it walks the user through the intake.
type Lead struct {
	ID             string    `json:"id"`
	Source         Source    `json:"source"`
	CreatedAt      time.Time `json:"created_at"`
	Name           string    `json:"name"`
	Email          string    `json:"email,omitempty"`
	Nationality    string    `json:"nationality"`
	CurrentCountry string    `json:"current_country,omitempty"`
	Service        Service   `json:"service,omitempty"`
	MoveDate       string    `json:"move_date,omitempty"`
	FamilySize     int       `json:"family_size,omitempty"`
	JobOffer       string    `json:"job_offer,omitempty"` // "signed" | "interviewing" | "no" (telegram only)
	Message        string    `json:"message,omitempty"`
	GDPRConsent    bool      `json:"gdpr_consent"`
	ConsentVersion string    `json:"consent_version,omitempty"`
	ConsentedAt    time.Time `json:"consented_at,omitempty"`

	// Only set when Source == telegram — used by autoreply to DM the user back.
	TelegramChatID int64 `json:"telegram_chat_id,omitempty"`
}

type Category string

const (
	CategoryHot  Category = "hot"
	CategoryWarm Category = "warm"
	CategoryCold Category = "cold"
	CategorySpam Category = "spam"
)

type EstimateEUR struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Qualification is the AI verdict. All fields are guaranteed to be filled
// even when the LLM call fails — the qualifier's fallback returns a
// conservative warm/generic estimate.
type Qualification struct {
	Category Category    `json:"category"`
	Urgency  int         `json:"urgency"` // 1..5
	Estimate EstimateEUR `json:"estimate_eur"`
	Summary  string      `json:"summary"`
	Reply    string      `json:"reply"`
	Fallback bool        `json:"fallback,omitempty"` // true when returned by the safety net
}
