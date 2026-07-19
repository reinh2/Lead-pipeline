package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anker-relocation/pipeline/pkg/api"
	"github.com/anker-relocation/pipeline/pkg/crm"
	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/qualifier"
	"github.com/anker-relocation/pipeline/pkg/queue"
	"github.com/anker-relocation/pipeline/pkg/retry"
)

type failingCRM struct{}

func (failingCRM) Name() string { return "failing" }
func (failingCRM) Upsert(context.Context, models.Lead, models.Qualification) (string, error) {
	return "", errors.New("crm offline")
}
func (failingCRM) MarkClaimed(context.Context, string, string) error { return nil }
func (failingCRM) MarkSpam(context.Context, string) error            { return nil }

type flakyCRM struct{ attempts int }

func (c *flakyCRM) Name() string { return "flaky" }
func (c *flakyCRM) Upsert(context.Context, models.Lead, models.Qualification) (string, error) {
	c.attempts++
	if c.attempts < 3 {
		return "", &retry.HTTPStatusError{Status: 503}
	}
	return "deal-recovered", nil
}
func (*flakyCRM) MarkClaimed(context.Context, string, string) error { return nil }
func (*flakyCRM) MarkSpam(context.Context, string) error            { return nil }

func testLead(id string) models.Lead {
	return models.Lead{
		ID: id, Name: "Alex", Email: "alex@example.com", Nationality: "Canadian",
		Service: models.ServiceFullRelocation, FamilySize: 2, GDPRConsent: true,
	}
}

func TestProcess_CRMPreservesQualifiedFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leads.csv")
	csvSink, err := crm.NewCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline{
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		csv:  csvSink,
		crm:  failingCRM{},
		qual: qualifier.MockQualifier{},
	}
	lead := testLead("ld-crm-fallback")
	p.process(context.Background(), lead)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), lead.ID) || !strings.Contains(string(data), "warm") {
		t.Fatalf("fallback is missing raw lead or qualification: %s", data)
	}
}

func TestAccept_QueueFullLeavesDurableLeadAndReturnsUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leads.csv")
	csvSink, err := crm.NewCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		csv: csvSink,
		q:   queue.New(1, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	if err := p.Accept(context.Background(), testLead("ld-first")); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if err := p.Accept(context.Background(), testLead("ld-full")); !errors.Is(err, api.ErrUnavailable) {
		t.Fatalf("full queue error = %v, want ErrUnavailable", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ld-full") {
		t.Fatal("lead rejected by queue was not durably written")
	}
}

func TestProcess_RetriesTransientCRMFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leads.csv")
	csvSink, err := crm.NewCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	primary := &flakyCRM{}
	p := &pipeline{
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		csv:  csvSink,
		crm:  primary,
		qual: qualifier.MockQualifier{},
	}
	p.process(context.Background(), testLead("ld-retry"))
	if primary.attempts != 3 {
		t.Fatalf("CRM attempts = %d, want 3", primary.attempts)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "dead_letter:crm:flaky") {
		t.Fatalf("successful retry must not produce dead letter: %s", data)
	}
}
