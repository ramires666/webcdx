(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.WebCodexMCP = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';
  const VERSION = '2025-06-18', LIMIT = 2 * 1024 * 1024;
  function requestSignal(signal) {
    const deadline = AbortSignal.timeout(15000);
    return signal ? AbortSignal.any([signal, deadline]) : deadline;
  }
  function httpError(service, status) {
    const error = new Error(service + ' HTTP error ' + status);
    error.status = status;
    return error;
  }
  function createClient({ baseUrl, token, fetchImpl = fetch }) {
    let url;
    try { url = new URL(baseUrl); } catch (_) { throw new Error('Invalid MCP base URL'); }
    if (url.username || url.password || url.search || url.hash || url.pathname !== '/' || !(url.protocol === 'https:' || (url.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)))) throw new Error('MCP base URL must be an HTTPS origin (HTTP is allowed for localhost)');
    if (typeof token !== 'string' || !token) throw new Error('MCP token is required');
    const origin = url.origin;
    let nextId = 0;
    async function readBounded(response) {
      if (!response.body || !response.body.getReader) {
        const bytes = new TextEncoder().encode(await response.text());
        if (bytes.length > LIMIT) throw new Error('MCP response exceeds 2 MiB');
        return new TextDecoder().decode(bytes);
      }
      const reader = response.body.getReader(), chunks = []; let size = 0;
      try {
        while (true) {
          const { done, value } = await reader.read(); if (done) break;
          size += value.byteLength; if (size > LIMIT) { await reader.cancel(); throw new Error('MCP response exceeds 2 MiB'); }
          chunks.push(value);
        }
      } finally { reader.releaseLock?.(); }
      const bytes = new Uint8Array(size); let offset = 0;
      for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
      return new TextDecoder().decode(bytes);
    }
    function parse(text, id) {
      let payload;
      try { payload = JSON.parse(text); } catch (_) { throw new Error('Invalid MCP JSON response'); }
      if (!payload || payload.jsonrpc !== '2.0' || payload.id !== id) throw new Error('MCP response ID mismatch');
      if (payload.error) throw new Error('MCP RPC error ' + String(payload.error.code ?? '') + ': ' + String(payload.error.message || 'request failed'));
      return payload.result;
    }
    async function readRpc(response, id) {
      if (!/text\/event-stream/i.test(response.headers?.get?.('content-type') || '')) return parse(await readBounded(response), id);
      if (!response.body?.getReader) return parse(await readBounded(response), id);
      const reader = response.body.getReader(), decoder = new TextDecoder(); let buffer = '', size = 0;
      function eventResult(event) {
        const data = event.split(/\r?\n/).filter(line => line.startsWith('data:')).map(line => line.slice(5).replace(/^ /, '')).join('\n');
        if (!data) return null;
        let payload; try { payload = JSON.parse(data); } catch (_) { return null; }
        if (!payload || payload.id !== id) return null;
        if (payload.jsonrpc !== '2.0') throw new Error('MCP response ID mismatch');
        if (payload.error) throw new Error('MCP RPC error ' + String(payload.error.code ?? '') + ': ' + String(payload.error.message || 'request failed'));
        if (!Object.prototype.hasOwnProperty.call(payload, 'result')) throw new Error('Invalid MCP JSON-RPC response');
        return { found: true, result: payload.result };
      }
      try {
        while (true) {
          const { done, value } = await reader.read();
          if (value) {
            size += value.byteLength; if (size > LIMIT) throw new Error('MCP response exceeds 2 MiB');
            buffer += decoder.decode(value, { stream: !done });
            let boundary;
            while ((boundary = /\r?\n\r?\n/.exec(buffer))) {
              const result = eventResult(buffer.slice(0, boundary.index));
              buffer = buffer.slice(boundary.index + boundary[0].length);
              if (result?.found) { await reader.cancel(); return result.result; }
            }
          }
          if (done) break;
        }
        const result = eventResult(buffer);
        if (result?.found) return result.result;
      } finally { reader.releaseLock?.(); }
      throw new Error('MCP response ID mismatch');
    }
    async function request(method, params, signal) {
      const id = ++nextId;
      const response = await fetchImpl(origin + '/mcp/v5', { method: 'POST', signal: requestSignal(signal), headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': VERSION }, body: JSON.stringify({ jsonrpc: '2.0', id, method, params }) });
      if (!response.ok) throw httpError('MCP', response.status);
      if (response.status === 202 && !response.body) return undefined;
      return readRpc(response, id);
    }
    async function rpc(method, params = {}, signal) { return request(method, params, signal); }
    async function initialize(signal) {
      const result = await request('initialize', { protocolVersion: VERSION, capabilities: {}, clientInfo: { name: 'WebCodex AI Studio Bridge', version: '0.3.5' } }, signal);
      const response = await fetchImpl(origin + '/mcp/v5', { method: 'POST', signal: requestSignal(signal), headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': VERSION }, body: JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized', params: {} }) });
      if (!response.ok || response.status !== 202) throw httpError('MCP initialized notification', response.status);
      return result;
    }
    async function listTools(signal) {
      const all = []; let cursor;
      for (let page = 0; page < 20; page++) {
        const result = await rpc('tools/list', cursor ? { cursor } : {}, signal);
        if (!result || !Array.isArray(result.tools)) throw new Error('Invalid tools/list result');
        all.push(...result.tools); if (all.length > 1000) throw new Error('MCP tool limit exceeded');
        if (!result.nextCursor) return all;
        cursor = result.nextCursor;
      }
      throw new Error('MCP tools pagination limit exceeded');
    }
    async function listAgents(signal) {
      const result = await rpc('tools/call', { name: 'list_agents', arguments: {} }, signal);
      if (result?.isError === true) throw new Error('list_agents tool returned an error');
      if (result?.structuredContent && Array.isArray(result.structuredContent.agents)) return result.structuredContent.agents;
      for (const item of result?.content || []) if (item.type === 'text') {
        try { const data = JSON.parse(item.text); if (Array.isArray(data.agents)) return data.agents; } catch (_) {}
      }
      throw new Error('Invalid list_agents result');
    }
    async function createOperation({ key, name, arguments: args, agentId }, signal) {
      return bridge('/bridge/v1/operations', 'POST', { key, name, arguments: args, agent_id: agentId }, signal);
    }
    async function getOperation(id, signal) { return bridge('/bridge/v1/operations/' + encodeURIComponent(id), 'GET', undefined, signal); }
    async function bridge(path, method, body, signal) {
      const response = await fetchImpl(origin + path, { method, signal: requestSignal(signal), headers: { Authorization: 'Bearer ' + token, Accept: 'application/json', ...(body ? { 'Content-Type': 'application/json' } : {}) }, ...(body ? { body: JSON.stringify(body) } : {}) });
      if (!response.ok) throw httpError('Bridge', response.status);
      const text = await readBounded(response); try { return JSON.parse(text); } catch (_) { throw new Error('Invalid bridge JSON response'); }
    }
    return { rpc, initialize, listTools, listAgents, createOperation, getOperation };
  }
  return { createClient };
});
