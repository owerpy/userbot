# Board Userbot

Слушает Telegram-каналы под личным аккаунтом-подписчиком, разбирает объявления
о грузоперевозках через Groq и складывает в `board_ads` базы Truck Delivery.
Каналы берутся из `board_channels` (управляются в админ-панели → вкладка «Доска»).

## Как устроен деплой

Полностью автоматический: пушишь — через пару минут новая версия на сервере.

```
git push → Actions собирает (с кэшем) → GHCR → SSH на сервер → pull + restart
```

Нужные секреты в репозитории (Settings → Secrets → Actions):
`SERVER_HOST`, `SERVER_USER`, `SERVER_SSH_KEY`, `SERVER_PORT`, `GHCR_PAT`
— те же, что в репозитории truck-delivery.

`./update.sh` на сервере нужен только для ручного обновления (если правил `.env`
или хочешь перезапустить без пуша).

## Первая установка

```bash
cd /opt
sudo mkdir -p userbot && sudo chown -R $USER:$USER userbot
git clone https://github.com/owerpy/userbot.git userbot
cd userbot
./install.sh
```

Скрипт найдёт docker-сеть стека, спросит данные, создаст `.env`,
скачает образ, проведёт вход в Telegram и запустит контейнер.

## Обновление (быстро)

```bash
cd /opt/userbot
git pull        # только если менялись compose/скрипты
./update.sh     # скачать свежий образ и перезапустить — секунды
```

## Что понадобится

| Что | Где взять |
|-----|-----------|
| `TG_APP_ID`, `TG_APP_HASH` | https://my.telegram.org → API development tools |
| Номер телефона | аккаунт, подписанный на нужные каналы |
| Пароль Postgres | `POSTGRES_PASSWORD` из `.env` стека truck-delivery |
| `GROQ_API_KEY` | https://console.groq.com |

## Команды

```bash
sudo docker compose logs -f board-userbot   # логи
sudo docker compose restart board-userbot   # перезапуск
sudo docker compose down                    # остановить
```

## Важно

- Аккаунт должен быть **подписан** на канал (для приватных — состоять в нём).
- Сессия в томе `board_session` — не удаляй, иначе вход заново.
- Если образ в GHCR приватный, сервер должен быть залогинен:
  `echo ТОКЕН | sudo docker login ghcr.io -u owerpy --password-stdin`
