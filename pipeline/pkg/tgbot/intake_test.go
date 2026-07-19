package tgbot

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/notify"
)

type fakeSubmitter struct{ leads []models.Lead }

func (f *fakeSubmitter) Accept(_ context.Context, l models.Lead) error {
	f.leads = append(f.leads, l)
	return nil
}

type fakeMessenger struct {
	messages []string
	inline   []string
}

func (f *fakeMessenger) SendDM(_ context.Context, _ int64, text string) error {
	f.messages = append(f.messages, text)
	return nil
}

func (f *fakeMessenger) AnswerInline(_ context.Context, _ int64, text string, _ [][]string) error {
	f.inline = append(f.inline, text)
	return nil
}

func TestIntake_CallbackCompletesOneLead(t *testing.T) {
	ctx := context.Background()
	sub := &fakeSubmitter{}
	messenger := &fakeMessenger{}
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	b := newBot(messenger, sub, slog.Default(), time.Hour, time.Minute, func() time.Time { return now })

	b.Handle(ctx, message(42, "Alex", "/start"))
	if got := b.store.Get(42); got == nil || got.Step != StepConsent || got.Lead.GDPRConsent {
		t.Fatalf("/start did not create consent session: %+v", got)
	}

	b.HandleCallback(ctx, 42, "I agree")
	b.Handle(ctx, message(42, "Alex", "Canadian"))

	update := notify.Update{CallbackQuery: &notify.CallbackQuery{
		ID:      "callback-1",
		Data:    "intake:Yes, signed",
		Message: &notify.Message{Chat: notify.Chat{ID: 42}},
	}}
	b.HandleCallback(ctx, update.CallbackQuery.Message.Chat.ID, strings.TrimPrefix(update.CallbackQuery.Data, "intake:"))
	if got := b.store.Get(42); got == nil || got.Step != StepMoveDate || got.Lead.JobOffer != "signed" {
		t.Fatalf("intake callback did not advance job-offer step: %+v", got)
	}

	b.Handle(ctx, message(42, "Alex", "2026-09-01"))
	b.Handle(ctx, message(42, "Alex", "zero"))
	if got := b.store.Get(42); got == nil || got.Step != StepFamilySize {
		t.Fatalf("invalid family size advanced session: %+v", got)
	}
	b.Handle(ctx, message(42, "Alex", "3"))
	b.Handle(ctx, message(42, "Alex", "done"))

	if len(sub.leads) != 1 {
		t.Fatalf("want 1 submitted lead, got %d", len(sub.leads))
	}
	lead := sub.leads[0]
	if lead.Nationality != "Canadian" || lead.JobOffer != "signed" || lead.FamilySize != 3 || !lead.GDPRConsent {
		t.Fatalf("bad submitted lead: %+v", lead)
	}
	if b.store.Get(42) != nil {
		t.Fatal("session should be cleared after submit")
	}
}

func TestIntake_CommandsAndExpiredSession(t *testing.T) {
	ctx := context.Background()
	sub := &fakeSubmitter{}
	messenger := &fakeMessenger{}
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	b := newBot(messenger, sub, slog.Default(), 10*time.Minute, time.Minute, func() time.Time { return now })

	b.Handle(ctx, message(7, "Sam", "/start"))
	b.Handle(ctx, message(7, "Sam", "/cancel"))
	if b.store.Get(7) != nil {
		t.Fatal("/cancel should clear the session")
	}
	b.Handle(ctx, message(7, "Sam", "/restart"))
	if got := b.store.Get(7); got == nil || got.Step != StepConsent {
		t.Fatalf("/restart did not start a new session: %+v", got)
	}

	b.store.Put(&Session{ChatID: 8, Step: StepConsent, Started: now.Add(-11 * time.Minute), UpdatedAt: now.Add(-11 * time.Minute)})
	b.Handle(ctx, message(8, "", "I agree"))
	if b.store.Get(8) != nil {
		t.Fatal("expired session should be removed")
	}
	if got := messenger.messages[len(messenger.messages)-1]; !strings.Contains(got, "expired") {
		t.Fatalf("want expiry explanation, got %q", got)
	}
}

func TestIntake_DoesNotSubmitWithoutExplicitConsent(t *testing.T) {
	ctx := context.Background()
	sub := &fakeSubmitter{}
	b := newBot(&fakeMessenger{}, sub, slog.Default(), time.Hour, time.Minute, time.Now)

	b.Handle(ctx, message(9, "Ada", "/start"))
	b.Handle(ctx, message(9, "Ada", "Canadian"))
	b.Handle(ctx, message(9, "Ada", "No, cancel"))

	if len(sub.leads) != 0 {
		t.Fatalf("want no submitted leads without consent, got %d", len(sub.leads))
	}
	if b.store.Get(9) != nil {
		t.Fatal("declined-consent session should be cleared")
	}
}

func TestNormaliseJobOffer(t *testing.T) {
	cases := map[string]string{
		"Yes, signed":                "signed",
		"Interviewing at two places": "interviewing",
		"No, still looking":          "no",
	}
	for in, want := range cases {
		if got := normaliseJobOffer(in); got != want {
			t.Errorf("normaliseJobOffer(%q) = %q, want %q", in, got, want)
		}
	}
}

func message(chatID int64, firstName, text string) *notify.Message {
	return &notify.Message{From: notify.From{FirstName: firstName}, Chat: notify.Chat{ID: chatID}, Text: text}
}
