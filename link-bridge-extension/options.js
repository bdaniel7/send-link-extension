// Best-effort guess at which browser this is, used to pre-fill the label
// the first time settings are opened. Brave identifies itself via
// navigator.brave rather than the user agent.
async function detectBrowserName() {
  try {
    if (navigator.brave && (await navigator.brave.isBrave())) return 'Brave';
  } catch {}
  const ua = navigator.userAgent;
  if (ua.includes('Edg/')) return 'Edge';
  if (ua.includes('OPR/')) return 'Opera';
  if (ua.includes('Vivaldi/')) return 'Vivaldi';
  if (ua.includes('Firefox/')) return 'Firefox';
  return 'Chrome';
}

async function load() {
  const { relayUrl = '', apiToken = '', autoOpen = false } = await chrome.storage.sync.get([
    'relayUrl', 'apiToken', 'autoOpen'
  ]);
  // The label is per browser install, so it lives in local storage, not sync.
  const { label } = await chrome.storage.local.get('label');

  document.getElementById('relayUrl').value = relayUrl;
  document.getElementById('apiToken').value = apiToken;
  document.getElementById('autoOpen').checked = autoOpen;
  // Never set yet => suggest a name. Set to '' on purpose => leave it empty.
  document.getElementById('label').value = label === undefined ? await detectBrowserName() : label;
}

document.getElementById('save').addEventListener('click', async () => {
  const relayUrl = document.getElementById('relayUrl').value.trim().replace(/\/$/, '');
  const apiToken = document.getElementById('apiToken').value.trim();
  const autoOpen = document.getElementById('autoOpen').checked;
  const label = document.getElementById('label').value.trim().slice(0, 32);
  const status = document.getElementById('status');

  if (relayUrl) {
    let origin;
    try {
      origin = new URL(relayUrl).origin + '/*';
    } catch {
      status.textContent = "That doesn't look like a valid URL.";
      return;
    }
    const granted = await chrome.permissions.request({ origins: [origin] });
    if (!granted) {
      status.textContent = 'Permission for that URL was not granted — settings not saved.';
      return;
    }
  }

  await chrome.storage.sync.set({ relayUrl, apiToken, autoOpen });
  await chrome.storage.local.set({ label });
  status.textContent = 'Saved.';
  setTimeout(() => (status.textContent = ''), 2000);
});

load();
