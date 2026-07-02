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

Каждый пользователь открывает service settings в Android/Desktop, копирует `Service client ID` и передаёт его администратору. Добавьте полученные UUID в `.env` через запятую:

```env
USER_IDS=550e8400-e29b-41d4-a716-446655440000,7d444840-9dc0-11d1-b245-5ffdce74fad2
```

Один контейнер держит один общий service-call для всего allowlist. Клиенты используют одну service-call ссылку и подключаются к bootstrap-каналу последовательно; после `SessionReady` клиент уходит в свой work-call. Cookie vault и лимит одного work-call разделены по проверенному `USER_ID`.

`MAX_ACTIVE_USERS` ограничивает общее число одновременных work-call независимо от размера allowlist. Для VPS с 1 CPU / 1 GB начните с `MAX_ACTIVE_USERS=2` и `RESOURCES=moderate`; `WORK_TTL` автоматически освобождает забытые work-call (default `30m`).

На этом промежуточном этапе UUID — allowlist identifier, а не криптографический client secret. Не публикуйте service-call ссылку открыто. Следующий security layer должен добавить подписанные client credentials без изменения call-carried транспорта.

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

Передайте эту WB Stream ссылку всем разрешённым клиентам. Добавьте её как service-call destination, выберите work platform и ручной `egressId`. Клиент передаст свой ID, Yandex cookies и запрос рабочей сессии внутри звонка, остановит служебное подключение после `SessionReady` и подключится к созданному work-call.

## Обновление

```sh
docker compose pull
docker compose up -d --remove-orphans
```

Не удаляйте volume `creator-data`: в нём находятся зашифрованный cookie vault, session state и текущая service-call ссылка. Vault key храните отдельно от volume и резервной копии данных.

## Публикация

Workflow `.github/workflows/docker-creator-service.yml` публикует multi-arch образы `linux/amd64` и `linux/arm64` при тегах `v*` и ручном запуске. Release-теги получают semver, `latest` и sha tags; ручной запуск публикует sha tag.
