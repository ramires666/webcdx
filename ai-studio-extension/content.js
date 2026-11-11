(() => {
  'use strict';
  if (window.top !== window || location.origin !== 'https://aistudio.google.com') return;
  if (window.__webcodexBridgeLoaded) return;
  window.__webcodexBridgeLoaded = true;
  const adapter=WebCodexStudio.createAdapter(document,location);
  const pageInstance=Date.now()+'-'+Math.random();
  let sessionId='', baselineText='', conversationKey='', lastText='', lastDelivered='', inFlight='', observerTimer=0, stopping=false, newChatPending=false, attempted=new Set(), recoverTurn='', recoverTools=[];
  let deliveryPending = false, retired = false;
  const serviceNodes=new Set();
  const send=async(type,data={})=>{
    try{return await chrome.runtime.sendMessage({type,sessionId,...data});}
    catch(e){if(/Extension context invalidated/i.test(String(e.message||e))){retired=true;sessionId='';clearTimeout(observerTimer);clearInterval(tickTimer);observer.disconnect();return {accepted:false};}throw e;}
  };
  if (!window.__webcodexBridgeReattach) send('studio.hello',{pageInstance}).catch(()=>{});
  delete window.__webcodexBridgeReattach;
  function snapshot(operationId,conversationReplaced=false){const s=adapter.inspect();return {supported:s.supported,generating:s.generating,editorEmpty:s.editorEmpty,conversationKey:s.conversationKey,error:s.error,...(conversationReplaced?{conversationReplaced:true}:{}),delivered:operationId?adapter.hasDelivery(operationId):undefined};}
  async function observe(){if(!sessionId)return;const s=adapter.inspect(),conversationReplaced=newChatPending&&s.supported&&!s.generating&&s.editorEmpty&&!s.latestModel;if(conversationReplaced){newChatPending=false;await send('studio.snapshot',{...snapshot(undefined,true),error:'Conversation was replaced'});return;}if(newChatPending)return;if(s.conversationKey!==conversationKey||!s.supported){await send('studio.snapshot',snapshot());return;}
    if(s.generating)return;
    if(stopping){stopping=false;lastText=s.latestModel?.text||'';return;}
    const model=s.latestModel;if(!model||model.text===baselineText||!model.text||model.text===lastText)return;
    const recoveringTurn=!!recoverTurn;if(recoveringTurn){try{if(!WebCodexProtocol.parseCall(model.text,recoverTurn,recoverTools))return;}catch{return;}}
    const marker=sessionId+'\0'+model.text;if(attempted.has(model.text)||inFlight===marker)return;const callSession=sessionId;inFlight=marker;try{const response=await send('studio.call',{text:model.text,conversationKey,generating:s.generating});if(sessionId===callSession&&response?.accepted){lastText=model.text;if(recoveringTurn)recoverTurn='';attempted.add(model.text);if(/^```webcodex-call\s/.test(model.text)&&model.element)serviceNodes.add(model.element);}}finally{if(inFlight===marker)inFlight='';}
  }
  const schedule=()=>{clearTimeout(observerTimer);observerTimer=setTimeout(()=>observe().catch(()=>{}),250);};
  document.addEventListener('click',event=>{const button=event.target?.closest?.('button');const labels=[button?.getAttribute('aria-label'),button?.getAttribute('title'),button?.innerText].map(value=>String(value||'').trim().toLowerCase());const label=labels.join(' ');if(labels.some(value=>/^(new chat|новый чат)$/.test(value)))newChatPending=true;if(adapter.inspect().generating&&(/\b(stop|cancel)\b|остановить|отмена/.test(label)||button?.querySelector?.('.stoppable-spinner, ms-stoppable-spinner')))stopping=true;},true);
  document.addEventListener('keydown',event=>{if(event.key==='Escape'&&adapter.inspect().generating)stopping=true;},true);
  const observer=new MutationObserver(schedule);observer.observe(document.documentElement,{subtree:true,childList:true,characterData:true,attributes:true});
  const tickTimer=setInterval(()=>{if(retired)return;if(sessionId){if(newChatPending)observe().catch(()=>{});else{send('studio.tick',snapshot()).catch(()=>{});observe().catch(()=>{});}}else{send('studio.hello',{pageInstance}).catch(()=>{});}},2000);
  chrome.runtime.onMessage.addListener((m,_sender,reply)=>{
    if(!m||!['studio.arm','studio.stage','studio.deliver','studio.snapshot','studio.disarm','studio.cleanup'].includes(m.type)){reply({ok:false});return;}
    if(m.type==='studio.arm'){
      const s=adapter.inspect();if(!s.supported||m.recover&&s.conversationKey!==m.expectedConversationKey){reply({supported:false,error:s.error||'AI Studio conversation changed',conversationKey:s.conversationKey});return;}
      if(!m.hideServiceMessages)adapter.restoreCleanup();serviceNodes.clear();
      sessionId=m.sessionId;conversationKey=s.conversationKey;recoverTurn=m.recover?m.recoverTurn||'':'';recoverTools=m.recover?m.toolNames||[]:[];baselineText=m.recover?'':s.latestModel?.text||'';lastText=m.recover?'':baselineText;lastDelivered='';inFlight='';stopping=false;newChatPending=false;attempted.clear();reply(snapshot());if(recoverTurn)schedule();return;
    }
    if(!sessionId||m.sessionId!==sessionId){reply({ok:false,error:'Stale session'});return;}
    if(m.type==='studio.cleanup'){if(m.final)adapter.annotateContext();if(!m.enabled)adapter.restoreCleanup();else if(m.final)adapter.cleanup({turns:[...serviceNodes],operationIds:m.operationIds||[]});reply({ok:true});return;}
    if(m.type==='studio.disarm'){sessionId='';inFlight='';reply({ok:true});return;}
    if(m.type==='studio.stage'){
      (async()=>{
        let s=adapter.inspect();
        if(!s.supported||s.conversationKey!==conversationKey||!s.editorEmpty){const error=!s.supported?(s.error||'AI Studio editor is not supported'):s.conversationKey!==conversationKey?'AI Studio conversation changed':'AI Studio prompt field is not empty; existing text was preserved';reply({ok:false,...snapshot(),error});return;}
        if(!await adapter.stageSystemInstructions(m.systemText)){reply({ok:false,...snapshot(),error:adapter.getSystemStageError?.()||'Не удалось заполнить System instructions: редактор не найден или неоднозначен'});return;}
        s=adapter.inspect();
        if(sessionId!==m.sessionId||!s.supported||s.conversationKey!==conversationKey||!s.editorEmpty){const error=sessionId!==m.sessionId?'AI Studio session changed':!s.supported?(s.error||'AI Studio editor is not supported'):s.conversationKey!==conversationKey?'AI Studio conversation changed':'AI Studio prompt field is not empty; existing text was preserved';reply({ok:false,...snapshot(),error});return;}
        const ok=adapter.stage(m.text);
        reply({ok,...snapshot(),error:ok?undefined:'Could not stage session instructions in the empty prompt field'});
      })().catch(e=>reply({ok:false,...snapshot(),error:String(e.message||e)}));
      return true;
    }
    if(m.type==='studio.snapshot'){const state=snapshot(m.operationId);if(state.delivered&&recoverTurn){const model=adapter.inspect().latestModel;let valid=false;try{valid=!!model&&!!WebCodexProtocol.parseCall(model.text,recoverTurn,recoverTools);}catch{}if(valid)schedule();else{recoverTurn='';baselineText=model?.text||'';lastText=baselineText;}}reply(state);return;}
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
        }catch(e){reply({ok:false,attempted:true,...snapshot(),error:'Delivery outcome is unknown'});}
        finally{deliveryPending=false;}
      })();
      return true;
    }
  });
})();
