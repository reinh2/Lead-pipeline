package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestWritePrometheus(t *testing.T) {
	r := New(func() int { return 4 })
	r.LeadAccepted()
	r.LeadProcessed(150 * time.Millisecond)
	r.AIFallback()
	r.AutoReplySent()
	r.IntegrationError("telegram")
	var b strings.Builder
	r.WritePrometheus(&b)
	text := b.String()
	for _, want := range []string{
		"lead_pipeline_leads_accepted_total 1",
		"lead_pipeline_queue_depth 4",
		`lead_pipeline_integration_errors_total{integration="telegram"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
}
