async function action(type) {
  const status = document.getElementById('status');
  try {
    const result = await chrome.runtime.sendMessage({type, publicWeb:document.getElementById('public').checked, allowNew:document.getElementById('new').checked});
    if (result.error) throw Error(result.error);
    status.textContent = `${result.paused ? 'Пауза' : 'Готово'} · Подключено вкладок: ${result.count}`;
  } catch (error) { status.textContent = error.message; }
}
for (const id of ['attach','pause','resume','disconnect']) document.getElementById(id).onclick = () => action(id);
action('status');
