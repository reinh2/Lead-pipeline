package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

type recordingSubmitter struct{ leads []models.Lead }

func (s *recordingSubmitter) Accept(_ context.Context, lead models.Lead) error {
	s.leads = append(s.leads, lead)
	return nil
}

func baseLead() models.Lead {
	return models.Lead{
		Name: "Alex", Email: "alex@example.com", Nationality: "Canadian",
		Service: models.ServiceFullRelocation, MoveDate: time.Now().UTC().AddDate(1, 0, 0).Format(time.DateOnly), FamilySize: 2, GDPRConsent: true,
	}
}

func TestValidate_OK(t *testing.T) {
	if err := ValidateWebLead(baseLead()); err != nil {
		t.Fatalf("want ok, got %v", err)
	}
}

func TestValidate_RejectsWithoutConsent(t *testing.T) {
	l := baseLead()
	l.GDPRConsent = false
	err := ValidateWebLead(l)
	if err == nil || !strings.Contains(err.Error(), "GDPR") {
		t.Fatalf("want GDPR error, got %v", err)
	}
}

func TestValidate_BadEmail(t *testing.T) {
	l := baseLead()
	l.Email = "not-an-email"
	if err := ValidateWebLead(l); err == nil {
		t.Fatal("want email error")
	}
}

func TestValidate_TelegramContactWithoutEmail(t *testing.T) {
	l := baseLead()
	l.Email = ""
	l.TelegramUsername = "relocating_alex"
	if err := ValidateWebLead(l); err != nil {
		t.Fatalf("Telegram contact should be accepted: %v", err)
	}
	l.TelegramUsername = "not valid"
	if err := ValidateWebLead(l); err == nil {
		t.Fatal("invalid Telegram username should be rejected")
	}
}

func TestValidate_BadService(t *testing.T) {
	l := baseLead()
	l.Service = "chauffeur"
	if err := ValidateWebLead(l); err == nil {
		t.Fatal("want service error")
	}
}

func TestValidate_FamilySize(t *testing.T) {
	l := baseLead()
	l.FamilySize = 0
	if err := ValidateWebLead(l); err == nil {
		t.Fatal("want family_size error")
	}
}

func TestValidate_MoveDate(t *testing.T) {
	for name, moveDate := range map[string]string{
		"missing":   "",
		"malformed": "2007-26-10",
		"past":      "2020-01-01",
	} {
		t.Run(name, func(t *testing.T) {
			l := baseLead()
			l.MoveDate = moveDate
			if err := ValidateWebLead(l); err == nil {
				t.Fatalf("move_date %q should be rejected", moveDate)
			}
		})
	}
}

func TestRoutesRejectUnknownJSONAndOversizedFields(t *testing.T) {
	sub := &recordingSubmitter{}
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), sub, []string{"https://example.test"})
	for name, body := range map[string]string{
		"unknown": `{"name":"Alex","email":"alex@example.com","nationality":"Canadian","service":"full_relocation","family_size":1,"gdpr_consent":true,"unexpected":true}`,
		"long":    `{"name":"` + strings.Repeat("x", 121) + `","email":"alex@example.com","nationality":"Canadian","service":"full_relocation","family_size":1,"gdpr_consent":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/leads", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			srv.Routes().ServeHTTP(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", res.Code, res.Body.String())
			}
		})
	}
	if len(sub.leads) != 0 {
		t.Fatal("invalid requests must not be submitted")
	}
}

func TestRoutesRequireJSONContentTypeAndAllowConfiguredOrigin(t *testing.T) {
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), &recordingSubmitter{}, []string{"https://example.test"})
	req := httptest.NewRequest(http.MethodPost, "/api/leads", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://example.test")
	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", res.Code)
	}
	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "https://example.test" {
		t.Fatalf("CORS origin = %q", got)
	}
}

func TestClientIPOnlyTrustsConfiguredProxyAndRateLimiterCleansKeys(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodPost, "/api/leads", nil)
	req.RemoteAddr = "198.51.100.3:1234"
	req.Header.Set("X-Real-IP", "203.0.113.9")
	if got := clientIP(req, []*net.IPNet{trusted}); got != "198.51.100.3" {
		t.Fatalf("untrusted peer IP = %q", got)
	}
	req.RemoteAddr = "10.1.2.3:1234"
	if got := clientIP(req, []*net.IPNet{trusted}); got != "203.0.113.9" {
		t.Fatalf("trusted proxy IP = %q", got)
	}
	rl := newRateLimiter(1, time.Minute)
	rl.maxKeys, rl.idleTTL = 2, time.Nanosecond
	if !rl.allow("one") || !rl.allow("two") {
		t.Fatal("initial keys should be allowed")
	}
	time.Sleep(time.Millisecond)
	rl.calls = 63
	if !rl.allow("three") {
		t.Fatal("expired limiter keys should be cleaned")
	}
}

func TestReadinessAndMetricsEndpoints(t *testing.T) {
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), &recordingSubmitter{}, nil)
	srv.Ready = func(context.Context) error { return errors.New("disk unavailable") }
	srv.Metrics = func(w io.Writer) { _, _ = io.WriteString(w, "demo_metric 1\n") }

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d, want 503", res.Code)
	}

	srv.Ready = func(context.Context) error { return nil }
	res = httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"ok":true`) {
		t.Fatalf("ready response = %d %s", res.Code, res.Body.String())
	}

	res = httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") == "" || res.Body.String() != "demo_metric 1\n" {
		t.Fatalf("metrics response = %d, %q, %q", res.Code, res.Header().Get("Content-Type"), res.Body.String())
	}
}

func TestPublicConfigUsesValidatedTelegramUsername(t *testing.T) {
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), &recordingSubmitter{}, nil)
	srv.TelegramBotUsername = "@anker_relocation_bot"
	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/public-config", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "https://t.me/anker_relocation_bot") {
		t.Fatalf("config response = %d %s", res.Code, res.Body.String())
	}

	srv.TelegramBotUsername = "https://evil.example"
	res = httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/public-config", nil))
	if strings.Contains(res.Body.String(), "evil") {
		t.Fatalf("invalid username escaped config validation: %s", res.Body.String())
	}
}
