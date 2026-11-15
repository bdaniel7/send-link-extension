# Link Relay (Go)

A small relay server that lets the Link Bridge browser extension pass links between two or more browsers. It stores links in a JSON file and pushes a "new link" nudge over WebSocket so other browsers pick them up immediately.

Standard library only: no external dependencies, nothing to download at build time, and it builds to a single static binary.

## Requirements

- Go 1.22 or newer, only to build. The resulting binary needs no runtime.
- A writable working directory (the relay creates `links.json` there).

## Build

Build on the machine that will run it:

```bash
go build -o link-relay .
```

Or cross-compile, for example for a Raspberry Pi running 64-bit Linux, and copy the binary over:

```bash
GOOS=linux GOARCH=arm64 go build -o link-relay .
scp link-relay pi@<host>:/opt/link-relay-go/
```

## Configure

Configuration is through environment variables:

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `API_TOKEN` | yes | none | Shared secret. The relay refuses to start without it. Use a long random value, e.g. `openssl rand -hex 32`. |
| `PORT` | no | `8787` | TCP port to listen on. |

The relay does **not** read `.env` itself. Either let systemd load it (see below) or export the variables in your shell:

```bash
cp .env.example .env
# edit .env and set API_TOKEN
set -a; source .env; set +a
./link-relay
```

## Run under systemd

Put the binary and `.env` in `/opt/link-relay-go`, then create `/etc/systemd/system/link-relay.service`:

```ini
[Unit]
Description=Link Bridge relay
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/link-relay-go
EnvironmentFile=/opt/link-relay-go/.env
ExecStart=/opt/link-relay-go/link-relay
Restart=on-failure
User=pi

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now link-relay
sudo systemctl status link-relay
journalctl -u link-relay -f
```

`WorkingDirectory` matters because `links.json` is written relative to it, and the service user must be able to write there. In `.env`, write plain `KEY=value` lines with no quotes and no `export`.

## API

REST endpoints use `Authorization: Bearer <API_TOKEN>`.

### `POST /links`

Add a link.

```json
{ "url": "https://example.com", "title": "Example", "deviceId": "device-a", "label": "Brave" }
```

`url` and `deviceId` are required. `label` is optional: a name for the sending browser, trimmed, stripped of control characters and capped at 40 characters. Responds `201` with `{"ok": true, "id": "<uuid>"}`. Every connected device except the sender is then notified over WebSocket.

### `GET /links?deviceId=<id>`

Return links this device hasn't received yet, excluding links it sent itself. Each returned link is marked as seen by that device, so a second call returns `[]`.

```json
[{ "id": "...", "url": "https://example.com", "title": "Example", "createdAt": 1787947979166, "fromLabel": "Brave" }]
```

`createdAt` is Unix time in milliseconds. `fromLabel` is the sender's label and is omitted if it didn't send one.

### `GET /devices?deviceId=<id>`

Return the labels of the other browsers currently connected over WebSocket, so an extension can name its destination (for example "Send this page to Brave"). Labels are distinct, non-empty and sorted; devices that haven't set a label are omitted.

```json
{ "labels": ["Brave", "Firefox"] }
```

### `GET /stream?token=<API_TOKEN>&deviceId=<id>&label=<name>` (WebSocket)

Real-time channel. Browsers can't set custom headers on a WebSocket handshake, so the token goes in the query string here instead. A bad token gets HTTP `401`. `label` is optional and is how this device appears in other devices' `GET /devices` results.

The server sends two kinds of text messages:

| Message | Meaning |
| --- | --- |
| `{"type":"new-link"}` | Another device sent a link. Call `GET /links`. |
| `{"type":"devices-changed"}` | Another device just connected. Call `GET /devices` to refresh the browser names shown in menus. |
| `{"type":"heartbeat"}` | Sent every 20 seconds so the extension's service worker stays awake. No action needed. |

The server also sends protocol-level pings and drops connections that don't answer the previous one. The socket never carries link data. `GET /links` remains the single source of truth for delivery.

## Behaviour notes

- **Expiry:** links older than 48 hours are dropped the next time `GET /links` runs.
- **Storage:** a single `links.json` file. All reads and writes happen inside one mutex-guarded load-modify-save step, so concurrent requests can't lose updates.
- **Multiple devices:** delivery is tracked per device in a `seenBy` list rather than deleting on read, so more than two devices can share one relay.
- **Multiple connections per device** are allowed and all receive pushes.
- **Device names aren't stored.** `GET /devices` reports whoever is connected right now; extensions cache the last names they saw.

## Logging

The relay logs to standard output (under systemd, view it with `journalctl -u link-relay -f`). Each accepted link produces one line:

```
2026/11/15 14:35:56 received url:https://example.com/page from host:192.168.1.20 label:Brave
```

The ` label:` part appears only when the sending browser has a label set.

- `host` is the client IP as seen by the relay. Behind a reverse proxy it will be the proxy's address.
- Control characters in the URL are replaced with `?` so a crafted URL can't forge extra log lines.
- Requests with a bad token or a missing `url`/`deviceId` are rejected before this line is logged.
- Full URLs are logged, including any query strings. Keep that in mind if you send links that contain secrets.

## Quick test

```bash
export TOKEN=<your token>

# send as device A
curl -X POST http://localhost:8787/links \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"url":"https://example.com","title":"Example","deviceId":"a"}'

# device A sees nothing (own link), device B receives it once
curl "http://localhost:8787/links?deviceId=a" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8787/links?deviceId=b" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8787/links?deviceId=b" -H "Authorization: Bearer $TOKEN"   # now []
```

To watch the WebSocket, `websocat "ws://localhost:8787/stream?token=$TOKEN&deviceId=b"` works, then POST a link as another device.

## Security notes

- The relay speaks plain HTTP. That is fine inside a WireGuard or Headscale mesh, which already encrypts the traffic. If you expose it any other way, put it behind TLS.
- Because the WebSocket token is in the URL, a reverse proxy may log it. Keep the relay on the private network, or scrub query strings from logs.
- One shared token, no rate limiting, no per-user accounts. It is meant for a handful of your own devices.
- Behind a reverse proxy such as HAProxy or nginx, allow WebSocket upgrades on `/stream` and set a long enough tunnel or idle timeout.

## Limitations

- The built-in WebSocket implementation supports unfragmented text frames, ping/pong and close, which is all the extension uses. It does not handle continuation frames.
- Storage is one JSON file, which is plenty for a few links a day but not designed for heavy traffic.

## Files

```
main.go       Config, wiring, server startup
hub.go        HTTP handlers, connection hub (broadcast + heartbeat), auth
websocket.go  Minimal RFC 6455 WebSocket implementation
store.go      Link type and JSON-file storage
.env.example  Example configuration
```
