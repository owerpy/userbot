#!/usr/bin/env bash
# Установка board-userbot на сервер. Запускать из папки board-userbot.
set -e

echo "══════════════════════════════════════════"
echo "  Board Userbot — установка"
echo "══════════════════════════════════════════"
echo ""

# ── 1. Определяем сеть стека truck-delivery ──
NET=$(sudo docker network ls --format '{{.Name}}' | grep -E 'truck.*net' | head -1)
if [ -z "$NET" ]; then
  echo "❌ Не нашёл docker-сеть truck_net. Запущен ли стек truck-delivery?"
  echo "   Список сетей:"; sudo docker network ls
  exit 1
fi
echo "✅ Сеть найдена: $NET"
sed -i "s|name: truck-delivery_truck_net|name: $NET|" docker-compose.yml

# ── 2. Собираем .env ──
if [ -f .env ]; then
  echo "ℹ️  .env уже есть — использую его. (Удали файл, чтобы ввести заново.)"
else
  echo ""
  echo "Данные Telegram API — получить на https://my.telegram.org"
  echo "(вкладка API development tools)"
  read -p "TG_APP_ID   : " APP_ID
  read -p "TG_APP_HASH : " APP_HASH
  read -p "Твой номер (+998...): " PHONE
  read -p "2FA пароль (если есть, иначе Enter): " TFA
  echo ""
  read -p "Пароль Postgres (POSTGRES_PASSWORD из стека): " PGPASS
  read -p "GROQ_API_KEY: " GROQ

  cat > .env <<ENVEOF
TG_APP_ID=$APP_ID
TG_APP_HASH=$APP_HASH
TG_PHONE=$PHONE
TG_2FA=$TFA

# Подключение к той же базе, где board_ads (контейнер truck_postgres в общей сети)
DATABASE_URL=postgres://truck:$PGPASS@truck_postgres:5432/truck_delivery?sslmode=disable

GROQ_API_KEY=$GROQ
GROQ_MODEL=llama-3.3-70b-versatile
TG_SESSION_DIR=/data/session
ENVEOF
  chmod 600 .env
  echo "✅ .env создан"
fi

# ── 3. Скачиваем готовый образ (собран в GitHub Actions) ──
echo ""
echo "⬇️  Скачиваю образ из GHCR..."
sudo docker compose pull

# ── 4. Первый вход в Telegram (интерактивно) ──
echo ""
echo "══════════════════════════════════════════"
echo "  Сейчас нужно войти в Telegram."
echo "  Придёт код — введи его."
echo "══════════════════════════════════════════"
sudo docker compose run --rm board-userbot || true

echo ""
echo "Если вход прошёл (в логах было 'logged in') — запускаю в фоне."
read -p "Продолжить? [y/N]: " OK
if [[ "$OK" =~ ^[Yy]$ ]]; then
  sudo docker compose up -d
  echo ""
  echo "✅ Userbot запущен."
  echo "   Логи:      sudo docker compose logs -f board-userbot"
  echo "   Перезапуск: sudo docker compose restart board-userbot"
else
  echo "Ок. Запустишь потом: sudo docker compose up -d"
fi
