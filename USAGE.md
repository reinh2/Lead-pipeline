# Полный гайд по использованию Anker Relocation Lead Pipeline

Этот документ — пошаговое руководство от «клонировал репозиторий» до «работает в проде со всеми интеграциями». Все команды предполагают, что вы находитесь в папке `lead-pipeline/`.

## Оглавление

1. [Что вы получаете](#что-вы-получаете)
2. [Требования](#требования)
3. [Быстрый старт (demo-режим, 2 минуты)](#быстрый-старт-demo-режим-2-минуты)
4. [Как устроен проект](#как-устроен-проект)
5. [Локальная разработка без Docker](#локальная-разработка-без-docker)
6. [Проверка того, что всё работает](#проверка-того-что-всё-работает)
7. [Подключение реальных интеграций](#подключение-реальных-интеграций)
   - [OpenRouter — AI-квалификация](#1-openrouter--ai-квалификация)
   - [Telegram — бот-приёмник + уведомления менеджеру](#2-telegram--бот-приёмник--уведомления-менеджеру)
   - [HubSpot CRM](#3-hubspot-crm)
   - [Google Sheets](#4-google-sheets-запасной-crm)
   - [SMTP — авто-ответ по email](#5-smtp--авто-ответ-по-email)
   - [Slack (опционально)](#6-slack-опциональный-второй-канал-уведомлений)
8. [Демо-сценарии для показа заказчику](#демо-сценарии-для-показа-заказчику)
9. [Как менеджер работает с лидом](#как-менеджер-работает-с-лидом)
10. [Отладка и логи](#отладка-и-логи)
11. [Тесты](#тесты)
12. [Деплой в прод](#деплой-в-прод)
13. [Частые вопросы](#частые-вопросы)

---

## Что вы получаете

Систему автоматизации входящих обращений для условного релокейт-агентства:

- **Лендинг** (`landing/`) — статический одностраничник с формой заявки и кнопкой «написать в Telegram».
- **Go-сервис** (`pipeline/`) — принимает лиды с формы и из Telegram-бота, прогоняет через AI-квалификатор, записывает в CRM, мгновенно шлёт менеджеру карточку с кнопками и отправляет клиенту авто-ответ.
- **Demo-режим** — всё работает end-to-end без единого API-ключа.
- **Постепенное включение интеграций** — можно подключать OpenRouter/HubSpot/Telegram/SMTP/Slack по одному, по мере готовности ключей.

Ключевая идея — **speed-to-lead**: пока конкуренты отвечают экспату через 4 часа, вы отвечаете за 30 секунд.

---

## Требования

Для demo-режима хватит одного из двух:

- **Docker Desktop** ≥ 4.30 (умеет `docker compose`) — самый простой путь;
- **Go 1.22+** — если хотите запускать бинарник напрямую.

Для реальных интеграций дополнительно понадобятся аккаунты в тех сервисах, которые вы хотите включить (OpenRouter, HubSpot, Telegram, Google Cloud, SMTP-провайдер, Slack). Их можно подключать по одному.

---

## Быстрый старт (demo-режим, 2 минуты)

```bash
cd lead-pipeline
cp .env.example .env       # ничего в него писать НЕ нужно для демо
make up                    # docker compose: nginx (:8080) + Go-сервис (:8081)
```

Проверка:

```bash
curl http://localhost:8081/healthz
# {"ok":true}
```

Отправка тестового лида:

```bash
make demo-lead-hot         # «подписан оффер, переезжаю через 6 недель»
make demo-lead             # обычный лид «интервьюируюсь»
```

Открыть в браузере: <http://localhost:8080> — на лендинге заполнить форму и нажать «Send enquiry».

Логи в реальном времени:

```bash
make logs
```

Что вы там увидите на каждый лид:

- `qualified category=hot urgency=5 fallback=false` — вердикт AI;
- `manager notification` — карточка для менеджера (в demo уходит в stdout);
- `autoreply (email demo)` — текст авто-ответа клиенту (в demo тоже в stdout);
- в файле `data/leads.csv` появляются строки: сначала сырой лид (status=new), потом обогащённый (status=hot|warm|cold|spam).

Остановить:

```bash
make down
```

---

## Как устроен проект

```
lead-pipeline/
├── landing/                  # статический сайт (HTML + Tailwind CDN)
│   ├── index.html            # одностраничник с формой
│   └── nginx.conf            # проксирует /api/ в pipeline
├── pipeline/                 # Go-сервис
│   ├── cmd/pipeline/main.go  # точка входа, вся склейка
│   └── pkg/
│       ├── api/              # HTTP, валидация, CORS, rate-limit
│       ├── qualifier/        # AI-классификация (OpenRouter + mock)
│       ├── crm/              # HubSpot / Google Sheets / CSV
│       ├── notify/           # Telegram и Slack адаптеры
│       ├── tgbot/            # анкета в Telegram (state machine)
│       ├── autoreply/        # авто-ответ клиенту (SMTP / TG DM)
│       ├── queue/            # in-memory очередь с воркером
│       ├── models/           # Lead, Qualification
│       └── logging/          # slog helpers
├── data/leads.csv            # запасное хранилище лидов
├── docker-compose.yml
├── Makefile
├── .env.example              # шаблон конфигурации
├── README.md                 # краткое описание
└── USAGE.md                  # этот файл
```

Как летит один лид через систему:

```
Форма на лендинге ─┐
                   ├─► POST /api/leads ─► CSV (всегда) ─► очередь ─► AI ─► CRM ─► уведомление менеджеру
Telegram-бот ──────┘                                                                  │
                                                                                       └─► авто-ответ клиенту
```

Главный принцип: **лид не теряется никогда**. CSV пишется до попытки прогнать AI/CRM/уведомления. Если OpenRouter упал — работает fallback. Если HubSpot ответил 500 — уведомление всё равно уйдёт, а лид останется в CSV.

---

## Локальная разработка без Docker

Если вы правите Go-код и хотите быстрый цикл:

```bash
cd pipeline
go run ./cmd/pipeline
# сервис слушает :8081, пишет CSV в ../data/leads.csv
```

Отдельно в другом терминале можно открыть лендинг напрямую как файл (`landing/index.html`) — но тогда форма не сможет достучаться до `/api/leads` из-за CORS. Проще всего для UI-правок оставить `make up` и открывать <http://localhost:8080>, а Go-сервис перезапускать через:

```bash
docker compose restart pipeline
```

Для чистых unit-тестов Docker не нужен:

```bash
cd pipeline && go test ./...
```

---

## Проверка того, что всё работает

Полезные ручные проверки:

```bash
# 1. Health-check
curl http://localhost:8081/healthz

# 2. Валидация: без согласия GDPR должен быть 400
curl -i -X POST http://localhost:8081/api/leads \
  -H 'Content-Type: application/json' \
  -d '{"name":"X","email":"x@y.z","nationality":"N","service":"visa_only","family_size":1,"gdpr_consent":false}'
# HTTP/1.1 400 Bad Request
# {"error":"GDPR consent is required"}

# 3. Валидный лид
make demo-lead-hot

# 4. Rate-limit: 10 запросов с одного IP за минуту — 11-й должен получить 429
for i in $(seq 1 11); do
  curl -o /dev/null -s -w "%{http_code}\n" -X POST http://localhost:8081/api/leads \
    -H 'Content-Type: application/json' \
    -d '{"name":"T","email":"t@t.t","nationality":"X","service":"visa_only","family_size":1,"gdpr_consent":true}'
done
```

Проверка CSV:

```bash
column -t -s , data/leads.csv | tail
```

---

## Подключение реальных интеграций

Все интеграции необязательные. Включайте по одной, проверяя после каждой `make demo-lead`.

Общий рабочий процесс:

1. Открыть `.env`.
2. Заполнить блок нужной интеграции.
3. Перезапустить сервис: `make down && make up` (или `docker compose restart pipeline`).
4. В логе `make logs` вы увидите строчку вида `notifier: telegram` вместо `notifier: stdout (demo)`.
5. Прогнать `make demo-lead-hot` и посмотреть результат в реальном сервисе.

### 1. OpenRouter — AI-квалификация

Заменяет keyword-based mock на настоящую LLM.

1. Зарегистрируйтесь на <https://openrouter.ai>.
2. Профиль → Keys → создать ключ.
3. В `.env`:

   ```env
   OPENROUTER_API_KEY=sk-or-v1-...
   OPENROUTER_MODEL=google/gemini-2.5-flash-lite
   OPENROUTER_TIMEOUT_SECONDS=15
   ```

4. `make down && make up`.
5. `make demo-lead-hot` — в логе должно быть `qualifier: openrouter model=...` и `qualified ... fallback=false`.

Что происходит: сервис отправляет системный промпт + 4 few-shot примера + текущий лид, требует JSON-ответ со схемой `{category, urgency, estimate_eur, summary, reply}`. При ошибке — 3 ретрая с backoff, потом безопасный fallback (`category=warm`), лид не теряется.

**Смена модели.** Просто поменяйте `OPENROUTER_MODEL`. Например `anthropic/claude-haiku-4-5`, `openai/gpt-4o-mini` — любая модель из каталога OpenRouter, поддерживающая JSON-режим.

### 2. Telegram — бот-приёмник + уведомления менеджеру

Один и тот же бот выполняет две роли: (а) ведёт анкету с клиентом, (б) шлёт менеджерам карточки новых лидов с кнопками.

**Шаг 1. Создать бота.**

- В Telegram написать [@BotFather](https://t.me/BotFather).
- Команда `/newbot` → задать имя (например `Anker Relocation Bot`) и юзернейм (например `anker_relocation_bot`).
- BotFather выдаст токен вида `1234567890:AAG...` — это `TELEGRAM_BOT_TOKEN`.

**Шаг 2. Узнать chat_id менеджерской группы.**

- Создайте группу в Telegram, добавьте туда своих менеджеров и вашего бота.
- Дайте боту право писать в группу.
- Добавьте в ту же группу [@RawDataBot](https://t.me/RawDataBot) на 30 секунд — он пришлёт JSON с `chat.id`. Для супергрупп id начинается с `-100...`.
- Удалите RawDataBot из группы.

**Шаг 3. Прописать в `.env`:**

```env
TELEGRAM_BOT_TOKEN=1234567890:AAG...
TELEGRAM_MANAGER_CHAT_ID=-1001234567890
TELEGRAM_BOT_USERNAME=anker_relocation_bot
```

**Шаг 4.** Перезапустить сервис. В логах: `notifier: telegram`.

**Шаг 5. Проверка.**

- `make demo-lead-hot` → в менеджерской группе появляется карточка с эмодзи 🔥, полями лида, оценкой в EUR и двумя кнопками «✅ Claim lead» / «🗑 Mark spam».
- Нажать «Claim lead» → карточка обновится подписью «Claimed by @yourusername», и в CRM статус изменится.

**Шаг 6. Бот-анкета.**

- Клиент открывает `t.me/anker_relocation_bot`, жмёт Start.
- Бот задаёт 4 вопроса (гражданство, статус оффера — inline-кнопки, дата переезда, размер семьи) + свободный текст.
- После «done» лид уходит в тот же пайплайн, что и с формы, но с `source=telegram` и `chat_id`, чтобы авто-ответ пришёл прямо в бот.

Не забудьте обновить кнопку на лендинге — она ведёт на `https://t.me/<TELEGRAM_BOT_USERNAME>` (сейчас захардкожен `anker_relocation_bot`; при желании поменяйте в `landing/index.html`, ищите `id="tg-cta"`).

### 3. HubSpot CRM

Основное хранилище лидов, если оно у вас настроено. Если нет — сервис автоматически откатится на Sheets или CSV.

**Шаг 1. Создать private app в HubSpot.**

- Settings (шестерёнка справа сверху) → Integrations → Private Apps → Create private app.
- Basic info: имя `Anker Pipeline`.
- Scopes: включить `crm.objects.contacts.read`, `crm.objects.contacts.write`, `crm.objects.deals.read`, `crm.objects.deals.write`.
- Create → скопировать токен (`pat-eu1-...` или `pat-na1-...`).

**Шаг 2. `.env`:**

```env
HUBSPOT_TOKEN=pat-eu1-XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX
# HUBSPOT_PIPELINE_ID=default   # оставьте пустым — возьмётся дефолтный sales-пайплайн
```

**Шаг 3.** Перезапуск, логи: `crm: hubspot`.

**Шаг 4. Проверка.**

- `make demo-lead-hot`.
- В HubSpot → Contacts: появится Priya Sharma / Alex Chen с nationality/country.
- Contacts → выбранный контакт → Deals: привязана сделка «Alex Chen — full_relocation» с amount = среднему из оценки AI (для hot — €6,375), стадия `qualifiedtobuy`.
- Нажать в Telegram «Claim lead» — стадия переедет в `appointmentscheduled`, поле `claimed_by` заполнится юзернеймом менеджера. Нажать «Mark spam» — стадия `closedlost`, причина `spam`.

Перед включением создайте custom properties: `lead_id`, `qualification`, `source`, `claimed_by` (для Deal) и `nationality`, `consent_version`, `consent_timestamp` (для Contact). Повторная доставка ищет Deal по `lead_id`, обновляет его и не создаёт дубль; Contact по-прежнему ищется по email.

### 4. Google Sheets (запасной CRM)

Хорошо, если HubSpot у вас пока нет, а видеть лиды нужно.

**Шаг 1. Создать таблицу.**

- Google Sheets → создать новую таблицу «Anker Leads».
- Первая вкладка → переименовать в `Leads`.
- В первой строке создайте точную CSV-шапку: `lead_id, created_at, source, name, email, nationality, current_country, service, move_date, family_size, job_offer, message, gdpr_consent, consent_version, consented_at, category, urgency, estimate_min_eur, estimate_max_eur, summary, status`. Сервис проверяет её до первой записи.
- Из URL достать spreadsheet id: `https://docs.google.com/spreadsheets/d/<ID>/edit`.

**Шаг 2. Service account.**

- Google Cloud Console → создать проект (или взять существующий).
- APIs & Services → Enable APIs → включить **Google Sheets API**.
- IAM & Admin → Service Accounts → Create service account → имя `anker-sheets`.
- В созданном аккаунте вкладка Keys → Add key → JSON → сохранить файл, например как `secrets/sheets_sa.json`.

**Шаг 3. Дать доступ.**

- Открыть JSON — там `client_email` вида `anker-sheets@<project>.iam.gserviceaccount.com`.
- В Google Sheets нажать Share → добавить этот email с правами Editor.

**Шаг 4. `.env`:**

```env
SHEETS_SPREADSHEET_ID=1AbCd...
SHEETS_SA_JSON=/run/secrets/sheets_sa.json
SHEETS_TAB=Leads
```

**Шаг 5. Подмонтировать ключ read-only.** Не меняйте основной compose-файл. Используйте готовый overlay:

```bash
SHEETS_SA_FILE="$PWD/secrets/sheets_sa.json" \
  docker compose -f docker-compose.yml -f docker-compose.sheets.yml up -d
```

В `.env` укажите `SHEETS_SA_JSON=/run/secrets/sheets_sa.json`.

**Шаг 6.** Перезапуск, логи: `crm: google_sheets`. `make demo-lead-hot` → в таблице появляется новая строка. Sheets — append-only экспорт; изменение статуса хранится в CSV fallback.

Важно: если задан и `HUBSPOT_TOKEN`, и `SHEETS_SPREADSHEET_ID` — приоритет у HubSpot. Sheets срабатывает только когда HubSpot не сконфигурирован.

### 5. SMTP — авто-ответ по email

Чтобы клиент, приславший форму, получил письмо с текстом от AI в течение 30 секунд.

Подойдёт любой SMTP-провайдер: SendGrid, Mailgun, Amazon SES, Postmark, Zoho, Gmail (только для тестов, не для прода). Пример для SendGrid:

1. Завести аккаунт SendGrid, подтвердить домен-отправитель.
2. Создать API-ключ с правом `Mail Send`.
3. `.env`:

   ```env
   SMTP_HOST=smtp.sendgrid.net
   SMTP_PORT=587
   SMTP_USER=apikey
   SMTP_PASS=SG.xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
   SMTP_FROM="Anker Relocation <hello@anker-relocation.example>"
   ```

4. Перезапуск, логи: `autoreply: smtp`.
5. `make demo-lead-hot` — на `alex@example.com` уйдёт письмо (в реальности замените адрес в Makefile на свой, чтобы увидеть).

Порт 465 — TLS с самого начала; 587 — STARTTLS. Оба поддержаны.

### 6. Slack (опциональный второй канал уведомлений)

Если менеджеры сидят и в Slack, и в Telegram — можно дублировать.

**Шаг 1. Incoming Webhook.**

- <https://api.slack.com/apps> → Create New App → From scratch → имя `Anker Pipeline`, ваш workspace.
- Features → Incoming Webhooks → Activate → Add New Webhook → выбрать канал → скопировать URL вида `https://hooks.slack.com/services/T.../B.../...`.

**Шаг 2. Signing secret** (нужен только если хотите отдельный HTTP-endpoint для кнопок Slack; в этой сборке сам сервис пока обрабатывает только Telegram-callback'и, но проверка подписи в `pkg/notify/slack.go` уже готова и покрыта тестами).

- В том же приложении: Basic Information → Signing Secret → Copy.

**Шаг 3. `.env`:**

```env
SLACK_WEBHOOK_URL=https://hooks.slack.com/services/T.../B.../XXXX
SLACK_SIGNING_SECRET=xxxxxxxxxxxxxxxxxx
```

**Шаг 4.** Перезапуск, логи: `notifier: slack` (одновременно с Telegram, если оба сконфигурированы). `make demo-lead-hot` → в Slack-канале появится Block Kit карточка с кнопками.

---

## Демо-сценарии для показа заказчику

**Сценарий 1: горячий лид.**

```bash
make demo-lead-hot
```

Покажите:

- логи (30 секунд от POST до отправленного авто-ответа);
- Telegram-группу с карточкой 🔥 и кнопками;
- HubSpot-сделку в стадии `qualifiedtobuy` с amount ≈ €6,375;
- нажмите «Claim lead» — сделка переезжает, карточка обновляется.

**Сценарий 2: холодный лид.**

```bash
make demo-lead
```

Клиент «интервьюируется, оффера пока нет». AI ставит `warm` (или `cold` у mock), суммарий: «Actively planning, needs a scoping call».

**Сценарий 3: спам.**

```bash
curl -X POST http://localhost:8081/api/leads \
  -H 'Content-Type: application/json' \
  -d '{"name":"Crypto Bot","email":"spam@x.io","nationality":"n/a","current_country":"n/a","service":"full_relocation","move_date":"2027-01-01","family_size":1,"message":"10x crypto returns and SEO backlinks!!!","gdpr_consent":true}'
```

Категория `spam`, никакого письма клиенту не уходит (`reply` пустой), карточка идёт с эмодзи 🗑 — менеджер жмёт «Mark spam» и в HubSpot сделка закрывается как `closedlost/spam`.

**Сценарий 4: без интернета до OpenRouter.**

Отключите ключ (`OPENROUTER_API_KEY=`) и перезапустите. Сервис работает как и раньше на mock-квалификаторе. Или наоборот: оставьте ключ, но заблокируйте DNS `openrouter.ai` — сервис после 3 ретраев вернёт `category=warm, fallback=true` и лид всё равно доедет до CRM/уведомлений.

---

## Как менеджер работает с лидом

1. Приходит уведомление в Telegram-группу (или Slack). Эмодзи сразу говорит о приоритете: 🔥 hot → звонить сейчас, 👍 warm → сегодня, ❄️ cold → в nurturing-рассылку, 🗑 spam → в мусор.
2. Свободный менеджер жмёт **«✅ Claim lead»** — карточка обновляется («Claimed by @sam»), в HubSpot сделка меняет стадию, кнопки исчезают, чтобы никто не взял второй раз.
3. Менеджер идёт в HubSpot → Deals → находит сделку по имени, там уже есть контакт, оценка и связь с email.
4. Клиент к этому моменту уже получил AI-ответ с диапазоном цен и предложением 15-минутного звонка. Менеджер пишет/звонит и продолжает разговор.
5. Если карточка оказалась спамом или дублем — **«🗑 Mark spam»**, сделка закрывается как lost.

---

## Отладка и логи

Формат логов — JSON (`slog`), удобно грепать по `lead_id`.

```bash
# все действия по конкретному лиду
docker compose logs pipeline | grep ld_abcdef1234567890

# только ошибки
docker compose logs pipeline | grep '"level":"ERROR"'

# что видит квалификатор
docker compose logs pipeline | grep qualified
```

Типичные проблемы:

| Симптом | Причина / что смотреть |
|---|---|
| `500 internal error` при POST | смотреть логи — вероятнее всего, CSV-путь недоступен |
| `429 rate limit exceeded` | 10 запросов/мин с одного IP — норма для anti-abuse; в проде поставьте reverse-proxy с X-Real-IP |
| Лид не пришёл в Telegram | проверить `TELEGRAM_MANAGER_CHAT_ID` (для супергрупп начинается с `-100`), проверить, что бот добавлен в группу и умеет писать |
| Кнопки в Telegram не работают | нужен long-poll — убедитесь, что сервис не остановлен; вебхуков в этой сборке нет |
| `qualifier ... fallback=true` | OpenRouter недоступен / модель невалидная — проверьте `OPENROUTER_MODEL` в каталоге |
| В HubSpot дубли контактов | не должно быть — дедуп идёт по email, проверьте, что email нормализован (сервис делает `TrimSpace + ToLower`) |
| В Google Sheets тишина | проверьте, что service account поделён с таблицей как Editor, что путь `SHEETS_SA_JSON` подмонтирован в контейнер, и что Sheets API включён в GCP-проекте |

---

## Тесты

```bash
cd pipeline
go test ./...
# ok  api       — валидация формы, GDPR
# ok  crm       — CSV, mock CRM, интерфейс
# ok  notify    — карточка, Slack blocks, HMAC-подпись, защита от replay
# ok  qualifier — парсинг JSON от LLM (чистый / с ``` / с прозой / битый), fallback, mock
# ok  tgbot     — state-machine анкеты
```

Покрыты в том числе критические места безопасности: подпись Slack, отклонение form без GDPR-consent, устойчивость к битому JSON от LLM.

---

## Деплой в прод

Минимальный вариант — один VPS с Docker:

1. Купить домен, направить A-запись на IP сервера.
2. Поставить Docker + Docker Compose.
3. Скопировать репозиторий, заполнить `.env` реальными значениями.
4. Перед nginx-контейнером лендинга поставить Caddy или Traefik для HTTPS (Let's Encrypt). Пример `docker-compose.override.yml` с Caddy обычно занимает 15 строк.
5. Пробросить порт 443 наружу, 80 → редирект.
6. `data/leads.csv` и `secrets/` — вынести в volume/mount, регулярно бекапить.
7. Настроить логирование — `docker compose logs` → journald или Loki/Datadog.

Отдельно советую:

- **Ограничить CORS** в проде: `CORS_ORIGINS=https://anker-relocation.example`.
- **Ужесточить rate-limit** через reverse-proxy (nginx `limit_req_zone`), потому что встроенный делит по IP приложения (за прокси нужно правильно проксировать `X-Real-IP` — сервис его читает).
- **Мониторинг**: `/healthz` уже есть — воткните в Uptime Kuma или аналог, чтобы падение сервиса не съело SLA скорости ответа.
- **Секреты**: не коммитьте `.env`. Для прода — Docker secrets или HashiCorp Vault.

---

## Частые вопросы

**Можно ли добавить второй язык лендинга?**
Да, `landing/` — это статический HTML. Скопируйте `index.html` → `de.html`, переведите строки, добавьте переключатель. Форма продолжает слать в `/api/leads`.

**Как изменить прайс-анкеры для AI?**
`pipeline/pkg/qualifier/openrouter.go`, константа `systemPrompt`. Также правим few-shot примеры в `fewShots`. После правки — `go test ./...` и `make down && make up`.

**А если я хочу другой CRM (Pipedrive, amoCRM, Bitrix24)?**
Добавьте файл `pkg/crm/pipedrive.go`, реализуйте интерфейс `CRM` (`Name`, `Upsert`, `MarkClaimed`, `MarkSpam`), и в `cmd/pipeline/main.go` добавьте ещё одну ветку выбора по env-переменной. Всё остальное менять не нужно.

**Можно ли добавить SMS?**
Да, по образцу `notify/telegram.go` сделайте `notify/twilio.go`, реализуйте `Notifier`, добавьте в `notifiers` в main. Никакой другой код трогать не придётся.

**Что с GDPR?**
Форма не принимается без явного чекбокса согласия. Согласие пишется в CSV (`gdpr_consent=true`) и в HubSpot (в кастомное поле — надо один раз создать в HubSpot). Данные хранятся в EU при использовании HubSpot EU-датацентра и Google Cloud EU-региона. Для полного соответствия добавьте страницу privacy notice и endpoint для запросов на удаление (сейчас не реализован — MVP).

**Сколько это выдержит нагрузки?**
Один инстанс на скромной VPS-ке — сотни лидов в час без проблем (узкое место — задержка OpenRouter, а он держит спокойно). Для тысяч в час: заменить `pkg/queue` на NATS/Redis, поднять 2–3 инстанса за балансировщиком, разделить CSV → Postgres.

**Как удалить лид по запросу клиента?**
Пока — руками: `hubspot` → удалить контакт + сделку; в CSV — `grep -v ld_xxx`. Автоматизация под GDPR-erasure — отдельный ticket.

---

Удачи с автоматизацией. Если что-то из гайда не сработало — начните с `make logs` и грепа по `lead_id`, там всё видно.
