package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
