package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/retry"
)

// CallbackAction is what the manager clicked on the notification card.
type CallbackAction string

const (
	ActionClaim CallbackAction = "claim"
	ActionSpam  CallbackAction = "spam"
)

// CallbackHandler is invoked by the bot poller when a manager taps a button.
// It receives the deal id (as posted in the card) and the manager's display
// name so the CRM can record who claimed the lead.
type CallbackHandler func(ctx context.Context, action CallbackAction, dealID, manager string)

// IntakeCallbackHandler receives the originating chat and value of an intake
// button. It is intentionally separate from manager-card actions.
type IntakeCallbackHandler func(ctx context.Context, chatID int64, value string)

// Telegram implements Notifier and, when Poll() is running, also handles the
// inline-button callbacks. Two roles, one bot.
type Telegram struct {
	token         string
	managerChatID int64
	apiBaseURL    string
	http          *http.Client
	log           *slog.Logger

	mu    sync.Mutex
	cards map[string]cardState // dealID -> last message posted
}

type cardState struct {
	ChatID    int64
	MessageID int
	Text      string
}

func NewTelegram(token string, managerChatID int64, log *slog.Logger) *Telegram {
	return &Telegram{
		token:         token,
		managerChatID: managerChatID,
		apiBaseURL:    "https://api.telegram.org",
		http:          &http.Client{Timeout: 10 * time.Second},
		log:           log,
		cards:         map[string]cardState{},
	}
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Notify(ctx context.Context, l models.Lead, q models.Qualification, dealID string) error {
	card := BuildCard(l, q, dealID)
	text := card.Emoji + "  *" + escapeMD(card.Title) + "*\n"
	for _, line := range card.Lines {
		text += "• " + escapeMD(line) + "\n"
	}
	text += "\nLead `" + l.ID + "` · Deal `" + dealID + "`"

	kb := map[string]any{
		"inline_keyboard": [][]map[string]string{{
			{"text": "✅ Claim lead", "callback_data": "claim:" + dealID},
			{"text": "🗑 Mark spam", "callback_data": "spam:" + dealID},
		}},
	}
	body := map[string]any{
		"chat_id":      t.managerChatID,
		"text":         text,
		"parse_mode":   "Markdown",
		"reply_markup": kb,
	}
	msgID, err := t.sendMessage(ctx, body)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.cards[dealID] = cardState{ChatID: t.managerChatID, MessageID: msgID, Text: text}
	t.mu.Unlock()
	return nil
}

// SendDM posts a plain-text message to an arbitrary chat id. Used by the
// autoreply layer to reply to Telegram-sourced leads in-bot.
func (t *Telegram) SendDM(ctx context.Context, chatID int64, text string) error {
	_, err := t.sendMessage(ctx, map[string]any{"chat_id": chatID, "text": text})
	return err
}

func (t *Telegram) sendMessage(ctx context.Context, body map[string]any) (int, error) {
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := t.call(ctx, "sendMessage", body, &out); err != nil {
		return 0, err
	}
	if !out.OK {
		return 0, fmt.Errorf("telegram sendMessage: %s", out.Description)
	}
	return out.Result.MessageID, nil
}

// MarkCard edits the card in place, appending a footer with the resolution.
func (t *Telegram) MarkCard(ctx context.Context, dealID, footer string) error {
	t.mu.Lock()
	st, ok := t.cards[dealID]
	t.mu.Unlock()
	if !ok {
		return nil
	}
	newText := st.Text + "\n\n_" + escapeMD(footer) + "_"
	body := map[string]any{
		"chat_id":      st.ChatID,
		"message_id":   st.MessageID,
		"text":         newText,
		"parse_mode":   "Markdown",
		"reply_markup": map[string]any{"inline_keyboard": [][]any{}},
	}
	return t.call(ctx, "editMessageText", body, nil)
}

// Poll runs a getUpdates long-poll loop and dispatches manager actions, intake
// buttons, and ordinary messages. Blocks until ctx is done.
func (t *Telegram) Poll(ctx context.Context, onCallback CallbackHandler, onIntake IntakeHandler, onIntakeCallback IntakeCallbackHandler) {
	var offset int64
	for {
		if ctx.Err() != nil {
			return
		}
		body := map[string]any{"timeout": 25, "offset": offset, "allowed_updates": []string{"message", "callback_query"}}
		var out struct {
			OK     bool     `json:"ok"`
			Result []Update `json:"result"`
		}
		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := t.call(reqCtx, "getUpdates", body, &out)
		cancel()
		if err != nil {
			t.log.Warn("telegram poll error", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range out.Result {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			t.dispatch(ctx, u, onCallback, onIntake, onIntakeCallback)
		}
	}
}

func (t *Telegram) dispatch(ctx context.Context, u Update, onCallback CallbackHandler, onIntake IntakeHandler, onIntakeCallback IntakeCallbackHandler) {
	if callback := u.CallbackQuery; callback != nil {
		data := callback.Data
		ack := "This action is no longer available."
		switch {
		case strings.HasPrefix(data, "claim:") && onCallback != nil:
			manager := callback.From.Username
			if manager == "" {
				manager = callback.From.FirstName
			}
			onCallback(ctx, ActionClaim, strings.TrimPrefix(data, "claim:"), manager)
			ack = "Got it — claim"
		case strings.HasPrefix(data, "spam:") && onCallback != nil:
			manager := callback.From.Username
			if manager == "" {
				manager = callback.From.FirstName
			}
			onCallback(ctx, ActionSpam, strings.TrimPrefix(data, "spam:"), manager)
			ack = "Got it — spam"
		case strings.HasPrefix(data, "intake:") && onIntakeCallback != nil && callback.Message != nil:
			onIntakeCallback(ctx, callback.Message.Chat.ID, strings.TrimPrefix(data, "intake:"))
			ack = "Answer received"
		}
		// Ack every callback so the Telegram client never keeps the button spinner.
		_ = t.call(ctx, "answerCallbackQuery", map[string]any{
			"callback_query_id": callback.ID,
			"text":              ack,
		}, nil)
		return
	}
	if u.Message != nil && onIntake != nil {
		onIntake(ctx, u.Message)
	}
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    From     `json:"from"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}
type From struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}
type Message struct {
	MessageID int    `json:"message_id"`
	From      From   `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}
type Chat struct {
	ID int64 `json:"id"`
}

// IntakeHandler receives ordinary user messages so the tgbot package can drive
// its intake state machine over the same long-poll loop as the notifier.
type IntakeHandler func(ctx context.Context, m *Message)

func (t *Telegram) call(ctx context.Context, method string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode telegram %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.apiBaseURL+"/bot"+t.token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("create telegram %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read telegram %s response: %w", method, err)
	}
	if resp.StatusCode >= 400 {
		return &retry.HTTPStatusError{Status: resp.StatusCode, Body: truncateTelegramResponse(data)}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func truncateTelegramResponse(data []byte) string {
	const max = 300
	if len(data) > max {
		return string(data[:max]) + "…"
	}
	return string(data)
}

// escapeMD escapes Telegram Markdown v1 special characters.
func escapeMD(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '_', '*', '`', '[':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// AnswerInline is a helper used elsewhere for inline-keyboard question flows.
func (t *Telegram) AnswerInline(ctx context.Context, chatID int64, text string, options [][]string) error {
	rows := make([][]map[string]string, 0, len(options))
	for _, row := range options {
		r := make([]map[string]string, 0, len(row))
		for _, o := range row {
			r = append(r, map[string]string{"text": o, "callback_data": "intake:" + o})
		}
		rows = append(rows, r)
	}
	body := map[string]any{
		"chat_id":      chatID,
		"text":         text,
		"reply_markup": map[string]any{"inline_keyboard": rows},
	}
	_, err := t.sendMessage(ctx, body)
	return err
}

var _ = strconv.Itoa
