async function renderInbox() {
  const { inbox = [] } = await chrome.storage.local.get('inbox');
  const list = document.getElementById('inbox');
  const empty = document.getElementById('empty');
  list.innerHTML = '';

  if (!inbox.length) {
    empty.style.display = 'block';
    return;
  }
  empty.style.display = 'none';

  for (const link of inbox) {
    const li = document.createElement('li');
    const titleSpan = document.createElement('span');
    titleSpan.className = 'title';
    titleSpan.textContent = link.title || link.url;
    const urlSpan = document.createElement('span');
    urlSpan.className = 'url';
    urlSpan.textContent = link.url;
    li.appendChild(titleSpan);
    li.appendChild(urlSpan);

    li.addEventListener('click', async () => {
      chrome.tabs.create({ url: link.url });
      const { inbox: current = [] } = await chrome.storage.local.get('inbox');
      const updated = current.filter((l) => l.id !== link.id);
      await chrome.storage.local.set({ inbox: updated });
      chrome.action.setBadgeText({ text: updated.length ? String(updated.length) : '' });
      renderInbox();
    });

    list.appendChild(li);
  }
}

document.getElementById('send-current').addEventListener('click', async () => {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab?.url) return;
  await chrome.runtime.sendMessage({ type: 'SEND_LINK', url: tab.url, title: tab.title });
  const btn = document.getElementById('send-current');
  const original = btn.textContent;
  btn.textContent = 'Sent';
  setTimeout(() => (btn.textContent = original), 1200);
});

document.getElementById('open-options').addEventListener('click', (e) => {
  e.preventDefault();
  chrome.runtime.openOptionsPage();
});

(async () => {
  await chrome.runtime.sendMessage({ type: 'CHECK_NOW' });
  renderInbox();
})();
