(() => {
  'use strict';
  if (window.top !== window || location.origin !== 'https://aistudio.google.com') return;
  if (window.__webcodexBridgeLoaded) return;
  window.__webcodexBridgeLoaded = true;
  const adapter=WebCodexStudio.createAdapter(document,location);
  const pageInstance=Date.now()+'-'+Math.random();
  let sessionId='', baselineText='', conversationKey='', lastText='', lastDelivered='', inFlight='', observerTimer=0, stopping=false, newChatPending=false, newChatRequestInFlight=false, attempted=new Set(), recoverTurn='', recoverTools=[], turnless=false, turnlessRecovery=false, observedUserText='', observedResultId='', observedContext='';
  let awaitingResponse=false, generationSeen=false, generationCounter=0, baselineModelText='';
  let deliveryPending = false, retired = false, allowedFollowupId = '';
  const serviceNodes=new Set();
  const send=async(type,data={})=>{
    try{return await chrome.runtime.sendMessage({type,sessionId,...data});}
    catch(e){if(/Extension context invalidated/i.test(String(e.message||e))){retired=true;sessionId='';clearTimeout(observerTimer);clearInterval(tickTimer);observer.disconnect();return {accepted:false};}throw e;}
  };
  if (!window.__webcodexBridgeReattach) send('studio.hello',{pageInstance}).catch(()=>{});
  delete window.__webcodexBridgeReattach;
  function parsedResult(text){try{const result=JSON.parse(text||'');return result?.type==='webcodex-result'&&typeof result.operation_id==='string'&&result.operation_id?result:null;}catch{return null;}}
  function visibleResultId(user){const parsed=parsedResult(user?.text)?.operation_id,id=typeof user?.operationId==='string'&&user.operationId?user.operationId:'';return id||parsed||'';}
  function snapshot(operationId,conversationReplaced=false,expectedResultText){const s=adapter.inspect(),resultId=visibleResultId(s.latestUser);return {supported:s.supported,generating:s.generating,editorEmpty:s.editorEmpty,conversationKey:s.conversationKey,error:s.error,...(conversationReplaced?{conversationReplaced:true}:{}),deliveredOperationId:resultId,delivered:operationId?resultId===operationId||adapter.hasDelivery(operationId,expectedResultText):undefined};}
  function trackGeneration(s){if(turnless&&s.generating&&!s.controlsPending){if(!awaitingResponse){awaitingResponse=true;generationCounter++;attempted.clear();lastText='';baselineText='';baselineModelText=s.latestModel?.text||'';}generationSeen=true;}}
  async function observe(){const s=adapter.inspect(),conversationReplaced=newChatPending&&s.supported&&(!s.generating||s.controlsPending)&&s.editorEmpty&&!s.latestModel;if(conversationReplaced){if(newChatRequestInFlight)return;newChatRequestInFlight=true;try{const transition={...snapshot(undefined,true),error:'Conversation was replaced'};if(sessionId)await send('studio.snapshot',transition);else await send('studio.hello',{pageInstance,...transition});}finally{newChatRequestInFlight=false;}return;}if(!sessionId||newChatPending)return;if(!s.supported){await send('studio.snapshot',snapshot());return;}
    if(turnless){const userText=s.latestUser?.text||'',resultId=visibleResultId(s.latestUser);if(resultId)observedResultId=resultId;observedUserText=userText;const context=JSON.stringify([observedResultId,observedUserText]);if(context!==observedContext){observedContext=context;attempted.clear();lastText='';baselineText='';}trackGeneration(s);if(resultId&&resultId===allowedFollowupId&&s.modelAfterUser===true&&!awaitingResponse){generationCounter++;awaitingResponse=true;generationSeen=true;baselineModelText='';}if(s.modelAfterUser===false)return;}
    if(s.generating)return;
    if(stopping){stopping=false;lastText=s.latestModel?.text||'';if(turnless){awaitingResponse=false;allowedFollowupId='';}return;}
    const model=s.latestModel;if(!model||model.text===baselineText||!model.text||model.text===lastText)return;
    if(turnless&&(!awaitingResponse||!generationSeen&&model.text===baselineModelText))return;
    const recoveringTurn=!!recoverTurn;if(recoveringTurn){try{if(!WebCodexProtocol.parseCall(model.text,recoverTurn,recoverTools))return;}catch{return;}}
    const marker=sessionId+'\0'+(turnless?observedResultId+'\0'+observedUserText+'\0':'')+model.text;if(attempted.has(model.text)||inFlight===marker)return;const callSession=sessionId,callGeneration=generationCounter,callContext=observedContext;inFlight=marker;try{const response=await send('studio.call',{text:model.text,conversationKey,generating:s.generating,...(turnless?{responseKey:JSON.stringify([pageInstance,generationCounter,observedResultId,observedUserText,model.text]),precedingResultId:observedResultId}:{})});if(sessionId===callSession&&response?.accepted&&(!turnless||callGeneration===generationCounter&&callContext===observedContext)){lastText=model.text;if(turnless){awaitingResponse=false;if(observedResultId===allowedFollowupId)allowedFollowupId='';}if(recoveringTurn)recoverTurn='';attempted.add(model.text);if(/^```webcodex-call\s/.test(model.text)&&model.element)serviceNodes.add(model.element);}}finally{if(inFlight===marker)inFlight='';}
  }
  const schedule=()=>{if(turnless)trackGeneration(adapter.inspect());clearTimeout(observerTimer);observerTimer=setTimeout(()=>observe().catch(()=>{}),250);};
  document.addEventListener('click',event=>{const button=event.target?.closest?.('button,a,[role="button"],[role="link"]');const labels=[button?.getAttribute('aria-label'),button?.getAttribute('title'),button?.innerText,button?.textContent].map(value=>String(value||'').trim().toLowerCase());const label=labels.join(' ');if(labels.some(value=>/^(new chat|новый чат)$/.test(value)))newChatPending=true;if(turnless&&labels.some(value=>/^(run|send|запустить|отправить)$/.test(value.replace(/\s*keyboard_return\s*$/i,'').trim()))){const s=adapter.inspect();awaitingResponse=true;generationSeen=false;generationCounter++;attempted.clear();lastText='';baselineText='';baselineModelText=s.latestModel?.text||'';}if(adapter.inspect().generating&&(/\b(stop|cancel)\b|остановить|отмена/.test(label)||button?.querySelector?.('.stoppable-spinner, ms-stoppable-spinner')))stopping=true;},true);
  document.addEventListener('keydown',event=>{if(event.key==='Escape'&&adapter.inspect().generating)stopping=true;},true);
  const observer=new MutationObserver(schedule);observer.observe(document.documentElement,{subtree:true,childList:true,characterData:true,attributes:true});
  const tickTimer=setInterval(()=>{if(retired)return;if(newChatPending)observe().catch(()=>{});else if(sessionId){send('studio.tick',snapshot()).catch(()=>{});observe().catch(()=>{});}else{send('studio.hello',{pageInstance}).catch(()=>{});}},2000);
  chrome.runtime.onMessage.addListener((m,_sender,reply)=>{
    if(!m||!['studio.arm','studio.stage','studio.deliver','studio.snapshot','studio.disarm','studio.cleanup'].includes(m.type)){reply({ok:false});return;}
    if(m.type==='studio.arm'){
      const s=adapter.inspect();if(!s.supported){reply({supported:false,error:s.error||'AI Studio conversation changed',conversationKey:s.conversationKey});return;}
      if(!m.hideServiceMessages)adapter.restoreCleanup();serviceNodes.clear();
      sessionId=m.sessionId;conversationKey=s.conversationKey;turnless=!!m.turnless;turnlessRecovery=turnless&&!!m.recover;awaitingResponse=false;generationSeen=false;generationCounter=0;allowedFollowupId='';baselineModelText=s.latestModel?.text||'';observedUserText=s.latestUser?.text||'';observedResultId='';observedContext=JSON.stringify([observedResultId,observedUserText]);recoverTurn=m.recover?m.recoverTurn||'':'';recoverTools=m.recover?m.toolNames||[]:[];baselineText=m.recover?'':s.latestModel?.text||'';lastText=m.recover?'':baselineText;lastDelivered='';inFlight='';stopping=false;newChatPending=false;newChatRequestInFlight=false;attempted.clear();reply(snapshot());if(recoverTurn)schedule();return;
    }
    if(!sessionId||m.sessionId!==sessionId){reply({ok:false,error:'Stale session'});return;}
    if(m.type==='studio.cleanup'){if(m.final)adapter.annotateContext();if(!m.enabled)adapter.restoreCleanup();else if(m.final)adapter.cleanup({turns:[...serviceNodes],operationIds:m.operationIds||[]});reply({ok:true});return;}
    if(m.type==='studio.disarm'){sessionId='';inFlight='';allowedFollowupId='';reply({ok:true});return;}
    if(m.type==='studio.stage'){
      (async()=>{
        let s=adapter.inspect();
        if(!s.supported||(m.text&&!s.editorEmpty)){const error=!s.supported?(s.error||'AI Studio editor is not supported'):'AI Studio prompt field is not empty; existing text was preserved';reply({ok:false,...snapshot(),error});return;}
        if(!await adapter.stageSystemInstructions(m.systemText)){reply({ok:false,...snapshot(),error:adapter.getSystemStageError?.()||'Не удалось заполнить System instructions: редактор не найден или неоднозначен'});return;}
        s=adapter.inspect();
        if(sessionId!==m.sessionId||!s.supported||(m.text&&!s.editorEmpty)){const error=sessionId!==m.sessionId?'AI Studio session changed':!s.supported?(s.error||'AI Studio editor is not supported'):'AI Studio prompt field is not empty; existing text was preserved';reply({ok:false,...snapshot(),error});return;}
        if(m.text===''){reply({ok:true,...snapshot()});return;}
        const ok=adapter.stage(m.text);
        reply({ok,...snapshot(),error:ok?undefined:'Could not stage session instructions in the empty prompt field'});
      })().catch(e=>reply({ok:false,...snapshot(),error:String(e.message||e)}));
      return true;
    }
    if(m.type==='studio.snapshot'){const state=snapshot(m.operationId,false,m.expectedResultText);if(state.delivered&&turnless){observedResultId=m.operationId||observedResultId;}if(state.delivered&&(recoverTurn||turnlessRecovery)){const current=adapter.inspect(),model=current.latestModel;let valid=false;try{valid=!!model&&(!turnless||current.modelAfterUser!==false)&&!!WebCodexProtocol.parseCall(model.text,turnless?null:recoverTurn,recoverTools);}catch{}if(turnless){recoverTurn='';turnlessRecovery=false;baselineText=valid?'':model?.text||'';lastText=valid?'':baselineText;if(valid){awaitingResponse=true;generationSeen=true;baselineModelText='';schedule();}}else if(valid)schedule();else{recoverTurn='';baselineText=model?.text||'';lastText=baselineText;}}reply(state);return;}
    if(m.type==='studio.deliver'){
      const s=adapter.inspect();const state=snapshot();if(!s.supported||(!m.submit&&!s.editorEmpty)||s.generating){reply({ok:false,attempted:false,...state});return;}
      const wrapped=`\`\`\`webcodex-result\n${m.text}\n\`\`\``;
      if(!m.submit){const ok=adapter.stage(wrapped);reply({ok,attempted:false,...state});return;}
      if(lastDelivered===m.text){reply({ok:false,attempted:true,...state});return;}
      if(deliveryPending){reply({ok:false,attempted:true,...state});return;}
      let operationId;try{const result=JSON.parse(m.text);if(result.type!=='webcodex-result')throw Error();operationId=result.operation_id;}catch{reply({ok:false,attempted:false,...state});return;}
      const deliverySession=sessionId;
      deliveryPending=true;
      (async()=>{
        try{
          const ok=await adapter.submit(wrapped,()=>sessionId===deliverySession);
          if(ok){lastDelivered=m.text;if(turnless)allowedFollowupId=operationId;}
          const proof=snapshot(operationId,false,m.text);reply({ok,attempted:ok,confirmed:ok&&proof.delivered,...proof});
        }catch(e){reply({ok:false,attempted:true,...snapshot(),error:'Delivery outcome is unknown'});}
        finally{deliveryPending=false;}
      })();
      return true;
    }
  });
})();
