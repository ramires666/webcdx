const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const WebCodexProtocol=require('./protocol.js');
const WebCodexMCP=require('./mcp-client.js');

function harness(stored={}) {
  const listeners={};const data={...stored};const sent=[];
  const event=()=>({addListener:fn=>{listeners[fn.name||Math.random()]=fn;}});
  const chrome={runtime:{id:'test-id',getURL:p=>'chrome-extension://test-id/'+p,onMessage:{addListener(fn){listeners.message=fn;}}},
    storage:{session:{setAccessLevel:async x=>sent.push(['session-access',x]),get:async k=>({[k]:data[k]}),set:async x=>Object.assign(data,x)},local:{setAccessLevel:async x=>sent.push(['local-access',x])}},
    action:{onClicked:{addListener(fn){listeners.click=fn;}}},sidePanel:{open:async()=>{}},alarms:{create(){},onAlarm:{addListener(){}}},
    permissions:{contains:async()=>true},tabs:{get:async()=>({url:'https://aistudio.google.com/app/prompts/abc'}),sendMessage:async()=>({supported:true,conversationKey:'c',ok:true})},
    identity:{getRedirectURL:()=> 'https://test.chromiumapp.org/webcodex',launchWebAuthFlow:async()=>''}};
  const context={chrome,crypto:require('node:crypto').webcrypto,URL,URLSearchParams,TextEncoder,TextDecoder,Uint8Array,ArrayBuffer,JSON,Promise,Set,Map,Date,Math,Error,String,Object,Number,RegExp,console,btoa:s=>Buffer.from(s,'binary').toString('base64'),fetch:async()=>{throw Error('fetch unexpected')},
    WebCodexProtocol,WebCodexMCP,importScripts(){},setTimeout,clearTimeout};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'background.js'),'utf8'),context,{filename:'background.js'});
  return {listeners,data,sent,chrome};
}
function message(h,msg,sender){return new Promise(resolve=>{const async=h.listeners.message(msg,sender,resolve);if(async!==true)resolve(undefined);});}
test('rejects panel privileges from page and frame senders',async()=>{
  const h=harness();await new Promise(r=>setTimeout(r,0));
  const page=await message(h,{type:'bridge.status'},{url:'https://aistudio.google.com/'});assert.equal(page.ok,false);assert.equal(page.error,'Panel sender required');
  const frame=await message(h,{type:'studio.call'},{url:'https://aistudio.google.com/',frameId:3,tab:{id:1}});assert.equal(frame.ok,false);assert.equal(frame.error,'Untrusted Studio sender');
});
test('session restore keeps access token private from panel state',async()=>{
  const h=harness({webcodexState:{connected:false,gateUrl:'',clientId:'',agents:[],tools:[],accessToken:'secret',session:{phase:'idle',agentId:'',toolNames:[],automatic:false,paused:false,turn:'',instructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:null}}});
  await new Promise(r=>setTimeout(r,0));
  const reply=await message(h,{type:'bridge.status'},{url:'chrome-extension://test-id/panel.html'});
  assert.equal(reply.ok,true);assert.equal(JSON.stringify(reply).includes('secret'),false);
  assert.equal(h.sent.some(x=>x[0]==='session-access'&&x[1].accessLevel==='TRUSTED_CONTEXTS'),true);
  assert.equal(h.sent.some(x=>x[0]==='local-access'&&x[1].accessLevel==='TRUSTED_CONTEXTS'),true);
});
