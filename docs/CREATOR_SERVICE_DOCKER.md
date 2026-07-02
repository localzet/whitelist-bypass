# Creator service в Docker

Образ `ghcr.io/<owner>/whitelist-bypass-creator-service` запускает WB Stream service-call и принимает `CookieSubmit`/`SessionCreate` только через звонок. Обычный HTTP/WebSocket API между клиентом и сервером не используется.

## Подготовка

```sh
mkdir -p creator-service/secrets
cd creator-service
cp /path/to/docker-compose.creator-service.yml docker-compose.yml
cp /path/to/.env.creator-service.example .env
cp /path/to/egresses.example.json egresses.json
openssl rand -base64 32 > secrets/vault-key.txt
cp /secure/path/cookies-wbstream.json secrets/cookies-wbstream.json
chmod 600 .env secrets/* egresses.json
```

Укажите в `.env` стабильный UUID клиента как `USER_ID`. Этот же ID создаётся и хранится клиентским приложением автоматически. На текущем этапе один контейнер соответствует одному service-call и одному `USER_ID`.

## Запуск

```sh
docker compose pull
docker compose up -d
docker compose logs -f creator-service
```

После успешного подключения ссылка служебного звонка появится в persistent volume:

```sh
docker compose exec creator-service cat /data/service-call.txt
```

Добавьте эту WB Stream ссылку в клиент как service-call destination, выберите work platform и ручной `egressId`. Клиент передаст Yandex cookies и запрос рабочей сессии внутри звонка, остановит служебное подключение после `SessionReady` и подключится к созданному work-call.

## Обновление

```sh
docker compose pull
docker compose up -d --remove-orphans
```

Не удаляйте volume `creator-data`: в нём находятся зашифрованный cookie vault, session state и текущая service-call ссылка. Vault key храните отдельно от volume и резервной копии данных.

## Публикация

Workflow `.github/workflows/docker-creator-service.yml` публикует multi-arch образы `linux/amd64` и `linux/arm64` при тегах `v*` и ручном запуске. Release-теги получают semver, `latest` и sha tags; ручной запуск публикует sha tag.
