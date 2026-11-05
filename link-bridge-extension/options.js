async function load() {
  const { relayUrl = '', apiToken = '', autoOpen = false } = await chrome.storage.sync.get([
    'relayUrl', 'apiToken', 'autoOpen'
  ]);
  document.getElementById('relayUrl').value = relayUrl;
  document.getElementById('apiToken').value = apiToken;
  document.getElementById('autoOpen').checked = autoOpen;
}

document.getElementById('save').addEventListener('click', async () => {
  const relayUrl = document.getElementById('relayUrl').value.trim().replace(/\/$/, '');
  const apiToken = document.getElementById('apiToken').value.trim();
  const autoOpen = document.getElementById('autoOpen').checked;
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
  status.textContent = 'Saved.';
  setTimeout(() => (status.textContent = ''), 2000);
});

load();
