package crm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anker-relocation/pipeline/pkg/models"
)

func TestHubSpotUpsertIsIdempotentAndUsesClaimedBy(t *testing.T) {
	var dealExists bool
	var createDeals, patchDeals int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/crm/v3/objects/contacts/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/crm/v3/objects/contacts":
			_, _ = w.Write([]byte(`{"id":"contact-1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/crm/v3/objects/deals/search":
			if dealExists {
				_, _ = w.Write([]byte(`{"results":[{"id":"deal-1"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"results":[]}`))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/crm/v3/objects/deals":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			props := body["properties"].(map[string]any)
			if props[hubSpotLeadIDProperty] != "ld-1" {
				t.Fatalf("deal missing lead id property: %#v", props)
			}
			createDeals++
			dealExists = true
			_, _ = w.Write([]byte(`{"id":"deal-1"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/crm/v3/objects/deals/deal-1":
			var body map[string]map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			props := body["properties"]
			if _, hasOwner := props["hubspot_owner_id"]; hasOwner {
				t.Fatal("Telegram username must not be sent as hubspot_owner_id")
			}
			if r.URL.Query().Get("claim") == "" && props[hubSpotClaimedByProperty] == nil && props[hubSpotLeadIDProperty] == nil {
				t.Fatalf("unexpected deal update properties: %#v", props)
			}
			patchDeals++
			_, _ = w.Write([]byte(`{"id":"deal-1"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	h := NewHubSpot("token", "pipeline", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.baseURL, h.http = server.URL, server.Client()
	lead := models.Lead{ID: "ld-1", Name: "Alex Chen", Email: "alex@example.com", Nationality: "Canadian", Service: models.ServiceFullRelocation}
	qual := models.Qualification{Category: models.CategoryHot, Estimate: models.EstimateEUR{Min: 100, Max: 200}}
	if id, err := h.Upsert(context.Background(), lead, qual); err != nil || id != "deal-1" {
		t.Fatalf("first upsert = (%q, %v)", id, err)
	}
	if id, err := h.Upsert(context.Background(), lead, qual); err != nil || id != "deal-1" {
		t.Fatalf("second upsert = (%q, %v)", id, err)
	}
	if err := h.MarkClaimed(context.Background(), "deal-1", "sam"); err != nil {
		t.Fatal(err)
	}
	if createDeals != 1 || patchDeals < 2 {
		t.Fatalf("createDeals=%d patchDeals=%d, want 1 create and updates", createDeals, patchDeals)
	}
}

func TestHubSpotHTTPStatusHandlingAndEmptyIDs(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			h := NewHubSpot("token", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
			h.baseURL, h.http = server.URL, server.Client()
			_, err := h.do(context.Background(), http.MethodGet, "/test", nil)
			if status == http.StatusNotFound {
				if !errors.Is(err, errNotFound) {
					t.Fatalf("error = %v, want errNotFound", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "HTTP") {
				t.Fatalf("error = %v, want HTTP status error", err)
			}
		})
	}
	if _, err := objectID([]byte(`{"id":""}`), "test"); err == nil {
		t.Fatal("empty HubSpot id must be rejected")
	}
}
