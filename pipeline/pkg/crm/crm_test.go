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
