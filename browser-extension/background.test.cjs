const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const crypto = require('node:crypto');
const manifest = JSON.parse(fs.readFileSync(`${__dirname}/manifest.json`));
const event = () => ({addListener(fn){this.listener=fn}});
function environment() {
  const sent = [], commands = [];
  const chrome = {runtime:{id:'extension',onMessage:event()},action:{setBadgeText:async()=>{},setBadgeBackgroundColor:async()=>{}},
    tabs:{get:async id=>({id,url:'https://example.com/'}),onCreated:event(),remove:async()=>{}},
    debugger:{onDetach:event(),onEvent:event(),sendCommand:async(source,method,params)=>{commands.push({source,method,params});return {ok:true}}},
    downloads:{onDeterminingFilename:event(),onChanged:event()}};
  const context = vm.createContext({chrome,URL,Map,Set,Promise,setTimeout,clearTimeout});
  vm.runInContext(fs.readFileSync(`${__dirname}/background.js`,'utf8'),context);
  context.sent=sent;
  vm.runInContext(`port={postMessage:message=>sent.push(message)};attached.set(7,{origin:'https://example.com',publicWeb:false,created:false})`,context);
  return {context,chrome,sent,commands,run:code=>vm.runInContext(code,context)};
}
test('manifest public key matches the native host extension ID',()=>{
  const hex=crypto.createHash('sha256').update(Buffer.from(manifest.key,'base64')).digest('hex').slice(0,32);
  const id=[...hex].map(x=>String.fromCharCode(97+parseInt(x,16))).join('');
  assert.equal(id,'pmobmihkpbblajdanleplliihfmcdmpp');
  assert.equal(manifest.manifest_version,3);
});
test('URL and exact-tab scope reject forbidden navigation and raw methods',async()=>{
  const e=environment();
  for(const url of ['file:///secret','chrome://settings','javascript:alert(1)','https://other.com','http://127.0.0.1','https://user:pass@example.com'])
    assert.equal(e.run(`permitted(${JSON.stringify(url)},attached.get(7))`),false,url);
  await assert.rejects(e.run(`execute({tab_id:99,method:'Page.navigate',params:{url:'https://example.com/'}})`));
  await assert.rejects(e.run(`execute({tab_id:7,method:'Storage.getCookies'})`));
  await assert.rejects(e.run(`execute({tab_id:7,method:'Page.navigate',params:{url:'https://other.com/'}})`));
  assert.equal(e.commands.length,0);
  await e.run(`execute({tab_id:7,method:'Page.getFrameTree'})`);
  assert.equal(e.commands.length,1);
});
test('local pause wins and personal tabs cannot be closed',async()=>{
  const e=environment();
  e.run('paused=true;localPaused=true');
  await assert.rejects(e.run(`execute({tab_id:7,method:'Page.navigate',params:{url:'https://example.com/'}})`));
  e.run('paused=false');
  await assert.rejects(e.run(`execute({tab_id:7,method:'WebCodex.closeTab'})`));
  assert.equal(e.commands.length,0);
});
test('events omit console secrets and headers and redact URL query',async()=>{
  const e=environment();
  await e.chrome.debugger.onEvent.listener({tabId:7},'Runtime.consoleAPICalled',{type:'error',args:[{value:'PASSWORD_SECRET'}]});
  await e.chrome.debugger.onEvent.listener({tabId:7},'Network.responseReceived',{response:{status:500,url:'https://example.com/?token=SECRET',headers:{Authorization:'SECRET'}}});
  assert(!JSON.stringify(e.sent).includes('SECRET'));
  assert.equal(e.sent.at(-1).result.response.url,'https://example.com/');
});
test('cross-process iframe commands stay inside the attached tab',async()=>{
  const e=environment();
  await e.chrome.debugger.onEvent.listener({tabId:7},'Target.attachedToTarget',{sessionId:'child-session',targetInfo:{type:'iframe',targetId:'child-frame'}});
  await e.run(`execute({tab_id:7,frame_id:'child-frame',method:'Runtime.evaluate',params:{expression:'document.title',contextId:3}})`);
  assert.equal(e.commands.at(-1).source.tabId,7);
  assert.equal(e.commands.at(-1).source.sessionId,'child-session');
});
