package crm

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// CSV is the never-fail fallback sink. Every accepted lead lands here first,
// so if HubSpot or Sheets throws we still have the record on disk.
type CSV struct {
	path string
	mu   sync.Mutex
}

func NewCSV(path string) (*CSV, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	c := &CSV{path: path}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := c.writeHeader(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *CSV) Name() string { return "csv" }

var header = []string{
	"lead_id", "created_at", "source", "name", "email", "nationality", "current_country",
	"service", "move_date", "family_size", "job_offer", "message", "gdpr_consent",
	"category", "urgency", "estimate_min_eur", "estimate_max_eur", "summary", "status",
}

func (c *CSV) writeHeader() error {
	f, err := os.Create(c.path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.Write(header); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

// AppendRaw writes a lead row before qualification runs — used as the
// unconditional first stop for every accepted lead.
func (c *CSV) AppendRaw(l models.Lead) error {
	return c.append(l, models.Qualification{}, "new")
}

func (c *CSV) Upsert(_ context.Context, l models.Lead, q models.Qualification) (string, error) {
	if err := c.append(l, q, string(q.Category)); err != nil {
		return "", err
	}
	return l.ID, nil
}

func (c *CSV) MarkClaimed(_ context.Context, id, manager string) error {
	return c.appendStatus(id, "claimed_by:"+manager)
}
func (c *CSV) MarkSpam(_ context.Context, id string) error {
	return c.appendStatus(id, "spam")
}

func (c *CSV) appendStatus(id, status string) error {
	return c.append(models.Lead{ID: id}, models.Qualification{}, status)
}

func (c *CSV) append(l models.Lead, q models.Qualification, status string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	row := []string{
		l.ID,
		l.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		string(l.Source),
		l.Name, l.Email, l.Nationality, l.CurrentCountry,
		string(l.Service), l.MoveDate, strconv.Itoa(l.FamilySize), l.JobOffer, l.Message,
		strconv.FormatBool(l.GDPRConsent),
		string(q.Category), strconv.Itoa(q.Urgency),
		strconv.Itoa(q.Estimate.Min), strconv.Itoa(q.Estimate.Max),
		q.Summary, status,
	}
	if err := w.Write(row); err != nil {
		return fmt.Errorf("csv write: %w", err)
	}
	w.Flush()
	return w.Error()
}
