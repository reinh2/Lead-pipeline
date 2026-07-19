package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// Notifier posts a new-lead card to a sales channel and later edits it in
// response to manager actions (Claim / Spam). Implementations must be safe
// to call concurrently.
type Notifier interface {
	Name() string
	Notify(ctx context.Context, l models.Lead, q models.Qualification, dealID string) error
}

// Card is the channel-independent view of the notification. Telegram and Slack
// adapters render it into their own message format.
type Card struct {
	Emoji  string
	Title  string
	Lines  []string
	DealID string
	LeadID string
}

func BuildCard(l models.Lead, q models.Qualification, dealID string) Card {
	return Card{
		Emoji:  emojiFor(q.Category),
		Title:  fmt.Sprintf("%s new lead: %s (%s)", strings.Title(string(q.Category)), l.Name, l.Nationality),
		DealID: dealID,
		LeadID: l.ID,
		Lines: []string{
			"Service: " + string(l.Service),
			"Move date: " + defaultIfEmpty(l.MoveDate, "not set"),
			"Family size: " + itoa(l.FamilySize),
			"Source: " + string(l.Source),
			"Summary: " + q.Summary,
			fmt.Sprintf("Estimate: €%d – €%d", q.Estimate.Min, q.Estimate.Max),
		},
	}
}

// PlainText renders the card as the fallback stdout notification used in demo
// mode when no real notifier is configured.
func (c Card) PlainText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", c.Emoji, c.Title)
	for _, l := range c.Lines {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	fmt.Fprintf(&b, "  Lead: %s  Deal: %s\n", c.LeadID, c.DealID)
	fmt.Fprintf(&b, "  [ ✅ Claim ]   [ 🗑 Mark spam ]   [ 💬 Contact ]\n")
	return b.String()
}

func emojiFor(c models.Category) string {
	switch c {
	case models.CategoryHot:
		return "🔥"
	case models.CategoryWarm:
		return "👍"
	case models.CategoryCold:
		return "❄️"
	case models.CategorySpam:
		return "🗑"
	}
	return "•"
}

func defaultIfEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
