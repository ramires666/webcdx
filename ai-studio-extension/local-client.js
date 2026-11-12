(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.WebCodexLocal = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';
  const ENDPOINTS = Array.from({ length: 10 }, (_, i) => `http://127.0.0.1:${47831 + i}`);
  const LIMIT = 4 * 1024 * 1024;
  function unavailable(message) { const error = Error(message); error.code = 'NATIVE_DISCONNECTED'; return error; }
  function createClient({ fetchImpl = globalThis.fetch, extensionId = globalThis.chrome?.runtime?.id, version = '0.3.3', endpoint = '', agentIdentity = '' } = {}) {
    if (endpoint && !ENDPOINTS.includes(endpoint)) throw Error('Invalid saved local endpoint');
    let selected = endpoint, identity = agentIdentity, nextId = 0, closed = false;
    const active = new Set();
    async function request(base, method, params, signal, timeout = 15000) {
      if (closed) throw unavailable('Local client closed');
      const body = JSON.stringify({ id: String(++nextId), method, params });
      if (new TextEncoder().encode(body).byteLength > 1024 * 1024) throw Error('Local request exceeds 1 MiB');
      const controller = new AbortController(); active.add(controller);
      const abort = () => controller.abort();
      if (signal?.aborted) abort(); else signal?.addEventListener('abort', abort, { once: true });
      const timer = setTimeout(abort, timeout);
      try {
        const response = await fetchImpl(base + '/studio/rpc', { method: 'POST', credentials: 'omit', cache: 'no-store', redirect: 'error', signal: controller.signal,
          headers: { 'Content-Type': 'application/json', 'X-WebCodex-Extension': extensionId, ...(identity ? { 'X-WebCodex-Agent': identity } : {}) }, body });
        if (!response.ok) throw unavailable(response.status === 409 ? 'The saved local agent is unavailable; another agent will not receive its operations' : `Local agent HTTP ${response.status}`);
        let text = '';
        if (response.body?.getReader) {
          const reader = response.body.getReader(), decoder = new TextDecoder(); let size = 0;
          try { while (true) { const { done, value } = await reader.read(); if (done) break; size += value.byteLength; if (size > LIMIT) { await reader.cancel(); throw Error('Local response exceeds 4 MiB'); } text += decoder.decode(value, { stream: true }); } text += decoder.decode(); }
          finally { reader.releaseLock(); }
        } else { text = await response.text(); if (new TextEncoder().encode(text).byteLength > LIMIT) throw Error('Local response exceeds 4 MiB'); }
        const frame = JSON.parse(text);
        if (frame?.id !== JSON.parse(body).id) throw Error('Local response ID mismatch');
        if (frame.error) { const error = Error(frame.error.message || 'Local RPC failed'); error.code = frame.error.code; throw error; }
        if (!Object.hasOwn(frame, 'result')) throw Error('Invalid local response');
        return frame.result;
      } catch (error) {
        if (error instanceof TypeError || controller.signal.aborted) throw unavailable('Local agent disconnected or did not respond');
        throw error;
      } finally { clearTimeout(timer); active.delete(controller); signal?.removeEventListener('abort', abort); }
    }
    async function initialize(signal) {
      const params = { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'WebCodex AI Studio Bridge', version } };
      for (const base of selected ? [selected] : ENDPOINTS) {
        try {
          const result = await request(base, 'initialize', params, signal, 1000);
          if (!result?.localAgent?.id || (identity && result.localAgent.id !== identity)) throw unavailable('Local agent identity mismatch');
          selected = base; identity = result.localAgent.id; return result;
        } catch (error) { if (selected || signal?.aborted || closed) throw error; }
      }
      throw unavailable('Запустите локальный webcodex-agent.exe: доступный агент не найден');
    }
    async function rpc(method, params = {}, signal) {
      if (!selected || !identity) throw unavailable('Local agent has not been discovered');
      return request(selected, method, params, signal);
    }
    async function listTools(signal) { const result = await rpc('tools/list', {}, signal); if (!Array.isArray(result?.tools)) throw Error('Invalid tools/list result'); return result.tools; }
    async function listAgents(signal) {
      const result = await rpc('tools/call', { name: 'list_agents', arguments: {} }, signal);
      if (result?.isError || !Array.isArray(result?.structuredContent?.agents)) throw Error('Invalid list_agents result');
      return result.structuredContent.agents;
    }
    function createOperation({ key, name, arguments: args, agentId }, signal) { return rpc('operations/create', { key, name, arguments: args, agentId }, signal); }
    function getOperation(id, signal) { return rpc('operations/get', { id }, signal); }
    function close() { closed = true; for (const controller of active) controller.abort(); }
    return { rpc, initialize, listTools, listAgents, createOperation, getOperation, close, get endpoint() { return selected; }, get agentIdentity() { return identity; } };
  }
  return { createClient, ENDPOINTS };
});
