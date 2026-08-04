# Board Userbot

Слушает Telegram-каналы под личным аккаунтом-подписчиком, разбирает объявления
о грузоперевозках через Groq и складывает в таблицу `board_ads` базы Truck Delivery.
Список каналов берётся из `board_channels` (управляется через админ-API).

## Быстрая установка

```bash
# на сервере, рядом со стеком truck-delivery
cd /opt
sudo unzip ~/board-userbot.zip -d board-userbot
cd board-userbot
./install.sh
```

Скрипт сам найдёт docker-сеть стека, спросит данные, соберёт образ,
проведёт вход в Telegram и запустит контейнер.

## Что понадобится

| Что | Где взять |
|-----|-----------|
| `TG_APP_ID`, `TG_APP_HASH` | https://my.telegram.org → API development tools |
| Номер телефона | твой аккаунт, подписанный на нужные каналы |
| Пароль Postgres | `POSTGRES_PASSWORD` из `.env` стека truck-delivery |
| `GROQ_API_KEY` | https://console.groq.com |

## Добавить каналы

Через админ-API бэкенда:
```
POST /api/v1/admin/board/channels   {"channel":"@yuk_kanal","title":"Грузы UZ"}
GET  /api/v1/admin/board/channels
PATCH /api/v1/admin/board/channels/:id  {"is_active":false}
DELETE /api/v1/admin/board/channels/:id
```
Userbot подхватывает изменения автоматически (проверяет список раз в 2 минуты).

## Команды

```bash
sudo docker compose logs -f board-userbot   # логи
sudo docker compose restart board-userbot   # перезапуск
sudo docker compose down                    # остановить
```

## Важно

- Аккаунт должен быть **подписан** на канал (для приватных — состоять в нём).
- Сессия хранится в томе `board_session` — не удаляй, иначе вход заново.
- Не подписывайся разом на сотни каналов с одного аккаунта — Telegram может ограничить.
  Если не хочешь светить основной аккаунт, заведи отдельный под это.
