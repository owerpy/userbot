#!/usr/bin/env bash
# Быстрое обновление userbot: скачать свежий образ из GHCR и перезапустить.
set -e
cd "$(dirname "$0")"
echo "⬇️  Скачиваю свежий образ..."
sudo docker compose pull
echo "🔄 Перезапускаю..."
sudo docker compose up -d
echo "✅ Готово. Логи: sudo docker compose logs -f board-userbot"
