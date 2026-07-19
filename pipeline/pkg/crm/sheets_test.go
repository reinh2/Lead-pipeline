package crm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

func TestSheetsUpsertAndStatusUpdatesUseOneCRMRow(t *testing.T) {
	rows := [][]string{append([]string(nil), sheetsHeader...)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/values/CRM!1:1"):
			_ = json.NewEncoder(w).Encode(map[string]any{"values": [][]string{rows[0]}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/values/CRM!A:W"):
			_ = json.NewEncoder(w).Encode(map[string]any{"values": rows})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":append"):
			rows = append(rows, requestRow(t, r))
			_ = json.NewEncoder(w).Encode(map[string]any{"updates": map[string]any{}})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/values/CRM!A2:W2"):
			rows[1] = requestRow(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{"updatedCells": len(rows[1])})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Sheets{
		spreadsheetID: "sheet", tab: "CRM", apiBaseURL: server.URL, http: server.Client(),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), token: "token", tokenExp: time.Now().Add(time.Hour),
	}
	lead := models.Lead{ID: "ld-1", Name: "Alex", Service: models.ServiceFullRelocation, FamilySize: 1}
	if id, err := s.Upsert(context.Background(), lead, models.Qualification{Category: models.CategoryWarm}); err != nil || id != lead.ID {
		t.Fatalf("upsert = (%q, %v)", id, err)
	}
	if id, err := s.Upsert(context.Background(), lead, models.Qualification{Category: models.CategoryHot, Urgency: 5}); err != nil || id != lead.ID {
		t.Fatalf("repeat upsert = (%q, %v)", id, err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want header plus one CRM row", len(rows))
	}
	if got := rows[1][16]; got != string(models.CategoryHot) {
		t.Fatalf("category = %q, want %q after upsert", got, models.CategoryHot)
	}
	if err := s.MarkClaimed(context.Background(), lead.ID, "@manager"); err != nil {
		t.Fatalf("mark claimed: %v", err)
	}
	if got := rows[1][sheetsStatusColumn]; got != crmStatusClaimed {
		t.Fatalf("status = %q, want %q", got, crmStatusClaimed)
	}
	if got := rows[1][sheetsAssigneeColumn]; got != "@manager" {
		t.Fatalf("assigned_to = %q, want @manager", got)
	}
	if err := s.MarkSpam(context.Background(), lead.ID); err != nil {
		t.Fatalf("mark spam: %v", err)
	}
	if got := rows[1][sheetsStatusColumn]; got != crmStatusSpam {
		t.Fatalf("status = %q, want %q", got, crmStatusSpam)
	}
	if got := rows[1][sheetsAssigneeColumn]; got != "" {
		t.Fatalf("assigned_to = %q, want empty after spam", got)
	}
}

func TestSheetsInitializesBlankCRMTab(t *testing.T) {
	var wroteHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/values/CRM!1:1"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/values/CRM!A1:W1"):
			wroteHeader = true
			_ = json.NewEncoder(w).Encode(map[string]any{"updatedCells": len(sheetsHeader)})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/values/CRM!A:W"):
			_ = json.NewEncoder(w).Encode(map[string]any{"values": [][]string{stringsToRow(sheetsHeader)}})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":append"):
			_ = json.NewEncoder(w).Encode(map[string]any{"updates": map[string]any{}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	s := &Sheets{spreadsheetID: "sheet", tab: "CRM", apiBaseURL: server.URL, http: server.Client(), log: slog.New(slog.NewTextHandler(io.Discard, nil)), token: "token", tokenExp: time.Now().Add(time.Hour)}
	if _, err := s.Upsert(context.Background(), models.Lead{ID: "ld-blank"}, models.Qualification{}); err != nil {
		t.Fatalf("upsert to blank tab: %v", err)
	}
	if !wroteHeader {
		t.Fatal("expected the CRM header to be initialized")
	}
}

func requestRow(t *testing.T, r *http.Request) []string {
	t.Helper()
	var body struct {
		Values [][]any `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(body.Values) != 1 {
		t.Fatalf("values = %#v, want one row", body.Values)
	}
	row := make([]string, len(body.Values[0]))
	for i, value := range body.Values[0] {
		row[i] = stringify(value)
	}
	return row
}

func stringify(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", v), "0"), ".")
	default:
		return fmt.Sprint(v)
	}
}

func stringsToRow(values []string) []string { return append([]string(nil), values...) }
