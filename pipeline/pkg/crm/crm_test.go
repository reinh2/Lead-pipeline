package crm

import (
	"context"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// mockCRM verifies the interface can be satisfied by a simple in-memory type.
type mockCRM struct {
	upserts, claims, spams int
}

func (*mockCRM) Name() string { return "mock" }
func (m *mockCRM) Upsert(context.Context, models.Lead, models.Qualification) (string, error) {
	m.upserts++
	return "dl_1", nil
}
func (m *mockCRM) MarkClaimed(context.Context, string, string) error { m.claims++; return nil }
func (m *mockCRM) MarkSpam(context.Context, string) error            { m.spams++; return nil }

func TestInterface_MockSatisfies(t *testing.T) {
	var c CRM = &mockCRM{}
	if _, err := c.Upsert(context.Background(), models.Lead{}, models.Qualification{}); err != nil {
		t.Fatal(err)
	}
}

func TestCSV_WritesRowsAndHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leads.csv")
	c, err := NewCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	l := models.Lead{ID: "ld_a", Name: "Alex", Email: "a@b.c", Service: models.ServiceFullRelocation, FamilySize: 1, GDPRConsent: true}
	if err := c.AppendRaw(l); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(context.Background(), l, models.Qualification{Category: models.CategoryHot, Urgency: 5, Estimate: models.EstimateEUR{Min: 100, Max: 200}, Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkClaimed(context.Background(), l.ID, "sam"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.Contains(got, "lead_id,created_at,source,name") {
		t.Fatalf("missing header: %s", got)
	}
	if !strings.Contains(got, "ld_a") || !strings.Contains(got, "hot") || !strings.Contains(got, "claimed_by:sam") {
		t.Fatalf("missing rows: %s", got)
	}
	// A status change after restart must retain the full lead snapshot.
	c, err = NewCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MarkSpam(context.Background(), l.ID); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	for _, row := range rows[1:] {
		if len(row) == 0 || row[0] != l.ID || row[3] != l.Name || row[4] != l.Email {
			t.Fatalf("status event lost lead data: %v", row)
		}
	}
}

func TestCSV_RecordTaskFailureKeepsFullSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leads.csv")
	c, err := NewCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	l := models.Lead{ID: "ld_dead", Name: "Alex", Email: "a@b.c", Service: models.ServiceFullRelocation}
	q := models.Qualification{Category: models.CategoryWarm, Urgency: 3, Summary: "Needs a callback"}
	if err := c.RecordTaskFailure(context.Background(), l, q, "notify:telegram"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "dead_letter:notify:telegram") || !strings.Contains(string(data), "Needs a callback") {
		t.Fatalf("dead-letter event is incomplete: %s", data)
	}
}

func TestCSV_MigratesLegacySchemaWithoutLosingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leads.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := csv.NewWriter(f)
	if err := w.Write(legacyHeaderV1); err != nil {
		t.Fatal(err)
	}
	// This reproduces the previously released file: the rows already contain
	// consent metadata, while the header still has the v1 shape.
	row := []string{
		"ld_legacy", "2026-07-18T23:42:56Z", "web", "Integration Test", "integration@example.test", "Canadian", "Canada",
		"full_relocation", "2026-10-01", "2", "", "Automated local integration check.", "true", "2026-07-19", "2026-07-18T23:42:56Z",
		"warm", "3", "2500", "6000", "Actively planning a move.", "warm",
	}
	if err := w.Write(row); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	c, err := NewCSV(path)
	if err != nil {
		t.Fatalf("NewCSV should migrate the legacy log: %v", err)
	}
	if err := c.MarkClaimed(context.Background(), "ld_legacy", "sam"); err != nil {
		t.Fatalf("migrated log must remain usable: %v", err)
	}

	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !sameFields(rows[0], header) {
		t.Fatalf("header = %v, want current schema %v", rows[0], header)
	}
	if len(rows) != 3 || rows[1][0] != "ld_legacy" || rows[1][13] != "2026-07-19" || rows[2][20] != "claimed_by:sam" {
		t.Fatalf("migration lost or changed event data: %v", rows)
	}
	backups, err := filepath.Glob(path + ".pre-consent-migration-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("legacy CSV backup = %v, %v; want exactly one backup", backups, err)
	}
}

func TestCSV_MigratesRowsThatPrecedeConsentMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leads.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := csv.NewWriter(f)
	_ = w.Write(legacyHeaderV1)
	_ = w.Write([]string{
		"ld_old", "2026-07-18T23:42:56Z", "web", "Old Lead", "old@example.test", "Canadian", "Canada",
		"full_relocation", "", "1", "", "", "true", "warm", "3", "2500", "6000", "Needs review.", "warm",
	})
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := NewCSV(path); err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][13] != "" || rows[1][14] != "" || rows[1][15] != "warm" {
		t.Fatalf("legacy row was not expanded correctly: %v", rows[1])
	}
}
