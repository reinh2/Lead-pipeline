package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// Submitter is what the API needs from the pipeline: enqueue and CSV-persist
// a lead so it is never lost even if the queue is full.
type Submitter interface {
	Accept(ctx context.Context, lead models.Lead) error
}

// ErrUnavailable means the lead was durably recorded but cannot be scheduled
// for immediate processing because the bounded demo queue is unavailable.
var ErrUnavailable = errors.New("lead processing temporarily unavailable")

type Server struct {
	Log            *slog.Logger
	Sub            Submitter
	Origins        []string
	Limiter        *rateLimiter
	TrustedProxies []*net.IPNet
	// Ready is optional. When supplied it verifies dependencies needed before
	// this instance may accept traffic.
	Ready func(context.Context) error
	// Metrics writes Prometheus text metrics. It is a callback so the API
	// package stays independent from the application metrics package.
	Metrics func(io.Writer)
	// TelegramBotUsername is an optional public bot username used only to
	// provide the landing page's runtime Telegram link.
	TelegramBotUsername string
}

func New(log *slog.Logger, sub Submitter, origins []string) *Server {
	return &Server{
		Log:     log,
		Sub:     sub,
		Origins: origins,
		Limiter: newRateLimiter(10, time.Minute),
	}
}

// NewWithTrustedProxies accepts forwarded client IP headers only when the
// direct peer belongs to one of the explicitly configured reverse-proxy CIDRs.
func NewWithTrustedProxies(log *slog.Logger, sub Submitter, origins []string, trustedProxies []*net.IPNet) *Server {
	s := New(log, sub, origins)
	s.TrustedProxies = trustedProxies
	return s
}

func ParseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	proxies := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		_, network, err := net.ParseCIDR(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("trusted proxy CIDR %q: %w", part, err)
		}
		proxies = append(proxies, network)
	}
	return proxies, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/api/public-config", s.handlePublicConfig)
	mux.HandleFunc("/api/leads", s.handleLead)
	return s.withCORS(mux)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.Ready != nil {
		if err := s.Ready(r.Context()); err != nil {
			s.Log.Warn("readiness check failed", "err", err)
			writeErr(w, http.StatusServiceUnavailable, "service is not ready")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	if s.Metrics == nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.Metrics(w)
}

func (s *Server) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	username := strings.TrimPrefix(strings.TrimSpace(s.TelegramBotUsername), "@")
	telegramURL := ""
	if !telegramUsernameRE.MatchString(username) {
		username = ""
	} else {
		telegramURL = "https://t.me/" + username
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"telegram_url": telegramURL})
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
	ip := clientIP(r, s.TrustedProxies)
	if !s.Limiter.allow(ip) {
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
		return
	}
	var body struct {
		Name             string `json:"name"`
		Email            string `json:"email"`
		TelegramUsername string `json:"telegram_username"`
		Nationality      string `json:"nationality"`
		CurrentCountry   string `json:"current_country"`
		Service          string `json:"service"`
		MoveDate         string `json:"move_date"`
		FamilySize       int    `json:"family_size"`
		Message          string `json:"message"`
		GDPRConsent      bool   `json:"gdpr_consent"`
	}
	if contentType := r.Header.Get("Content-Type"); !isJSONContentType(contentType) {
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	now := time.Now().UTC()
	lead := models.Lead{
		Name:             strings.TrimSpace(body.Name),
		Email:            strings.TrimSpace(strings.ToLower(body.Email)),
		TelegramUsername: strings.TrimPrefix(strings.TrimSpace(body.TelegramUsername), "@"),
		Nationality:      strings.TrimSpace(body.Nationality),
		CurrentCountry:   strings.TrimSpace(body.CurrentCountry),
		Service:          models.Service(body.Service),
		MoveDate:         strings.TrimSpace(body.MoveDate),
		FamilySize:       body.FamilySize,
		Message:          strings.TrimSpace(body.Message),
		GDPRConsent:      body.GDPRConsent,
		Source:           models.SourceWeb,
		CreatedAt:        now,
		ID:               NewID(),
		ConsentVersion:   ConsentVersion,
		ConsentedAt:      now,
	}
	if err := ValidateWebLead(lead); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Sub.Accept(r.Context(), lead); err != nil {
		s.Log.Error("submit failed", "lead_id", lead.ID, "err", err)
		if errors.Is(err, ErrUnavailable) {
			writeErr(w, http.StatusServiceUnavailable, "lead processing temporarily unavailable")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "lead_id": lead.ID})
}

// -- validation ---------------------------------------------------------------

var emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var telegramUsernameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)

const ConsentVersion = "2026-07-19"

func ValidateWebLead(l models.Lead) error {
	if !l.GDPRConsent {
		return errors.New("GDPR consent is required")
	}
	if l.Name == "" {
		return errors.New("name is required")
	}
	if err := maxRunes("name", l.Name, 120); err != nil {
		return err
	}
	if err := maxRunes("email", l.Email, 254); err != nil {
		return err
	}
	if err := maxRunes("telegram_username", l.TelegramUsername, 32); err != nil {
		return err
	}
	if err := maxRunes("nationality", l.Nationality, 80); err != nil {
		return err
	}
	if err := maxRunes("current_country", l.CurrentCountry, 80); err != nil {
		return err
	}
	if err := maxRunes("move_date", l.MoveDate, 32); err != nil {
		return err
	}
	moveDate, err := time.Parse(time.DateOnly, l.MoveDate)
	if err != nil {
		return errors.New("move_date must be a valid date in YYYY-MM-DD format")
	}
	if moveDate.Before(time.Now().UTC().Truncate(24 * time.Hour)) {
		return errors.New("move_date must not be in the past")
	}
	if err := maxRunes("message", l.Message, 4000); err != nil {
		return err
	}
	if l.Email == "" && l.TelegramUsername == "" {
		return errors.New("email or Telegram username is required")
	}
	if l.Email != "" && !emailRE.MatchString(l.Email) {
		return errors.New("valid email is required")
	}
	if l.TelegramUsername != "" && !telegramUsernameRE.MatchString(l.TelegramUsername) {
		return errors.New("valid Telegram username is required")
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

func maxRunes(field, value string, max int) error {
	if utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s must be at most %d characters", field, max)
	}
	return nil
}

// -- helpers ------------------------------------------------------------------

func (s *Server) withCORS(h http.Handler) http.Handler {
	allowAll := false
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

func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

func clientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer != nil && isTrustedProxy(peer, trustedProxies) {
		if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); forwarded != nil {
			return forwarded.String()
		}
	}
	if peer != nil {
		return peer.String()
	}
	return host
}

func isTrustedProxy(ip net.IP, proxies []*net.IPNet) bool {
	for _, proxy := range proxies {
		if proxy.Contains(ip) {
			return true
		}
	}
	return false
}

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ld_" + hex.EncodeToString(b)
}

// -- rate limiter -------------------------------------------------------------

type rateLimiter struct {
	max     int
	window  time.Duration
	mu      sync.Mutex
	hits    map[string]rateBucket
	maxKeys int
	idleTTL time.Duration
	calls   uint64
}

type rateBucket struct {
	hits    []time.Time
	touched time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, hits: map[string]rateBucket{}, maxKeys: 10000, idleTTL: 10 * time.Minute}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	rl.calls++
	if rl.calls%64 == 0 {
		rl.cleanup(now)
	}
	bucket, exists := rl.hits[key]
	if !exists && len(rl.hits) >= rl.maxKeys {
		return false
	}
	cutoff := now.Add(-rl.window)
	fresh := bucket.hits[:0]
	for _, t := range bucket.hits {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= rl.max {
		rl.hits[key] = rateBucket{hits: fresh, touched: now}
		return false
	}
	rl.hits[key] = rateBucket{hits: append(fresh, now), touched: now}
	return true
}

func (rl *rateLimiter) cleanup(now time.Time) {
	for key, bucket := range rl.hits {
		if now.Sub(bucket.touched) >= rl.idleTTL {
			delete(rl.hits, key)
		}
	}
}
