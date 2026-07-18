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
	"sync"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
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

// Telegram implements Notifier and, when Poll() is running, also handles the
// inline-button callbacks. Two roles, one bot.
type Telegram struct {
	token         string
	managerChatID int64
	http          *http.Client
	log           *slog.Logger

	mu       sync.Mutex
	cards    map[string]cardState // dealID -> last message posted
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

// Poll runs a getUpdates long-poll loop and dispatches inline-button taps to
// the CallbackHandler. Blocks until ctx is done.
func (t *Telegram) Poll(ctx context.Context, onCallback CallbackHandler, onIntake IntakeHandler) {
	var offset int64
	for {
		if ctx.Err() != nil {
			return
		}
		body := map[string]any{"timeout": 25, "offset": offset, "allowed_updates": []string{"message", "callback_query"}}
		var out struct {
			OK     bool      `json:"ok"`
			Result []Update  `json:"result"`
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
			t.dispatch(ctx, u, onCallback, onIntake)
		}
	}
}

func (t *Telegram) dispatch(ctx context.Context, u Update, onCallback CallbackHandler, onIntake IntakeHandler) {
	if u.CallbackQuery != nil && onCallback != nil {
		data := u.CallbackQuery.Data
		manager := u.CallbackQuery.From.Username
		if manager == "" {
			manager = u.CallbackQuery.From.FirstName
		}
		var action CallbackAction
		var dealID string
		if len(data) > 6 && data[:6] == "claim:" {
			action, dealID = ActionClaim, data[6:]
		} else if len(data) > 5 && data[:5] == "spam:" {
			action, dealID = ActionSpam, data[5:]
		} else {
			return
		}
		onCallback(ctx, action, dealID, manager)
		// Ack the button press so the client stops spinning.
		_ = t.call(ctx, "answerCallbackQuery", map[string]any{
			"callback_query_id": u.CallbackQuery.ID,
			"text":              "Got it — " + string(action),
		}, nil)
		return
	}
	if u.Message != nil && onIntake != nil {
		onIntake(ctx, u.Message)
	}
}

type Update struct {
	UpdateID      int64    `json:"update_id"`
	Message       *Message `json:"message"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		From From   `json:"from"`
		Data string `json:"data"`
	} `json:"callback_query"`
}
type From struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}
type Message struct {
	MessageID int    `json:"message_id"`
	From      From   `json:"from"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

// IntakeHandler receives ordinary user messages so the tgbot package can drive
// its intake state machine over the same long-poll loop as the notifier.
type IntakeHandler func(ctx context.Context, m *Message)

func (t *Telegram) call(ctx context.Context, method string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.token+"/"+method, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram %s HTTP %d: %s", method, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
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
