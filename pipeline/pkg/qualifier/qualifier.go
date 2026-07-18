package qualifier

import (
	"context"
	"strings"

	"github.com/anker-relocation/pipeline/pkg/models"
)

type Qualifier interface {
	Qualify(ctx context.Context, l models.Lead) models.Qualification
}

// Fallback returns a safe, generic warm qualification. Used when the LLM is
// down, times out, or emits malformed JSON — never let the pipeline drop a
// lead because the model misbehaved.
func Fallback(l models.Lead) models.Qualification {
	est := estimateFromService(l.Service, l.FamilySize)
	summary := "New lead — auto-qualification unavailable, manual review needed."
	reply := "Thanks for reaching out to Anker Relocation! We received your enquiry and will get back to you within 15 minutes with next steps."
	return models.Qualification{
		Category: models.CategoryWarm,
		Urgency:  3,
		Estimate: est,
		Summary:  summary,
		Reply:    reply,
		Fallback: true,
	}
}

func estimateFromService(s models.Service, family int) models.EstimateEUR {
	var min, max int
	switch s {
	case models.ServiceVisaOnly:
		min, max = 800, 1500
	case models.ServiceApartmentOnly:
		min, max = 1000, 2000
	case models.ServiceFullRelocation:
		min, max = 2500, 6000
	default:
		min, max = 1500, 3500
	}
	if family >= 3 {
		min = min * 3 / 2
		max = max * 3 / 2
	}
	return models.EstimateEUR{Min: min, Max: max}
}

// MockQualifier is a keyword-based stand-in that runs the whole pipeline
// end-to-end with zero API keys. Not clever, but deterministic and enough to
// demo hot vs cold vs spam behaviour.
type MockQualifier struct{}

func (MockQualifier) Qualify(_ context.Context, l models.Lead) models.Qualification {
	text := strings.ToLower(l.Name + " " + l.Email + " " + l.Message + " " + l.JobOffer)
	est := estimateFromService(l.Service, l.FamilySize)

	if hasAny(text, spamMarkers) {
		return models.Qualification{
			Category: models.CategorySpam,
			Urgency:  1,
			Estimate: models.EstimateEUR{},
			Summary:  "Looks like spam / unrelated crypto pitch.",
			Reply:    "",
		}
	}
	hot := hasAny(text, hotMarkers) || l.JobOffer == "signed"
	cold := hasAny(text, coldMarkers)
	switch {
	case hot:
		return models.Qualification{
			Category: models.CategoryHot,
			Urgency:  5,
			Estimate: est,
			Summary:  "Signed job offer or concrete relocation timeline — call within the hour.",
			Reply:    hotReply(l, est),
		}
	case cold:
		return models.Qualification{
			Category: models.CategoryCold,
			Urgency:  2,
			Estimate: est,
			Summary:  "Early-stage research, no offer or firm date. Nurture via email.",
			Reply:    coldReply(l),
		}
	default:
		return models.Qualification{
			Category: models.CategoryWarm,
			Urgency:  3,
			Estimate: est,
			Summary:  "Actively planning a move; needs a scoping call to confirm timing.",
			Reply:    warmReply(l, est),
		}
	}
}

var (
	hotMarkers  = []string{"signed", "offer", "start date", "starting", "next month", "6 weeks", "four weeks", "already", "contract"}
	coldMarkers = []string{"someday", "thinking about", "just curious", "maybe next year", "no rush", "researching"}
	spamMarkers = []string{"crypto", "bitcoin", "seo services", "loan", "viagra", "casino"}
)

func hasAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func hotReply(l models.Lead, e models.EstimateEUR) string {
	return "Hi " + firstName(l.Name) + ", thanks for reaching out — this looks like exactly what we handle every week. For your situation the full package usually lands between €" +
		fmtI(e.Min) + " and €" + fmtI(e.Max) + ". I can hop on a 15-minute call today; what timezone are you in?"
}

func warmReply(l models.Lead, e models.EstimateEUR) string {
	return "Hi " + firstName(l.Name) + ", thanks for the details. Based on what you shared, we'd typically scope this between €" +
		fmtI(e.Min) + " and €" + fmtI(e.Max) + ". Want to book a free 15-minute scoping call this week?"
}

func coldReply(l models.Lead) string {
	return "Hi " + firstName(l.Name) + ", happy to help when things firm up. I'll send our Berlin relocation checklist so you can get a head start, and you can reply here once you have a concrete date."
}

func firstName(n string) string {
	if i := strings.IndexByte(n, ' '); i > 0 {
		return n[:i]
	}
	if n == "" {
		return "there"
	}
	return n
}

func fmtI(n int) string {
	// simple thousands formatting without importing text/message
	s := ""
	for n > 0 {
		chunk := n % 1000
		n /= 1000
		if n > 0 {
			s = "," + threeDigits(chunk) + s
		} else {
			s = itoa(chunk) + s
		}
	}
	if s == "" {
		return "0"
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func threeDigits(n int) string {
	var b [3]byte
	for i := 2; i >= 0; i-- {
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[:])
}
