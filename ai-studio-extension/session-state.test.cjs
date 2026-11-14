const test=require('node:test');
const assert=require('node:assert/strict');
const {transition}=require('./session-state.js');

test('pure transitions match the existing lifecycle phases before activating effects',()=>{
  let session={sessionId:'chat',phase:'waiting_model',paused:false,autoApprove:false,count:0};
  const events=[
    [{type:'call_received',call:{name:'read_file',arguments:{path:'a'}},safe:true},'ready',0],
    [{type:'dispatch_intent',key:'key',arguments:{path:'a'},authorizationRevision:0},'recovering',1],
    [{type:'result_ready',operationKey:'key'},'result_ready',0],
    [{type:'delivery_intent',authorizationRevision:0},'delivery_uncertain',1],
    [{type:'delivery_confirmed',operationId:'op'},'waiting_model',0]
  ];
  for(const [event,phase,effectCount]of events){
    if(event.type==='result_ready'){session={...session,operationId:'op',resultText:'saved result',nextTurn:'next'};}
    const before=structuredClone(session);
    const observed=transition(Object.freeze(session),event);
    assert.deepEqual(session,before,'observer cannot mutate state');
    assert.equal(observed.session.phase,phase);
    assert.equal(observed.effects.length,effectCount);
    assert.equal(observed.accepted,true);
    session=observed.session;
  }
  assert.equal(session.count,1);assert.equal(session.pendingCall,null);assert.equal(session.resultText,'');
});

test('late generations and operation acknowledgements cannot consume a newer result',()=>{
  const session={sessionId:'new-chat',phase:'delivery_uncertain',operationKey:'new-key',operationId:'new-op',pendingCall:{name:'write_file'},resultText:'new result'};
  for(const event of [
    {type:'delivery_confirmed',sessionId:'old-chat',operationId:'new-op'},
    {type:'delivery_confirmed',sessionId:'new-chat',operationId:'old-op'},
    {type:'result_ready',operationKey:'old-key'}
  ]){
    const result=transition(session,event);assert.equal(result.accepted,false);assert.equal(result.session,session);assert.deepEqual(result.effects,[]);
  }
});

test('pausing preserves execution and a confirmed result while preventing new effects',()=>{
  const session={sessionId:'chat',phase:'recovering',operationKey:'key',operationId:'op',pendingCall:{name:'write_file'},count:0};
  let next=transition(session,{type:'pause',reason:'user_pause'}).session;
  assert.equal(next.pausedPhase,'recovering');
  next={...next,resultText:'result',nextTurn:'next'};
  next=transition(next,{type:'result_ready',operationKey:'key'}).session;
  assert.equal(next.phase,'paused');assert.equal(next.pausedPhase,'result_ready');
  assert.equal(transition(next,{type:'delivery_intent'}).accepted,false);
  next=transition(next,{type:'delivery_confirmed',operationId:'op'}).session;
  assert.equal(next.phase,'paused');assert.equal(next.pausedPhase,'waiting_model');assert.equal(next.pendingCall,null);
});
