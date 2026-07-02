# Creator service в Docker

Образ `ghcr.io/<owner>/whitelist-bypass-creator-service` создаёт служебный звонок в Telemost и принимает `CookieSubmit`/`SessionCreate` только через его VP8-туннель. Отдельный HTTP/WebSocket API между клиентом и сервером не используется.

## Подготовка

```sh
mkdir -p creator-service/secrets
cd creator-service
cp /path/to/docker-compose.creator-service.yml docker-compose.yml
cp /path/to/.env.creator-service.example .env
cp /path/to/egresses.example.json egresses.json
openssl rand -base64 32 > secrets/vault-key.txt
cp /secure/path/cookies-yandex.json secrets/cookies-yandex.json
chmod 600 .env secrets/* egresses.json
```

`cookies-yandex.json` должен быть экспортирован из авторизованной сессии Яндекса в Creator. Сервер использует эти cookies только для создания и удержания общего служебного звонка Telemost. Пользовательские cookies поступают от клиентов внутри этого звонка и сохраняются раздельно в зашифрованном vault.

Каждый пользователь копирует `Service client ID` из настроек Android/Desktop и передаёт администратору. Добавьте UUID в `.env` через запятую:

```env
USER_IDS=550e8400-e29b-41d4-a716-446655440000,7d444840-9dc0-11d1-b245-5ffdce74fad2
```

Один контейнер держит один общий служебный звонок Telemost. После `SessionReady` клиент отключается от него и переходит в свой рабочий звонок. Vault и лимит одного рабочего звонка разделены по проверенному `USER_ID`.

`MAX_ACTIVE_USERS` ограничивает общее число одновременно работающих звонков. Для VPS с 1 CPU / 1 GB начните с `MAX_ACTIVE_USERS=2` и `RESOURCES=moderate`. `WORK_TTL` завершает забытые рабочие процессы, по умолчанию через 30 минут.

`WORK_COOKIE_SOURCE=user` сохраняет исходную модель: клиент отправляет свои cookies через service-call, а сервер создаёт work-call от имени этого пользователя. Если Telemost возвращает `401 Unauthorized` при создании work-call на VPS, хотя те же cookies работают локально, задайте `WORK_COOKIE_SOURCE=service`. В этом режиме work-call создаётся сервисными cookies контейнера (`SERVICE_COOKIES`), а клиентские cookies не используются для Telemost work-call. Это обход антифрод-проверок Яндекса на перенос пользовательской сессии между IP.

UUID пока является allowlist-идентификатором, а не криптографическим секретом. Не публикуйте ссылку служебного звонка. Следующий слой безопасности должен добавить подписанные client credentials, сохранив транспорт внутри звонка.

## Запуск

```sh
docker compose pull
docker compose up -d
docker compose logs -f creator-service
```

Ссылка служебного звонка сохраняется в persistent volume:

```sh
docker compose exec creator-service cat /data/service-call.txt
```

Передайте эту Telemost-ссылку разрешённым клиентам. Добавьте её как service-call destination, выберите рабочую платформу и при необходимости ручной `egressId`.

При перезапуске контейнер автоматически перечитает `/data/service-call.txt` и подключится к уже существующему служебному звонку. `SERVICE_ROOM` нужен только как ручной override, если вы хотите принудительно подключиться к другой Telemost-ссылке.

Чтобы принудительно создать новый служебный звонок, остановите контейнер и удалите сохранённую ссылку из volume:

```sh
docker compose stop creator-service
docker compose run --rm --entrypoint sh creator-service -c 'rm -f /data/service-call.txt'
docker compose up -d
```

## Обновление

```sh
docker compose pull
docker compose up -d --remove-orphans
```

Не удаляйте volume `creator-data`: в нём находятся зашифрованный cookie vault, состояние процессов и текущая ссылка. Vault key храните отдельно от volume и его резервной копии.

## Публикация

Workflow `.github/workflows/docker-creator-service.yml` публикует multi-arch образы `linux/amd64` и `linux/arm64` при тегах `v*` и ручном запуске. Release-теги получают semver, `latest` и sha tags; ручной запуск публикует sha tag.
