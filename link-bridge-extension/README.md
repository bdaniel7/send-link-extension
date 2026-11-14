# Link Bridge (browser extension)

Send links between two Chromium browsers (for example Chrome and Brave) through a relay server you host yourself. Right-click a page or link in one browser, and it shows up in the other.

The same extension folder works in both browsers. It needs a running relay: see the `link-relay-go` project.

## How it works

```
Browser A (extension)  --POST /links-->  Relay  --"new-link" over WebSocket-->  Browser B (extension)
                                                                                      |
                                                              GET /links  <-----------+
```

1. In browser A you right-click a page or link and choose **Send this link to other browser**. The extension POSTs it to the relay.
2. The relay pushes a tiny `{"type":"new-link"}` message down every other connected browser's WebSocket.
3. Browser B reacts by calling `GET /links`, which returns links it hasn't seen yet (never its own sends).
4. B either opens them straight away (auto-open) or adds them to the popup inbox and badges the toolbar icon.

The WebSocket carries no link data. It is only a nudge, so there is a single source of truth for delivery.

### Staying connected under Manifest V3

MV3 service workers get killed after about 30 seconds of inactivity, and after roughly 5 minutes even if active. The extension handles this in three layers:

- **Heartbeat:** while the worker is alive, the relay sends an application-level heartbeat every 20 seconds. A real `onmessage` event is what resets the worker's idle timer. Protocol-level pings never reach extension code.
- **Quick retry:** if the socket closes while the worker is still alive, it retries after 3 seconds.
- **Alarm watchdog:** a `chrome.alarms` alarm fires once a minute (Chrome's minimum for production extensions). Alarms can wake a dead service worker, which reconnects the socket and does one catch-up `GET /links`.

In practice delivery is near-instant. Right after the worker has been recycled, it can take up to about a minute.

## Install

Do this in each browser.

1. Unzip the extension somewhere permanent. The browser loads it directly from that folder, so don't move or delete it afterwards.
2. Open `chrome://extensions` (Chrome) or `brave://extensions` (Brave) and turn on **Developer mode**.
3. Click **Load unpacked** and select the `link-bridge-extension` folder.
4. Click the extension icon, open **Settings**, and fill in the relay details (see below).

If you move the folder or edit the code, click the reload icon on the extension's card.

## Configure

Open the extension's **Settings** page:

| Setting | What to enter |
| --- | --- |
| Relay URL | Base URL of your relay, e.g. `http://<pi-mesh-ip>:8787` or `https://relay.example.com`. No trailing path. |
| API token | The `API_TOKEN` you set on the relay. It must be identical in both browsers. |
| Auto-open | If ticked, received links open in new tabs immediately instead of waiting in the inbox. |

When you save, the browser asks for permission to access the relay's origin. Accept it. If you decline, nothing is saved.

## Usage

- **Right-click a page** and choose *Send this page to other browser*.
- **Right-click a link** and choose *Send this link to other browser*.
- **Toolbar popup:** *Send current tab* sends the active tab. Opening the popup also checks the relay immediately.
- **Inbox:** received links are listed in the popup (up to 50). Click one to open it and remove it from the list. The toolbar badge shows how many are waiting.

## Where data is stored

| Data | Storage | Notes |
| --- | --- | --- |
| Relay URL, API token, auto-open | `chrome.storage.sync` | Syncs between devices signed into the same browser account. |
| Device ID, inbox | `chrome.storage.local` | Per browser install. The device ID is generated on first run and is how the relay avoids echoing your own links back to you. |

The API token is stored in plain text in extension storage. If you don't want it to roam with browser sync, change `chrome.storage.sync` to `chrome.storage.local` in `background.js` and `options.js`.

## Permissions

| Permission | Why |
| --- | --- |
| `contextMenus` | The right-click send entries. |
| `storage` | Settings, device ID, inbox. |
| `tabs` | Reading the active tab's URL and title, and opening received links. |
| `notifications` | "New link received" and error notices. |
| `alarms` | The one-minute watchdog that wakes the service worker. |
| optional host permission | Requested at runtime for your relay's origin only, when you save settings. |

## Troubleshooting

**Nothing arrives in the other browser**
- Check the relay from the same machine: `curl -H "Authorization: Bearer <token>" "<relay-url>/links?deviceId=test"` should return `[]`.
- Make sure both browsers use the same relay URL and token. A wrong token gives HTTP 401.
- Open `chrome://extensions`, click **service worker** on the extension card, and look at the console for errors.

**Saving settings does nothing**
- You probably dismissed the permission prompt. Save again and accept it.

**Links take up to a minute**
- The service worker was recycled and is waiting for the next alarm tick. That is expected occasionally.

**"Send failed" notification**
- The relay is unreachable or returned an error. Check the relay is running and reachable over your VPN or network.

**Behind a reverse proxy**
- WebSocket upgrades must be allowed on `/stream`, with a long enough tunnel or idle timeout (the heartbeat is every 20 seconds).

## Limitations

- Polling fallback and watchdog run at most once a minute (Chrome's alarm floor in production).
- The inbox keeps the 50 most recent links. The relay keeps links for 48 hours.
- Built for two or a few devices sharing one relay and one token. There are no per-user accounts.

## Files

```
manifest.json   MV3 manifest
background.js   Service worker: context menus, WebSocket, watchdog alarm, send/receive
popup.html/js   Toolbar popup: inbox and "Send current tab"
options.html/js Settings page (relay URL, token, auto-open)
icons/          Toolbar and notification icons
```
