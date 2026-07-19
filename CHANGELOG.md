# Changelog

Все заметные изменения этого demo case study фиксируются здесь.

## [0.1.0] — 2026-07-19

### Added

- Лендинг и Telegram-анкета как два канала приёма лидов.
- AI-квалификация через OpenRouter с безопасным mock fallback.
- Google Sheets CRM через Apps Script без Google Cloud и service-account ключей.
- Карточки менеджера в Telegram: Claim, Spam и прямая связь с лидом по Telegram или email.
- Выбор канала ответа на лендинге: email либо Telegram username.
- CSV event log, dead-letter записи, `/readyz`, `/metrics`, Docker Compose, CI и Trivy config scan.

### Fixed

- Карточки веб-лидов больше не отклоняются Telegram из-за недопустимого `mailto:` URL: кнопка email открывает готовое письмо в Gmail.
- Таймаут Telegram long-poll согласован с 25-секундным polling request.
- Ошибки сетевых запросов Telegram больше не включают bot token в логи.
- Healthcheck лендинга использует IPv4 loopback и не становится ложным `unhealthy` из-за IPv6 `localhost`.

### Release checklist

```bash
make release-check
make up
```

Затем откройте <http://localhost:8080>, отправьте тестовую заявку и проверьте карточку в менеджерской Telegram-группе.
