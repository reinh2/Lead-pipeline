package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// Submitter is what the API needs from the pipeline: enqueue and CSV-persist
// a lead so it is never lost even if the queue is full.
type Submitter interface {
	Accept(ctx context.Context, lead models.Lead) error
}

type Server struct {
	Log      *slog.Logger
	Sub      Submitter
	Origins  []string
	Limiter  *rateLimiter
}

func New(log *slog.Logger, sub Submitter, origins []string) *Server {
	return &Server{
		Log:     log,
		Sub:     sub,
		Origins: origins,
		Limiter: newRateLimiter(10, time.Minute),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/leads", s.handleLead)
	return s.withCORS(mux)
}

func (s *Server) handleLead(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r)
	if !s.Limiter.allow(ip) {
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
		return
	}
	var body struct {
		Name           string `json:"name"`
		Email          string `json:"email"`
		Nationality    string `json:"nationality"`
		CurrentCountry string `json:"current_country"`
		Service        string `json:"service"`
		MoveDate       string `json:"move_date"`
		FamilySize     int    `json:"family_size"`
		Message        string `json:"message"`
		GDPRConsent    bool   `json:"gdpr_consent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	lead := models.Lead{
		Name:           strings.TrimSpace(body.Name),
		Email:          strings.TrimSpace(strings.ToLower(body.Email)),
		Nationality:    strings.TrimSpace(body.Nationality),
		CurrentCountry: strings.TrimSpace(body.CurrentCountry),
		Service:        models.Service(body.Service),
		MoveDate:       body.MoveDate,
		FamilySize:     body.FamilySize,
		Message:        strings.TrimSpace(body.Message),
		GDPRConsent:    body.GDPRConsent,
		Source:         models.SourceWeb,
		CreatedAt:      time.Now().UTC(),
		ID:             NewID(),
	}
	if err := ValidateWebLead(lead); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Sub.Accept(r.Context(), lead); err != nil {
		s.Log.Error("submit failed", "lead_id", lead.ID, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "lead_id": lead.ID})
}

// -- validation ---------------------------------------------------------------

var emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func ValidateWebLead(l models.Lead) error {
	if !l.GDPRConsent {
		return errors.New("GDPR consent is required")
	}
	if l.Name == "" {
		return errors.New("name is required")
	}
	if !emailRE.MatchString(l.Email) {
		return errors.New("valid email is required")
	}
	if l.Nationality == "" {
		return errors.New("nationality is required")
	}
	switch l.Service {
	case models.ServiceVisaOnly, models.ServiceApartmentOnly, models.ServiceFullRelocation:
	default:
		return errors.New("service must be visa_only, apartment_only or full_relocation")
	}
	if l.FamilySize < 1 || l.FamilySize > 20 {
		return errors.New("family_size must be between 1 and 20")
	}
	return nil
}

// -- helpers ------------------------------------------------------------------

func (s *Server) withCORS(h http.Handler) http.Handler {
	allowAll := len(s.Origins) == 0
	for _, o := range s.Origins {
		if o == "*" {
			allowAll = true
		}
	}
	set := map[string]bool{}
	for _, o := range s.Origins {
		set[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowAll {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" && set[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		h.ServeHTTP(w, r)
	})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.Index(v, ","); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	return r.RemoteAddr
}

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ld_" + hex.EncodeToString(b)
}

// -- rate limiter -------------------------------------------------------------

type rateLimiter struct {
	max    int
	window time.Duration
	mu     sync.Mutex
	hits   map[string][]time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	fresh := rl.hits[key][:0]
	for _, t := range rl.hits[key] {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= rl.max {
		rl.hits[key] = fresh
		return false
	}
	rl.hits[key] = append(fresh, now)
	return true
}
