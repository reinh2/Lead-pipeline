package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// Slack posts the same card to a channel via an incoming webhook and
// verifies interactivity requests using the workspace signing secret.
type Slack struct {
	webhookURL    string
	signingSecret string
	http          *http.Client
	log           *slog.Logger
}

func NewSlack(webhookURL, signingSecret string, log *slog.Logger) *Slack {
	return &Slack{
		webhookURL:    webhookURL,
		signingSecret: signingSecret,
		http:          &http.Client{Timeout: 10 * time.Second},
		log:           log,
	}
}

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Notify(ctx context.Context, l models.Lead, q models.Qualification, dealID string) error {
	blocks := SlackBlocks(l, q, dealID)
	body, _ := json.Marshal(map[string]any{"blocks": blocks})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack HTTP %d: %s", resp.StatusCode, string(data))
	}
	return nil
}

// SlackBlocks renders the standard new-lead card as Block Kit blocks. Exported
// so tests can inspect the structure without a live webhook.
func SlackBlocks(l models.Lead, q models.Qualification, dealID string) []map[string]any {
	return []map[string]any{
		{"type": "header",
			"text": map[string]any{"type": "plain_text", "text": emojiFor(q.Category) + " " + string(q.Category) + " lead: " + l.Name}},
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": q.Summary}},
		{"type": "section", "fields": []map[string]any{
			{"type": "mrkdwn", "text": "*Service:* " + string(l.Service)},
			{"type": "mrkdwn", "text": "*Move date:* " + defaultIfEmpty(l.MoveDate, "not set")},
			{"type": "mrkdwn", "text": "*Nationality:* " + l.Nationality},
			{"type": "mrkdwn", "text": "*Family:* " + strconv.Itoa(l.FamilySize)},
		}},
		{"type": "section", "text": map[string]any{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Estimate:* €%d – €%d", q.Estimate.Min, q.Estimate.Max),
		}},
		{"type": "actions", "block_id": "lead:" + dealID, "elements": []map[string]any{
			{"type": "button", "style": "primary", "text": map[string]any{"type": "plain_text", "text": "✅ Claim lead"}, "action_id": "claim", "value": dealID},
			{"type": "button", "style": "danger", "text": map[string]any{"type": "plain_text", "text": "🗑 Mark spam"}, "action_id": "spam", "value": dealID},
		}},
	}
}

// VerifySignature validates a Slack interactivity request per
// https://api.slack.com/authentication/verifying-requests-from-slack
func VerifySignature(signingSecret, timestamp, signatureHeader string, body []byte) bool {
	if signingSecret == "" {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if abs(time.Now().Unix()-ts) > 300 {
		return false
	}
	base := "v0:" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(base))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signatureHeader))
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
