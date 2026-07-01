# Multiconnect: архитектура и контекст разработки

Актуально на 2026-07-01 для ветки `main`; последняя итерация добавляет автоматический egress discovery и раздельные probes.

Дополнение после итераций multiconnect: добавлены `relay/egress`, versioned control handshake, `--egress-config` на creator и `--egress-id` на joiner. Joiner после handshake автоматически получает безопасный список профилей и отдельный health/latency probe каждого маршрута. Android и Windows/Linux desktop сохраняют discovery и предлагают доступные ID. Пример серверного конфига лежит в `docs/egresses.example.json`.

Следующая архитектурная итерация — разделить control plane и data plane. У каждого пользователя остаётся стабильный служебный звонок. Через него joiner получает discovery и отправляет `CreateSession(egressId)`. Creator создаёт или переиспользует отдельный рабочий звонок, привязанный к `(userId, egressId)`, и отвечает `sessionId`, ссылкой и TTL. Joiner штатно завершает служебный transport и подключается к рабочему звонку. Нельзя переключать egress глобально внутри общего звонка: это меняет маршрут всех пользователей и смешивает существующие TCP/UDP-сессии. Нужны per-user sessions, лимиты, идемпотентный request ID, TTL/cleanup и авторизация управляющих запросов.

## 1. Цель

Целевая схема:

```text
Android / Windows / Linux joiner
        |
        | звонок через VK / Telemost / WB Stream / DION
        v
Creator на сервере в РФ
        |
        | выбранный SOCKS5 upstream
        v
Один из зарубежных серверов (до 8 и более)
        |
        v
Internet
```

Пользователь должен:

1. сохранить несколько звонков на joiner;
2. выбрать звонок и зарубежную точку выхода;
3. переключиться между профилями без повторного ручного ввода;
4. настроить на creator несколько SOCKS5 upstream-профилей;
5. не передавать joiner-у адреса, логины и пароли upstream-прокси.

Под «multiconnect» в этом документе понимается выбор одного профиля подключения из нескольких. Одновременный запуск нескольких VPN/TUN-сессий на одном устройстве в первую версию не входит: системный TUN, таблица маршрутизации и локальный SOCKS listener являются эксклюзивными ресурсами.

## 2. Фактическое состояние проекта

### Общая архитектура

- `relay/` — общий Go-код SOCKS5, мультиплексирования TCP/UDP, DC/VP8-туннеля, обфускации и платформенных bindings.
- `headless/{vk,telemost,wbstream,dion}/` — headless creator-процессы.
- `android-app/` — Android joiner: `VpnService`, tun2socks и headless Pion.
- `joiner-desktop-app/` — Electron UI и Go backend для Windows/Linux/macOS.
- `creator-app/` — Electron creator, запускающий relay/headless binaries.
- `headless/*-joiner/` — отдельные CLI joiner-ы; набор платформ пока неоднороден.

Транспорт уже мультиплексирует TCP/UDP-соединения внутри одного туннеля. Это не то же самое, что несколько egress-профилей.

### Что уже реализовано

- Android уже хранит список `CallConfig` в `SharedPreferences`: `id`, `name`, `url`, индивидуальные `tunnelMode`, VP8 pacing и `dualTrack`.
- Android умеет добавлять, выбирать, переименовывать и удалять назначения, а также запоминает активное.
- Desktop joiner поддерживает VK, Telemost, WB Stream и DION, TUN на Windows/Linux/macOS, reconnect и параметры туннеля.
- Creator поддерживает один SOCKS5 upstream на процесс через `--upstream-socks`, `--upstream-user`, `--upstream-pass`.
- TCP и UDP creator-трафик могут идти через upstream; UDP требует SOCKS5 `UDP ASSOCIATE`.
- Electron creator сохраняет один глобальный upstream в `localStorage` и передаёт его дочерним процессам.
- Туннель имеет control frame `MsgConfig`/`MsgConfigAck`, но payload сейчас содержит только VP8 `fps`, `batch`, `trackCount` и не имеет явной версии протокола.
- Модель creator по-прежнему рассчитана на одного активного joiner-а на звонок. Новый peer сбрасывает текущий `RelayBridge`.

### Чего не хватает

- До текущей итерации в `CallConfig` Android не было `egressId`; сейчас поле добавлено и старый JSON остаётся совместимым.
- Desktop joiner не сохраняет список назначений: ссылка и настройки живут только в текущей форме.
- Headless Linux CLI не имеет общего хранилища профилей и единого launcher-а для всех платформ.
- `RelayBridge` содержит один `*Socks5Upstream`; выбора по идентификатору нет.
- Нет протокольного handshake для выбора egress и понятного ответа об ошибке.
- Нет health-check, circuit breaker и наблюдаемого статуса upstream.
- Конфигурация upstream дублируется во флагах VK/Telemost/WB/DION, relay, bot, Docker и Electron creator.
- Нет автоматических unit/integration tests выбора, отказа и переключения upstream.

## 3. Рекомендуемая модель

Нужно разделить две независимые сущности.

### Join destination

Хранится только на joiner:

```json
{
  "schemaVersion": 1,
  "id": "uuid",
  "name": "Germany via VK",
  "callLink": "https://vk.com/call/join/...",
  "egressId": "de-fra-1",
  "tunnelMode": "VIDEO",
  "vp8Fps": 24,
  "vp8Batch": 30,
  "dualTrack": false
}
```

`egressId` — стабильный публичный идентификатор, но не адрес прокси. Один звонок можно сохранить несколько раз с разными названиями и `egressId`, либо менять egress отдельным selector-ом.

### Creator egress profile

Хранится только на creator. Для первой версии следует использовать JSON: Go читает его стандартной библиотекой без новой зависимости.

```json
{
  "schemaVersion": 1,
  "defaultEgress": "direct",
  "egresses": [
    { "id": "direct", "type": "direct", "enabled": true },
    {
      "id": "de-fra-1",
      "type": "socks5",
      "address": "10.20.0.11:1080",
      "username": "wb",
      "passwordEnv": "WB_EGRESS_DE_FRA_1_PASSWORD",
      "enabled": true
    }
  ]
}
```

Рекомендуемый CLI:

```text
--egress-config /etc/whitelist-bypass/egresses.json
--default-egress direct
```

Существующие `--upstream-*` оставить на один релиз как deprecated compatibility path, преобразуя их внутри процесса в профиль `legacy-default`. Одновременное указание файла и legacy-флагов должно завершаться понятной ошибкой.

Пароль предпочтительно получать из environment или отдельного secret-файла с правами `0600`. Пароль в аргументах процесса виден через process listing и не должен быть рекомендуемым способом.

## 4. Протокол выбора egress

Не следует расширять текущий бинарный `MsgConfig` полями переменной длины: он уже отвечает за VP8 pacing, не версионирован и его старые реализации принимают payload длиной от 4 байт.

Нужно добавить отдельные control messages:

```text
ClientHello  -> protocolVersion, capabilities, requestedEgressId
ServerHello  -> protocolVersion, selectedEgressId, capabilities
ControlError -> code, safeMessage
```

Практический вариант payload — компактный JSON с жёстким лимитом, например 4 KiB. Эти сообщения редки, поэтому бинарная микрооптимизация не оправдана. Перед обработкой обычных `CONNECT`/`UDP` creator должен завершить handshake.

Правила:

- пустой `requestedEgressId` означает `defaultEgress`;
- неизвестный, disabled или нездоровый профиль отклоняется до открытия пользовательских соединений;
- creator никогда не отправляет address/username/password;
- выбранный egress неизменяем для текущей tunnel-сессии;
- переключение выполняется через controlled reconnect: закрыть соединения, переподключить звонок, пройти новый handshake;
- старый joiner определяется отсутствием `ClientHello` и получает legacy/default egress в течение переходного периода;
- новый joiner со старым creator должен показать «creator не поддерживает выбор точки выхода», а не молча использовать другой сервер;
- в логах допустим `egressId`, но credentials и полный пользовательский destination должны маскироваться.

Обфускация уже выводит ключ из join link, но выбор egress всё равно должен рассматриваться как недоверенный ввод. Creator применяет allowlist, лимит длины/алфавита ID и fail-closed поведение. Произвольный SOCKS address от joiner принимать нельзя — это создаст SSRF/доступ к внутренней сети creator.

## 5. Изменения по слоям

### Go core

1. Добавить `EgressConfig`, `EgressProfile`, parser/validator и `EgressRegistry` в новый пакет, например `relay/egress`.
2. Ввести малый интерфейс `Dialer` (`DialTCP`, `UDPAssociate`) и реализации direct/SOCKS5. `RelayBridge` должен зависеть от интерфейса, а не от конкретного `Socks5Upstream`.
3. Создавать dialer один раз после handshake и фиксировать его на session scope.
4. Добавить новые versioned control messages, лимиты payload и typed error codes.
5. Не использовать глобальный mutable upstream: это создаст гонку, при которой существующие TCP/UDP-сессии окажутся на разных выходах.
6. Вынести общую регистрацию egress-флагов из четырёх creator main packages, чтобы не продолжать дублирование.

### Android

1. Расширить `CallConfig` полем `egressId: String?` и сохранить обратную совместимость JSON: отсутствие поля означает default egress.
2. В форме назначения добавить выбор/ввод egress ID; адрес и credentials там не показывать.
3. Передавать egress ID в gomobile/headless joiner и отображать подтверждённый creator-ом `selectedEgressId`.
4. При выборе другого профиля делать штатный stop/start через существующий service lifecycle.
5. Добавить миграционные tests для старого JSON и instrumented test переключения активного назначения.

### Windows/Linux desktop joiner

1. Ввести типизированный `DestinationProfile` и repository поверх Electron `safeStorage`/userData JSON.
2. Перенести текущие поля формы в профиль и добавить список профилей, CRUD и active profile.
3. Передавать `--egress-id` в Go backend.
4. Не хранить SOCKS password joiner-а в plain `localStorage`; чувствительные локальные настройки защищать через `safeStorage`.
5. Переключение выполнять stop -> дождаться завершения backend/TUN cleanup -> start нового профиля. Нельзя запускать два backend-процесса одновременно.

Для headless Linux добавить `--profile <name>` и `--config <path>`, но использовать ту же JSON-схему назначения. Electron и CLI должны различаться только storage adapter-ом, а не бизнес-моделью.

### Creator и deployment

1. Electron creator: editor списка egress-профилей, default selector, валидация уникальности ID и connection test.
2. Headless creator: общий `--egress-config`; Docker — read-only mount config плюс secrets через environment/file.
3. VK bot: передавать дочерним creator-процессам один config path, а не копировать credentials в CLI args.
4. Health-check выполнять отдельно для каждого профиля. Для SOCKS5 достаточно TCP probe через прокси к заранее заданному `host:port`; внешний IP-check сделать опциональным, чтобы не зависеть от стороннего HTTP API.
5. На старте проверять схему и уникальность ID; недоступность одного upstream не должна останавливать creator, если default доступен. Недоступный выбранный профиль должен давать typed error.

## 6. Варианты эксплуатации

### Рекомендуемый

Один стабильный звонок на creator и восемь egress-профилей. Joiner выбирает `egressId`, creator маршрутизирует весь session egress через соответствующий SOCKS5.

Плюсы: меньше аккаунтов/звонков, единая ссылка, простое добавление девятого сервера. Минус: требует протокольных изменений и совместимых версий обеих сторон.

### Упрощённый переходный

Запустить восемь creator-процессов, каждому дать отдельный звонок и один существующий `--upstream-socks`. На joiner сохранить восемь назначений.

Плюсы: Android уже почти готов, core менять не требуется. Минусы: восемь процессов/звонков, выше расход RAM и нагрузка на аккаунты, сложнее мониторинг. Desktop joiner всё равно нужно научить хранить профили.

Этот режим полезен как этап 0 и operational fallback, но не как конечная архитектура.

## 7. Этапы реализации

### Этап 0 — проверить инфраструктуру

- Поднять 8 SOCKS5 endpoints и проверить TCP + UDP ASSOCIATE с российского creator.
- Зафиксировать стабильные ID (`de-fra-1`, `nl-ams-1` и т. п.). ID не должны зависеть от IP.
- Проверить текущую цепочку по одному `--upstream-socks` штатным `headless/tests/test-upstream-socks.sh`.

### Этап 1 — серверный core и протокол

- Config/validation/registry/dialer abstraction.
- Handshake и typed errors.
- Compatibility adapter для `--upstream-*`.
- Unit tests parser, validation, selection, unknown/disabled/default.
- Integration test: два локальных SOCKS5 upstream с различимыми exit markers, TCP и UDP.

### Этап 2 — Android

- Миграция `CallConfig` и UI egress selector.
- Передача/подтверждение egress ID.
- Tests persistence, migration и reconnect.

### Этап 3 — Windows/Linux

- Общая profile model/storage.
- UI CRUD/selection и CLI config/profile.
- Проверка TUN cleanup и reconnect на обеих ОС.

### Этап 4 — creator UI и эксплуатация

- Profile editor, health/status, безопасное хранение secrets.
- Docker/systemd examples, structured logs и runbook.
- Deprecation warning для legacy upstream flags; удаление только в следующем major/minor release по принятой политике совместимости.

## 8. Критерии готовности

- Старые сохранённые Android-звонки загружаются и используют default egress.
- Android, Windows и Linux сохраняют минимум 8 именованных назначений и корректно восстанавливают активное после рестарта.
- Один creator загружает минимум 8 SOCKS5-профилей.
- Выбранный egress подтверждается server handshake и проверяется фактическим exit IP/marker.
- TCP и UDP не смешивают egress внутри одной сессии.
- Переключение закрывает старые TCP/UDP connections и не оставляет TUN/routes/processes.
- Unknown/disabled egress не падает обратно на default молча.
- Старые joiner-ы продолжают работать через default/legacy egress в заявленный compatibility period.
- Credentials отсутствуют в joiner config, логах и process arguments рекомендуемого deployment.
- Unit tests, Go race tests, Android tests и сборки Windows/Linux/Android проходят.

## 9. Риски и технический долг

- Текущий control protocol не имеет общего version negotiation. Добавлять новые возможности без него дальше рискованно.
- `RelayBridge.SetUpstreamSocks` принимает concrete type и mutable pointer; для multiconnect это нужно заменить session-scoped abstraction.
- Четыре headless creator main package дублируют config/flags/resource parsing. Перед добавлением новых флагов нужен небольшой общий пакет, без крупного переписывания signaling.
- Android persistence проглатывает любую JSON-ошибку и возвращает пустой список. Повреждение одного элемента не должно уничтожать все назначения; нужны поэлементный parse и диагностируемая миграция.
- Desktop Electron preload сейчас использует `any` для settings. Перед расширением следует протянуть общий тип и runtime validation на IPC boundary.
- SOCKS5 UDP support должен проверяться явно: TCP health-check не гарантирует работу DNS/QUIC/UDP приложений.
- Один звонок по-прежнему обслуживает одного активного joiner-а. Поддержка нескольких одновременных joiner-ов — отдельная задача: нужны peer identity, отдельный tunnel/bridge на peer и лимиты ресурсов.

## 10. Состояние рабочего дерева и перенос на другой компьютер

На момент анализа `HEAD` совпадает с `origin/main` (`6571875`), но локально Git показывает 365 изменённых файлов: `52995 insertions`, `52995 deletions`. Проверка с игнорированием пробелов на концах строк не показывает содержательных изменений; это массовая разница CRLF/LF. Эти файлы не относятся к multiconnect и не должны попадать в функциональные коммиты.

Перед продолжением дома:

```sh
git clone <repository-url>
cd whitelist-bypass
git fetch --all --prune
git switch main
git pull --ff-only
git log -1 --oneline
git status --short
```

Затем открыть этот файл и начинать с этапа 0/1. Не переносить текущее массово dirty рабочее дерево архивом. Если оно всё же оказалось на новом компьютере, сначала убедиться командой `git diff --ignore-space-at-eol`, что содержательных изменений нет; только после этого отдельно нормализовать line endings. Не применять `git reset --hard` к дереву с непроверенными пользовательскими изменениями.

Рекомендуется закрепить окончания строк в отдельном инфраструктурном коммите через `.gitattributes` (например, LF для исходников/shell и CRLF только для действительно Windows-специфичных файлов). Это изменение нельзя смешивать с multiconnect.

## 11. Первое следующее изменение

Начать с отдельного PR/коммита `feat(relay): add versioned egress selection`:

1. tests для JSON schema и handshake;
2. `relay/egress` с parser/validator/registry;
3. dialer interface и legacy adapter;
4. новые control messages и server-side selection;
5. wiring во все headless creators;
6. integration test двух upstream-профилей.

UI и persistence не следует начинать до фиксации схемы и протокольного контракта: иначе Android и desktop независимо закрепят несовместимые модели.

## 12. Итерация server control plane

Критичное эксплуатационное ограничение: в условиях активных блокировок joiner не должен зависеть от обычного HTTP/WebSocket API до creator-сервера. Такой API может быть полезен для локальной панели администратора, но не должен быть обязательным каналом между клиентом и сервером. Управляющий обмен между joiner и creator должен идти через уже разрешённый транспорт звонка.

Первая реализационная итерация добавляет call-carried session control:

- `MsgSessionCreate` — joiner отправляет через служебный звонок `requestId`, `egressId`, опциональные `platform` и `mode`;
- `MsgSessionReady` — creator отвечает `sessionId`, рабочей `joinLink`, подтверждённым `egressId` и TTL;
- `RelayBridge.RequestSession`, `SetOnSessionCreate`, `SetOnSessionReady` — транспорт только доставляет control messages, а создание звонков остаётся в orchestration-слое;
- `relay/controlplane.Manager` — удерживает лимиты: максимум один service session и один work session на пользователя, idempotency по `(userId, requestId)`, TTL cleanup и замену старого work session при переключении egress.
- `relay/controlplane.Orchestrator` — резолвит default/requested egress, не вызывает фабрику повторно для уже обработанного `requestId`, создаёт work-call через интерфейс `WorkCallFactory` и закрывает заменённую work session.
- `relay/controlplane.ProcessWorkCallFactory` — запускает существующие `headless-*-creator` binaries, ждёт `--write-file`, использует `CookieResolver` для user-scoped cookies и закрывает процесс при cleanup session.
- `MsgCookieSubmit` / `MsgCookieAck` и `relay/controlplane.CookieVault` — позволяют joiner передать cookies через служебный звонок, а creator хранит их в зашифрованном user-scoped vault и выдаёт временный JSON-файл только локальному headless creator.
- `relay/controlplane.ServiceHandler` — подключает `RelayBridge` служебного звонка к `CookieVault` и `Orchestrator`, так что platform-specific creator-service должен только создать service-call и вызвать `BindBridge`.
- `headless/creator-service` — первый headless entrypoint для server-side режима: поднимает WB Stream service-call, принимает cookies/session requests через звонок, хранит cookies в vault и запускает work-call через существующие headless creators. Docker поддерживает режим `SERVICE_MODE=creator-service`.
- `joiner-desktop-app/desktop-joiner --service-control` — подключается к service-call, отправляет `MsgCookieSubmit` и `MsgSessionCreate`, печатает `SERVICE_SESSION_READY`. Electron wrapper распознаёт этот marker и автоматически запускает обычный work-call joiner с полученной ссылкой.

Следующая итерация должна перенести этот service-control flow в Android/headless controllers и заменить ручной путь к cookie-файлу в desktop UI на безопасный локальный экспорт/выбор cookies.

### Android/headless service-control transport

- Общий `relay` CLI принимает `--service-control` и параметры пользователя, cookie submit, work platform, egress и idempotency request ID. Это покрывает VK, Telemost, WB Stream и DION headless joiner без дублирования platform-specific control-plane.
- В service-control режиме relay не открывает SOCKS5 listener и не запускает VPN: служебный звонок переносит только управляющие кадры `CookieSubmit` и `SessionCreate`.
- `HeadlessRelayController` передаёт типизированную `ServiceControlConfig` в native relay и разбирает `SERVICE_SESSION_READY` в `ServiceSessionReady` callback. Cookie payload не попадает в stdout или Android logs.
- Пользовательский Android flow пока не завершён: нужны Yandex login/cookie export в app-private storage, lifecycle orchestration `service call -> work call` и UI выбора egress. Передавать cookie через обычный API или хранить их в preferences нельзя.

Android lifecycle orchestration теперь реализован через `ServiceJoinController`: service destination подключается к WB Stream service-call, получает `SessionReady`, полностью останавливает служебный native relay и только затем запускает обычный headless joiner для work-call. Тот же controller используется из Activity и foreground service. Destination хранит флаг service-control и целевую work platform; ручной `egressId` остаётся частью destination. При переподключении service-call relay повторяет тот же idempotent session request, поэтому потеря ответа не создаёт дополнительный work-call.

Не завершён Yandex login/export UI. Android уже ищет cookie-файл только в app-private `files/service-cookies/<userId>/<platform>.json`; до появления безопасного exporter service flow использует ранее сохранённые server-side cookies либо завершается серверной ошибкой. Cookie payload и содержимое файла не записываются в preferences или логи.
