# 1. Карта системы

## Процессы

**В приложении HA** (`alice/app/run.py` поднимает всё в одном контейнере):

| Процесс | Код | Порт | Роль |
|---|---|---|---|
| gateway | `alice/app/gateway.py` | 8099 (Ingress) | панель, Wiki, allowlist API менеджера |
| manager | `alice/manager/service.py` (1707 строк), `admin.py`, `updater.py`, `proxy.py` | внутренний | реестр колонок, SSH-операции, установка/обновление мостов, сбор состояния |
| Home Functions (HF) | `alice/server/src/*.ts` → `home-functions/main.mjs` | 18080 (наружу 18081) | каталог устройств, `/resolve`, `/functions`, `/voice-phrases`, `/now-playing`, прокси `/sendspin/*` |
| music | `alice/music/sendspin_service.py` | 127.0.0.1:18095 | Sendspin-плеер на каждую колонку, поиск/запуск через HA → MA |

**На колонке**: `quasar-bridge` (Go, `alice/bridge`), слушает 127.0.0.1:18861 (прокси uniproxy, `/healthz`, `/metrics`, `/volume`, `/player`, `/airplay`) и LAN-порт с токеном (`serveLAN`). Рядом штатные `quasar`/`maind`, сокет событий Алисы 127.0.0.1:9875, микшер ALSA `Master`, кадровый буфер кольца `/dev/fb0`.

**Внешние**: Home Assistant (WebSocket + REST), Music Assistant (Sendspin 8927, API 8095), LLM-шлюз (OpenAI-совместимый), облако Яндекса (uniproxy).

## Потоки

```
Голос:      микрофон → quasar → bridge(/uni.ws прокси) ──┬─ облако Яндекса (по разрешению моста)
                                                         ├─ HF: /functions, /resolve, POST /functions → HA
                                                         ├─ LLM (планировщик дома / ответы)
                                                         └─ music: /sendspin/play → HA music_assistant.* → MA
Музыка MA:  MA ─Sendspin→ music(py) ─HTTP поток кадров→ HF(/sendspin/stream) → bridge → station-pcm → ALSA
Состояние:  bridge ─POST /now-playing (на изменение, раз в 15 с)→ HF (память)
            manager ─GET /now-playing раз в секунду→ HF;  manager → HA (Yandex/MA media_player)
            панель ─long-poll /api/speakers-wait→ manager (+ полный опрос раз в 30 с)
Словарь:    bridge ─GET /voice-phrases раз в 5 с (ETag)→ HF
Подсветка:  bridge: 9875 (состояние Алисы) + микшер + звуковая карта + Sendspin → кольцо 60 кадр/с
            manager ─SSH→ indicator.json на колонке
```

## Где сейчас «не первоисточник»

| Данные | Сейчас | Первоисточник |
|---|---|---|
| Трек MA на карточке | менеджер ищет HA-сущность шаблоном, читает её состояние; панель следит за `hass.states` | MA (событие очереди) или Sendspin metadata, которую music уже получает |
| Выполнение команды | опрос состояния каждые 100 мс до 2 с (`server/src/execution.ts`) | событие `state_changed` из уже открытого WebSocket HA |
| Каталог устройств | пересборка на каждый `POST /functions` (`getDevices(true)`, `main.ts`) | кэш, сбрасываемый событиями `*_registry_updated` (подписка уже есть) |
| Состояние колонки для панели | bridge → HF → менеджер (опрос 1 с) → панель | bridge → менеджер (одно событие) → панель |
| Разбор команд умного дома | своя грамматика `resolveLightCommand` + LLM-планировщик | HA Assist intents (`conversation/process`), своё — только дополнения |
| Поиск музыки | HA-сервисы `music_assistant.search/play_media` + своё ранжирование | API MA напрямую (тот же поиск, очередь, top_tracks, radio) |
| Музыка играет? (кольцо) | звуковая карта + флаг Sendspin + `SetMusic` от HA | один флаг плеера моста (он сам пишет в ALSA) |
