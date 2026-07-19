package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

func sampleLead() models.Lead {
	return models.Lead{
		ID: "ld_1", Name: "Alex Chen", Nationality: "Canadian",
		Service: models.ServiceFullRelocation, MoveDate: "2026-09-01",
		FamilySize: 3, Source: models.SourceWeb,
	}
}
func sampleQual() models.Qualification {
	return models.Qualification{
		Category: models.CategoryHot, Urgency: 5,
		Estimate: models.EstimateEUR{Min: 3750, Max: 9000},
		Summary:  "Signed N26 offer, family, Sept start.",
	}
}

func TestBuildCard_HotEmojiAndFields(t *testing.T) {
	c := BuildCard(sampleLead(), sampleQual(), "deal_42")
	if c.Emoji != "🔥" {
		t.Fatalf("want fire emoji, got %s", c.Emoji)
	}
	if !strings.Contains(c.Title, "Alex Chen") || !strings.Contains(c.Title, "Canadian") {
		t.Fatalf("bad title %q", c.Title)
	}
	joined := strings.Join(c.Lines, "\n")
	if !strings.Contains(joined, "Family size: 3") || !strings.Contains(joined, "€3750 – €9000") {
		t.Fatalf("missing fields:\n%s", joined)
	}
}

func TestSlackBlocks_HasActionButtonsWithDealID(t *testing.T) {
	bs := SlackBlocks(sampleLead(), sampleQual(), "deal_99")
	// Last block should be actions with both buttons carrying deal_99.
	last := bs[len(bs)-1]
	if last["type"] != "actions" {
		t.Fatalf("last block not actions: %v", last)
	}
	elems := last["elements"].([]map[string]any)
	if len(elems) != 2 {
		t.Fatalf("want 2 buttons got %d", len(elems))
	}
	if elems[0]["action_id"] != "claim" || elems[0]["value"] != "deal_99" {
		t.Fatalf("bad claim button: %v", elems[0])
	}
	if elems[1]["action_id"] != "spam" {
		t.Fatalf("bad spam button: %v", elems[1])
	}
}

func TestSlackBlocksEscapesUserControlledMrkdwn(t *testing.T) {
	lead := sampleLead()
	lead.Name = "<Alex & Co>"
	lead.Nationality = "<Canadian>"
	qual := sampleQual()
	qual.Summary = "<script>&"
	blocks := SlackBlocks(lead, qual, "deal_1")
	header := blocks[0]["text"].(map[string]any)["text"].(string)
	summary := blocks[1]["text"].(map[string]any)["text"].(string)
	fields := blocks[2]["fields"].([]map[string]any)
	if strings.Contains(header, "<Alex") || strings.Contains(summary, "<script>") || strings.Contains(fields[2]["text"].(string), "<Canadian>") {
		t.Fatalf("unescaped Slack content: %#v", blocks)
	}
}

func TestVerifySignature_OK(t *testing.T) {
	secret := "shh"
	body := []byte(`payload={"foo":"bar"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !VerifySignature(secret, ts, sig, body) {
		t.Fatal("valid signature should verify")
	}
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	secret := "shh"
	body := []byte(`payload={"foo":"bar"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if VerifySignature(secret, ts, sig, []byte("payload=evil")) {
		t.Fatal("tampered body must not verify")
	}
}

func TestVerifySignature_Replay(t *testing.T) {
	secret := "shh"
	body := []byte(`payload={}`)
	ts := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if VerifySignature(secret, ts, sig, body) {
		t.Fatal("old timestamp must fail (replay protection)")
	}
}

func TestTelegramDispatch_IntakeCallbackForwardsChatAndAcknowledges(t *testing.T) {
	var callbackBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/answerCallbackQuery" {
			t.Fatalf("unexpected Telegram method path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&callbackBody); err != nil {
			t.Fatalf("decode callback acknowledgement: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	tg := NewTelegram("token", 0, slog.Default())
	tg.apiBaseURL = server.URL
	var gotChatID int64
	var gotValue string
	tg.dispatch(context.Background(), Update{CallbackQuery: &CallbackQuery{
		ID:      "cb-1",
		Data:    "intake:Yes, signed",
		Message: &Message{Chat: Chat{ID: 42}},
	}}, nil, nil, func(_ context.Context, chatID int64, value string) {
		gotChatID, gotValue = chatID, value
	})

	if gotChatID != 42 || gotValue != "Yes, signed" {
		t.Fatalf("intake callback = (%d, %q), want (42, %q)", gotChatID, gotValue, "Yes, signed")
	}
	if callbackBody["callback_query_id"] != "cb-1" || callbackBody["text"] != "Answer received" {
		t.Fatalf("unexpected callback acknowledgement: %#v", callbackBody)
	}
}

func TestTelegramNotifyAddsAContactButton(t *testing.T) {
	var body struct {
		ReplyMarkup struct {
			InlineKeyboard [][]map[string]string `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessage" {
			t.Fatalf("unexpected Telegram method path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	tg := NewTelegram("token", -100, slog.Default())
	tg.apiBaseURL = server.URL
	lead := sampleLead()
	lead.Email = "alex@example.com"
	if err := tg.Notify(context.Background(), lead, sampleQual(), "deal-email"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if len(body.ReplyMarkup.InlineKeyboard) != 2 {
		t.Fatalf("button rows = %d, want action and contact rows", len(body.ReplyMarkup.InlineKeyboard))
	}
	button := body.ReplyMarkup.InlineKeyboard[1][0]
	if button["text"] != "✉️ Write email" || !strings.HasPrefix(button["url"], "https://mail.google.com/mail/?") || !strings.Contains(button["url"], "to=alex%40example.com") {
		t.Fatalf("email contact button = %#v", button)
	}
}

func TestContactButtonForTelegramLead(t *testing.T) {
	button := contactButton(models.Lead{Source: models.SourceTelegram, TelegramChatID: 490407716})
	if button == nil || button["url"] != "tg://user?id=490407716" {
		t.Fatalf("telegram contact button = %#v", button)
	}
}

func TestContactButtonForWebTelegramUsername(t *testing.T) {
	button := contactButton(models.Lead{Source: models.SourceWeb, TelegramUsername: "relocating_alex"})
	if button == nil || button["url"] != "https://t.me/relocating_alex" {
		t.Fatalf("web Telegram contact button = %#v", button)
	}
}

func TestTelegramMarkCardKeepsContactButton(t *testing.T) {
	var body struct {
		ReplyMarkup struct {
			InlineKeyboard [][]map[string]string `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/editMessageText" {
			t.Fatalf("unexpected Telegram method path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode card update: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	tg := NewTelegram("token", -100, slog.Default())
	tg.apiBaseURL = server.URL
	tg.cards["deal-1"] = cardState{
		ChatID: -100, MessageID: 5, Text: "card",
		ContactButton: map[string]string{"text": "💬 Write in Telegram", "url": "tg://user?id=42"},
	}
	if err := tg.MarkCard(context.Background(), "deal-1", "Claimed by @manager"); err != nil {
		t.Fatalf("mark card: %v", err)
	}
	if len(body.ReplyMarkup.InlineKeyboard) != 1 || body.ReplyMarkup.InlineKeyboard[0][0]["url"] != "tg://user?id=42" {
		t.Fatalf("contact button was lost after claim: %#v", body.ReplyMarkup.InlineKeyboard)
	}
}
