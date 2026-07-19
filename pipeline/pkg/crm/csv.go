package crm

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// CSV is the never-fail fallback sink. Every accepted lead lands here first,
// so if HubSpot or Sheets throws we still have the record on disk.
type CSV struct {
	path   string
	mu     sync.Mutex
	latest map[string]csvRecord
}

type csvRecord struct {
	lead   models.Lead
	qual   models.Qualification
	status string
}

func NewCSV(path string) (*CSV, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	c := &CSV{path: path, latest: make(map[string]csvRecord)}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := c.writeHeader(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if err := c.loadLatest(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *CSV) Name() string { return "csv" }

// Check verifies that the fallback event log remains accessible. It is used by
// readiness rather than health so a live process with an unavailable disk is
// removed from load balancing.
func (c *CSV) Check() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open csv fallback: %w", err)
	}
	return f.Close()
}

var header = []string{
	"lead_id", "created_at", "source", "name", "email", "nationality", "current_country",
	"service", "move_date", "family_size", "job_offer", "message", "gdpr_consent", "consent_version", "consented_at",
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
	if err := w.Error(); err != nil {
		return err
	}
	return f.Sync()
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

// RecordTaskFailure adds an actionable dead-letter event while keeping the
// complete lead and qualification snapshot available for manual replay.
func (c *CSV) RecordTaskFailure(_ context.Context, l models.Lead, q models.Qualification, task string) error {
	return c.append(l, q, "dead_letter:"+task)
}

func (c *CSV) MarkClaimed(_ context.Context, id, manager string) error {
	return c.appendStatus(id, "claimed_by:"+manager)
}
func (c *CSV) MarkSpam(_ context.Context, id string) error {
	return c.appendStatus(id, "spam")
}

func (c *CSV) appendStatus(id, status string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, ok := c.latest[id]
	if !ok {
		return fmt.Errorf("csv status update: lead %q not found", id)
	}
	return c.appendLocked(record.lead, record.qual, status)
}

func (c *CSV) append(l models.Lead, q models.Qualification, status string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appendLocked(l, q, status)
}

// appendLocked persists a complete snapshot for every status event. This is an
// append-only event log: no event is represented by an otherwise empty row.
func (c *CSV) appendLocked(l models.Lead, q models.Qualification, status string) error {
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
		strconv.FormatBool(l.GDPRConsent), l.ConsentVersion, l.ConsentedAt.Format(time.RFC3339),
		string(q.Category), strconv.Itoa(q.Urgency),
		strconv.Itoa(q.Estimate.Min), strconv.Itoa(q.Estimate.Max),
		q.Summary, status,
	}
	if err := w.Write(row); err != nil {
		return fmt.Errorf("csv write: %w", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("csv sync: %w", err)
	}
	c.latest[l.ID] = csvRecord{lead: l, qual: q, status: status}
	return nil
}

// loadLatest rebuilds the latest full snapshot for each lead so status changes
// after a restart keep their original data instead of appending empty rows.
func (c *CSV) loadLatest() error {
	f, err := os.Open(c.path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	head, err := r.Read()
	if err == io.EOF {
		return c.writeHeader()
	}
	if err != nil {
		return fmt.Errorf("read csv header: %w", err)
	}
	index := make(map[string]int, len(head))
	for i, name := range head {
		index[name] = i
	}
	if _, ok := index["lead_id"]; !ok {
		return fmt.Errorf("csv header is missing lead_id")
	}
	for {
		row, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read csv row: %w", err)
		}
		get := func(name string) string {
			i, ok := index[name]
			if !ok || i >= len(row) {
				return ""
			}
			return row[i]
		}
		id := get("lead_id")
		if id == "" {
			continue
		}
		createdAt, _ := time.Parse(time.RFC3339, get("created_at"))
		familySize, _ := strconv.Atoi(get("family_size"))
		consent, _ := strconv.ParseBool(get("gdpr_consent"))
		consentedAt, _ := time.Parse(time.RFC3339, get("consented_at"))
		urgency, _ := strconv.Atoi(get("urgency"))
		estimateMin, _ := strconv.Atoi(get("estimate_min_eur"))
		estimateMax, _ := strconv.Atoi(get("estimate_max_eur"))
		c.latest[id] = csvRecord{
			lead: models.Lead{
				ID: id, CreatedAt: createdAt, Source: models.Source(get("source")), Name: get("name"), Email: get("email"),
				Nationality: get("nationality"), CurrentCountry: get("current_country"), Service: models.Service(get("service")),
				MoveDate: get("move_date"), FamilySize: familySize, JobOffer: get("job_offer"), Message: get("message"), GDPRConsent: consent,
				ConsentVersion: get("consent_version"), ConsentedAt: consentedAt,
			},
			qual: models.Qualification{
				Category: models.Category(get("category")), Urgency: urgency,
				Estimate: models.EstimateEUR{Min: estimateMin, Max: estimateMax}, Summary: get("summary"),
			},
			status: get("status"),
		}
	}
}
