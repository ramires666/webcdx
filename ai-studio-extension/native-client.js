(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.WebCodexNative = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';
  const HOST = 'org.webcodex.studio', TIMEOUT = 15000;
  class NativeClientError extends Error {
    constructor(message, code) { super(message); this.name = 'NativeClientError'; this.code = code; }
  }
  function createClient({ runtime = chrome.runtime, version = '0.3.0' } = {}) {
    const port = runtime.connectNative(HOST), pending = new Map(), issued = new Set();
    let nextId = 0, closed = false;
    function failAll(error) {
      for (const request of pending.values()) { clearTimeout(request.timer); request.signal?.removeEventListener('abort', request.abort); request.reject(error); }
      pending.clear();
    }
    function protocolError(message) { return new NativeClientError(message, 'NATIVE_PROTOCOL_ERROR'); }
    function settle(request, error, result) {
      pending.delete(request.id); clearTimeout(request.timer); request.signal?.removeEventListener('abort', request.abort);
      if (error) request.reject(error); else request.resolve(result);
    }
    function onMessage(frame) {
      if (!frame || typeof frame.id !== 'string') { failAll(protocolError('Invalid native response')); return; }
      const request = pending.get(frame.id);
      if (!request) { if (!issued.has(frame.id)) failAll(protocolError('Native response ID mismatch')); return; }
      if (Object.prototype.hasOwnProperty.call(frame, 'chunk')) {
        if (typeof frame.chunk !== 'string' || !Number.isInteger(frame.index) || !Number.isInteger(frame.total) || frame.total < 1 || frame.index < 0 || frame.index >= frame.total) { settle(request, protocolError('Invalid native response chunk')); return; }
        if (!request.chunks) request.chunks = { total: frame.total, parts: new Map(), count: 0, size: 0 };
        const chunks = request.chunks;
        const size = new TextEncoder().encode(frame.chunk).byteLength;
        if (chunks.total !== frame.total || chunks.parts.has(frame.index)) { settle(request, protocolError('Inconsistent native response chunks')); return; }
        chunks.size += size;
        chunks.parts.set(frame.index,frame.chunk); chunks.count++;
        if (chunks.count !== chunks.total) return;
        let text = ''; for(let i=0;i<chunks.total;i++)text+=chunks.parts.get(i);
        try { frame = JSON.parse(text); } catch (_) { settle(request, protocolError('Invalid chunked native JSON response')); return; }
        if (!frame || frame.id !== request.id) { settle(request, protocolError('Native response ID mismatch')); return; }
      }
      if (frame.error) settle(request, new NativeClientError(String(frame.error.message || 'Native request failed'), frame.error.code));
      else if (Object.prototype.hasOwnProperty.call(frame, 'result')) settle(request, null, frame.result);
      else settle(request, protocolError('Invalid native response'));
    }
    port.onMessage.addListener(onMessage);
    port.onDisconnect.addListener(() => {
      closed = true;
      const detail = runtime.lastError?.message;
      failAll(new NativeClientError(detail || 'Native host disconnected', 'NATIVE_DISCONNECTED'));
    });
    function rpc(method, params = {}, signal) {
      if (closed) return Promise.reject(new NativeClientError('Native host disconnected', 'NATIVE_DISCONNECTED'));
      if (signal?.aborted) return Promise.reject(new NativeClientError('Native request aborted', 'NATIVE_ABORTED'));
      const id = String(++nextId), message = { id, method, params };
      let serialized;
      try { serialized = JSON.stringify(message); } catch (_) { return Promise.reject(protocolError('Native request is not serializable')); }
      return new Promise((resolve, reject) => {
        const request = { id, resolve, reject, signal };
        request.abort = () => { if (pending.has(id)) settle(request, new NativeClientError('Native request aborted', 'NATIVE_ABORTED')); };
        request.timer = setTimeout(() => { if (pending.has(id)) settle(request, new NativeClientError('Native request timed out', 'NATIVE_TIMEOUT')); }, TIMEOUT);
        pending.set(id, request); issued.add(id); signal?.addEventListener('abort', request.abort, { once: true });
        try { port.postMessage(message); } catch (error) { pending.delete(id); clearTimeout(request.timer); signal?.removeEventListener('abort', request.abort); reject(new NativeClientError(error.message || 'Native host unavailable', 'NATIVE_UNAVAILABLE')); }
      });
    }
    async function initialize(signal) {
      return rpc('initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'WebCodex AI Studio Bridge', version } }, signal);
    }
    async function listTools(signal) {
      const result = await rpc('tools/list', {}, signal);
      if (!result || !Array.isArray(result.tools)) throw new Error('Invalid tools/list result');
      return result.tools;
    }
    async function listAgents(signal) {
      const result = await rpc('tools/call', { name: 'list_agents', arguments: {} }, signal);
      if (result?.isError === true) throw new Error('list_agents tool returned an error');
      if (Array.isArray(result?.agents)) return result.agents;
      if (result?.structuredContent && Array.isArray(result.structuredContent.agents)) return result.structuredContent.agents;
      for (const item of result?.content || []) if (item.type === 'text') {
        try { const data = JSON.parse(item.text); if (Array.isArray(data.agents)) return data.agents; } catch (_) {}
      }
      throw new Error('Invalid list_agents result');
    }
    function createOperation({ key, name, arguments: args, agentId }, signal) {
      return rpc('operations/create', { key, name, arguments: args, agentId }, signal);
    }
    function getOperation(id, signal) { return rpc('operations/get', { id }, signal); }
    function close() {
      if (closed) return;
      closed = true;
      failAll(new NativeClientError('Native client closed', 'NATIVE_CLOSED'));
      port.disconnect();
    }
    return { rpc, initialize, listTools, listAgents, createOperation, getOperation, close };
  }
  return { createClient, NativeClientError };
});
