const ALARM_NAME = 'link-bridge-watchdog';
const WATCHDOG_PERIOD_MINUTES = 1; // Chrome's production minimum for repeating alarms.
const STALE_AFTER_MS = 45000; // no message (including heartbeats) in this long => treat socket as dead.

let ws = null;
let connecting = false;
let lastMessageAt = 0;

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.create({
    id: 'send-page',
    title: 'Send this page to other browser',
    contexts: ['page']
  });
  chrome.contextMenus.create({
    id: 'send-link',
    title: 'Send this link to other browser',
    contexts: ['link']
  });
  chrome.alarms.create(ALARM_NAME, { periodInMinutes: WATCHDOG_PERIOD_MINUTES });
});

chrome.runtime.onStartup.addListener(() => {
  chrome.alarms.create(ALARM_NAME, { periodInMinutes: WATCHDOG_PERIOD_MINUTES });
});

// Top-level code re-runs every time Chrome (re)starts this service worker —
// including after it's been killed for inactivity or hit the ~5 minute cap.
// That's what makes the alarm-driven wake-up actually restore the socket.
connectWebSocket();

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name !== ALARM_NAME) return;
  // Watchdog: reconnect if the socket is gone or has gone quiet, and do one
  // catch-up poll regardless, in case a push was missed while disconnected.
  if (!ws || ws.readyState === WebSocket.CLOSED || Date.now() - lastMessageAt > STALE_AFTER_MS) {
    connectWebSocket();
  }
  pollForLinks();
});

chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'sync' && (changes.relayUrl || changes.apiToken)) {
    if (ws) { try { ws.close(); } catch {} ws = null; }
    connectWebSocket();
  }
});

chrome.contextMenus.onClicked.addListener((info) => {
  if (info.menuItemId === 'send-page') {
    sendLink(info.pageUrl, '');
  } else if (info.menuItemId === 'send-link' && info.linkUrl) {
    sendLink(info.linkUrl, '');
  }
});

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg?.type === 'SEND_LINK') {
    sendLink(msg.url, msg.title).then(() => sendResponse({ ok: true }));
    return true; // keep the message channel open for the async response
  }
  if (msg?.type === 'CHECK_NOW') {
    pollForLinks().then(() => sendResponse({ ok: true }));
    return true;
  }
});

async function getSettings() {
  const { relayUrl, apiToken, autoOpen } = await chrome.storage.sync.get([
    'relayUrl', 'apiToken', 'autoOpen'
  ]);
  return { relayUrl: relayUrl || '', apiToken: apiToken || '', autoOpen: !!autoOpen };
}

async function getDeviceId() {
  const { deviceId } = await chrome.storage.local.get('deviceId');
  if (deviceId) return deviceId;
  const id = crypto.randomUUID();
  await chrome.storage.local.set({ deviceId: id });
  return id;
}

function notify(title, message) {
  chrome.notifications.create({
    type: 'basic',
    iconUrl: 'icons/icon128.png',
    title,
    message
  });
}

async function connectWebSocket() {
  if (connecting || (ws && ws.readyState === WebSocket.OPEN)) return;
  connecting = true;
  try {
    const { relayUrl, apiToken } = await getSettings();
    if (!relayUrl || !apiToken) return;
    const deviceId = await getDeviceId();

    const wsUrl = relayUrl.replace(/^http/, 'ws') +
      `/stream?token=${encodeURIComponent(apiToken)}&deviceId=${encodeURIComponent(deviceId)}`;

    const socket = new WebSocket(wsUrl);

    socket.onopen = () => {
      lastMessageAt = Date.now();
      // Catch up on anything that arrived while we were disconnected.
      pollForLinks();
    };
    socket.onmessage = (event) => {
      lastMessageAt = Date.now();
      let msg;
      try { msg = JSON.parse(event.data); } catch { return; }
      if (msg.type === 'new-link') pollForLinks();
      // 'heartbeat' messages need no action — just arriving keeps this
      // service worker's activity timer from expiring.
    };
    socket.onclose = () => {
      if (ws === socket) ws = null;
      // Best-effort quick retry if the worker is still alive; the alarm
      // above is the real safety net if the worker itself gets killed
      // before this timer fires.
      setTimeout(() => connectWebSocket(), 3000);
    };
    socket.onerror = () => {
      try { socket.close(); } catch {}
    };

    ws = socket;
  } finally {
    connecting = false;
  }
}

async function sendLink(url, title) {
  const { relayUrl, apiToken } = await getSettings();
  if (!relayUrl || !apiToken) {
    notify('Link Bridge not configured', 'Set your relay URL and token in the extension options.');
    return;
  }
  const deviceId = await getDeviceId();
  try {
    const res = await fetch(`${relayUrl}/links`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${apiToken}`
      },
      body: JSON.stringify({ url, title, deviceId })
    });
    if (!res.ok) throw new Error(`relay responded ${res.status}`);
  } catch (err) {
    notify('Link Bridge: send failed', String(err));
  }
}

async function pollForLinks() {
  const { relayUrl, apiToken, autoOpen } = await getSettings();
  if (!relayUrl || !apiToken) return;
  const deviceId = await getDeviceId();

  try {
    const res = await fetch(`${relayUrl}/links?deviceId=${encodeURIComponent(deviceId)}`, {
      headers: { Authorization: `Bearer ${apiToken}` }
    });
    if (!res.ok) throw new Error(`relay responded ${res.status}`);
    const incoming = await res.json();
    if (!incoming.length) return;

    if (autoOpen) {
      for (const link of incoming) {
        chrome.tabs.create({ url: link.url });
      }
      return;
    }

    const { inbox = [] } = await chrome.storage.local.get('inbox');
    const updated = [...incoming, ...inbox].slice(0, 50);
    await chrome.storage.local.set({ inbox: updated });
    chrome.action.setBadgeText({ text: String(updated.length) });
    chrome.action.setBadgeBackgroundColor({ color: '#0F6E56' });
    notify('New link received', incoming[0].title || incoming[0].url);
  } catch (err) {
    console.error('Link Bridge poll failed', err);
  }
}
