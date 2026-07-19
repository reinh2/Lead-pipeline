package qualifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

const systemPrompt = `You are the intake analyst at Anker Relocation, a Berlin agency helping expats move to Germany.
Classify a new lead and draft a friendly first reply.

Price anchors (in EUR):
- visa_only: 800-1500
- apartment_only: 1000-2000
- full_relocation: 2500-6000
- families of 3+: add 50% on top

Hot signals: "signed offer", concrete start date, employer name, target date within 8 weeks.
Cold signals: vague timelines ("someday", "thinking about"), no offer, no target date, "just curious".
Spam: crypto pitches, SEO services, loan offers, unrelated marketing.

Respond ONLY with a single JSON object that matches this schema exactly, no prose, no markdown fences:
{
  "category": "hot" | "warm" | "cold" | "spam",
  "urgency": 1-5,
  "estimate_eur": {"min": integer, "max": integer},
  "summary": "one line for the sales manager (max 140 chars)",
  "reply": "short friendly customer-facing reply that mentions the price range and proposes a next step (max 500 chars)"
}`

var fewShots = []struct {
	user string
	ans  models.Qualification
}{
	{
		user: `name: Alex Chen | nationality: Canadian | service: full_relocation | move_date: 2026-09-01 | family_size: 3 | message: Signed offer with N26, start Sep 1. Need visa, apartment in Prenzlauer Berg, school for two kids.`,
		ans: models.Qualification{Category: models.CategoryHot, Urgency: 5, Estimate: models.EstimateEUR{Min: 3750, Max: 9000},
			Summary: "Signed N26 offer, family of 3, Sept start — full package, call today.",
			Reply:   "Hi Alex, great timing — for a family move like yours we usually scope between €3,750 and €9,000 depending on the school and neighbourhood. Can we jump on a 15-min call today?"},
	},
	{
		user: `name: Jamie Doe | nationality: American | service: full_relocation | move_date: | family_size: 1 | message: Thinking about maybe moving to Berlin someday, just curious about costs.`,
		ans: models.Qualification{Category: models.CategoryCold, Urgency: 2, Estimate: models.EstimateEUR{Min: 2500, Max: 6000},
			Summary: "Early research, no offer or date. Send checklist and nurture.",
			Reply:   "Hi Jamie, happy to send our Berlin relocation checklist so you can plan ahead. Reply when you have a start date and we'll build a scope."},
	},
	{
		user: `name: Priya Sharma | nationality: Indian | service: full_relocation | move_date: 2026-10-01 | family_size: 2 | message: Interviewing at two Berlin startups, likely offer next month. Wife will join.`,
		ans: models.Qualification{Category: models.CategoryWarm, Urgency: 3, Estimate: models.EstimateEUR{Min: 2500, Max: 6000},
			Summary: "Actively interviewing, likely offer in a month. Book scoping call.",
			Reply:   "Hi Priya, thanks for the details — for a couple relocating together we usually land between €2,500 and €6,000. Want to book a free 15-min scoping call this week?"},
	},
	{
		user: `name: Coin Marketing | nationality: n/a | service: full_relocation | move_date: | family_size: 1 | message: We offer 10x crypto returns and SEO backlinks for your site!!!`,
		ans: models.Qualification{Category: models.CategorySpam, Urgency: 1, Estimate: models.EstimateEUR{},
			Summary: "Crypto/SEO spam.", Reply: ""},
	},
}

type OpenRouterConfig struct {
	APIKey  string
	Model   string
	Timeout time.Duration
}

type OpenRouter struct {
	cfg  OpenRouterConfig
	http *http.Client
	log  *slog.Logger
}

func NewOpenRouter(cfg OpenRouterConfig, log *slog.Logger) *OpenRouter {
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &OpenRouter{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		log:  log,
	}
}

func (o *OpenRouter) Qualify(ctx context.Context, l models.Lead) models.Qualification {
	msgs := []map[string]string{{"role": "system", "content": systemPrompt}}
	for _, ex := range fewShots {
		j, _ := json.Marshal(ex.ans)
		msgs = append(msgs,
			map[string]string{"role": "user", "content": ex.user},
			map[string]string{"role": "assistant", "content": string(j)},
		)
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": renderLead(l)})

	body := map[string]any{
		"model":           o.cfg.Model,
		"messages":        msgs,
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0.2,
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				o.log.Warn("qualifier canceled", "lead_id", l.ID)
				return Fallback(l)
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		q, err := o.call(ctx, body)
		if err == nil {
			return q
		}
		lastErr = err
		o.log.Warn("qualifier attempt failed", "lead_id", l.ID, "attempt", attempt+1, "err", err)
	}
	o.log.Error("qualifier giving up, using fallback", "lead_id", l.ID, "err", lastErr)
	return Fallback(l)
}

func (o *OpenRouter) call(ctx context.Context, body map[string]any) (models.Qualification, error) {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return models.Qualification{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "Anker Relocation Pipeline")

	resp, err := o.http.Do(req)
	if err != nil {
		return models.Qualification{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return models.Qualification{}, fmt.Errorf("openrouter HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var wire struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return models.Qualification{}, err
	}
	if len(wire.Choices) == 0 {
		return models.Qualification{}, errors.New("openrouter: no choices")
	}
	return ParseQualification(wire.Choices[0].Message.Content)
}

// ParseQualification robustly extracts the JSON verdict from an LLM reply,
// tolerating markdown fences and stray prose around the JSON object.
func ParseQualification(content string) (models.Qualification, error) {
	s := strings.TrimSpace(content)
	// Trim ``` fences if present.
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	// Take the first {...} block.
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return models.Qualification{}, errors.New("no JSON object in LLM output")
	}
	s = s[start : end+1]

	var q models.Qualification
	if err := json.Unmarshal([]byte(s), &q); err != nil {
		return models.Qualification{}, err
	}
	switch q.Category {
	case models.CategoryHot, models.CategoryWarm, models.CategoryCold, models.CategorySpam:
	default:
		return models.Qualification{}, fmt.Errorf("invalid category %q", q.Category)
	}
	if q.Urgency < 1 || q.Urgency > 5 {
		q.Urgency = 3
	}
	if q.Summary == "" {
		return models.Qualification{}, errors.New("missing summary")
	}
	return q, nil
}

func renderLead(l models.Lead) string {
	return fmt.Sprintf(
		"name: %s | nationality: %s | service: %s | move_date: %s | family_size: %d | job_offer: %s | message: %s",
		l.Name, l.Nationality, l.Service, l.MoveDate, l.FamilySize, l.JobOffer, l.Message,
	)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
