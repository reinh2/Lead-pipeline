package api

import (
	"strings"
	"testing"

	"github.com/anker-relocation/pipeline/pkg/models"
)

func baseLead() models.Lead {
	return models.Lead{
		Name: "Alex", Email: "alex@example.com", Nationality: "Canadian",
		Service: models.ServiceFullRelocation, FamilySize: 2, GDPRConsent: true,
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
