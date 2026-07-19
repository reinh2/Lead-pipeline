package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// SheetsWebhook sends lead updates to a Google Apps Script web app bound to a
// spreadsheet. It is the low-setup alternative to a Google Cloud service
// account: no JSON key, Docker mount or Cloud billing configuration is needed.
type SheetsWebhook struct {
	webhookURL string
	secret     string
	http       *http.Client
	log        *slog.Logger
}

func NewSheetsWebhook(webhookURL, secret string, log *slog.Logger) (*SheetsWebhook, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(webhookURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("sheets webhook URL must be a valid https URL")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("sheets webhook secret is required")
	}
	return &SheetsWebhook{
		webhookURL: webhookURL,
		secret:     secret,
		http:       &http.Client{Timeout: 12 * time.Second},
		log:        log,
	}, nil
}

func (s *SheetsWebhook) Name() string { return "google_sheets_webhook" }

func (s *SheetsWebhook) Upsert(ctx context.Context, l models.Lead, q models.Qualification) (string, error) {
	err := s.call(ctx, map[string]any{
		"action": "upsert",
		"lead": map[string]any{
			"lead_id": l.ID, "created_at": l.CreatedAt.Format(time.RFC3339), "source": string(l.Source),
			"name": l.Name, "email": l.Email, "nationality": l.Nationality, "current_country": l.CurrentCountry,
			"service": string(l.Service), "move_date": l.MoveDate, "family_size": l.FamilySize,
			"job_offer": l.JobOffer, "message": l.Message, "gdpr_consent": l.GDPRConsent,
			"consent_version": l.ConsentVersion, "consented_at": l.ConsentedAt.Format(time.RFC3339),
			"category": string(q.Category), "urgency": q.Urgency, "estimate_min_eur": q.Estimate.Min,
			"estimate_max_eur": q.Estimate.Max, "summary": q.Summary,
		},
	})
	if err != nil {
		return "", err
	}
	return l.ID, nil
}

func (s *SheetsWebhook) MarkClaimed(ctx context.Context, id, manager string) error {
	return s.call(ctx, map[string]any{"action": "claim", "lead_id": id, "manager": manager})
}

func (s *SheetsWebhook) MarkSpam(ctx context.Context, id string) error {
	return s.call(ctx, map[string]any{"action": "spam", "lead_id": id})
}

func (s *SheetsWebhook) call(ctx context.Context, payload map[string]any) error {
	payload["secret"] = s.secret
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode sheets webhook request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create sheets webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("call sheets webhook: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read sheets webhook response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("sheets webhook HTTP %d: %s", resp.StatusCode, truncateResponse(data))
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode sheets webhook response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("sheets webhook rejected request: %s", result.Error)
	}
	return nil
}
