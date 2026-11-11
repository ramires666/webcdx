(() => {
  'use strict';
  if (window.top !== window || location.origin !== 'https://aistudio.google.com') return;
  if (window.__webcodexBridgeLoaded) return;
  window.__webcodexBridgeLoaded = true;
  const adapter=WebCodexStudio.createAdapter(document,location);
  const pageInstance=Date.now()+'-'+Math.random();
  let sessionId='', baselineText='', conversationKey='', lastText='', lastDelivered='', inFlight='', observerTimer=0, stopping=false, sawModel=false, attempted=new Set();
  let deliveryPending = false, retired = false;
  const send=async(type,data={})=>{
    try{return await chrome.runtime.sendMessage({type,sessionId,...data});}
    catch(e){if(/Extension context invalidated/i.test(String(e.message||e))){retired=true;sessionId='';clearTimeout(observerTimer);clearInterval(tickTimer);observer.disconnect();return {accepted:false};}throw e;}
  };
  if (!window.__webcodexBridgeReattach) send('studio.hello',{pageInstance}).catch(()=>{});
  delete window.__webcodexBridgeReattach;
  function snapshot(operationId){const s=adapter.inspect();return {supported:s.supported,generating:s.generating,editorEmpty:s.editorEmpty,conversationKey:s.conversationKey,error:s.error,delivered:operationId?adapter.hasDelivery(operationId):undefined};}
  async function observe(){if(!sessionId)return;const s=adapter.inspect();if(s.conversationKey!==conversationKey||!s.supported){await send('studio.snapshot',snapshot());return;}
    if(sawModel&&!s.latestModel){await send('studio.snapshot',{...snapshot(),supported:false,error:'Conversation was replaced'});return;}
    if(s.latestModel)sawModel=true;
    if(s.generating)return;
    if(stopping){stopping=false;lastText=s.latestModel?.text||'';return;}
    const model=s.latestModel;if(!model||model.text===baselineText||!model.text||model.text===lastText)return;
    const marker=sessionId+'\0'+model.text;if(attempted.has(model.text)||inFlight===marker)return;const callSession=sessionId;inFlight=marker;try{const response=await send('studio.call',{text:model.text,conversationKey,generating:s.generating});if(sessionId===callSession&&response?.accepted){lastText=model.text;attempted.add(model.text);}}finally{if(inFlight===marker)inFlight='';}
  }
  const schedule=()=>{clearTimeout(observerTimer);observerTimer=setTimeout(()=>observe().catch(()=>{}),250);};
  document.addEventListener('click',event=>{const button=event.target?.closest?.('button');const label=((button?.getAttribute('aria-label')||'')+' '+(button?.getAttribute('title')||'')+' '+(button?.innerText||'')).trim().toLowerCase();if(adapter.inspect().generating&&(/\b(stop|cancel)\b|остановить|отмена/.test(label)||button?.querySelector?.('.stoppable-spinner, ms-stoppable-spinner')))stopping=true;},true);
  document.addEventListener('keydown',event=>{if(event.key==='Escape'&&adapter.inspect().generating)stopping=true;},true);
  const observer=new MutationObserver(schedule);observer.observe(document.documentElement,{subtree:true,childList:true,characterData:true,attributes:true});
  const tickTimer=setInterval(()=>{if(retired)return;if(sessionId){send('studio.tick',snapshot()).catch(()=>{});observe().catch(()=>{});}else{send('studio.hello',{pageInstance}).catch(()=>{});}},2000);
  chrome.runtime.onMessage.addListener((m,_sender,reply)=>{
    if(!m||!['studio.arm','studio.stage','studio.deliver','studio.snapshot','studio.disarm'].includes(m.type)){reply({ok:false});return;}
    if(m.type==='studio.arm'){
      const s=adapter.inspect();if(!s.supported){reply({supported:false,error:s.error});return;}
      sessionId=m.sessionId;conversationKey=s.conversationKey;baselineText=s.latestModel?.text||'';lastText=baselineText;lastDelivered='';inFlight='';stopping=false;sawModel=!!s.latestModel;attempted.clear();reply(snapshot());return;
    }
    if(!sessionId||m.sessionId!==sessionId){reply({ok:false,error:'Stale session'});return;}
    if(m.type==='studio.disarm'){sessionId='';inFlight='';reply({ok:true});return;}
    if(m.type==='studio.stage'){const s=adapter.inspect();const ok=s.supported&&s.conversationKey===conversationKey&&adapter.stage(m.text);reply({ok,...snapshot()});return;}
    if(m.type==='studio.snapshot'){reply(snapshot(m.operationId));return;}
    if(m.type==='studio.deliver'){
      const s=adapter.inspect(),state=snapshot();if(!s.supported||(!m.submit&&!s.editorEmpty)||s.generating||s.conversationKey!==conversationKey){reply({ok:false,attempted:false,...state});return;}
      if(!m.submit){const ok=adapter.stage(m.text);reply({ok,attempted:false,...state});return;}
      if(lastDelivered===m.text){reply({ok:false,attempted:true,...state});return;}
      if(deliveryPending){reply({ok:false,attempted:true,...state});return;}
      let operationId;try{const result=JSON.parse(m.text);if(result.type!=='webcodex-result')throw Error();operationId=result.operation_id;}catch{reply({ok:false,attempted:false,...state});return;}
      const deliverySession=sessionId,deliveryConversation=conversationKey;
      deliveryPending=true;
      (async()=>{
        try{
          const ok=await adapter.submit(m.text,()=>sessionId===deliverySession&&conversationKey===deliveryConversation);
          if(ok)lastDelivered=m.text;
          reply({ok,attempted:ok,confirmed:ok&&adapter.hasDelivery(operationId),...snapshot()});
        }catch(e){reply({ok:false,attempted:true,error:'Delivery outcome is unknown',...snapshot()});}
        finally{deliveryPending=false;}
      })();
      return true;
    }
  });
})();
