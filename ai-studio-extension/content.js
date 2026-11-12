(() => {
  'use strict';
  if (window.top !== window || location.origin !== 'https://aistudio.google.com') return;
  const reattaching = !!window.__webcodexBridgeReattach;
  if (window.__webcodexBridgeLoaded && !reattaching) return;
  window.__webcodexBridgeDispose?.();
  window.__webcodexBridgeLoaded = true;
  const adapter=WebCodexStudio.createAdapter(document,location);
  const contentVersion='0.3.10';
  const pageInstance=Date.now()+'-'+Math.random();
  let sessionId='', baselineText='', conversationKey='', lastText='', lastDelivered='', inFlight='', observerTimer=0, stopping=false, newChatPending=false, newChatRequestInFlight=false, attempted=new Set(), recoverTurn='', recoverTools=[], turnless=false, turnlessRecovery=false, observedUserText='', observedResultId='', observedContext='';
  let awaitingResponse=false, generationSeen=false, generationCounter=0, baselineModelText='';
  let deliveryPending = false, retired = false, allowedFollowupId = '';
  let hadConversation = false, emptyObservations = 0;
  let checkInstructions=false, instructionCheckPending=false, systemStagePending=false;
  let historyCandidate='', historyObservations=0, navigationRevision=0;
  const replyQuietMs=1000, invalidReplyQuietMs=8000;
  let pendingReply=null, replyTimer=0;
  const serviceNodes=new Set();
  const send=async(type,data={})=>{
    try{return await chrome.runtime.sendMessage({type,sessionId,contentVersion,...data});}
    catch(e){if(/Extension context invalidated/i.test(String(e.message||e))){retire();return {accepted:false};}throw e;}
  };
  delete window.__webcodexBridgeReattach;
  function parsedResult(text){try{const result=JSON.parse(text||'');return WebCodexProtocol.isResult(result)&&typeof result.operation_id==='string'&&result.operation_id?result:null;}catch{return null;}}
  async function responseKeyFor(generation,resultId,userText,modelText){
    const digest=async text=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(text))),byte=>byte.toString(16).padStart(2,'0')).join('');
    const [userHash,modelHash]=await Promise.all([digest(userText),digest(modelText)]);
    return JSON.stringify([pageInstance,generation,resultId,userHash,modelHash]);
  }
  function visibleResultId(user){const parsed=parsedResult(user?.text)?.operation_id,id=typeof user?.operationId==='string'&&user.operationId?user.operationId:'';return id||parsed||'';}
  function snapshot(operationId,conversationReplaced=false,expectedResultText){const s=adapter.inspect(),resultId=visibleResultId(s.latestUser);return {supported:s.supported,generating:s.generating,editorEmpty:s.editorEmpty,conversationKey:s.conversationKey,error:s.error,...(conversationReplaced?{conversationReplaced:true}:{}),deliveredOperationId:resultId,delivered:operationId?resultId===operationId||adapter.hasDelivery(operationId,expectedResultText):undefined};}
  function hello(){const s=adapter.inspect();return{pageInstance,supported:s.supported,editorEmpty:s.editorEmpty,generating:s.generating,hasHistory:!!s.latestModel||!!s.latestUser,blankChat:s.supported&&s.editorEmpty&&(!s.generating||s.controlsPending)&&!s.latestModel&&!s.latestUser};}
  function resetReply(){pendingReply=null;clearTimeout(replyTimer);replyTimer=0;}
  function replySettled(s){
    // Run/Stop can switch before the last code tokens reach the DOM. Re-read the
    // original answer at the deadline; normalized call text can hide ongoing edits.
    const model=s.latestModel,source=model.sourceText??model.text;
    const key=JSON.stringify([sessionId,generationCounter,observedContext,source]);
    if(pendingReply?.key!==key)pendingReply={key,since:Date.now()};
    const parsed=WebCodexProtocol.analyzeReply(model.text);
    const quietMs=parsed.kind==='error'?invalidReplyQuietMs:replyQuietMs;
    const remaining=quietMs-(Date.now()-pendingReply.since);
    clearTimeout(replyTimer);replyTimer=0;
    if(remaining<=0)return true;
    replyTimer=setTimeout(()=>{replyTimer=0;observe().catch(()=>{});},remaining);
    return false;
  }
  function rememberHistory(){historyObservations=0;historyCandidate='';checkInstructions=false;}
  function requestInstructionCheck(){
    if(retired)return;
    navigationRevision++;checkInstructions=true;historyObservations=0;historyCandidate='';resetReply();
    awaitingResponse=false;generationSeen=false;allowedFollowupId='';
  }
  function chatLinkKind(event){
    if(event.button>0||event.ctrlKey||event.metaKey||event.shiftKey||event.altKey)return '';
    const link=event.target?.closest?.('a,[role="link"]'),href=link?.getAttribute?.('href');
    if(!href||href.startsWith('#')||String(link.getAttribute?.('target')||'').toLowerCase()==='_blank'||link.getAttribute?.('download')!=null||link.getAttribute?.('aria-current')==='page')return '';
    try{
      const target=new URL(href,location.origin+'/');
      if(target.origin!==location.origin||!/^\/(?:app\/)?prompts(?:\/|$)/.test(target.pathname))return '';
      return /\/(?:new_chat|new)\/?$/.test(target.pathname)?'new':'saved';
    }catch{return '';}
  }
  async function ensureSavedInstructions(s){
    if(!checkInstructions)return false;
    if(s.generating||deliveryPending)return true;
    if(systemStagePending||instructionCheckPending)return true;
    if(!s.latestModel&&!s.latestUser)return false;
    const signature=JSON.stringify([s.latestUser?.text||'',s.latestModel?.sourceText??s.latestModel?.text??'']);
    if(signature!==historyCandidate){historyCandidate=signature;historyObservations=0;}
    if(++historyObservations<2)return true;
    instructionCheckPending=true;
    const requestSession=sessionId,requestGeneration=generationCounter,requestNavigation=navigationRevision;
    try{
      const response=await send('studio.ensureInstructions');
      if(response?.accepted&&sessionId===requestSession&&navigationRevision===requestNavigation){
        const current=adapter.inspect();
        const currentSignature=JSON.stringify([current.latestUser?.text||'',current.latestModel?.sourceText??current.latestModel?.text??'']);
        if(currentSignature!==signature){historyObservations=0;return true;}
        rememberHistory();
        if(generationCounter===requestGeneration&&!awaitingResponse&&!allowedFollowupId){
          baselineText=current.latestModel?.text||'';lastText=baselineText;baselineModelText=baselineText;
          observedResultId=visibleResultId(current.latestUser);observedUserText=current.latestUser?.text||'';observedContext=JSON.stringify([observedResultId,observedUserText]);
          attempted.clear();allowedFollowupId='';generationSeen=false;
        }
        schedule();
      }
    }finally{instructionCheckPending=false;}
    return true;
  }
  function trackGeneration(s){if(s.generating)resetReply();if(turnless&&s.generating&&!s.controlsPending){if(!awaitingResponse){awaitingResponse=true;generationCounter++;attempted.clear();lastText='';baselineText='';baselineModelText=s.latestModel?.text||'';}generationSeen=true;}}
  async function observe(){if(retired)return;const s=adapter.inspect();if(s.supported&&s.latestModel&&s.latestUser){hadConversation=true;emptyObservations=0;}const blank=s.supported&&(!s.generating||s.controlsPending)&&s.editorEmpty&&!s.latestModel&&!s.latestUser;if(!blank)emptyObservations=0;else if(hadConversation&&!newChatPending&&!awaitingResponse&&!deliveryPending&&!allowedFollowupId&&!inFlight&&!checkInstructions&&++emptyObservations>=2)newChatPending=true;const conversationReplaced=newChatPending&&blank;if(conversationReplaced){if(newChatRequestInFlight)return;newChatRequestInFlight=true;try{const transition={...snapshot(undefined,true),error:'Conversation was replaced'};if(sessionId)await send('studio.snapshot',transition);else await send('studio.hello',{pageInstance,...transition});}finally{newChatRequestInFlight=false;}return;}if(!sessionId||newChatPending)return;if(!s.supported){await send('studio.snapshot',snapshot());return;}
    if(systemStagePending||await ensureSavedInstructions(s))return;
    if(turnless){const userText=s.latestUser?.text||'',resultId=visibleResultId(s.latestUser);if(resultId)observedResultId=resultId;observedUserText=userText;const context=JSON.stringify([observedResultId,observedUserText]);if(context!==observedContext){observedContext=context;attempted.clear();lastText='';baselineText='';}trackGeneration(s);if(resultId&&resultId===allowedFollowupId&&s.modelAfterUser===true&&!awaitingResponse){generationCounter++;awaitingResponse=true;generationSeen=true;baselineModelText='';}if(s.modelAfterUser===false)return;}
    if(s.generating){resetReply();return;}
    if(stopping){resetReply();stopping=false;lastText=s.latestModel?.text||'';if(turnless){awaitingResponse=false;allowedFollowupId='';}return;}
    const model=s.latestModel;if(!model||model.text===baselineText||!model.text||model.text===lastText)return;
    if(turnless&&(!awaitingResponse||!generationSeen&&model.text===baselineModelText))return;
    const recoveringTurn=!!recoverTurn;if(recoveringTurn){try{if(!WebCodexProtocol.parseCall(model.text,recoverTurn,recoverTools))return;}catch{return;}}
    if(!replySettled(s))return;
    const marker=sessionId+'\0'+(turnless?observedResultId+'\0'+observedUserText+'\0':'')+model.text;if(attempted.has(model.text)||inFlight===marker)return;const callSession=sessionId,callGeneration=generationCounter,callContext=observedContext;inFlight=marker;try{
      const contextFields=turnless?{responseKey:await responseKeyFor(callGeneration,observedResultId,observedUserText,model.text),precedingResultId:observedResultId}:{};
      const current=adapter.inspect();
      if(sessionId!==callSession||current.generating||current.latestModel?.text!==model.text||(current.latestModel?.sourceText??model.text)!==(model.sourceText??model.text)||turnless&&(callGeneration!==generationCounter||callContext!==observedContext||!awaitingResponse))return;
      const response=await send('studio.call',{text:model.text,conversationKey,generating:false,...contextFields});if(sessionId===callSession&&response?.accepted&&(!turnless||callGeneration===generationCounter&&callContext===observedContext)){lastText=model.text;if(turnless){awaitingResponse=false;if(observedResultId===allowedFollowupId)allowedFollowupId='';}if(recoveringTurn)recoverTurn='';attempted.add(model.text);if(/^```helper-call\s/.test(model.text)&&model.element)serviceNodes.add(model.element);}}finally{if(inFlight===marker)inFlight='';}
  }
  const schedule=()=>{if(retired)return;if(turnless&&!checkInstructions)trackGeneration(adapter.inspect());clearTimeout(observerTimer);observerTimer=setTimeout(()=>observe().catch(()=>{}),250);};
  document.addEventListener('click',event=>{
    if(retired)return;
    const button=event.target?.closest?.('button,a,[role="button"],[role="link"]');
    const copy=button?.cloneNode?.(true);
    if(copy)for(const icon of copy.querySelectorAll('[aria-hidden="true"], .material-symbols-outlined, .material-icons, mat-icon, svg'))icon.remove();
    const labels=[button?.getAttribute('aria-label'),button?.getAttribute('title'),button?.innerText,button?.textContent,copy?.innerText||copy?.textContent]
      .map(value=>String(value||'').trim().toLowerCase().replace(/\s*keyboard_return\s*$/i,'').replace(/\s*\(?\s*(?:ctrl|control)\s*\+\s*enter\s*\)?$/i,'').trim());
    const label=labels.join(' ');
    if(labels.some(value=>/^(new chat|new prompt|create (?:a )?new (?:chat|prompt)|новый чат|новый запрос)$/.test(value)))newChatPending=true;
    else {const navigation=chatLinkKind(event);if(navigation==='new')newChatPending=true;else if(navigation==='saved')requestInstructionCheck();}
    if(turnless&&labels.some(value=>/^(run|send|запустить|отправить)$/.test(value))){const s=adapter.inspect();awaitingResponse=true;generationSeen=false;generationCounter++;attempted.clear();lastText='';baselineText='';baselineModelText=s.latestModel?.text||'';}
    if(adapter.inspect().generating&&(/\b(stop|cancel)\b|остановить|отмена/.test(label)||button?.querySelector?.('.stoppable-spinner, ms-stoppable-spinner')))stopping=true;
  },true);
  window.addEventListener?.('popstate',requestInstructionCheck);
  document.addEventListener('keydown',event=>{if(retired)return;if(event.key==='Escape'&&adapter.inspect().generating)stopping=true;},true);
  const observer=new MutationObserver(schedule);observer.observe(document.documentElement,{subtree:true,childList:true,characterData:true,attributes:true});
  const tickTimer=setInterval(()=>{if(retired)return;if(newChatPending)observe().catch(()=>{});else if(sessionId){send('studio.tick',snapshot()).catch(()=>{});observe().catch(()=>{});}else{send('studio.hello',hello()).catch(()=>{});}},2000);
  function retire(){if(retired)return;retired=true;resetReply();sessionId='';clearTimeout(observerTimer);clearInterval(tickTimer);observer.disconnect();window.removeEventListener?.('popstate',requestInstructionCheck);if(window.__webcodexBridgeDispose===retire){window.__webcodexBridgeLoaded=false;delete window.__webcodexBridgeDispose;}}
  window.__webcodexBridgeDispose=retire;
  chrome.runtime.onMessage.addListener((m,_sender,reply)=>{
    if(retired)return;
    if(!m||!['studio.arm','studio.stage','studio.deliver','studio.snapshot','studio.disarm','studio.cleanup','studio.captureWrite','studio.captureCall'].includes(m.type)){reply({ok:false});return;}
    if(m.type==='studio.arm'){
      const s=adapter.inspect();if(!s.supported){reply({supported:false,error:s.error||'AI Studio conversation changed',conversationKey:s.conversationKey});return;}
      if(!m.hideServiceMessages)adapter.restoreCleanup();serviceNodes.clear();
      hadConversation=!!s.latestModel&&!!s.latestUser;emptyObservations=0;
      resetReply();rememberHistory();
      sessionId=m.sessionId;conversationKey=s.conversationKey;turnless=!!m.turnless;turnlessRecovery=turnless&&!!m.recover;awaitingResponse=false;generationSeen=false;generationCounter=0;allowedFollowupId='';baselineModelText=s.latestModel?.text||'';observedUserText=s.latestUser?.text||'';observedResultId='';observedContext=JSON.stringify([observedResultId,observedUserText]);recoverTurn=m.recover?m.recoverTurn||'':'';recoverTools=m.recover?m.toolNames||[]:[];baselineText=m.recover?'':s.latestModel?.text||'';lastText=m.recover?'':baselineText;lastDelivered='';inFlight='';stopping=false;newChatPending=false;newChatRequestInFlight=false;attempted.clear();reply(snapshot());if(recoverTurn)schedule();return;
    }
    if(!sessionId||m.sessionId!==sessionId){reply({ok:false,error:'Stale session'});return;}
    if(m.type==='studio.captureWrite'||m.type==='studio.captureCall'){
      const s=adapter.inspect();
      if(checkInstructions||!s.supported||s.generating||!s.latestModel){reply({ok:false,error:'Дождитесь полного ответа модели и завершения перехода в чат'});return;}
      try{
        const selected=m.type==='studio.captureWrite'?[m.tool]:m.tools||[];
        const call=WebCodexProtocol.parseCall(s.latestModel.text,null,selected);
        if(!call||m.type==='studio.captureWrite'&&call.name!=='write_file')throw Error('В последнем ответе нет единственного корректного выбранного вызова');
        resetReply();baselineText=s.latestModel.text;lastText=baselineText;awaitingResponse=false;allowedFollowupId='';
        reply({ok:true,text:s.latestModel.text,pageInstance,conversationKey:s.conversationKey});
      }catch(error){reply({ok:false,error:String(error.message||error)});}return;
    }
    if(m.type==='studio.cleanup'){if(m.final)adapter.annotateContext();if(!m.enabled)adapter.restoreCleanup();else if(m.final)adapter.cleanup({turns:[...serviceNodes],operationIds:m.operationIds||[]});reply({ok:true});return;}
    if(m.type==='studio.disarm'){resetReply();sessionId='';inFlight='';allowedFollowupId='';reply({ok:true});return;}
    if(m.type==='studio.stage'){
      if(systemStagePending){reply({ok:false,error:'System instructions are already being checked'});return;}
      systemStagePending=true;
      (async()=>{
        let s=adapter.inspect();
        if(m.ensureOnly&&s.generating&&!s.controlsPending){reply({ok:false,...snapshot(),error:'Wait for generation to finish before checking system instructions'});return;}
        if(!s.supported||(m.text&&!s.editorEmpty)){const error=!s.supported?(s.error||'AI Studio editor is not supported'):'AI Studio prompt field is not empty; existing text was preserved';reply({ok:false,...snapshot(),error});return;}
        if(!await adapter.stageSystemInstructions(m.systemText)){reply({ok:false,...snapshot(),error:adapter.getSystemStageError?.()||'Не удалось заполнить System instructions: редактор не найден или неоднозначен'});return;}
        s=adapter.inspect();
        if(sessionId!==m.sessionId||!s.supported||(m.text&&!s.editorEmpty)){const error=sessionId!==m.sessionId?'AI Studio session changed':!s.supported?(s.error||'AI Studio editor is not supported'):'AI Studio prompt field is not empty; existing text was preserved';reply({ok:false,...snapshot(),error});return;}
        if(!m.ensureOnly)rememberHistory(s);
        if(m.text===''){reply({ok:true,...snapshot()});return;}
        const ok=adapter.stage(m.text);
        reply({ok,...snapshot(),error:ok?undefined:'Could not stage session instructions in the empty prompt field'});
      })().catch(e=>reply({ok:false,...snapshot(),error:String(e.message||e)})).finally(()=>{systemStagePending=false;});
      return true;
    }
    if(m.type==='studio.snapshot'){const state=snapshot(m.operationId,false,m.expectedResultText);if(state.delivered&&turnless){observedResultId=m.operationId||observedResultId;}if(state.delivered&&(recoverTurn||turnlessRecovery)){const current=adapter.inspect(),model=current.latestModel;let valid=false;try{valid=!!model&&(!turnless||current.modelAfterUser!==false)&&!!WebCodexProtocol.parseCall(model.text,turnless?null:recoverTurn,recoverTools);}catch{}if(turnless){recoverTurn='';turnlessRecovery=false;baselineText=valid?'':model?.text||'';lastText=valid?'':baselineText;if(valid){awaitingResponse=true;generationSeen=true;baselineModelText='';schedule();}}else if(valid)schedule();else{recoverTurn='';baselineText=model?.text||'';lastText=baselineText;}}reply(state);return;}
    if(m.type==='studio.deliver'){
      const s=adapter.inspect();const state=snapshot();if(!s.supported||(!m.submit&&!s.editorEmpty)||s.generating){reply({ok:false,attempted:false,...state});return;}
      if(checkInstructions){reply({ok:false,attempted:false,...state,error:'Переход между чатами: результат сохранён, отправка ожидает проверки'});return;}
      const wrapped=`\`\`\`helper-result\n${m.text}\n\`\`\``;
      if(!m.submit){const ok=adapter.stage(wrapped);reply({ok,attempted:false,...state});return;}
      if(lastDelivered===m.text){reply({ok:false,attempted:true,...state});return;}
      if(deliveryPending){reply({ok:false,attempted:true,...state});return;}
      let operationId;try{const result=JSON.parse(m.text);if(!WebCodexProtocol.isResult(result))throw Error();operationId=result.operation_id;}catch{reply({ok:false,attempted:false,...state});return;}
      const deliverySession=sessionId,deliveryNavigation=navigationRevision;
      deliveryPending=true;
      (async()=>{
        try{
          const ok=await adapter.submit(wrapped,()=>sessionId===deliverySession&&navigationRevision===deliveryNavigation);
          if(ok){lastDelivered=m.text;if(turnless)allowedFollowupId=operationId;}
          const proof=snapshot(operationId,false,m.text);reply({ok,attempted:ok,confirmed:ok&&proof.delivered,...proof});
        }catch(e){reply({ok:false,attempted:true,...snapshot(),error:'Delivery outcome is unknown'});}
        finally{deliveryPending=false;}
      })();
      return true;
    }
  });
  if(!reattaching)send('studio.hello',hello()).catch(()=>{});
})();
