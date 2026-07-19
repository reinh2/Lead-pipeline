# Anker Relocation — Lead Pipeline

[Русская версия](README.md)

This is a **demo case study** for a relocation-service lead pipeline. A landing form and Telegram intake write each lead durably before asynchronous qualification, CRM export, manager notification and customer auto-reply.

## Run the demo

```bash
cd lead-pipeline
cp .env.example .env
make up
```

- Landing: <http://localhost:8080>
- Readiness: <http://localhost:8081/readyz>
- Metrics: <http://localhost:8081/metrics>
- Quality gate: `make check`

No API keys are needed: the demo uses a mock qualifier, CSV event log and stdout notifications. Use fictional data only.

## Design choices

| Area | Demo behaviour | Production direction |
|---|---|---|
| Durable fallback | fsync CSV event log before HTTP `202` | PostgreSQL `leads` + `lead_events` |
| Queue | Bounded in-memory worker queue | Outbox plus SQS, NATS JetStream, Redis Streams or a worker fleet |
| AI | OpenRouter with keyword fallback | Provider monitoring, quality review and explicit limits |
| Retry | Context-aware bounded retry for transient failures | Persistent retry queue and operator replay UI |
| Observability | `/readyz`, `/metrics`, JSON logs and CSV backup scripts | Prometheus/Alertmanager and centralized logs |

## Safety and limitations

- This is not a real company website. All testimonials, metrics, prices and outcomes are clearly marked as demo data.
- It is not GDPR compliant without a real privacy process, retention/deletion workflow, HTTPS and a designated data controller.
- Google Sheets is an append-only export; CSV remains the demo fallback event log.
- Some notification providers have no idempotency key, so a retry may produce a duplicate notification. Exhausted tasks are recorded as `dead_letter:*` events.

See the [Russian README](README.md) for the architecture and lead sequence diagrams, integration configuration and backup/restore commands.
