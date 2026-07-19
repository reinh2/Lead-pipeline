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

const (
	defaultSessionTTL      = 30 * time.Minute
	defaultCleanupInterval = 5 * time.Minute
)

// Step is the intake state machine's cursor.
type Step int

const (
	StepStart Step = iota
	StepConsent
	StepNationality
	StepJobOffer
	StepMoveDate
	StepFamilySize
	StepFreeText
	StepDone
)

func (s Step) Prompt() string {
	switch s {
	case StepConsent:
		return "Before we collect any relocation details: do you agree to the processing of your information for this enquiry?"
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

// Session is the per-chat state carried while walking the user through the
// intake questions. Name is kept outside Lead until consent is explicit.
type Session struct {
	ChatID    int64
	Name      string
	Step      Step
	Lead      models.Lead
	Started   time.Time
	UpdatedAt time.Time
}

type Store struct {
	mu   sync.Mutex
	byID map[int64]*Session
}

func NewStore() *Store { return &Store{byID: map[int64]*Session{}} }

func (s *Store) Get(chatID int64) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSession(s.byID[chatID])
}

func (s *Store) Put(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[sess.ChatID] = cloneSession(sess)
}

func (s *Store) Delete(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, chatID)
}

func (s *Store) DeleteExpired(now time.Time, ttl time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for chatID, sess := range s.byID {
		updated := sess.UpdatedAt
		if updated.IsZero() {
			updated = sess.Started
		}
		if !updated.IsZero() && now.Sub(updated) >= ttl {
			delete(s.byID, chatID)
			deleted++
		}
	}
	return deleted
}

func cloneSession(sess *Session) *Session {
	if sess == nil {
		return nil
	}
	clone := *sess
	return &clone
}

type messenger interface {
	SendDM(ctx context.Context, chatID int64, text string) error
	AnswerInline(ctx context.Context, chatID int64, text string, options [][]string) error
}

// Bot glues the store to a Telegram client and a Submitter (the pipeline).
type Bot struct {
	tg              messenger
	store           *Store
	log             *slog.Logger
	submit          api.Submitter
	ttl             time.Duration
	cleanupInterval time.Duration
	now             func() time.Time
}

func New(tg *notify.Telegram, sub api.Submitter, log *slog.Logger) *Bot {
	return newBot(tg, sub, log, defaultSessionTTL, defaultCleanupInterval, time.Now)
}

func newBot(tg messenger, sub api.Submitter, log *slog.Logger, ttl, cleanupInterval time.Duration, now func() time.Time) *Bot {
	return &Bot{
		tg:              tg,
		store:           NewStore(),
		log:             log,
		submit:          sub,
		ttl:             ttl,
		cleanupInterval: cleanupInterval,
		now:             now,
	}
}

// RunCleanup periodically removes abandoned intake sessions from memory.
func (b *Bot) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(b.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := b.store.DeleteExpired(b.now(), b.ttl); n > 0 {
				b.log.Info("expired telegram intake sessions removed", "count", n)
			}
		}
	}
}

// Handle is the ordinary-message handler wired into notify.Telegram.Poll.
func (b *Bot) Handle(ctx context.Context, m *notify.Message) {
	if m == nil || m.Chat.ID == 0 || strings.TrimSpace(m.Text) == "" {
		return
	}
	b.handleInput(ctx, m.Chat.ID, m.From.FirstName, m.Text)
}

// HandleCallback advances an intake session from an inline-button value.
// Telegram acknowledgement is handled by notify.Telegram after this returns.
func (b *Bot) HandleCallback(ctx context.Context, chatID int64, value string) {
	if chatID == 0 || strings.TrimSpace(value) == "" {
		return
	}
	b.handleInput(ctx, chatID, "", value)
}

func (b *Bot) handleInput(ctx context.Context, chatID int64, firstName, raw string) {
	text := strings.TrimSpace(raw)
	lower := strings.ToLower(text)
	if lower == "/start" || lower == "/restart" {
		b.start(ctx, chatID, firstName)
		return
	}
	if lower == "/cancel" {
		b.store.Delete(chatID)
		b.send(ctx, chatID, "Your enquiry has been cancelled. Send /start whenever you'd like to begin again.")
		return
	}

	sess := b.store.Get(chatID)
	if sess == nil {
		b.send(ctx, chatID, "Send /start to begin your relocation enquiry.")
		return
	}
	if b.expired(sess) {
		b.store.Delete(chatID)
		b.send(ctx, chatID, "Your session expired for privacy. Send /restart to begin again.")
		return
	}

	sess.UpdatedAt = b.now()
	switch sess.Step {
	case StepConsent:
		b.handleConsent(ctx, sess, text)
	case StepNationality:
		sess.Lead.Nationality = text
		sess.Step = StepJobOffer
		b.store.Put(sess)
		b.askJobOffer(ctx, chatID)
	case StepJobOffer:
		sess.Lead.JobOffer = normaliseJobOffer(text)
		sess.Step = StepMoveDate
		b.store.Put(sess)
		b.send(ctx, chatID, StepMoveDate.Prompt())
	case StepMoveDate:
		sess.Lead.MoveDate = text
		sess.Step = StepFamilySize
		b.store.Put(sess)
		b.send(ctx, chatID, StepFamilySize.Prompt())
	case StepFamilySize:
		familySize, ok := parseFamilySize(text)
		if !ok {
			b.store.Put(sess)
			b.send(ctx, chatID, "Please enter a whole number from 1 to 20. "+StepFamilySize.Prompt())
			return
		}
		sess.Lead.FamilySize = familySize
		sess.Step = StepFreeText
		b.store.Put(sess)
		b.send(ctx, chatID, StepFreeText.Prompt())
	case StepFreeText:
		if lower != "done" {
			sess.Lead.Message = text
		}
		if err := b.submit.Accept(ctx, sess.Lead); err != nil {
			b.log.Error("tg intake submit failed", "lead_id", sess.Lead.ID, "err", err)
			b.store.Put(sess)
			b.send(ctx, chatID, "We couldn't submit your enquiry yet. Please send your last answer again, or /cancel.")
			return
		}
		sess.Step = StepDone
		b.store.Delete(chatID)
		b.send(ctx, chatID, "Got everything — one of our specialists will be in touch shortly. 🙌")
	default:
		b.store.Delete(chatID)
		b.send(ctx, chatID, "Something went wrong with this session. Send /restart to begin again.")
	}
}

func (b *Bot) start(ctx context.Context, chatID int64, firstName string) {
	name := strings.TrimSpace(firstName)
	if name == "" {
		name = "Friend"
	}
	now := b.now()
	b.store.Put(&Session{ChatID: chatID, Name: name, Step: StepConsent, Started: now, UpdatedAt: now})
	b.send(ctx, chatID, "Hi "+name+"! I'll ask a few questions to help with your relocation.")
	b.askConsent(ctx, chatID)
}

func (b *Bot) handleConsent(ctx context.Context, sess *Session, text string) {
	if consents(text) {
		now := b.now().UTC()
		sess.Lead = models.Lead{
			ID:             api.NewID(),
			Source:         models.SourceTelegram,
			CreatedAt:      now,
			Name:           sess.Name,
			TelegramChatID: sess.ChatID,
			GDPRConsent:    true,
			ConsentVersion: api.ConsentVersion,
			ConsentedAt:    now,
			Service:        models.ServiceFullRelocation,
			FamilySize:     1,
		}
		sess.Step = StepNationality
		b.store.Put(sess)
		b.send(ctx, sess.ChatID, StepNationality.Prompt())
		return
	}
	if declines(text) {
		b.store.Delete(sess.ChatID)
		b.send(ctx, sess.ChatID, "No information has been submitted. You can send /start if you change your mind.")
		return
	}
	b.store.Put(sess)
	b.send(ctx, sess.ChatID, "Please choose whether you agree before we collect relocation details.")
	b.askConsent(ctx, sess.ChatID)
}

func (b *Bot) askConsent(ctx context.Context, chatID int64) {
	if b.tg != nil {
		if err := b.tg.AnswerInline(ctx, chatID, StepConsent.Prompt(), [][]string{{"I agree", "No, cancel"}}); err != nil {
			b.log.Warn("send telegram consent buttons", "chat_id", chatID, "err", err)
		}
	}
}

func (b *Bot) askJobOffer(ctx context.Context, chatID int64) {
	if b.tg != nil {
		if err := b.tg.AnswerInline(ctx, chatID, StepJobOffer.Prompt(), [][]string{{"Yes, signed", "Interviewing"}, {"No, still looking"}}); err != nil {
			b.log.Warn("send telegram job-offer buttons", "chat_id", chatID, "err", err)
		}
	}
}

func (b *Bot) send(ctx context.Context, chatID int64, text string) {
	if b.tg == nil {
		return
	}
	if err := b.tg.SendDM(ctx, chatID, text); err != nil {
		b.log.Warn("send telegram intake message", "chat_id", chatID, "err", err)
	}
}

func (b *Bot) expired(sess *Session) bool {
	updated := sess.UpdatedAt
	if updated.IsZero() {
		updated = sess.Started
	}
	return !updated.IsZero() && b.now().Sub(updated) >= b.ttl
}

func parseFamilySize(text string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	return n, err == nil && n >= 1 && n <= 20
}

func consents(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	return t == "i agree" || t == "agree" || t == "yes" || t == "yes, i agree" || t == "consent"
}

func declines(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	return t == "no" || t == "no, cancel" || t == "cancel"
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
