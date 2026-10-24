async function action(type) {
  const status = document.getElementById('status');
  try {
    const result = await chrome.runtime.sendMessage({type, enabled:document.getElementById('continuous').checked, alias:document.getElementById('alias').value, publicWeb:document.getElementById('public').checked, allowNew:document.getElementById('new').checked});
    if (result.error) throw Error(result.error);
    document.getElementById('continuous').checked = result.continuous;
    document.getElementById('new').checked = result.allowNew;
    document.getElementById('alias').value=result.alias||'';
    status.textContent = `${result.paused ? 'Пауза' : result.connected ? 'Готово' : 'Ожидание агента'} · Доступно вкладок: ${result.count}`;
  } catch (error) { status.textContent = error.message; }
}
for (const id of ['attach','pause','resume','disconnect']) document.getElementById(id).onclick = () => action(id);
document.getElementById('continuous').onchange = () => action('continuous');
document.getElementById('new').onchange = () => action('options');
action('status');

document.getElementById('alias').onchange=()=>action('identity');
