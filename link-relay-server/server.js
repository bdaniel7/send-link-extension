const express = require('express');
const http = require('http');
const { WebSocketServer } = require('ws');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const { URL } = require('url');

const app = express();
app.use(express.json());

const PORT = process.env.PORT || 8787;
const API_TOKEN = process.env.API_TOKEN;
const DATA_FILE = path.join(__dirname, 'links.json');
const MAX_AGE_MS = 48 * 60 * 60 * 1000; // drop links older than 48h
const HEARTBEAT_INTERVAL_MS = 20000;

if (!API_TOKEN) {
  console.error('Set the API_TOKEN environment variable before starting (see .env.example).');
  process.exit(1);
}

function loadLinks() {
  try {
    return JSON.parse(fs.readFileSync(DATA_FILE, 'utf8'));
  } catch {
    return [];
  }
}

function saveLinks(links) {
  fs.writeFileSync(DATA_FILE, JSON.stringify(links, null, 2));
}

app.use((req, res, next) => {
  const token = (req.headers.authorization || '').replace(/^Bearer\s+/i, '');
  if (token !== API_TOKEN) return res.status(401).json({ error: 'unauthorized' });
  next();
});

// A browser calls this to push a link into the relay.
app.post('/links', (req, res) => {
  const { url, title, deviceId } = req.body || {};
  if (!url || !deviceId) {
    return res.status(400).json({ error: 'url and deviceId are required' });
  }
  const links = loadLinks();
  const entry = {
    id: crypto.randomUUID(),
    url,
    title: title || '',
    from: deviceId,
    createdAt: Date.now(),
    seenBy: []
  };
  links.push(entry);
  saveLinks(links);
  res.status(201).json({ ok: true, id: entry.id });

  // Wake up every other connected client so it polls right away instead of
  // waiting for its next watchdog tick.
  broadcastExcept(deviceId, { type: 'new-link' });
});

// A browser polls this to fetch links it hasn't seen yet, excluding its own sends.
// Marking seenBy (rather than deleting) lets more than two devices share one relay later.
// This stays the source of truth for delivery — the WebSocket below only ever
// tells a client "go call this," so the seen/unseen bookkeeping never has to
// live in two places at once.
app.get('/links', (req, res) => {
  const deviceId = req.query.deviceId;
  if (!deviceId) return res.status(400).json({ error: 'deviceId is required' });

  const now = Date.now();
  const links = loadLinks().filter((l) => now - l.createdAt < MAX_AGE_MS);
  const unseen = links.filter((l) => l.from !== deviceId && !l.seenBy.includes(deviceId));
  unseen.forEach((l) => l.seenBy.push(deviceId));

  saveLinks(links);
  res.json(unseen.map(({ id, url, title, createdAt }) => ({ id, url, title, createdAt })));
});

// --- WebSocket push layer ---------------------------------------------
// Browsers can't set custom headers on a WebSocket handshake, so the token
// travels as a query parameter here instead of an Authorization header.

const server = http.createServer(app);
const wss = new WebSocketServer({ noServer: true });

// deviceId -> Set of live sockets for that device (normally just one)
const clients = new Map();

server.on('upgrade', (req, socket, head) => {
  let token, deviceId;
  try {
    const url = new URL(req.url, `http://${req.headers.host}`);
    if (url.pathname !== '/stream') throw new Error('not found');
    token = url.searchParams.get('token');
    deviceId = url.searchParams.get('deviceId');
  } catch {
    socket.destroy();
    return;
  }
  if (token !== API_TOKEN || !deviceId) {
    socket.write('HTTP/1.1 401 Unauthorized\r\n\r\n');
    socket.destroy();
    return;
  }
  wss.handleUpgrade(req, socket, head, (ws) => {
    ws.deviceId = deviceId;
    wss.emit('connection', ws, req);
  });
});

wss.on('connection', (ws) => {
  if (!clients.has(ws.deviceId)) clients.set(ws.deviceId, new Set());
  clients.get(ws.deviceId).add(ws);
  ws.isAlive = true;
  ws.on('pong', () => { ws.isAlive = true; });
  ws.on('close', () => {
    clients.get(ws.deviceId)?.delete(ws);
  });
});

function broadcastExcept(excludeDeviceId, payload) {
  const data = JSON.stringify(payload);
  for (const [deviceId, sockets] of clients) {
    if (deviceId === excludeDeviceId) continue;
    for (const ws of sockets) {
      if (ws.readyState === ws.OPEN) ws.send(data);
    }
  }
}

// Ping for liveness, and separately send an app-level heartbeat message.
// Protocol-level pong frames never reach the extension's JS, so they can't
// keep its service worker's activity timer from expiring — an actual
// message dispatched to onmessage does.
setInterval(() => {
  for (const [deviceId, sockets] of clients) {
    for (const ws of sockets) {
      if (!ws.isAlive) {
        ws.terminate();
        sockets.delete(ws);
        continue;
      }
      ws.isAlive = false;
      ws.ping();
      if (ws.readyState === ws.OPEN) {
        ws.send(JSON.stringify({ type: 'heartbeat', ts: Date.now() }));
      }
    }
    if (sockets.size === 0) clients.delete(deviceId);
  }
}, HEARTBEAT_INTERVAL_MS);

server.listen(PORT, () => console.log(`Link relay listening on port ${PORT}`));
