(function(root){
  'use strict';
  function transition(session,event){
    if(event.sessionId&&event.sessionId!==session.sessionId)return{session,effects:[],accepted:false};
    if(event.operationKey&&session.operationKey&&event.operationKey!==session.operationKey)return{session,effects:[],accepted:false};
    const next={...session,revision:(session.revision||0)+1},effects=[];
    switch(event.type){
      case 'call_received':
        if(session.paused||!['waiting_model','ready'].includes(session.phase)||session.pendingCall)return{session,effects,accepted:false};
        next.pendingCall=event.call;
        next.phase=event.safe||session.autoApprove?'ready':'waiting_approval';
        next.responseRevision=event.responseRevision;
        next.error='';next.reasonCode='';
        break;
      case 'dispatch_intent':
        if(session.paused||!session.pendingCall||!['ready','waiting_approval','recovering'].includes(session.phase))return{session,effects,accepted:false};
        next.operationKey=event.key;next.dispatchArgs=event.arguments;
        next.phase='recovering';next.executionStatus='intent_saved';
        next.effect={kind:'create_operation',key:event.key,sessionId:session.sessionId,coordinatorId:event.coordinatorId,authorizationRevision:event.authorizationRevision,requiresAutoApproval:!!event.requiresAutoApproval,responseRevision:session.responseRevision};
        effects.push(next.effect);
        break;
      case 'result_ready':
        next.phase=session.paused?'paused':'result_ready';
        if(session.paused)next.pausedPhase='result_ready';
        next.executionStatus=event.status||'completed';next.deliveryStatus='not_sent';
        next.effect=null;
        break;
      case 'operation_status':
        if(event.operationId&&session.operationId&&event.operationId!==session.operationId)return{session,effects,accepted:false};
        next.operationId=event.operationId||session.operationId;
        next.executionStatus=event.status;
        if(event.status==='outcome_unknown'){
          next.phase='paused';next.paused=true;next.pausedPhase='';next.resumeBlocked=true;next.reasonCode='outcome_unknown';
        }
        break;
      case 'delivery_intent':
        if(session.paused||session.phase!=='result_ready'||!session.resultText)return{session,effects,accepted:false};
        next.phase=event.attachments?'attaching_files':'delivery_uncertain';
        next.deliveryStatus='intent_saved';
        next.effect={kind:'deliver_result',key:session.operationId||session.operationKey,sessionId:session.sessionId,coordinatorId:event.coordinatorId,authorizationRevision:event.authorizationRevision};
        effects.push(next.effect);
        break;
      case 'delivery_confirmed':
        if(!session.resultText&&!(event.legacyTurnProof&&session.phase==='delivery_uncertain'&&session.nextTurn&&!session.operationKey&&!session.operationId&&!session.pendingCall)||event.operationId!==(session.operationId||session.operationKey))return{session,effects,accepted:false};
        next.completedOperationIds=event.operationId?[...(session.completedOperationIds||[]),event.operationId]:[...(session.completedOperationIds||[])];
        next.error='';next.reasonCode='';next.deliveryStatus='confirmed';next.effect=null;
        next.phase=session.paused?'paused':'waiting_model';
        if(session.paused)next.pausedPhase='waiting_model';
        next.turn=session.nextTurn;next.pendingCall=null;next.resultText='';next.attachments=[];
        delete next.attachmentDelivery;
        next.operationKey='';next.operationId='';next.dispatchArgs=null;next.count=(session.count||0)+1;
        if(next.notice?.startsWith('Прикрепляются файлы:'))next.notice='';
        break;
      case 'pause':
        if(session.phase!=='paused')next.pausedPhase=session.phase;
        next.paused=true;next.phase='paused';next.reasonCode=event.reason;
        next.userPaused=event.reason==='user_pause'||event.reason==='user_stop';
        break;
      case 'resume':
        if(session.phase==='stopped'||session.resumeBlocked||session.phase==='delivery_uncertain'||session.pausedPhase==='delivery_uncertain'||session.reasonCode==='outcome_unknown'||event.consent===false)return{session,effects,accepted:false};
        next.paused=false;next.userPaused=false;next.count=0;
        next.phase=session.pausedPhase||(session.pendingCall?(session.operationKey?'recovering':'waiting_approval'):(session.resultText?'result_ready':'waiting_model'));
        if(next.phase==='ready'&&session.pendingCall&&!event.safe&&!session.autoApprove)next.phase='waiting_approval';
        next.pausedPhase='';next.error='';next.reasonCode='';
        break;
      case 'stop':
        next.phase='stopped';next.paused=true;next.userPaused=true;next.reasonCode='user_stop';
        break;
      default:throw Error('Unknown session transition: '+event.type);
    }
    return{session:next,effects,accepted:true};
  }
  function legacyReason(message){
    const reasons={
      'Conversation changed':'conversation_changed',
      'AI Studio session is unavailable; stop and re-arm this chat':'page_unavailable',
      'Result exceeds 32 KiB; reduce the tool output before delivery.':'legacy_size_limit',
      'Session operation limit reached; resume to continue.':'legacy_operation_limit'
    };
    return reasons[message]||(/outcome is unknown/.test(message||'')?'outcome_unknown':'');
  }
  const api={transition,legacyReason};
  if(typeof module!=='undefined'&&module.exports)module.exports=api;
  root.WebCodexSession=api;
})(typeof globalThis!=='undefined'?globalThis:this);
