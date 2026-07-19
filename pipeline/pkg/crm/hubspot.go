package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/retry"
)

const (
	hubSpotLeadIDProperty        = "lead_id"
	hubSpotQualificationProperty = "qualification"
	hubSpotSourceProperty        = "source"
	hubSpotClaimedByProperty     = "claimed_by"
)

// HubSpot stores the lead ID in a custom deal property and searches it before
// creating a deal. The account must define lead_id, qualification, source,
// nationality and claimed_by custom properties before enabling this adapter.
type HubSpot struct {
	token      string
	pipelineID string
	baseURL    string
	http       *http.Client
	log        *slog.Logger
}

func NewHubSpot(token, pipelineID string, log *slog.Logger) *HubSpot {
	return &HubSpot{
		token:      token,
		pipelineID: pipelineID,
		baseURL:    "https://api.hubapi.com",
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
	dealID, found, err := h.findDealByLeadID(ctx, l.ID)
	if err != nil {
		return "", fmt.Errorf("hubspot find deal: %w", err)
	}
	if found {
		if err := h.updateDeal(ctx, dealID, l, q); err != nil {
			return "", fmt.Errorf("hubspot update deal: %w", err)
		}
		return dealID, nil
	}
	dealID, err = h.createDeal(ctx, contactID, l, q)
	if err != nil {
		return "", fmt.Errorf("hubspot create deal: %w", err)
	}
	return dealID, nil
}

func (h *HubSpot) upsertContact(ctx context.Context, l models.Lead) (string, error) {
	first, last := splitName(l.Name)
	props := map[string]string{
		"email":             l.Email,
		"firstname":         first,
		"lastname":          last,
		"nationality":       l.Nationality,
		"country":           l.CurrentCountry,
		"consent_version":   l.ConsentVersion,
		"consent_timestamp": l.ConsentedAt.Format(time.RFC3339),
		"lifecyclestage":    "lead",
	}
	if id, ok, err := h.findContactByEmail(ctx, l.Email); err != nil {
		return "", err
	} else if ok {
		if _, err := h.do(ctx, http.MethodPatch, "/crm/v3/objects/contacts/"+id, map[string]any{"properties": props}); err != nil {
			return "", err
		}
		return id, nil
	}
	body, err := h.do(ctx, http.MethodPost, "/crm/v3/objects/contacts", map[string]any{"properties": props})
	if err != nil {
		return "", err
	}
	return objectID(body, "create contact")
}

func (h *HubSpot) findContactByEmail(ctx context.Context, email string) (string, bool, error) {
	q := url.Values{}
	q.Set("idProperty", "email")
	body, err := h.do(ctx, http.MethodGet, "/crm/v3/objects/contacts/"+url.PathEscape(email)+"?"+q.Encode(), nil)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	id, err := objectID(body, "find contact")
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (h *HubSpot) findDealByLeadID(ctx context.Context, leadID string) (string, bool, error) {
	body, err := h.do(ctx, http.MethodPost, "/crm/v3/objects/deals/search", map[string]any{
		"filterGroups": []map[string]any{{"filters": []map[string]string{{"propertyName": hubSpotLeadIDProperty, "operator": "EQ", "value": leadID}}}},
		"limit":        1,
	})
	if err != nil {
		return "", false, err
	}
	var out struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false, fmt.Errorf("decode deal search: %w", err)
	}
	if len(out.Results) == 0 {
		return "", false, nil
	}
	if out.Results[0].ID == "" {
		return "", false, errors.New("deal search returned an empty id")
	}
	return out.Results[0].ID, true, nil
}

func (h *HubSpot) dealProperties(l models.Lead, q models.Qualification) map[string]any {
	amount := (q.Estimate.Min + q.Estimate.Max) / 2
	return map[string]any{
		"dealname":                   fmt.Sprintf("%s — %s", l.Name, l.Service),
		"amount":                     amount,
		"dealstage":                  stageFromCategory(q.Category),
		"pipeline":                   defaultString(h.pipelineID, "default"),
		hubSpotLeadIDProperty:        l.ID,
		hubSpotQualificationProperty: string(q.Category),
		hubSpotSourceProperty:        string(l.Source),
	}
}

func (h *HubSpot) createDeal(ctx context.Context, contactID string, l models.Lead, q models.Qualification) (string, error) {
	body, err := h.do(ctx, http.MethodPost, "/crm/v3/objects/deals", map[string]any{
		"properties": h.dealProperties(l, q),
		"associations": []map[string]any{{
			"to":    map[string]string{"id": contactID},
			"types": []map[string]any{{"associationCategory": "HUBSPOT_DEFINED", "associationTypeId": 3}},
		}},
	})
	if err != nil {
		return "", err
	}
	return objectID(body, "create deal")
}

func (h *HubSpot) updateDeal(ctx context.Context, dealID string, l models.Lead, q models.Qualification) error {
	_, err := h.do(ctx, http.MethodPatch, "/crm/v3/objects/deals/"+dealID, map[string]any{"properties": h.dealProperties(l, q)})
	return err
}

func (h *HubSpot) MarkClaimed(ctx context.Context, id, manager string) error {
	_, err := h.do(ctx, http.MethodPatch, "/crm/v3/objects/deals/"+id, map[string]any{
		"properties": map[string]any{
			"dealstage":                 "appointmentscheduled",
			"hs_deal_stage_probability": 0.4,
			hubSpotClaimedByProperty:    manager,
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

var errNotFound = errors.New("hubspot: not found")

func (h *HubSpot) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode hubspot request: %w", err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(h.baseURL, "/")+path, buf)
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
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, &retry.HTTPStatusError{Status: resp.StatusCode, Body: truncateBody(data)}
	}
	return data, nil
}

func truncateBody(data []byte) string {
	const max = 300
	if len(data) > max {
		return string(data[:max]) + "…"
	}
	return string(data)
}

func objectID(body []byte, operation string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode %s response: %w", operation, err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("%s response has an empty id", operation)
	}
	return out.ID, nil
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
