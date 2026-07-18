package tgbot

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anker-relocation/pipeline/pkg/api"
	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/notify"
)

// Step is the intake state machine's cursor. Order matters — Next() steps
// through them linearly.
type Step int

const (
	StepStart Step = iota
	StepNationality
	StepJobOffer
	StepMoveDate
	StepFamilySize
	StepFreeText
	StepDone
)

func (s Step) Prompt() string {
	switch s {
	case StepNationality:
		return "Great! What's your nationality? (just type it)"
	case StepJobOffer:
		return "Do you already have a job offer in Germany?"
	case StepMoveDate:
		return "When would you like to move? (e.g. 2026-09-01, or 'next month')"
	case StepFamilySize:
		return "How many people are moving with you? (including yourself — 1, 2, 3…)"
	case StepFreeText:
		return "Anything else we should know? (or type 'done' to finish)"
	}
	return ""
}

func (s Step) Next() Step {
	if s >= StepDone {
		return StepDone
	}
	return s + 1
}

// Session is the per-chat state carried while walking the user through the
// intake questions. Access is guarded by Store.
type Session struct {
	ChatID  int64
	Step    Step
	Lead    models.Lead
	Started time.Time
}

type Store struct {
	mu   sync.Mutex
	byID map[int64]*Session
}

func NewStore() *Store { return &Store{byID: map[int64]*Session{}} }

func (s *Store) Get(chatID int64) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[chatID]
}
func (s *Store) Put(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[sess.ChatID] = sess
}
func (s *Store) Delete(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, chatID)
}

// Bot glues the store to a Telegram client and a Submitter (the pipeline).
// It exposes a single Handle method wired into notify.Telegram's poll loop.
type Bot struct {
	tg       *notify.Telegram
	store    *Store
	log      *slog.Logger
	submit   api.Submitter
}

func New(tg *notify.Telegram, sub api.Submitter, log *slog.Logger) *Bot {
	return &Bot{tg: tg, store: NewStore(), submit: sub, log: log}
}

// Handle is the IntakeHandler wired into notify.Telegram.Poll. It runs the
// linear state machine, tolerates free-text answers for every question, and
// submits the completed lead into the pipeline exactly like the web form.
func (b *Bot) Handle(ctx context.Context, m *notify.Message) {
	if m == nil || m.Chat.ID == 0 || strings.TrimSpace(m.Text) == "" {
		return
	}
	chatID := m.Chat.ID
	text := strings.TrimSpace(m.Text)
	lower := strings.ToLower(text)

	// /start (re)initialises the session.
	if lower == "/start" || b.store.Get(chatID) == nil {
		name := strings.TrimSpace(m.From.FirstName)
		if name == "" {
			name = "Friend"
		}
		s := &Session{
			ChatID:  chatID,
			Step:    StepNationality,
			Started: time.Now(),
			Lead: models.Lead{
				ID:             api.NewID(),
				Source:         models.SourceTelegram,
				CreatedAt:      time.Now().UTC(),
				Name:           name,
				TelegramChatID: chatID,
				GDPRConsent:    true, // implicit consent by talking to the bot; recorded per intake
				Service:        models.ServiceFullRelocation,
				FamilySize:     1,
			},
		}
		b.store.Put(s)
		_ = b.tg.SendDM(ctx, chatID,
			"Hi "+name+"! I'm the Anker Relocation assistant. I'll ask 4 quick questions and a human will follow up within 15 minutes.")
		_ = b.tg.SendDM(ctx, chatID, StepNationality.Prompt())
		return
	}

	sess := b.store.Get(chatID)
	switch sess.Step {
	case StepNationality:
		sess.Lead.Nationality = text
		sess.Step = StepJobOffer
		_ = b.tg.AnswerInline(ctx, chatID, StepJobOffer.Prompt(), [][]string{
			{"Yes, signed", "Interviewing"},
			{"No, still looking"},
		})
	case StepJobOffer:
		sess.Lead.JobOffer = normaliseJobOffer(text)
		sess.Step = StepMoveDate
		_ = b.tg.SendDM(ctx, chatID, StepMoveDate.Prompt())
	case StepMoveDate:
		sess.Lead.MoveDate = text
		sess.Step = StepFamilySize
		_ = b.tg.SendDM(ctx, chatID, StepFamilySize.Prompt())
	case StepFamilySize:
		if n, err := strconv.Atoi(strings.Fields(text)[0]); err == nil && n >= 1 && n <= 20 {
			sess.Lead.FamilySize = n
		}
		sess.Step = StepFreeText
		_ = b.tg.SendDM(ctx, chatID, StepFreeText.Prompt())
	case StepFreeText:
		if lower != "done" {
			sess.Lead.Message = text
		}
		sess.Step = StepDone
		_ = b.tg.SendDM(ctx, chatID, "Got everything — one of our specialists will be in touch shortly. 🙌")
		if err := b.submit.Accept(ctx, sess.Lead); err != nil {
			b.log.Error("tg intake submit failed", "lead_id", sess.Lead.ID, "err", err)
		}
		b.store.Delete(chatID)
		return
	}
	b.store.Put(sess)
}

func normaliseJobOffer(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "signed") || strings.Contains(t, "yes"):
		return "signed"
	case strings.Contains(t, "interview"):
		return "interviewing"
	case strings.Contains(t, "no") || strings.Contains(t, "looking"):
		return "no"
	}
	return t
}
