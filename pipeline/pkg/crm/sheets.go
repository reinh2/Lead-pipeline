package crm

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"crypto"
	"crypto/rand"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// Sheets is a thin Google Sheets adapter using a service account JSON key.
// Signs a JWT locally (no external OAuth library), exchanges it for a bearer
// token, then appends rows via the Sheets v4 API. Not fancy, but enough to
// be the "second-choice CRM" in front of the CSV fallback.
type Sheets struct {
	spreadsheetID string
	tab           string
	sa            saKey
	http          *http.Client
	log           *slog.Logger

	tokMu     sync.Mutex
	token     string
	tokenExp  time.Time
}

type saKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func NewSheets(spreadsheetID, tab, saPath string, log *slog.Logger) (*Sheets, error) {
	raw, err := os.ReadFile(saPath)
	if err != nil {
		return nil, fmt.Errorf("read sheets SA key: %w", err)
	}
	var k saKey
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, fmt.Errorf("parse sheets SA key: %w", err)
	}
	if k.TokenURI == "" {
		k.TokenURI = "https://oauth2.googleapis.com/token"
	}
	if tab == "" {
		tab = "Leads"
	}
	return &Sheets{
		spreadsheetID: spreadsheetID,
		tab:           tab,
		sa:            k,
		http:          &http.Client{Timeout: 10 * time.Second},
		log:           log,
	}, nil
}

func (s *Sheets) Name() string { return "google_sheets" }

func (s *Sheets) Upsert(ctx context.Context, l models.Lead, q models.Qualification) (string, error) {
	tok, err := s.getToken(ctx)
	if err != nil {
		return "", err
	}
	row := []any{
		l.ID, l.CreatedAt.Format(time.RFC3339), string(l.Source),
		l.Name, l.Email, l.Nationality, l.CurrentCountry,
		string(l.Service), l.MoveDate, l.FamilySize, l.JobOffer, l.Message,
		l.GDPRConsent, string(q.Category), q.Urgency,
		q.Estimate.Min, q.Estimate.Max, q.Summary, "new",
	}
	body := map[string]any{"values": [][]any{row}}
	u := fmt.Sprintf(
		"https://sheets.googleapis.com/v4/spreadsheets/%s/values/%s:append?valueInputOption=RAW",
		url.PathEscape(s.spreadsheetID), url.PathEscape(s.tab),
	)
	if _, err := s.doJSON(ctx, http.MethodPost, u, tok, body); err != nil {
		return "", err
	}
	return l.ID, nil
}

// MarkClaimed / MarkSpam are best-effort no-ops for Sheets — the CSV mirror
// already tracks status changes and the sheet is a read/search-friendly copy,
// not a source of truth for state transitions.
func (s *Sheets) MarkClaimed(_ context.Context, id, manager string) error {
	s.log.Info("sheets mark claimed (no-op)", "id", id, "manager", manager)
	return nil
}
func (s *Sheets) MarkSpam(_ context.Context, id string) error {
	s.log.Info("sheets mark spam (no-op)", "id", id)
	return nil
}

func (s *Sheets) doJSON(ctx context.Context, method, u, tok string, body any) ([]byte, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sheets HTTP %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func (s *Sheets) getToken(ctx context.Context) (string, error) {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	if time.Now().Before(s.tokenExp.Add(-30 * time.Second)) && s.token != "" {
		return s.token, nil
	}
	jwt, err := s.signJWT()
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", jwt)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.sa.TokenURI, bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("google token HTTP %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	s.token = out.AccessToken
	s.tokenExp = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return s.token, nil
}

func (s *Sheets) signJWT() (string, error) {
	header := base64URL([]byte(`{"alg":"RS256","typ":"JWT"}`))
	now := time.Now().Unix()
	claim := map[string]any{
		"iss":   s.sa.ClientEmail,
		"scope": "https://www.googleapis.com/auth/spreadsheets",
		"aud":   s.sa.TokenURI,
		"exp":   now + 3600,
		"iat":   now,
	}
	cj, _ := json.Marshal(claim)
	payload := base64URL(cj)
	signingInput := header + "." + payload

	block, _ := pem.Decode([]byte(s.sa.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("sheets: invalid PEM private key")
	}
	var priv *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		priv = k
	} else if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		var ok bool
		priv, ok = k.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("sheets: PKCS8 key is not RSA")
		}
	} else {
		return "", fmt.Errorf("sheets: unsupported key format")
	}
	h := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

var _ = strconv.Itoa // keep import stable across builds
