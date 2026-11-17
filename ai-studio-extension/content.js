(() => {
  'use strict';
  if (window.top !== window || location.origin !== 'https://aistudio.google.com') return;
  const reattaching = !!window.__webcodexBridgeReattach;
  if (window.__webcodexBridgeLoaded && !reattaching) return;
  window.__webcodexBridgeDispose?.();
  window.__webcodexBridgeLoaded = true;
  const adapter=WebCodexStudio.createAdapter(document,location);
  const contentVersion='0.5.9';
  const pageInstance=Date.now()+'-'+Math.random();
  let sessionMarker='';
  let sessionId='', baselineText='', conversationKey='', lastText='', lastDelivered='', inFlight='', observerTimer=0, stopping=false, newChatPending=false, newChatRequestInFlight=false, attempted=new Set(), recoverTurn='', recoverTools=[], turnless=false, turnlessRecovery=false, observedUserText='', observedResultId='', observedContext='';
  let awaitingResponse=false, generationSeen=false, generationCounter=0, baselineModelText='';
  let deliveryPending = false, attachmentPending=false, retired = false, allowedFollowupId = '', deliveryRevision=0;
  let hadConversation = false, emptyObservations = 0;
  let checkInstructions=false, instructionCheckPending=false, systemStagePending=false;
  let historyCandidate='', historyObservations=0, navigationRevision=0;
  const replyQuietMs=1000, invalidReplyQuietMs=8000;
  let pendingReply=null, replyTimer=0, sourceRecoveryPending=false, sourceRecoveryAttempt=null;
  const serviceNodes=new Set();
  const send=async(type,data={})=>{
    try{return await chrome.runtime.sendMessage({type,sessionId,contentVersion,...data});}
    catch(e){if(/Extension context invalidated/i.test(String(e.message||e))){retire();return {accepted:false};}throw e;}
  };
  delete window.__webcodexBridgeReattach;
  adapter.collapseChat?.();
  let chatWidth=0;
  const restoreChatWidth=()=>adapter.setChatWidth?.(chatWidth,true);
  const widthChanged=(changes,area)=>{
    if(!retired&&area==='local'&&changes.chatWidth){
      const value=changes.chatWidth.newValue;
      if(Number.isInteger(value)&&value>=0&&value<=100&&value%10===0){chatWidth=value;adapter.setChatWidth?.(chatWidth);}
    }
  };
  chrome.storage?.local?.get('chatWidth').then(saved=>{
    if(retired)return;
    const value=saved.chatWidth;
    if(Number.isInteger(value)&&value>=0&&value<=100&&value%10===0){chatWidth=value;adapter.setChatWidth?.(chatWidth);}
  }).catch(()=>{});
  chrome.storage?.onChanged?.addListener(widthChanged);
  window.addEventListener?.('resize',restoreChatWidth);

  function parsedResult(text){try{const result=JSON.parse(text||'');return WebCodexProtocol.isResult(result)&&typeof result.operation_id==='string'&&result.operation_id?result:null;}catch{return null;}}
  async function responseKeyFor(generation,resultId,userText,modelText){
    const digest=async text=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(text))),byte=>byte.toString(16).padStart(2,'0')).join('');
    const [userHash,modelHash]=await Promise.all([digest(userText),digest(modelText)]);
    return JSON.stringify([pageInstance,generation,resultId,userHash,modelHash]);
  }
  function visibleResultId(user){const parsed=parsedResult(user?.text)?.operation_id,id=typeof user?.operationId==='string'&&user.operationId?user.operationId:'';return id||parsed||'';}
  function snapshot(operationId,conversationReplaced=false,expectedResultText){const s=adapter.inspect(),resultId=visibleResultId(s.latestUser),delivered=operationId?resultId===operationId||adapter.hasDelivery(operationId,expectedResultText):undefined;if(delivered)adapter.collapseDelivery?.(operationId,expectedResultText);const observerStatus=attachmentPending?'attaching_files':!s.supported?'unsupported':s.editingReply?'editing_reply':checkInstructions||systemStagePending?'checking':s.generating?'generating':!s.latestModel||s.modelAfterUser===false?'waiting_model':turnless&&!awaitingResponse?'waiting_request':pendingReply?'settling':'observing';return {observerStatus,supported:s.supported,generating:s.generating,editorEmpty:s.editorEmpty,conversationKey:s.conversationKey,chatMarkers:s.chatMarkers||[],chatAddress:s.chatAddress||'',replyMarker:s.replyMarker||'',error:s.error,...(conversationReplaced?{conversationReplaced:true}:{}),deliveredOperationId:resultId,delivered};}
  function reportObserverError(error){if(!retired&&!sourceRecoveryPending&&sessionId)send('studio.observerError',{observerStatus:'error',observerError:String(error.message||error)}).catch(()=>{});}
  async function attachmentFiles(message,isCurrent){
    const files=[];
    for(const meta of message.attachments){
      const parts=[];let offset=0;
      while(offset<meta.encodedLength){
        if(!isCurrent())throw Error('Прикрепление остановлено');
        const response=await send('studio.fileChunk',{operationId:message.operationId,attachmentId:meta.id,offset});
        const chunk=response?.fileChunk;
        if(!response?.accepted||!chunk||!Number.isSafeInteger(chunk.nextOffset)||chunk.nextOffset<=offset||chunk.nextOffset>meta.encodedLength)throw Error(response?.error||'Неполная передача файла');
        const binary=atob(chunk.data);parts.push(Uint8Array.from(binary,char=>char.charCodeAt(0)));offset=chunk.nextOffset;
      }
      if(!isCurrent())throw Error('Прикрепление остановлено');
      const file=new File(parts,meta.name,{type:meta.mimeType});
      const hash=await crypto.subtle.digest('SHA-256',await file.arrayBuffer());
      const sha=Array.from(new Uint8Array(hash),value=>value.toString(16).padStart(2,'0')).join('');
      if(file.size!==meta.size||sha!==meta.sha256)throw Error('Контрольная сумма прикрепляемого файла не совпала');
      files.push(file);
    }
    return files;
  }
  function hello(){const s=adapter.inspect();return{pageInstance,chatMarkers:s.chatMarkers||[],chatAddress:s.chatAddress||'',replyMarker:s.replyMarker||'',supported:s.supported,editorEmpty:s.editorEmpty,generating:s.generating,hasHistory:!!s.latestModel||!!s.latestUser,blankChat:s.supported&&s.editorEmpty&&(!s.generating||s.controlsPending)&&!s.latestModel&&!s.latestUser};}
  function resetReply(){pendingReply=null;clearTimeout(replyTimer);replyTimer=0;}
  function replySettled(s){
    // Run/Stop can switch before the last code tokens reach the DOM. Re-read the
    // original answer at the deadline; normalized call text can hide ongoing edits.
    const model=s.latestModel,source=model.sourceText??model.text;
    const key=JSON.stringify([sessionId,generationCounter,observedContext,source]);
    if(pendingReply?.key!==key)pendingReply={key,since:Date.now(),parsed:WebCodexProtocol.analyzeReply(model.text)};
    const parsed=pendingReply.parsed;
    const quietMs=parsed.kind==='error'?invalidReplyQuietMs:replyQuietMs;
    const remaining=quietMs-(Date.now()-pendingReply.since);
    clearTimeout(replyTimer);replyTimer=0;
    if(remaining<=0)return true;
    replyTimer=setTimeout(()=>{replyTimer=0;observe().catch(reportObserverError);},remaining);
    return false;
  }
  async function recoverReplySource(s){
    const model=s.latestModel;
    if(!turnless||!sessionMarker||!adapter.recoverReplySource||!model)return s;
    const analysis=WebCodexProtocol.analyzeReply(model.text);
    const damagedStructuredHeader=analysis.kind==='frame'&&analysis.header.split(/\r?\n/).some(line=>{
      const field=/^[ \t]*([A-Za-z_][\w]*)[ \t]*[:=] ?([\[{].*)$/.exec(line);
      if(!field)return false;
      const property=recoverTools.find(tool=>tool.name===analysis.name)?.inputSchema?.properties?.[field[1]];
      if(field[1]!=='arguments'&&!['array','object'].includes(property?.type))return false;
      try{JSON.parse(field[2]);return false;}catch{return true;}
    });
    if(!model.sourceError&&analysis.kind!=='error'&&!damagedStructuredHeader)return s;
    const identity=[sessionId,generationCounter,observedContext,navigationRevision,model.element,model.sourceRevision,model.sourceText??model.text];
    if(sourceRecoveryAttempt?.every((value,index)=>value===identity[index]))return s;
    if(sourceRecoveryPending)return null;
    sourceRecoveryAttempt=identity;sourceRecoveryPending=true;
    try{
      let recovered=false;
      try{recovered=await adapter.recoverReplySource(model);}catch{}
      const current=adapter.inspect();
      if(retired||sessionId!==identity[0]||generationCounter!==identity[1]||observedContext!==identity[2]||navigationRevision!==identity[3]||current.generating||current.latestModel?.element!==model.element||current.latestModel?.sourceRevision!==model.sourceRevision)return null;
      return recovered?current:s;
    }finally{sourceRecoveryPending=false;}
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
      const response=await send('studio.ensureInstructions',{chatMarkers:s.chatMarkers||[],chatAddress:s.chatAddress||'',replyMarker:s.replyMarker||''});
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
  function expectResponse(){const s=adapter.inspect();recoverTurn='';turnlessRecovery=false;awaitingResponse=true;generationSeen=false;generationCounter++;attempted.clear();lastText='';baselineText='';baselineModelText=s.latestModel?.text||'';}
  async function observe(){if(retired||sourceRecoveryPending)return;adapter.setChatWidth?.(chatWidth);adapter.collapseChat?.();let s=adapter.inspect();if(s.supported&&s.latestModel&&s.latestUser){hadConversation=true;emptyObservations=0;}const blank=s.supported&&(!s.generating||s.controlsPending)&&s.editorEmpty&&!s.latestModel&&!s.latestUser;if(!blank)emptyObservations=0;else if(hadConversation&&!newChatPending&&!awaitingResponse&&!deliveryPending&&!allowedFollowupId&&!inFlight&&!checkInstructions&&++emptyObservations>=2)newChatPending=true;const conversationReplaced=newChatPending&&blank;if(conversationReplaced){if(newChatRequestInFlight)return;newChatRequestInFlight=true;try{const transition={...snapshot(undefined,true),error:'Conversation was replaced'};if(sessionId)await send('studio.snapshot',transition);else await send('studio.hello',{pageInstance,...transition});}finally{newChatRequestInFlight=false;}return;}if(!sessionId||newChatPending||s.editingReply)return;if(!s.supported){await send('studio.snapshot',snapshot());return;}
    if(systemStagePending||await ensureSavedInstructions(s))return;
    if(turnless){const userText=s.latestUser?.text||'',resultId=visibleResultId(s.latestUser);if(resultId){observedResultId=resultId;adapter.releaseFiles?.(resultId);}observedUserText=userText;const context=JSON.stringify([observedResultId,observedUserText]);if(context!==observedContext){observedContext=context;attempted.clear();lastText='';baselineText='';}trackGeneration(s);if(resultId&&resultId===allowedFollowupId&&s.modelAfterUser===true&&!awaitingResponse){generationCounter++;awaitingResponse=true;generationSeen=true;baselineModelText='';}if(s.modelAfterUser===false)return;}
    if(s.generating){resetReply();return;}
    if(stopping){resetReply();stopping=false;lastText=s.latestModel?.text||'';if(turnless){awaitingResponse=false;allowedFollowupId='';}return;}
    let model=s.latestModel;if(!model||model.text===baselineText||!model.text||model.text===lastText)return;
    if(s.modelError&&!s.generating){await send('studio.modelError',{error:s.modelError});lastText=model.text;return;}
    if(turnless&&(!awaitingResponse||!generationSeen&&model.text===baselineModelText))return;
    const recoveringTurn=!!recoverTurn;if(recoveringTurn){try{if(!WebCodexProtocol.parseCall(model.text,recoverTurn,recoverTools))return;}catch{return;}}
    if(!replySettled(s))return;
    s=await recoverReplySource(s);if(!s)return;model=s.latestModel;
    const marker=sessionId+'\0'+(turnless?observedResultId+'\0'+observedUserText+'\0':'')+model.text;if(attempted.has(model.text)||inFlight===marker)return;const callSession=sessionId,callGeneration=generationCounter,callContext=observedContext;inFlight=marker;try{
      const contextFields=turnless?{responseKey:await responseKeyFor(callGeneration,observedResultId,observedUserText,model.responseIdentityText??model.text),precedingResultId:observedResultId}:{};
      const current=adapter.inspect();
      if(sessionId!==callSession||current.generating||current.latestModel?.sourceRevision!==model.sourceRevision||current.latestModel?.text!==model.text||(current.latestModel?.sourceText??model.text)!==(model.sourceText??model.text)||turnless&&(callGeneration!==generationCounter||callContext!==observedContext||!awaitingResponse))return;
      const response=await send('studio.call',{text:model.text,sourceText:model.sourceText??model.text,sourceBlocks:model.sourceBlocks,sourceRemainder:model.sourceRemainder,recoveryRules:model.recoveryRules,sourceRevision:model.sourceRevision,...(model.sourceError?{sourceError:model.sourceError}:{}),conversationKey,generating:false,...contextFields});if(sessionId===callSession&&response?.accepted&&(!turnless||callGeneration===generationCounter&&callContext===observedContext)){lastText=model.text;if(turnless){awaitingResponse=false;if(observedResultId===allowedFollowupId)allowedFollowupId='';}if(recoveringTurn)recoverTurn='';attempted.add(model.text);if(response.toolCall&&model.element){serviceNodes.add(model.element);adapter.collapseCall?.(model);}}}finally{if(inFlight===marker)inFlight='';}
  }
  const schedule=()=>{if(retired||sourceRecoveryPending)return;try{if(turnless&&!checkInstructions)trackGeneration(adapter.inspect());clearTimeout(observerTimer);observerTimer=setTimeout(()=>observe().catch(reportObserverError),250);}catch(error){reportObserverError(error);}};
  document.addEventListener('click',event=>{
    if(retired||sourceRecoveryPending)return;
    const button=event.target?.closest?.('button,a,[role="button"],[role="link"]');
    const copy=button?.cloneNode?.(true);
    if(copy)for(const icon of copy.querySelectorAll('[aria-hidden="true"], .material-symbols-outlined, .material-icons, mat-icon, svg'))icon.remove();
    const labels=[button?.getAttribute('aria-label'),button?.getAttribute('title'),button?.innerText,button?.textContent,copy?.innerText||copy?.textContent]
      .map(value=>String(value||'').trim().toLowerCase());
    const label=labels.join(' ');
    if(labels.some(value=>/^(new chat|new prompt|create (?:a )?new (?:chat|prompt)|новый чат|новый запрос)$/.test(value)))newChatPending=true;
    else {const navigation=chatLinkKind(event);if(navigation==='new')newChatPending=true;else if(navigation==='saved')requestInstructionCheck();}
    if(turnless&&adapter.isSubmitControl(button)){if(sessionId&&sessionMarker&&!checkInstructions&&!systemStagePending)adapter.prepareSessionMessage?.(sessionMarker);expectResponse();}
    if(adapter.inspect().generating&&(/\b(stop|cancel)\b|остановить|отмена/.test(label)||button?.querySelector?.('.stoppable-spinner, ms-stoppable-spinner')))stopping=true;
  },true);
  window.addEventListener?.('popstate',requestInstructionCheck);
  document.addEventListener('keydown',event=>{
    if(retired||sourceRecoveryPending)return;
    if(event.key==='Escape'&&adapter.inspect().generating)stopping=true;
    const editor=event.target?.closest?.('textarea,[contenteditable="true"]');
    if(turnless&&event.key==='Enter'&&(event.ctrlKey||event.metaKey)&&!event.shiftKey&&!event.altKey&&editor&&editor.getAttribute?.('placeholder')!=='Optional tone and style instructions for the model'&&!editor.closest?.('ms-system-instructions,[role="dialog"]')){if(sessionId&&sessionMarker&&!checkInstructions&&!systemStagePending)adapter.prepareSessionMessage?.(sessionMarker);expectResponse();}
  },true);
  const observer=new MutationObserver(schedule);observer.observe(document.documentElement,{subtree:true,childList:true,characterData:true,attributes:true});
  const tickTimer=setInterval(()=>{if(retired||sourceRecoveryPending)return;try{if(newChatPending)observe().catch(reportObserverError);else if(sessionId){send('studio.tick',snapshot()).catch(reportObserverError);observe().catch(reportObserverError);}else{send('studio.hello',hello()).catch(()=>{});}}catch(error){reportObserverError(error);}},2000);
  function retire(){if(retired)return;retired=true;adapter.setChatWidth?.(0);window.removeEventListener?.('resize',restoreChatWidth);chrome.storage?.onChanged?.removeListener(widthChanged);resetReply();sessionId='';clearTimeout(observerTimer);clearInterval(tickTimer);observer.disconnect();window.removeEventListener?.('popstate',requestInstructionCheck);if(window.__webcodexBridgeDispose===retire){window.__webcodexBridgeLoaded=false;delete window.__webcodexBridgeDispose;}}
  window.__webcodexBridgeDispose=retire;
  chrome.runtime.onMessage.addListener((m,_sender,reply)=>{
    if(retired)return;
    if(!m||!['studio.arm','studio.stage','studio.deliver','studio.snapshot','studio.disarm','studio.cleanup','studio.captureWrite','studio.captureCall','studio.readReply','studio.bindMarker','studio.injectMarker','studio.cancelDelivery','studio.collapseCall','studio.collapseChat','studio.chatWidth'].includes(m.type)){reply({ok:false});return;}
    if(m.type==='studio.chatWidth'){
      if(!Number.isInteger(m.value)||m.value<0||m.value>100||m.value%10!==0){reply({ok:false,error:'Invalid chat width'});return;}
      chatWidth=m.value;adapter.setChatWidth?.(chatWidth);reply({ok:true});return;
    }
    if(m.type==='studio.collapseChat'){reply({ok:true,collapsed:adapter.collapseChat?.({force:true})||0});return;}
    if(m.type==='studio.arm'){
      const s=adapter.inspect();if(!s.supported){reply({supported:false,error:s.error||'AI Studio conversation changed',conversationKey:s.conversationKey});return;}
      if(!m.hideServiceMessages)adapter.restoreCleanup();serviceNodes.clear();
      hadConversation=!!s.latestModel&&!!s.latestUser;emptyObservations=0;
      resetReply();rememberHistory();
      sessionId=m.sessionId;sessionMarker=m.sessionMarker||'';conversationKey=s.conversationKey;turnless=!!m.turnless;turnlessRecovery=turnless&&!!m.recover;awaitingResponse=false;generationSeen=false;generationCounter=0;allowedFollowupId='';baselineModelText=s.latestModel?.text||'';observedUserText=s.latestUser?.text||'';observedResultId='';observedContext=JSON.stringify([observedResultId,observedUserText]);recoverTurn=m.recover?m.recoverTurn||'':'';recoverTools=m.toolNames||[];baselineText=m.recover?'':s.latestModel?.text||'';lastText=m.recover?'':baselineText;lastDelivered='';inFlight='';stopping=false;newChatPending=false;newChatRequestInFlight=false;attempted.clear();reply(snapshot());if(recoverTurn)schedule();return;
    }
    if(!sessionId||m.sessionId!==sessionId){reply({ok:false,error:'Stale session'});return;}
    if(m.type==='studio.bindMarker'){sessionMarker=m.sessionMarker||'';reply({ok:true});return;}
    if(m.type==='studio.injectMarker'){
      const s=adapter.inspect();
      if(checkInstructions||systemStagePending||sourceRecoveryPending||newChatPending||deliveryPending||!s.supported||s.generating||s.editingReply||m.bindingAddress&&s.chatAddress!==m.bindingAddress){reply({ok:false,error:'Дождитесь завершения ответа и проверки текущего чата'});return;}
      const ok=adapter.prepareSessionMessage(sessionMarker,{force:true});
      reply({ok,error:ok?undefined:'Не удалось вставить ID: проверьте поле сообщения и текущий сеанс'});return;
    }
    if(m.type==='studio.collapseCall'){
      const model=adapter.inspect().latestModel;
      if(!model||model.sourceRevision!==m.sourceRevision||model.text!==m.text){reply({ok:false});return;}
      reply({ok:adapter.collapseCall?.(model)===true});return;
    }
    if(m.type==='studio.cancelDelivery'){deliveryRevision++;reply({ok:true});return;}
    if(m.type==='studio.readReply'){
      let s=adapter.inspect(),model=s.latestModel;const revision=navigationRevision;
      if(checkInstructions||!s.supported||s.generating||s.editingReply||!model||s.modelAfterUser===false){reply({ok:false,error:'Дождитесь полного ответа в текущем чате'});return;}
      (async()=>{
        s=await recoverReplySource(s);if(!s)throw Error('Ответ изменился во время чтения');model=s.latestModel;
        const responseKey=await responseKeyFor(generationCounter,observedResultId,observedUserText,model.responseIdentityText??model.text);
        const current=adapter.inspect();
        if(sessionId!==m.sessionId||navigationRevision!==revision||current.generating||current.latestModel?.sourceRevision!==model.sourceRevision||current.latestModel?.text!==model.text||(current.latestModel?.sourceText??current.latestModel?.text)!==(model.sourceText??model.text))throw Error('Ответ изменился во время чтения');
        reply({ok:true,text:model.text,sourceText:model.sourceText??model.text,sourceBlocks:model.sourceBlocks,sourceRemainder:model.sourceRemainder,recoveryRules:model.recoveryRules,sourceRevision:model.sourceRevision,sourceError:model.sourceError,responseKey,conversationKey:s.conversationKey});
      })().catch(error=>reply({ok:false,error:String(error.message||error)}));return true;
    }
    if(m.type==='studio.captureWrite'||m.type==='studio.captureCall'){
      let s=adapter.inspect();
      if(checkInstructions||!s.supported||s.generating||s.editingReply||!s.latestModel){reply({ok:false,error:'Дождитесь полного ответа модели и завершения перехода в чат'});return;}
      (async()=>{
        s=await recoverReplySource(s);if(!s)throw Error('Ответ изменился во время чтения');
        const selected=m.type==='studio.captureWrite'?[m.tool]:m.tools||[];
        if(s.latestModel.sourceError)throw Error(s.latestModel.sourceError);
        const call=WebCodexProtocol.parseCall(s.latestModel.text,null,selected,{...s.latestModel,sessionMarker,allowUnmarked:true,lastFrame:true});
        if(!call||m.type==='studio.captureWrite'&&call.name!=='write_file')throw Error('В последнем ответе нет единственного корректного выбранного вызова');
        resetReply();baselineText=s.latestModel.text;lastText=baselineText;awaitingResponse=false;allowedFollowupId='';
        reply({ok:true,text:s.latestModel.text,sourceRevision:s.latestModel.sourceRevision,sourceBlocks:s.latestModel.sourceBlocks,sourceRemainder:s.latestModel.sourceRemainder,pageInstance,conversationKey:s.conversationKey});
      })().catch(error=>reply({ok:false,error:String(error.message||error)}));return true;
    }
    if(m.type==='studio.cleanup'){if(m.final||!adapter.inspect().generating)adapter.annotateContext();if(!m.enabled)adapter.restoreCleanup();else if(m.final)adapter.cleanup({turns:[...serviceNodes],operationIds:m.operationIds||[]});reply({ok:true});return;}
    if(m.type==='studio.disarm'){resetReply();sessionId='';sessionMarker='';inFlight='';allowedFollowupId='';reply({ok:true});return;}
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
    if(m.type==='studio.snapshot'){const state=snapshot(m.operationId,false,m.expectedResultText);if(state.delivered&&turnless){observedResultId=m.operationId||observedResultId;}if(state.delivered&&(recoverTurn||turnlessRecovery)){const current=adapter.inspect(),model=current.latestModel;let valid=false;try{valid=!!model&&(!turnless||current.modelAfterUser!==false)&&!!WebCodexProtocol.parseCall(model.text,turnless?null:recoverTurn,recoverTools,{...model,sessionMarker,allowUnmarked:true});}catch{}if(turnless){recoverTurn='';turnlessRecovery=false;baselineText=valid?'':model?.text||'';lastText=valid?'':baselineText;if(valid){awaitingResponse=true;generationSeen=true;baselineModelText='';schedule();}}else if(valid)schedule();else{recoverTurn='';baselineText=model?.text||'';lastText=baselineText;}}reply(state);return;}
    if(m.type==='studio.deliver'){
      if(newChatPending){reply({ok:false,attempted:false,...snapshot(),error:'Открыт новый чат; старый результат в него не отправляется'});return;}
      const s=adapter.inspect();const state=snapshot();if(!s.supported||(!m.submit&&!s.editorEmpty)||s.generating&&!s.controlsPending||s.editingReply){reply({ok:false,attempted:false,...state,error:s.editingReply?'Закройте редактор ответа AI Studio и нажмите «Передать результат»':s.generating?'Дождитесь окончания ответа AI Studio и нажмите «Передать результат»':s.error||'Проверьте поле ввода AI Studio; результат сохранён в панели'});return;}
      if(checkInstructions){reply({ok:false,attempted:false,...state,error:'Переход между чатами: результат сохранён, отправка ожидает проверки'});return;}
      const wrapped=`\`\`\`helper-result\n${m.text}\n\`\`\``;
      if(!m.submit){const ok=adapter.stage(wrapped);reply({ok,attempted:false,...state});return;}
      if(lastDelivered===m.text){reply({ok:false,attempted:true,...state});return;}
      if(deliveryPending){reply({ok:false,attempted:true,...state});return;}
      let operationId;try{const result=JSON.parse(m.text);if(!WebCodexProtocol.isResult(result))throw Error();operationId=result.operation_id;}catch{reply({ok:false,attempted:false,...state});return;}
      if(m.attachments?.length&&m.operationId!==operationId){reply({ok:false,attempted:false,...state,error:'File operation does not match the result'});return;}
      const existing=snapshot(operationId,false,m.text);
      if(existing.delivered){reply({ok:true,attempted:true,confirmed:true,...existing});return;}
      const deliverySession=sessionId,deliveryNavigation=navigationRevision;
      deliveryPending=true;
      if(m.attachments?.length){
        attachmentPending=true;
        const revision=deliveryRevision;
        const current=()=>sessionId===deliverySession&&navigationRevision===deliveryNavigation&&deliveryRevision===revision&&!newChatPending;
        reply({ok:true,attempted:false,pending:true,...state});
        (async()=>{
          let attempted=false;
          try{
            const files=await attachmentFiles(m,current);
            if(!current()||!adapter.stage(wrapped,{recover:true}))throw Error('Поле ввода изменилось; прикрепление остановлено');
            const stillOwned=()=>current()&&adapter.ownsDraft(wrapped)&&!adapter.inspect().generating;
            const attached=await adapter.attachFiles(files,{key:operationId,isCurrent:stillOwned});
            if(!attached.ok)throw Error(attached.error);
            if(!stillOwned())throw Error('Поле ввода изменилось; сообщение не отправлено');
            const intent=await send('studio.deliveryStarted',{operationId});
            if(!intent?.accepted||!stillOwned())throw Error('Отправка остановлена до нажатия Run');
            attempted=true;
            const ok=await adapter.submit(wrapped,stillOwned,{recover:true});
            if(ok){lastDelivered=m.text;if(turnless)allowedFollowupId=operationId;}
            const proof=snapshot(operationId,false,m.text);
            if(proof.delivered)adapter.releaseFiles?.(operationId);
            await send('studio.deliveryComplete',{operationId,ok,attempted:ok,confirmed:ok&&proof.delivered,error:ok?undefined:'AI Studio не подтвердила отправку сообщения'});
          }catch(error){await send('studio.deliveryComplete',{operationId,attempted,confirmed:false,error:String(error.message||error)}).catch(()=>{});}
          finally{deliveryPending=false;attachmentPending=false;}
        })();return;
      }
      (async()=>{
        try{
          const ok=await adapter.submit(wrapped,()=>sessionId===deliverySession&&navigationRevision===deliveryNavigation&&!newChatPending,{recover:true});
          if(ok){lastDelivered=m.text;if(turnless)allowedFollowupId=operationId;}
          const proof=snapshot(operationId,false,m.text);reply({ok,attempted:ok,confirmed:ok&&proof.delivered,...proof,...(!ok?{error:'Результат не отправлен. Если в поле ваш текст, сохраните его и очистите поле. Если там результат Helper, оставьте его без изменений и нажмите «Передать результат». Проверьте, что кнопка Run доступна.'}:{})});
        }catch(e){reply({ok:false,attempted:true,...snapshot(),error:'Delivery outcome is unknown'});}
        finally{deliveryPending=false;}
      })();
      return true;
    }
  });
  if(!reattaching)send('studio.hello',hello()).catch(()=>{});
})();
