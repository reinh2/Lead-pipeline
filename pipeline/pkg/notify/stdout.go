package notify

import (
	"context"
	"log/slog"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// Stdout is the demo-mode notifier — prints the card to the process log so
// you can see the whole pipeline work with zero credentials.
type Stdout struct {
	Log *slog.Logger
}

func (Stdout) Name() string { return "stdout" }

func (s Stdout) Notify(_ context.Context, l models.Lead, q models.Qualification, dealID string) error {
	// Avoid copying contact details and free-form text to process logs. The CSV
	// event log and configured manager channel retain the details when needed.
	if s.Log != nil {
		s.Log.Info("manager notification", "lead_id", l.ID, "deal_id", dealID, "category", q.Category)
	}
	return nil
}
