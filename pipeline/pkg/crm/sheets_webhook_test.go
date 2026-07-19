package crm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anker-relocation/pipeline/pkg/models"
)

func TestSheetsWebhookSendsCRMOperations(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, request)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	s := &SheetsWebhook{webhookURL: server.URL, secret: "test-secret", http: server.Client(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	lead := models.Lead{ID: "ld-webhook", Name: "Alex", Service: models.ServiceFullRelocation}
	if id, err := s.Upsert(context.Background(), lead, models.Qualification{Category: models.CategoryHot}); err != nil || id != lead.ID {
		t.Fatalf("upsert = (%q, %v)", id, err)
	}
	if err := s.MarkClaimed(context.Background(), lead.ID, "@manager"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.MarkSpam(context.Background(), lead.ID); err != nil {
		t.Fatalf("spam: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	if got := requests[0]["action"]; got != "upsert" {
		t.Fatalf("first action = %v", got)
	}
	if got := requests[0]["secret"]; got != "test-secret" {
		t.Fatalf("secret = %v", got)
	}
	if got := requests[1]["manager"]; got != "@manager" {
		t.Fatalf("manager = %v", got)
	}
	if got := requests[2]["action"]; got != "spam" {
		t.Fatalf("third action = %v", got)
	}
}

func TestNewSheetsWebhookRejectsUnsafeConfig(t *testing.T) {
	if _, err := NewSheetsWebhook("http://example.test", "secret", slog.Default()); err == nil {
		t.Fatal("expected http URL to be rejected")
	}
	if _, err := NewSheetsWebhook("https://example.test", "", slog.Default()); err == nil {
		t.Fatal("expected empty secret to be rejected")
	}
}
