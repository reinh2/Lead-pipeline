# Anker Relocation — AI Lead Pipeline (Demo Case Study)

[Русская версия](README.ru.md) · [Architecture deep-dive](ARCHITECTURE.md) · [Setup guide, RU](USAGE.md)

**An enquiry answered in ~30 seconds instead of 4 hours.** A complete, runnable lead-processing system built around a fictional Berlin relocation agency: a landing page and a Telegram bot capture leads, AI qualifies each one, the CRM gets a record, the manager gets a one-tap action card, and the customer gets an instant personal reply.

![Landing page of the demo relocation agency](docs/screenshots/landing-hero.jpg)

## The problem it solves

Service businesses lose deals in the gap between *"enquiry sent"* and *"manager replied"*. Enquiries arrive at night and on weekends, a large share of them are spam or early-stage browsing, and by the time someone answers the good ones, the customer has already booked a call with a competitor.

This pipeline closes that gap:

- **Reply in seconds, not hours.** The customer gets a personalized answer with a realistic price range the moment they submit — by email or Telegram.
- **Sales sees only leads worth calling.** AI marks each enquiry *hot / warm / cold / spam* with an urgency score, so nobody wastes mornings triaging a shared inbox.
- **Nothing gets lost.** Every lead is written to durable storage *before* the website even confirms receipt. If the CRM, the AI provider, or Telegram is down, the lead survives and the failure is recorded for replay.

> Target scenario: median first response **4 hours → 30 seconds**; ~40% of enquiries auto-flagged as spam or not-ready so the sales team never sees them. Personal data is processed only after explicit consent, enforced server-side.

## What happens to one lead

1. A visitor submits the form (or completes the Telegram bot intake):

![Lead form filled with a demo enquiry](docs/screenshots/landing-form.jpg)

2. The lead is fsync-ed to an append-only event log, the visitor sees "received" — everything after this point is asynchronous and retried.

3. AI (OpenRouter, with a deterministic keyword fallback) classifies it. Real log from this exact demo lead:

```json
{"level":"INFO","msg":"qualified","lead_id":"ld_3a0f62e2a21bed7d","source":"web","category":"hot","urgency":5,"fallback":false}
```

4. The manager's Telegram (or Slack) gets a card with one-tap actions — **Claim** and **Mark spam** update the CRM without opening it:

```
🔥  Hot new lead: Alex Chen (Canadian)
    Service: full_relocation
    Move date: 2026-09-01
    Family size: 3
    Source: web
    Summary: Signed job offer or concrete relocation timeline — call within the hour.
    Estimate: €3,750 – €9,000

    [ ✅ Claim ]   [ 🗑 Mark spam ]   [ 💬 Contact ]
```

5. The CRM (HubSpot or Google Sheets) receives the deal with the same estimate and summary.

6. The customer gets an instant reply matched to their situation:

> Hi Alex, thanks for reaching out — this looks like exactly what we handle every week. For your situation the full package usually lands between €3,750 and €9,000. I can hop on a 15-minute call today; what timezone are you in?

## Run it yourself in 5 minutes

No API keys required — without them the pipeline uses a mock qualifier, CSV storage and stdout notifications, so the whole flow is visible locally:

```bash
cd lead-pipeline
cp .env.example .env
make up             # docker compose: landing on :8080, pipeline on :8081
make demo-lead      # submit a realistic test lead
make demo-lead-hot  # a lead with a signed offer moving in 6 weeks
```

Open <http://localhost:8080>, submit the form, and watch `docker compose logs -f pipeline` show the qualification verdict, the CRM write and the manager card. Use fictional data only.

## Integrations

Each integration switches on when its keys appear in `.env`; everything else keeps working without it.

| Integration | What it gives you | Without keys |
|---|---|---|
| OpenRouter (any LLM) | Real AI qualification and reply drafting | Keyword-based mock qualifier |
| HubSpot | Deals created/updated in your sales pipeline | Google Sheets, then CSV |
| Google Sheets | A lightweight CRM your team already knows | CSV event log |
| Telegram | Intake bot + manager cards with Claim/Spam buttons | Cards printed to logs |
| Slack | Manager notifications in a channel | — |
| SMTP | Instant auto-reply emails to customers | Reply printed to logs |

## The boring parts, done right

The demo is small, but it is built the way production systems are:

- Write-ahead event log: a lead is on disk before the HTTP 202; CRM/notify/reply failures leave replayable dead-letter events.
- Bounded queue with backpressure (a controlled 503 instead of silent loss), graceful shutdown that drains in-flight work.
- Server-side validation, GDPR-consent enforcement, rate limiting, CORS and trusted-proxy handling.
- `/healthz`, `/readyz`, Prometheus `/metrics`, JSON logs, non-root container, CI with race-detector tests, a Docker Compose smoke test and a config vulnerability scan.
- Automatic migration of older on-disk data schemas, with timestamped backups and verifiable backup/restore scripts.

Details, diagrams and trade-offs: [ARCHITECTURE.md](ARCHITECTURE.md).

## Adapt it to your business

Relocation is just the example scenario. The same pipeline fits any business that lives on inbound enquiries — dental and medical clinics, real-estate and rental agencies, auto services, legal and immigration consultants, B2B services. What changes is the form fields, the qualification prompt and the reply templates; the reliability core stays the same. The AI provider, CRM and notification channels are pluggable adapters.

## Honest limitations

This is a **demo case study**, not a hosted product:

- The company, testimonials, prices and metrics on the landing are clearly labeled fictional; the response-time numbers above are the target scenario, not production measurements.
- Not GDPR-compliant as-is: a real deployment needs HTTPS, a named data controller, retention/deletion workflows.
- Google Sheets is a lightweight single-instance CRM, not a multi-user CRM replacement; some notification providers may deliver duplicates on retry.
- A demo video is planned; screenshots above are from the running demo.

## Tech stack

Go 1.22 (standard library only — `net/http`, `slog`, no frameworks) · static HTML/CSS/JS landing · nginx · Docker Compose · OpenRouter · HubSpot & Google Sheets & Telegram Bot APIs · GitHub Actions CI.

```bash
cd pipeline && go test ./...   # race-detector tests run in CI on every push
```

Project layout, diagrams and design decisions: [ARCHITECTURE.md](ARCHITECTURE.md). Russian-language setup and configuration guide: [USAGE.md](USAGE.md). Roadmap: [ROADMAP.md](ROADMAP.md).
