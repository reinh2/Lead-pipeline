package crm

import (
	"context"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// CRM is the sink for qualified leads. All implementations must be idempotent
// on lead.ID (or on email, if the backend dedups by contact).
type CRM interface {
	Name() string
	// Upsert stores/updates the lead + qualification. Returns a backend-specific
	// identifier the notify layer can echo back in the manager card. On any
	// error, callers should log and continue — the CSV writer already captured
	// the raw lead before this call.
	Upsert(ctx context.Context, l models.Lead, q models.Qualification) (id string, err error)
	// MarkClaimed updates the deal/row when a manager clicks "Claim lead".
	MarkClaimed(ctx context.Context, id, manager string) error
	// MarkSpam updates the deal/row when a manager clicks "Mark spam".
	MarkSpam(ctx context.Context, id string) error
}
