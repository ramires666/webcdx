// Per-port bounded queues. Controls/cancellation are handled outside this queue.
const COMMAND_QUEUE_LIMIT=8;
let commandConnection='',commandProtocol=1,commandControlEpoch=0;
let commandQueueState;
function newCommandState(nativePort){return {port:nativePort,entries:new Map(),waiting:[],running:false};}
function guardCommand(record){
  if(port!==record.state.port)throw Error('Native bridge disconnected');
  if(Date.now()>=record.deadline||performance.now()>=record.expiresAt)throw Error('Command deadline expired');
  if(record.cancelled)throw Error('Command cancelled');
  if(paused||localPaused)throw Error('Local control paused');
  if(record.epoch!==accessEpoch)throw Error('Command scope changed');
  if(record.message.command_protocol===2&&(record.message.connection_id!==commandConnection||record.message.scope_epoch!==accessEpoch))throw Error('Command scope changed');
}
function commandResponse(state,message){if(port===state.port)try{post(message)}catch{try{post({kind:'response',id:message.id,error:'Response exceeds native message limit'})}catch{}}}
function invalidateCommands(){
  const state=commandQueueState;if(!state)return;
  for(const record of state.entries.values())record.cancelled=true;
  for(const record of state.waiting){state.entries.delete(record.message.id);commandResponse(state,{kind:'response',id:record.message.id,error:'Command scope changed'});}
  state.waiting=[];
}
function cancelCommand(message,nativePort){
  const state=commandQueueState;
  if(!state||state.port!==nativePort||message.connection_id!==commandConnection)return;
  const record=state.entries.get(message.id);let cancelled=false;
  if(record){record.cancelled=true;if(!record.started){cancelled=true;state.entries.delete(message.id);state.waiting=state.waiting.filter(item=>item!==record);}}
  commandResponse(state,{kind:'cancel_ack',id:message.id,cancelled});
}
function enqueueCommand(message,nativePort){
  if(!commandQueueState||commandQueueState.port!==nativePort)commandQueueState=newCommandState(nativePort);
  const state=commandQueueState;
  for(const record of [...state.waiting]){try{guardCommand(record)}catch(error){state.entries.delete(record.message.id);state.waiting=state.waiting.filter(item=>item!==record);commandResponse(state,{kind:'response',id:record.message.id,error:error.message});}}
  if(typeof message.id!=='string'||!message.id||message.id.length>128)return;
  const fail=error=>commandResponse(state,{kind:'response',id:message.id,error});
  if(state.entries.has(message.id))return fail('Command id is already pending');
  if(state.waiting.length>=COMMAND_QUEUE_LIMIT)return fail('Command queue is full');
  if(message.command_protocol!=null&&![0,1,2].includes(message.command_protocol))return fail('Command protocol is not supported');
  if(message.command_protocol!==2&&commandHasEffect(message))return fail('Command protocol is not supported');
  if(message.command_protocol===2&&(!Number.isSafeInteger(message.deadline_ms)||!Number.isSafeInteger(message.scope_epoch)))return fail('Command protocol is not supported');
  const deadline=message.command_protocol===2?Math.min(message.deadline_ms,Date.now()+30000):Date.now()+30000;
  const record={message,state,deadline,expiresAt:performance.now()+Math.max(0,deadline-Date.now()),epoch:accessEpoch,cancelled:false,started:false};
  try{guardCommand(record)}catch(error){return fail(error.message)}
  state.entries.set(message.id,record);state.waiting.push(record);drainCommands(state).catch(()=>{});
}
function commandHasEffect(message){
  return message.method?.startsWith('Input.')||['Page.navigate','Page.reload','Page.navigateToHistoryEntry','Page.handleJavaScriptDialog','Page.bringToFront','DOM.setFileInputFiles','WebCodex.newTab','WebCodex.closeTab','WebCodex.enableDownloads','WebCodex.prepare'].includes(message.method)||(message.method==='Runtime.evaluate'&&message.params?.userGesture===true);
}
async function drainCommands(state){
  if(state.running)return;state.running=true;
  try{while(state.waiting.length){
    const record=state.waiting.shift();let result,error;
    try{guardCommand(record);record.started=true;result=await execute(record.message,()=>guardCommand(record));}
    catch(failure){error=failure.message;}
    state.entries.delete(record.message.id);
    commandResponse(state,{kind:'response',id:record.message.id,result:result??{},error});
  }}finally{state.running=false;}
}
