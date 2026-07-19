// Package metrics exposes the small, dependency-free Prometheus surface the
// demo needs to answer where leads are flowing or failing.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Registry holds bounded-cardinality service metrics. Integration names are
// controlled by the application, never copied from request data.
type Registry struct {
	accepted          atomic.Uint64
	processed         atomic.Uint64
	fallbackAI        atomic.Uint64
	autoReplies       atomic.Uint64
	processingNanos   atomic.Uint64
	queueDepth        func() int
	mu                sync.Mutex
	integrationErrors map[string]uint64
}

func New(queueDepth func() int) *Registry {
	return &Registry{queueDepth: queueDepth, integrationErrors: make(map[string]uint64)}
}

func (r *Registry) LeadAccepted() { r.accepted.Add(1) }
func (r *Registry) LeadProcessed(d time.Duration) {
	r.processed.Add(1)
	r.processingNanos.Add(uint64(d))
}
func (r *Registry) AIFallback()    { r.fallbackAI.Add(1) }
func (r *Registry) AutoReplySent() { r.autoReplies.Add(1) }

func (r *Registry) IntegrationError(integration string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.integrationErrors[integration]++
}

// WritePrometheus renders metrics in the Prometheus text exposition format.
func (r *Registry) WritePrometheus(w io.Writer) {
	counter(w, "lead_pipeline_leads_accepted_total", "Leads accepted for asynchronous processing.", r.accepted.Load())
	counter(w, "lead_pipeline_leads_processed_total", "Leads whose qualification completed.", r.processed.Load())
	counter(w, "lead_pipeline_ai_fallback_total", "Qualifications produced by the local fallback.", r.fallbackAI.Load())
	counter(w, "lead_pipeline_autoreplies_sent_total", "Successful auto-reply tasks, including demo mode.", r.autoReplies.Load())

	count := r.processed.Load()
	seconds := float64(r.processingNanos.Load()) / float64(time.Second)
	fmt.Fprintln(w, "# HELP lead_pipeline_processing_duration_seconds_total Total lead processing time in seconds.")
	fmt.Fprintln(w, "# TYPE lead_pipeline_processing_duration_seconds_total counter")
	fmt.Fprintf(w, "lead_pipeline_processing_duration_seconds_total %.9f\n", seconds)
	fmt.Fprintln(w, "# HELP lead_pipeline_processing_duration_seconds_count Number of completed processing measurements.")
	fmt.Fprintln(w, "# TYPE lead_pipeline_processing_duration_seconds_count counter")
	fmt.Fprintf(w, "lead_pipeline_processing_duration_seconds_count %d\n", count)

	depth := 0
	if r.queueDepth != nil {
		depth = r.queueDepth()
	}
	fmt.Fprintln(w, "# HELP lead_pipeline_queue_depth Current number of queued leads.")
	fmt.Fprintln(w, "# TYPE lead_pipeline_queue_depth gauge")
	fmt.Fprintf(w, "lead_pipeline_queue_depth %d\n", depth)

	r.mu.Lock()
	names := make([]string, 0, len(r.integrationErrors))
	for name := range r.integrationErrors {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make(map[string]uint64, len(names))
	for _, name := range names {
		values[name] = r.integrationErrors[name]
	}
	r.mu.Unlock()
	fmt.Fprintln(w, "# HELP lead_pipeline_integration_errors_total Failed integration tasks after retries.")
	fmt.Fprintln(w, "# TYPE lead_pipeline_integration_errors_total counter")
	for _, name := range names {
		fmt.Fprintf(w, "lead_pipeline_integration_errors_total{integration=%q} %d\n", escapeLabel(name), values[name])
	}
}

func counter(w io.Writer, name, help string, value uint64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
}

func escapeLabel(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(s)
}
