const test=require('node:test');
const assert=require('node:assert/strict');
const {analyzeReply,parseCall,findJSONValues}=require('./protocol.js');
const tools=[{name:'exec_command',inputSchema:{type:'object',properties:{cwd:{type:'string'},argv:{type:'array',items:{type:'string'}},text:{type:'string'},config:{type:'object'}},required:['cwd','argv'],additionalProperties:false}}];
const args={cwd:'W:\\projects\\игра',argv:['cmd.exe','/c','echo braces { [ ] } and "quotes"'],text:'```helper-call\n{"v":1,"name":"nested","arguments":{}}\n```\r\n\\end',config:{nested:[{name:'not a call',text:'} ] \\"'},42,true,null]}};
const call={v:1,name:'exec_command',arguments:args};

test('one scanner preserves strings and arguments across framing, whitespace and formatting combinations',t=>{
  const prefixes=['','Проверю файл.\n','Model · 9:56 PM\n','v\n','json','```helper-call\n','```JSON\r\n','~~~json\n','<header>Code</header>\n','Use [read_file](https://example.test), path {root}.\n'];
  const suffixes=['','\n```','\n~~~','\nПосле выполнения сообщу результат.','\nCopy Download'];
  let cases=0;
  for(const prefix of prefixes)for(const suffix of suffixes)for(const indent of [undefined,2,4]){
    const raw=JSON.stringify(call,null,indent);
    const parsed=parseCall(prefix+raw+suffix,null,tools);
    assert.deepEqual(parsed,{name:call.name,arguments:args});cases++;
  }
  t.diagnostic(cases+' framing combinations preserve the exact command arguments');
});

test('rendered Unicode spaces are normalized only outside JSON string values',()=>{
  const raw='{\u00a0"v"\u200b:\u202f1,\u2028"name":"exec_command",\u00a0"arguments": {"cwd":"W:\\\\a\u00a0b","argv":["echo","x\u200by"]}}';
  assert.deepEqual(parseCall(raw,null,tools),{name:'exec_command',arguments:{cwd:'W:\\a\u00a0b',argv:['echo','x\u200by']}});
  assert.equal(analyzeReply('{"v":1,"name":"exec_command","arguments":{"number":1\u200b2}}').kind,'error','formatting normalization must not merge numeric tokens');
});

test('nested call-shaped data, arrays, escaped JSON strings and tool results are never interpreted as a new call',()=>{
  for(const raw of [JSON.stringify({type:'helper-result',result:call}),JSON.stringify({example:call}),JSON.stringify([call]),JSON.stringify(JSON.stringify(call))])assert.equal(analyzeReply(raw).kind,'none',raw);
});

test('ambiguity, duplicate decoded keys and broken call JSON fail without guessing',()=>{
  const valid=JSON.stringify(call);
  for(const value of ['{example:'+valid+'}', '[example,'+valid+']']){
    assert.notEqual(analyzeReply(value).kind,'call','a malformed outer container must not expose a nested command');
  }
  assert.equal(analyzeReply(valid+'}').kind,'error');
  for(const raw of [valid+'\n'+valid,'{"v":1,"name":"exec_command","name":"exec_command","arguments":{"cwd":"x","argv":["echo"]}}','{"v":1,"na\\u006de":"exec_command","name":"exec_command","arguments":{}}','{"v":1,"name":"exec_command","arguments":{"cwd":"x","argv":["echo"],"config":{"x":1,"x":2}}}',valid.slice(0,-1),'"v":1,"name":"exec_command","arguments":{}',valid+'\n{"other":"JSON"}']){
    assert.equal(analyzeReply(raw).kind,'error',raw);assert.throws(()=>parseCall(raw,null,tools));
  }
});

test('unknown tools, injected arguments, invalid versions and excessive nesting cannot reach execution',()=>{
  const invalid=[{...call,v:2},{...call,name:'other'},{...call,arguments:{...args,agent_id:'injected'}},{...call,arguments:{...args,request_key:'injected'}},{...call,arguments:{...args,argv:'not-array'}}];
  for(const value of invalid)assert.throws(()=>parseCall(JSON.stringify(value),null,tools));
  const deep='{"v":1,"name":"exec_command","arguments":'+('['.repeat(80))+'0'+(']'.repeat(80))+'}';
  assert.equal(analyzeReply(deep).kind,'error');
  assert.deepEqual(parseCall(JSON.stringify(call)+'x'.repeat(65536),null,tools),{name:call.name,arguments:args});
});

test('result extraction returns only complete unambiguous top-level values',()=>{
  const result={type:'helper-result',operation_id:'op-one',result:{text:JSON.stringify(call)}};
  assert.deepEqual(findJSONValues('User · now\nJSON\n'+JSON.stringify(result)),[result]);
  assert.equal(findJSONValues('{"type":"helper-result","operation_id":"one","operation_id":"two"}').length,0);
  assert.equal(findJSONValues(JSON.stringify(result).slice(0,-1)).length,0);
});
