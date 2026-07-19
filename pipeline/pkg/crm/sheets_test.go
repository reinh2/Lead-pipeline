package crm

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

func TestSheetsUpsertChecksHeaderAndAppends(t *testing.T) {
	var appended bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/values/Leads!1:1"):
			_, _ = w.Write([]byte(`{"values":[["lead_id","created_at","source","name","email","nationality","current_country","service","move_date","family_size","job_offer","message","gdpr_consent","consent_version","consented_at","category","urgency","estimate_min_eur","estimate_max_eur","summary","status"]]}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":append"):
			appended = true
			_, _ = w.Write([]byte(`{"updates":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Sheets{
		spreadsheetID: "sheet", tab: "Leads", apiBaseURL: server.URL, http: server.Client(),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), token: "token", tokenExp: time.Now().Add(time.Hour),
	}
	lead := models.Lead{ID: "ld-1", Name: "Alex", Service: models.ServiceFullRelocation, FamilySize: 1}
	if id, err := s.Upsert(context.Background(), lead, models.Qualification{Category: models.CategoryWarm}); err != nil || id != lead.ID {
		t.Fatalf("upsert = (%q, %v)", id, err)
	}
	if !appended {
		t.Fatal("expected row append after a valid header check")
	}
}
