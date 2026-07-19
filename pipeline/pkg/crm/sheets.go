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
	"strings"
	"sync"
	"time"

	"crypto"
	"crypto/rand"

	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/retry"
)

// Sheets is a lightweight Google Sheets CRM using a service account JSON key.
// It keeps one row per lead and updates its manager status in place. The CSV
// sink remains the durable append-only event log and fallback.
type Sheets struct {
	spreadsheetID string
	tab           string
	apiBaseURL    string
	sa            saKey
	http          *http.Client
	log           *slog.Logger

	tokMu    sync.Mutex
	token    string
	tokenExp time.Time

	headerMu      sync.Mutex
	headerChecked bool
	opMu          sync.Mutex // keeps a find-and-update sequence atomic per process
}

const (
	sheetsDefaultTab = "CRM"
	sheetsLastColumn = "W"

	crmStatusNew     = "new"
	crmStatusClaimed = "claimed"
	crmStatusSpam    = "spam"
)

var sheetsHeader = []string{
	"lead_id", "created_at", "updated_at", "source", "name", "email",
	"nationality", "current_country", "service", "move_date", "family_size",
	"job_offer", "message", "gdpr_consent", "consent_version", "consented_at",
	"category", "urgency", "estimate_min_eur", "estimate_max_eur", "summary",
	"status", "assigned_to",
}

const (
	sheetsStatusColumn   = 21
	sheetsAssigneeColumn = 22
)

type saKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func NewSheets(spreadsheetID, tab, saPath string, log *slog.Logger) (*Sheets, error) {
	if strings.TrimSpace(spreadsheetID) == "" {
		return nil, fmt.Errorf("sheets spreadsheet id is required")
	}
	if strings.TrimSpace(saPath) == "" {
		return nil, fmt.Errorf("sheets service-account JSON path is required")
	}
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
	if k.ClientEmail == "" || k.PrivateKey == "" {
		return nil, fmt.Errorf("sheets service-account JSON must include client_email and private_key")
	}
	if tab == "" {
		tab = sheetsDefaultTab
	}
	return &Sheets{
		spreadsheetID: spreadsheetID,
		tab:           tab,
		apiBaseURL:    "https://sheets.googleapis.com",
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
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := s.ensureHeader(ctx, tok); err != nil {
		return "", err
	}

	status, assignee := crmStatusNew, ""
	if rowNumber, existing, found, err := s.findRow(ctx, tok, l.ID); err != nil {
		return "", err
	} else if found {
		status = sheetCell(existing, sheetsStatusColumn)
		assignee = sheetCell(existing, sheetsAssigneeColumn)
		if status == "" {
			status = crmStatusNew
		}
		if err := s.writeRow(ctx, tok, rowNumber, s.crmRow(l, q, status, assignee)); err != nil {
			return "", err
		}
		return l.ID, nil
	}
	if err := s.appendRow(ctx, tok, s.crmRow(l, q, status, assignee)); err != nil {
		return "", err
	}
	return l.ID, nil
}

func (s *Sheets) MarkClaimed(ctx context.Context, id, manager string) error {
	return s.updateStatus(ctx, id, crmStatusClaimed, manager)
}

func (s *Sheets) ensureHeader(ctx context.Context, tok string) error {
	s.headerMu.Lock()
	defer s.headerMu.Unlock()
	if s.headerChecked {
		return nil
	}
	u := fmt.Sprintf(
		"%s/v4/spreadsheets/%s/values/%s",
		strings.TrimRight(s.apiBaseURL, "/"), url.PathEscape(s.spreadsheetID), url.PathEscape(s.tab+"!1:1"),
	)
	body, err := s.doJSON(ctx, http.MethodGet, u, tok, nil)
	if err != nil {
		return fmt.Errorf("read sheets header: %w", err)
	}
	var out struct {
		Values [][]string `json:"values"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("decode sheets header: %w", err)
	}
	if len(out.Values) == 0 || len(out.Values[0]) == 0 {
		if err := s.writeHeader(ctx, tok); err != nil {
			return fmt.Errorf("initialize sheets CRM header: %w", err)
		}
		s.headerChecked = true
		return nil
	}
	if len(out.Values[0]) != len(sheetsHeader) {
		return fmt.Errorf("sheets tab %q has an incompatible header; create or rename an empty tab to %q for the CRM", s.tab, sheetsDefaultTab)
	}
	for i, want := range sheetsHeader {
		if out.Values[0][i] != want {
			return fmt.Errorf("sheets tab %q has an incompatible header at column %d; create or rename an empty tab to %q for the CRM", s.tab, i+1, sheetsDefaultTab)
		}
	}
	s.headerChecked = true
	return nil
}

func (s *Sheets) MarkSpam(ctx context.Context, id string) error {
	return s.updateStatus(ctx, id, crmStatusSpam, "")
}

func (s *Sheets) updateStatus(ctx context.Context, id, status, manager string) error {
	tok, err := s.getToken(ctx)
	if err != nil {
		return err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := s.ensureHeader(ctx, tok); err != nil {
		return err
	}
	rowNumber, existing, found, err := s.findRow(ctx, tok, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("sheets lead %q not found", id)
	}
	row := make([]any, len(sheetsHeader))
	for i := range row {
		row[i] = sheetCell(existing, i)
	}
	row[2] = time.Now().UTC().Format(time.RFC3339)
	row[sheetsStatusColumn] = status
	if status == crmStatusClaimed {
		row[sheetsAssigneeColumn] = manager
	} else {
		row[sheetsAssigneeColumn] = ""
	}
	if err := s.writeRow(ctx, tok, rowNumber, row); err != nil {
		return err
	}
	s.log.Info("sheets status updated", "id", id, "status", status, "manager", manager)
	return nil
}

func (s *Sheets) crmRow(l models.Lead, q models.Qualification, status, assignee string) []any {
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	return []any{
		l.ID, l.CreatedAt.Format(time.RFC3339), updatedAt, string(l.Source),
		l.Name, l.Email, l.Nationality, l.CurrentCountry,
		string(l.Service), l.MoveDate, l.FamilySize, l.JobOffer, l.Message,
		l.GDPRConsent, l.ConsentVersion, l.ConsentedAt.Format(time.RFC3339),
		string(q.Category), q.Urgency, q.Estimate.Min, q.Estimate.Max, q.Summary,
		status, assignee,
	}
}

func (s *Sheets) findRow(ctx context.Context, tok, id string) (int, []string, bool, error) {
	u := s.valuesURL(s.tab + "!A:" + sheetsLastColumn)
	body, err := s.doJSON(ctx, http.MethodGet, u, tok, nil)
	if err != nil {
		return 0, nil, false, fmt.Errorf("read sheets CRM rows: %w", err)
	}
	var out struct {
		Values [][]string `json:"values"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, nil, false, fmt.Errorf("decode sheets CRM rows: %w", err)
	}
	for i := 1; i < len(out.Values); i++ { // row 1 is the header
		if sheetCell(out.Values[i], 0) == id {
			return i + 1, out.Values[i], true, nil
		}
	}
	return 0, nil, false, nil
}

func (s *Sheets) appendRow(ctx context.Context, tok string, row []any) error {
	u := fmt.Sprintf("%s:append?valueInputOption=RAW", s.valuesURL(s.tab))
	_, err := s.doJSON(ctx, http.MethodPost, u, tok, map[string]any{"values": [][]any{row}})
	return err
}

func (s *Sheets) writeHeader(ctx context.Context, tok string) error {
	row := make([]any, len(sheetsHeader))
	for i, value := range sheetsHeader {
		row[i] = value
	}
	return s.writeRow(ctx, tok, 1, row)
}

func (s *Sheets) writeRow(ctx context.Context, tok string, rowNumber int, row []any) error {
	rangeRef := fmt.Sprintf("%s!A%d:%s%d", s.tab, rowNumber, sheetsLastColumn, rowNumber)
	u := s.valuesURL(rangeRef) + "?valueInputOption=RAW"
	_, err := s.doJSON(ctx, http.MethodPut, u, tok, map[string]any{"values": [][]any{row}})
	return err
}

func (s *Sheets) valuesURL(rangeRef string) string {
	return fmt.Sprintf(
		"%s/v4/spreadsheets/%s/values/%s",
		strings.TrimRight(s.apiBaseURL, "/"), url.PathEscape(s.spreadsheetID), url.PathEscape(rangeRef),
	)
}

func sheetCell(row []string, index int) string {
	if index < len(row) {
		return row[index]
	}
	return ""
}

func (s *Sheets) doJSON(ctx context.Context, method, u, tok string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &retry.HTTPStatusError{Status: resp.StatusCode, Body: truncateResponse(data)}
	}
	return data, nil
}

func (s *Sheets) getToken(ctx context.Context) (string, error) {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	if time.Now().Before(s.tokenExp.Add(-30*time.Second)) && s.token != "" {
		return s.token, nil
	}
	jwt, err := s.signJWT()
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", jwt)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.sa.TokenURI, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", &retry.HTTPStatusError{Status: resp.StatusCode, Body: truncateResponse(data)}
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

func truncateResponse(data []byte) string {
	const max = 300
	if len(data) > max {
		return string(data[:max]) + "…"
	}
	return string(data)
}
