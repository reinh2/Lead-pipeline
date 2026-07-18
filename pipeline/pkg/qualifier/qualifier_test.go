package qualifier

import (
	"context"
	"strings"
	"testing"

	"github.com/anker-relocation/pipeline/pkg/models"
)

func TestParse_Clean(t *testing.T) {
	in := `{"category":"hot","urgency":5,"estimate_eur":{"min":3000,"max":6000},"summary":"looks great","reply":"hi"}`
	q, err := ParseQualification(in)
	if err != nil {
		t.Fatal(err)
	}
	if q.Category != models.CategoryHot || q.Estimate.Min != 3000 {
		t.Fatalf("bad parse: %+v", q)
	}
}

func TestParse_WithMarkdownFence(t *testing.T) {
	in := "```json\n{\"category\":\"warm\",\"urgency\":3,\"estimate_eur\":{\"min\":1,\"max\":2},\"summary\":\"x\",\"reply\":\"y\"}\n```"
	q, err := ParseQualification(in)
	if err != nil {
		t.Fatal(err)
	}
	if q.Category != models.CategoryWarm {
		t.Fatalf("bad: %+v", q)
	}
}

func TestParse_ProseAroundJSON(t *testing.T) {
	in := "Sure! Here's my JSON:\n{\"category\":\"cold\",\"urgency\":2,\"estimate_eur\":{\"min\":1000,\"max\":2000},\"summary\":\"s\",\"reply\":\"r\"}\nhope this helps"
	q, err := ParseQualification(in)
	if err != nil {
		t.Fatal(err)
	}
	if q.Category != models.CategoryCold {
		t.Fatalf("bad: %+v", q)
	}
}

func TestParse_MalformedRejected(t *testing.T) {
	if _, err := ParseQualification("not JSON at all"); err == nil {
		t.Fatal("want error")
	}
	if _, err := ParseQualification(`{"category":"lukewarm","urgency":3,"estimate_eur":{"min":0,"max":0},"summary":"x","reply":"y"}`); err == nil {
		t.Fatal("want invalid category error")
	}
}

func TestFallback_NeverEmpty(t *testing.T) {
	q := Fallback(models.Lead{Service: models.ServiceFullRelocation, FamilySize: 4})
	if q.Category != models.CategoryWarm || !q.Fallback || q.Estimate.Max == 0 || q.Reply == "" {
		t.Fatalf("bad fallback: %+v", q)
	}
}

func TestMock_HotOnSignedOffer(t *testing.T) {
	l := models.Lead{Name: "Alex Chen", Service: models.ServiceFullRelocation, FamilySize: 3, Message: "signed offer with N26 start Sep 1"}
	q := MockQualifier{}.Qualify(context.Background(), l)
	if q.Category != models.CategoryHot {
		t.Fatalf("want hot got %s", q.Category)
	}
	if !strings.Contains(q.Reply, "Alex") {
		t.Fatalf("reply should personalise: %q", q.Reply)
	}
}

func TestMock_SpamOnCrypto(t *testing.T) {
	l := models.Lead{Name: "Bot", Message: "10x crypto returns and bitcoin airdrops"}
	q := MockQualifier{}.Qualify(context.Background(), l)
	if q.Category != models.CategorySpam {
		t.Fatalf("want spam got %s", q.Category)
	}
}

func TestMock_ColdOnVague(t *testing.T) {
	l := models.Lead{Name: "Jamie", Service: models.ServiceFullRelocation, Message: "just curious, thinking about moving someday"}
	q := MockQualifier{}.Qualify(context.Background(), l)
	if q.Category != models.CategoryCold {
		t.Fatalf("want cold got %s", q.Category)
	}
}
