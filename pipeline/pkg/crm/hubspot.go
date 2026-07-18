package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// HubSpot is a small wrapper over the Contacts + Deals REST API using a
// private-app token. Enough for create-or-update Contact and create Deal +
// update Deal stage on manager action. It does not try to be a full HubSpot
// SDK — we only need the surface used by this pipeline.
type HubSpot struct {
	token      string
	pipelineID string
	http       *http.Client
	log        *slog.Logger
}

func NewHubSpot(token, pipelineID string, log *slog.Logger) *HubSpot {
	return &HubSpot{
		token:      token,
		pipelineID: pipelineID,
		http:       &http.Client{Timeout: 10 * time.Second},
		log:        log,
	}
}

func (h *HubSpot) Name() string { return "hubspot" }

func (h *HubSpot) Upsert(ctx context.Context, l models.Lead, q models.Qualification) (string, error) {
	contactID, err := h.upsertContact(ctx, l)
	if err != nil {
		return "", fmt.Errorf("hubspot contact: %w", err)
	}
	dealID, err := h.createDeal(ctx, contactID, l, q)
	if err != nil {
		return "", fmt.Errorf("hubspot deal: %w", err)
	}
	return dealID, nil
}

func (h *HubSpot) upsertContact(ctx context.Context, l models.Lead) (string, error) {
	first, last := splitName(l.Name)
	props := map[string]string{
		"email":        l.Email,
		"firstname":    first,
		"lastname":     last,
		"nationality":  l.Nationality,
		"country":      l.CurrentCountry,
		"lifecyclestage": "lead",
	}
	// Search by email first — HubSpot's dedupe rules already dedupe by email
	// for contacts, but doing an explicit lookup keeps the flow explicit.
	if id, ok, err := h.findContactByEmail(ctx, l.Email); err != nil {
		return "", err
	} else if ok {
		_, err := h.do(ctx, http.MethodPatch, "/crm/v3/objects/contacts/"+id, map[string]any{"properties": props})
		if err != nil {
			return "", err
		}
		return id, nil
	}
	body, err := h.do(ctx, http.MethodPost, "/crm/v3/objects/contacts", map[string]any{"properties": props})
	if err != nil {
		return "", err
	}
	var out struct{ ID string `json:"id"` }
	_ = json.Unmarshal(body, &out)
	return out.ID, nil
}

func (h *HubSpot) findContactByEmail(ctx context.Context, email string) (string, bool, error) {
	q := url.Values{}
	q.Set("idProperty", "email")
	body, err := h.do(ctx, http.MethodGet, "/crm/v3/objects/contacts/"+url.PathEscape(email)+"?"+q.Encode(), nil)
	if err != nil {
		// 404 is expected when not found; do() surfaces it as errNotFound.
		if err == errNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	var out struct{ ID string `json:"id"` }
	_ = json.Unmarshal(body, &out)
	return out.ID, out.ID != "", nil
}

func (h *HubSpot) createDeal(ctx context.Context, contactID string, l models.Lead, q models.Qualification) (string, error) {
	stage := stageFromCategory(q.Category)
	amount := (q.Estimate.Min + q.Estimate.Max) / 2
	props := map[string]any{
		"dealname":  fmt.Sprintf("%s — %s", l.Name, l.Service),
		"amount":    amount,
		"dealstage": stage,
		"pipeline":  defaultString(h.pipelineID, "default"),
	}
	body, err := h.do(ctx, http.MethodPost, "/crm/v3/objects/deals", map[string]any{
		"properties": props,
		"associations": []map[string]any{{
			"to":    map[string]string{"id": contactID},
			"types": []map[string]any{{"associationCategory": "HUBSPOT_DEFINED", "associationTypeId": 3}},
		}},
	})
	if err != nil {
		return "", err
	}
	var out struct{ ID string `json:"id"` }
	_ = json.Unmarshal(body, &out)
	return out.ID, nil
}

func (h *HubSpot) MarkClaimed(ctx context.Context, id, manager string) error {
	_, err := h.do(ctx, http.MethodPatch, "/crm/v3/objects/deals/"+id, map[string]any{
		"properties": map[string]any{
			"dealstage":    "appointmentscheduled",
			"hs_deal_stage_probability": 0.4,
			"hubspot_owner_id": manager,
		},
	})
	return err
}

func (h *HubSpot) MarkSpam(ctx context.Context, id string) error {
	_, err := h.do(ctx, http.MethodPatch, "/crm/v3/objects/deals/"+id, map[string]any{
		"properties": map[string]any{"dealstage": "closedlost", "closed_lost_reason": "spam"},
	})
	return err
}

var errNotFound = fmt.Errorf("hubspot: not found")

func (h *HubSpot) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.hubapi.com"+path, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		return nil, errNotFound
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("hubspot HTTP %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func splitName(full string) (string, string) {
	for i := len(full) - 1; i >= 0; i-- {
		if full[i] == ' ' {
			return full[:i], full[i+1:]
		}
	}
	return full, ""
}

func stageFromCategory(c models.Category) string {
	switch c {
	case models.CategoryHot:
		return "qualifiedtobuy"
	case models.CategoryWarm:
		return "appointmentscheduled"
	case models.CategoryCold:
		return "presentationscheduled"
	case models.CategorySpam:
		return "closedlost"
	}
	return "appointmentscheduled"
}

func defaultString(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
