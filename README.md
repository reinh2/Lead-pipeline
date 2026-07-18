# Anker Relocation — конвейер обработки лидов

Автоматизация быстрого ответа на обращения для вымышленного берлинского релокейт-агентства, помогающего экспатам с визами, жильём и регистрацией по месту жительства (Anmeldung). У системы два входа — лендинг и Telegram-бот; далее лид квалифицируется AI, записывается в CRM и отправляется менеджеру с интерактивными кнопками.

Проект целиком запускается в **демо-режиме без API-ключей**. Чтобы включить реальные интеграции, достаточно постепенно заполнить соответствующие переменные в `.env`.

## Конвейер

```mermaid
flowchart LR
    A[Форма на лендинге] -->|POST /api/leads| Q[Очередь]
    B[Анкета Telegram-бота] -->|внутренняя отправка| Q
    Q --> AI[AI-квалификатор<br/>OpenRouter или mock]
    AI --> CRM[CRM<br/>HubSpot / Sheets / CSV]
    AI --> N[Уведомление менеджеру<br/>Telegram / Slack]
    AI --> R[Автоответ<br/>Email или Telegram-бот]
    N -.callback.-> CRM
```

Каждый этап выполняется в режиме best effort: ошибка на одном этапе не должна привести к потере лида. Лид сначала записывается в CSV, а затем обрабатывается асинхронно.

## Быстрый старт за 5 минут

```bash
cd lead-pipeline
cp .env.example .env
make up             # docker compose: лендинг (nginx :8080) + pipeline (:8081)
make demo-lead      # отправить реалистичный тестовый лид с лендинга
make demo-lead-hot  # лид с подписанным оффером и переездом через 6 недель
```

Затем откройте [http://localhost:8080](http://localhost:8080) и отправьте форму. В `docker compose logs -f pipeline` будут видны вердикт квалификатора, запись в `data/leads.csv` и карточка уведомления, выведенная в stdout.

Запуск для разработки без Docker:

```bash
cd pipeline
go run ./cmd/pipeline    # слушает :8081, использует ../data/leads.csv
```

## Интеграции

| Интеграция | Переменные окружения | Поведение без настройки |
|---|---|---|
| OpenRouter (AI-квалификация) | `OPENROUTER_API_KEY`, `OPENROUTER_MODEL` | Используется mock-квалификатор по ключевым словам |
| HubSpot CRM | `HUBSPOT_TOKEN` | Используются Google Sheets, если они настроены; иначе CSV |
| Google Sheets CRM | `SHEETS_SPREADSHEET_ID`, `SHEETS_SA_JSON` | Используется CSV |
| Telegram-бот и уведомления менеджеру | `TELEGRAM_BOT_TOKEN`, `TELEGRAM_MANAGER_CHAT_ID` | Уведомления выводятся в stdout, бот выключен |
| Slack | `SLACK_WEBHOOK_URL`, `SLACK_SIGNING_SECRET` | Slack-адаптер не регистрируется |
| SMTP-автоответ | `SMTP_HOST`, `SMTP_USER`, `SMTP_PASS`, `SMTP_FROM` | Исходящее письмо выводится в stdout |

Настраивайте только те интеграции, ключи для которых уже есть: остальные компоненты должны продолжать работать в деградированном режиме.

## Сценарий использования

> «Медианное время первого ответа: 4 часа → 30 секунд. 40% обращений автоматически помечаются как неготовые к покупке или спам, поэтому отдел продаж видит только лиды, с которыми стоит связаться. Обработка данных возможна только после согласия пользователя».

## Структура проекта

```
lead-pipeline/
├── landing/                # статический HTML-лендинг + Tailwind CDN
├── pipeline/               # Go-сервис
│   ├── cmd/pipeline/       # точка входа
│   └── pkg/
│       ├── api/            # HTTP, rate limit и валидация
│       ├── autoreply/      # email- или Telegram-автоответ
│       ├── crm/            # HubSpot / Sheets / CSV
│       ├── logging/        # вспомогательные функции slog
│       ├── models/         # Lead и Qualification
│       ├── notify/         # адаптеры Telegram и Slack
│       ├── qualifier/      # OpenRouter и mock-квалификатор
│       ├── queue/          # внутренняя очередь с воркером
│       └── tgbot/          # анкета в Telegram через long polling
├── data/                   # резервный CSV с лидами
├── docker-compose.yml
├── Makefile
└── .env.example
```

## Тесты

```bash
cd pipeline && go test ./...
```

Покрыты: разбор JSON от квалификатора и fallback при некорректном ответе, валидация формы и согласия на обработку данных, state machine Telegram-анкеты, построение карточек уведомлений, проверка подписи Slack и интерфейс CRM через mock.

Подробное русскоязычное руководство по запуску и настройке находится в [USAGE.md](USAGE.md).
