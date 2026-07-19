package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anker-relocation/pipeline/pkg/api"
	"github.com/anker-relocation/pipeline/pkg/autoreply"
	"github.com/anker-relocation/pipeline/pkg/crm"
	"github.com/anker-relocation/pipeline/pkg/logging"
	"github.com/anker-relocation/pipeline/pkg/metrics"
	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/notify"
	"github.com/anker-relocation/pipeline/pkg/qualifier"
	"github.com/anker-relocation/pipeline/pkg/queue"
	"github.com/anker-relocation/pipeline/pkg/retry"
	"github.com/anker-relocation/pipeline/pkg/tgbot"
)

// pipeline is the app-level struct that satisfies api.Submitter. It fans a
// single accepted lead out to: CSV (unconditional), the async worker (AI +
// CRM + notify + autoreply), and returns immediately.
type pipeline struct {
	log       *slog.Logger
	csv       *crm.CSV
	crm       crm.CRM // primary CRM (may equal csv)
	qual      qualifier.Qualifier
	q         *queue.Queue
	notifiers []notify.Notifier
	tg        *notify.Telegram // may be nil
	reply     *autoreply.Replier
	taskWG    sync.WaitGroup
	metrics   *metrics.Registry
}

func (p *pipeline) Accept(ctx context.Context, l models.Lead) error {
	// 1. Never lose a lead — CSV first, everything else best-effort.
	if err := p.csv.AppendRaw(l); err != nil {
		p.log.Error("csv append raw failed", "lead_id", l.ID, "err", err)
		return err
	}
	// 2. Fan the rest of the work to the worker queue and return immediately.
	if !p.q.Submit(l) {
		// The raw lead is already durable. Do not create an unbounded fallback
		// goroutine; callers receive a controlled 503 and can retry later.
		return api.ErrUnavailable
	}
	if p.metrics != nil {
		p.metrics.LeadAccepted()
	}
	return nil
}

func (p *pipeline) process(ctx context.Context, l models.Lead) {
	started := time.Now()
	defer func() {
		if p.metrics != nil {
			p.metrics.LeadProcessed(time.Since(started))
		}
	}()
	log := p.log.With("lead_id", l.ID, "source", l.Source)

	// AI qualification.
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	q := p.qual.Qualify(qctx, l)
	cancel()
	log.Info("qualified", "category", q.Category, "urgency", q.Urgency, "fallback", q.Fallback)
	if q.Fallback && p.metrics != nil {
		p.metrics.AIFallback()
	}

	// Persist the qualification in fallback storage even when the primary CRM is
	// available or failing. This keeps a recoverable full history on disk.
	dealID := l.ID
	if _, err := p.csv.Upsert(ctx, l, q); err != nil {
		log.Error("csv qualification append failed", "err", err)
	}
	if p.crm != nil && p.crm.Name() != "csv" {
		var id string
		err := retry.Do(ctx, retry.DefaultPolicy, func(callCtx context.Context) error {
			var upsertErr error
			id, upsertErr = p.crm.Upsert(callCtx, l, q)
			return upsertErr
		})
		if err != nil {
			log.Warn("crm upsert failed, fallback has the record", "crm", p.crm.Name(), "err", err)
			if p.metrics != nil {
				p.metrics.IntegrationError(p.crm.Name())
			}
			p.recordDeadLetter(ctx, l, q, "crm:"+p.crm.Name(), err)
		} else {
			dealID = id
		}
	}

	// Manager notifications and customer replies are independent tasks. Each is
	// retried only for transient failures and leaves a recoverable dead-letter
	// event when the retry budget is exhausted.
	for _, n := range p.notifiers {
		p.taskWG.Add(1)
		go func(nn notify.Notifier) {
			defer p.taskWG.Done()
			err := retry.Do(ctx, retry.DefaultPolicy, func(callCtx context.Context) error {
				return nn.Notify(callCtx, l, q, dealID)
			})
			if err != nil {
				log.Error("notification failed after retries", "notifier", nn.Name(), "err", err)
				if p.metrics != nil {
					p.metrics.IntegrationError(nn.Name())
				}
				p.recordDeadLetter(ctx, l, q, "notify:"+nn.Name(), err)
			}
		}(n)
	}

	// Auto-reply is deliberately not chained behind manager notification.
	if p.reply != nil {
		p.taskWG.Add(1)
		go func() {
			defer p.taskWG.Done()
			err := retry.Do(ctx, retry.DefaultPolicy, func(callCtx context.Context) error {
				return p.reply.Reply(callCtx, l, q)
			})
			if err != nil {
				log.Error("autoreply failed after retries", "err", err)
				if p.metrics != nil {
					p.metrics.IntegrationError("autoreply")
				}
				p.recordDeadLetter(ctx, l, q, "autoreply", err)
			} else if q.Reply != "" && p.metrics != nil {
				p.metrics.AutoReplySent()
			}
		}()
	}
}

func (p *pipeline) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.q == nil || !p.q.Ready() {
		return errors.New("lead queue is not accepting work")
	}
	if p.csv == nil {
		return errors.New("CSV fallback is not configured")
	}
	return p.csv.Check()
}

func (p *pipeline) recordDeadLetter(ctx context.Context, l models.Lead, q models.Qualification, task string, cause error) {
	if err := p.csv.RecordTaskFailure(ctx, l, q, task); err != nil {
		p.log.Error("dead-letter event write failed", "lead_id", l.ID, "task", task, "cause", cause, "err", err)
		return
	}
	p.log.Warn("dead-letter event recorded", "lead_id", l.ID, "task", task, "cause", cause)
}

func (p *pipeline) waitNotifications(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		p.taskWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func main() {
	log := logging.New()
	slog.SetDefault(log)

	port := envOr("PIPELINE_PORT", "8081")
	csvPath := envOr("CSV_PATH", "../data/leads.csv")
	origins := splitCSV(os.Getenv("CORS_ORIGINS"))
	trustedProxies, err := api.ParseTrustedProxyCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		log.Error("invalid trusted proxy CIDRs", "err", err)
		os.Exit(1)
	}

	// --- CSV is always available ---------------------------------------------
	csvSink, err := crm.NewCSV(csvPath)
	if err != nil {
		log.Error("csv init failed", "err", err)
		os.Exit(1)
	}

	// --- Primary CRM: HubSpot > Sheets > CSV --------------------------------
	var primary crm.CRM = csvSink
	if tok := os.Getenv("HUBSPOT_TOKEN"); tok != "" {
		primary = crm.NewHubSpot(tok, os.Getenv("HUBSPOT_PIPELINE_ID"), log)
		log.Info("crm: hubspot")
	} else if sid := os.Getenv("SHEETS_SPREADSHEET_ID"); sid != "" {
		s, err := crm.NewSheets(sid, envOr("SHEETS_TAB", "Leads"), os.Getenv("SHEETS_SA_JSON"), log)
		if err != nil {
			log.Warn("sheets init failed, falling back to csv", "err", err)
		} else {
			primary = s
			log.Info("crm: google_sheets")
		}
	} else {
		log.Info("crm: csv (demo)")
	}

	// --- AI qualifier: OpenRouter or mock ------------------------------------
	var qual qualifier.Qualifier = qualifier.MockQualifier{}
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		to, _ := strconv.Atoi(envOr("OPENROUTER_TIMEOUT_SECONDS", "15"))
		qual = qualifier.NewOpenRouter(qualifier.OpenRouterConfig{
			APIKey:  key,
			Model:   envOr("OPENROUTER_MODEL", "google/gemini-2.5-flash-lite"),
			Timeout: time.Duration(to) * time.Second,
		}, log)
		log.Info("qualifier: openrouter", "model", envOr("OPENROUTER_MODEL", "google/gemini-2.5-flash-lite"))
	} else {
		log.Info("qualifier: mock (demo)")
	}

	// --- Notifiers -----------------------------------------------------------
	var notifiers []notify.Notifier
	var tg *notify.Telegram
	if bt := os.Getenv("TELEGRAM_BOT_TOKEN"); bt != "" {
		cid, _ := strconv.ParseInt(os.Getenv("TELEGRAM_MANAGER_CHAT_ID"), 10, 64)
		tg = notify.NewTelegram(bt, cid, log)
		if cid != 0 {
			notifiers = append(notifiers, tg)
			log.Info("notifier: telegram")
		} else {
			log.Info("telegram bot enabled without manager chat id — intake only")
		}
	}
	if sh := os.Getenv("SLACK_WEBHOOK_URL"); sh != "" {
		notifiers = append(notifiers, notify.NewSlack(sh, os.Getenv("SLACK_SIGNING_SECRET"), log))
		log.Info("notifier: slack")
	}
	if len(notifiers) == 0 {
		notifiers = append(notifiers, notify.Stdout{Log: log})
		log.Info("notifier: stdout (demo)")
	}

	// --- Auto-reply ----------------------------------------------------------
	rep := &autoreply.Replier{Log: log, TG: tg}
	if host := os.Getenv("SMTP_HOST"); host != "" {
		port, _ := strconv.Atoi(envOr("SMTP_PORT", "587"))
		rep.SMTP = &autoreply.SMTPConfig{
			Host: host, Port: port,
			User: os.Getenv("SMTP_USER"), Pass: os.Getenv("SMTP_PASS"),
			From: envOr("SMTP_FROM", "hello@anker-relocation.example"),
		}
		log.Info("autoreply: smtp")
	} else {
		log.Info("autoreply: stdout (demo)")
	}

	// --- Wire pipeline + queue -----------------------------------------------
	p := &pipeline{
		log:       log,
		csv:       csvSink,
		crm:       primary,
		qual:      qual,
		q:         queue.New(128, log),
		notifiers: notifiers,
		tg:        tg,
		reply:     rep,
	}
	p.metrics = metrics.New(p.q.Len)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	p.q.Run(p.process)

	// --- Telegram poll loop (intake + callbacks) -----------------------------
	if tg != nil {
		bot := tgbot.New(tg, p, log)
		go bot.RunCleanup(ctx)
		go tg.Poll(ctx,
			func(cbCtx context.Context, action notify.CallbackAction, dealID, manager string) {
				log.Info("manager callback", "action", action, "deal", dealID, "manager", manager)
				var err error
				var footer string
				switch action {
				case notify.ActionClaim:
					err = primary.MarkClaimed(cbCtx, dealID, manager)
					footer = "Claimed by @" + manager
				case notify.ActionSpam:
					err = primary.MarkSpam(cbCtx, dealID)
					footer = "Marked as spam by @" + manager
				}
				if err != nil {
					log.Warn("crm callback update failed", "err", err)
				}
				if err := tg.MarkCard(cbCtx, dealID, footer); err != nil {
					log.Warn("edit card failed", "err", err)
				}
			},
			bot.Handle,
			bot.HandleCallback,
		)
	}

	// --- HTTP server ---------------------------------------------------------
	apiServer := api.NewWithTrustedProxies(log, p, origins, trustedProxies)
	apiServer.Ready = p.ready
	apiServer.Metrics = p.metrics.WritePrometheus
	apiServer.TelegramBotUsername = os.Getenv("TELEGRAM_BOT_USERNAME")
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           apiServer.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shCtx, shCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shCancel()
	_ = srv.Shutdown(shCtx)
	p.q.Close()
	if err := p.q.Wait(shCtx); err != nil {
		log.Warn("queue drain incomplete", "err", err)
	}
	if err := p.waitNotifications(shCtx); err != nil {
		log.Warn("notification drain incomplete", "err", err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
