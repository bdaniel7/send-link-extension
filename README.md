# Link Bridge

Send links from one browser to another, for example Chrome to Brave, through a relay server you host yourself. Right-click a page or link in one browser and it appears in the other, usually within a second.

This is useful when testing a page in multiple browsers or when trying to open a page stuffed with ads in a browser that supports ad blockers.

Link Bridge is made of two separate projects that talk to each other over a small HTTP and WebSocket API:

```
 Browser A                         Relay                          Browser B
 (extension)  --POST /links-->  (link-relay-go)  --"new-link"-->  (extension)
                                       |           over WebSocket       |
                                       +------<---- GET /links ---------+
```

## The projects

### [`link-bridge-extension`](link-bridge-extension/README.md)

A Manifest V3 browser extension for Chrome, Brave, and other Chromium browsers. The same folder works in every browser.

- Adds right-click entries to send the current page or a link to your other browser, named by its label (for example "Send this page to Brave"), plus a toolbar popup with a "Send current tab" button and an inbox of received links.
- Receives links either by opening them automatically or by queuing them in the popup with a badge count.
- Keeps a WebSocket open to the relay for near-instant delivery. A heartbeat, a quick retry, and a once-a-minute `chrome.alarms` watchdog work around Chrome's service worker lifetime limits.
- Settings are the relay URL, the shared API token, an auto-open toggle, and a label naming this browser. Other browsers use it in their menus and inbox, and the relay logs it.

### [`link-relay-go`](link-relay-go/README.md)

The relay server, written in Go with the standard library only, so it builds to a single static binary with no dependencies.

- `POST /links` stores a link, `GET /links?deviceId=...` returns the links a device hasn't received yet, `GET /devices` lists the labels of the other connected browsers, and `GET /stream` is the WebSocket that nudges connected devices when something new arrives.
- Stores links in one JSON file, expires them after 48 hours, and tracks delivery per device so more than two devices can share one relay.
- Protected by a single shared bearer token. Plain HTTP, intended to run on a private network or VPN, or behind TLS.

## How they fit together

The two projects only share an API contract. Neither depends on the other's code.

1. A browser POSTs a link to the relay with its device ID.
2. The relay saves it and pushes `{"type":"new-link"}` to every other connected device. The WebSocket never carries link data.
3. Each of those devices calls `GET /links`, which returns only what it hasn't seen and marks it as seen.
4. If a device was disconnected, it catches up with a `GET /links` when it reconnects and on every watchdog tick.

## Quick start

1. **Build and run the relay** on a machine both browsers can reach:
   ```bash
   cd link-relay-go
   go build -o link-relay .
   API_TOKEN=$(openssl rand -hex 32) ./link-relay   # note the token
   ```
   For a permanent setup, run it under systemd. See the relay's README.
2. **Load the extension** in each browser: open `chrome://extensions` or `brave://extensions`, turn on Developer mode, click **Load unpacked**, and select `link-bridge-extension`.
3. **Configure the extension** in each browser: open its Settings, enter the relay URL (for example `http://<host>:8787`) and the same API token, and accept the permission prompt.
4. **Try it:** right-click a link in one browser and choose *Send this link to other browser*. It shows up in the other browser's popup, or opens directly if auto-open is on.

## Layout

```
README.md                 This file
link-bridge-extension/    Browser extension (see its README)
link-relay-go/            Relay server in Go (see its README)
```

## Security in brief

- One shared token authenticates every device. There are no per-user accounts and no rate limiting.
- The relay uses plain HTTP. That is fine inside a WireGuard or Headscale mesh. Otherwise put it behind TLS.
- The WebSocket token travels in the URL, so keep the relay on a private network or keep query strings out of proxy logs.
- The extension stores the token in `chrome.storage.sync` by default. The extension README explains how to switch to local-only storage.
