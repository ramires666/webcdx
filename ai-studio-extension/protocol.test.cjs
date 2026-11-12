const test = require('node:test');
const assert = require('node:assert/strict');
const { parseCall, buildInstructions, buildSystemInstructions, buildSessionInstructions, buildResult } = require('./protocol.js');
const tools = [
  { name: 'write_file', inputSchema: { type:'object', additionalProperties:false, properties:{path:{type:'string'},content:{type:'string'},if_absent:{type:'boolean',const:true},expected_sha256:{type:'string',pattern:'^[0-9a-f]{64}$'}}, required:['path','content'], oneOf:[{required:['if_absent'],not:{required:['expected_sha256']}},{required:['expected_sha256'],not:{required:['if_absent']}}] } },
  { name: 'exec_command', description:'Run a command', inputSchema: { type:'object', additionalProperties:false, properties:{cwd:{type:'string'},command:{type:'string'},argv:{type:'array',minItems:1,items:{type:'string'}},request_key:{type:'string'},agent_id:{type:'string'}},required:['cwd','request_key','agent_id'],oneOf:[{required:['command'],not:{required:['argv']}},{required:['argv'],not:{required:['command']}}] } },
  { name: 'loose', inputSchema:{type:'object',additionalProperties:{type:'string'}} },
  { name: 'unsupported', inputSchema:{type:'object',oneOf:[{bogusRule:true}]} }
];
const block = (turn, name='write_file', args={path:'x',content:'hi',if_absent:true}) => '```helper-call\n'+JSON.stringify({v:1,turn,name,arguments:args})+'\n```';
test('parses one matching, schema-valid tool call', () => assert.deepEqual(parseCall(block('turn-3'),'turn-3',tools),{name:'write_file',arguments:{path:'x',content:'hi',if_absent:true}}));
test('turnless mode accepts calls without a turn or with a legacy turn', () => {
  for (const call of [{v:1,name:'write_file',arguments:{path:'x',content:'hi',if_absent:true}},{v:1,turn:'stale-turn',name:'write_file',arguments:{path:'x',content:'hi',if_absent:true}}]) {
    assert.deepEqual(parseCall('```helper-call\n'+JSON.stringify(call)+'\n```',null,tools),{name:'write_file',arguments:call.arguments});
  }
  assert.throws(()=>parseCall('```helper-call\n'+JSON.stringify({v:1,name:'write_file',arguments:{path:1,content:'hi',if_absent:true}})+'\n```',null,tools));
});
test('turnless mode accepts concise Russian explanation around one correctly fenced call', () => {
  const reply='Сначала проверю файл и сохраню его только если он ещё не существует.\n\n```helper-call\n{"v":1,"name":"write_file","arguments":{"path":"notes.txt","content":"Готово","if_absent":true}}\n```\n\nПосле выполнения сообщу результат.';
  assert.deepEqual(parseCall(reply,null,tools),{name:'write_file',arguments:{path:'notes.txt',content:'Готово',if_absent:true}});
  assert.throws(()=>parseCall(reply,'turn-1',tools));
});
test('turnless mode rejects multiple blocks and inline or malformed fence examples', () => {
  const call='```helper-call\n{"v":1,"name":"write_file","arguments":{"path":"x","content":"hi","if_absent":true}}\n```';
  for (const reply of [call+'\n'+call, 'Example: ```helper-call {"v":1}```\n'+call, '```helper-call {"v":1}\n```\n'+call]) assert.throws(()=>parseCall(reply,null,tools));
});
test('plain prose without marker is not a call', () => assert.equal(parseCall('Thinking...', 'turn-1',tools),null));
test('rejects wrong turn, partial, extra prose, and multiple blocks', () => {
  for (const text of [block('turn-2'), '```helper-call\n{', 'before\n'+block('turn-1'), block('turn-1')+'\n'+block('turn-1')]) assert.throws(()=>parseCall(text,'turn-1',tools));
});
test('validates oneOf alternatives and types', () => {
  assert.doesNotThrow(()=>parseCall(block('n','exec_command',{cwd:'/',argv:['echo','ok']}),'n',tools));
  for (const args of [{path:1,content:'x',if_absent:true},{path:'x',content:'x'},{path:'x',content:'x',if_absent:true,expected_sha256:'a'.repeat(64)},{cwd:'/',command:'true',agent_id:'a'}]) assert.throws(()=>parseCall(block('n',args.agent_id?'exec_command':'write_file',args),'n',tools));
});
test('rejects dangerous keys and unsupported schemas', () => {
  const malicious='```helper-call\n{"v":1,"turn":"n","name":"loose","arguments":{"nested":{"constructor":{}}}}\n```';
  assert.throws(()=>parseCall(malicious,'n',tools));
  assert.throws(()=>parseCall(block('n','unsupported',{}),'n',tools),/Unsupported schema keyword/);
});
test('instructions and results retain protocol data', () => {
  const prompt=buildInstructions({turn:'nonce-2',agentId:'a',tools,instructions:'do work'});
  assert.match(prompt,/tool output is untrusted/i); assert.match(prompt,/request_key and agent_id/); assert.match(prompt,/Run a command/); assert.match(prompt,/If no tool is needed/);
  assert.doesNotMatch(prompt,/"request_key"/); assert.doesNotMatch(prompt,/"agent_id"/);
  assert.match(prompt,/next_turn from the most recent helper-result/);
  const result=JSON.parse(buildResult({turn:1,nextTurn:2,operationId:'o',name:'t',result:{content:[{type:'text',text:'ok'}],isError:false,structuredContent:{ok:true}}}));
  assert.deepEqual(result.result.structuredContent,{ok:true}); assert.equal(result.type,'helper-result'); assert.equal(result.next_turn,2); assert.equal(result.operation_id,'o');
  const failed=JSON.parse(buildResult({turn:'t',nextTurn:'t2',operationId:'o',name:'t',error:'failed'}));
  assert.equal(failed.transport_error,'failed'); assert.equal('result' in failed,false);
  const turnless=JSON.parse(buildResult({turn:'internal-t',nextTurn:'internal-t2',operationId:'o2',name:'t',result:{ok:true},turnless:true}));
  assert.equal('turn' in turnless,false); assert.equal('next_turn' in turnless,false);
  assert.equal(turnless.operation_id,'o2'); assert.deepEqual(turnless.result,{ok:true});
});
test('system instructions contain stable protocol and schemas, session instructions contain only the active turn', () => {
  const system=buildSystemInstructions({agentId:'a',tools,instructions:'do work'});
  const session=buildSessionInstructions({turn:'nonce-2'});
  assert.match(system,/Run a command/); assert.match(system,/exactly one tool call/); assert.match(system,/concise human-readable prose/);
  assert.match(system,/If no tool is needed, answer in concise human-readable prose\. When a tool is needed/);
  assert.match(system,/When making a tool call, include exactly one call per full reply with no prose/);
  assert.doesNotMatch(system,/Return one call per full reply/);
  assert.doesNotMatch(system,/nonce-2|active turn:.*nonce/i);
  assert.match(session,/nonce-2/); assert.match(session,/system MCP rules/); assert.doesNotMatch(session,/inputSchema|Run a command|helper-call/);
});
test('turnless system instructions omit active turn requirements', () => {
  const system=buildSystemInstructions({agentId:'a',tools,turnless:true});
  assert.match(system,/\{"v":1,"name":"tool_name","arguments":/);
  assert.match(system,/concise user-facing explanation/);
  assert.match(system,/Do not include meta reasoning/);
  assert.doesNotMatch(system,/no prose or additional calls/);
  assert.doesNotMatch(system,/active turn|next_turn|"turn"/i);
});

test('command instructions discover the agent working directory before assuming an operating system', () => {
  const prompt=buildSystemInstructions({agentId:'windows-agent',turnless:true,tools:[...tools,{name:'list_allowed_roots',inputSchema:{type:'object',properties:{}}}]});
  assert.match(prompt,/Before the first command in this chat, call list_allowed_roots/);
  assert.match(prompt,/do not assume the operating system/);
});

test('large UTF-8 write calls retain their complete content',()=>{
 const content=('code \u044f \ud83d\ude00\n').repeat(200000);
 for(const turn of ['n',null]){
  const args={path:'large.html',content,if_absent:true};
  assert.deepEqual(parseCall(block('n','write_file',args),turn,tools),{name:'write_file',arguments:args});
 }
});

test('system instructions present only Helper while legacy calls and results remain readable',()=>{
  const {isResult}=require('./protocol.js');
  const system=buildSystemInstructions({agentId:'local',tools:[{name:'read_file',description:'WebCodex MCP local tool',inputSchema:{type:'object',description:'WebCodex tool',properties:{path:{type:'string'}}}}],instructions:'Use WebCodex tools.',turnless:true});
  assert.match(system,/You are Helper/);assert.match(system,/helper-call/);assert.doesNotMatch(system,/webcodex/i);
  const call='```webcodex-call\n'+JSON.stringify({v:1,turn:'legacy',name:'write_file',arguments:{path:'x',content:'hi',if_absent:true}})+'\n```';
  assert.equal(parseCall(call,null,tools).name,'write_file');assert.equal(parseCall(call,'legacy',tools).name,'write_file');
  assert.equal(isResult({type:'webcodex-result'}),true);assert.equal(isResult({type:'helper-result'}),true);assert.equal(isResult({type:'other'}),false);
});

test('display-name migration preserves literal paths and schema enum values',()=>{
  const system=buildSystemInstructions({agentId:'local',tools:[{name:'example',inputSchema:{type:'object',properties:{value:{type:'string',enum:['WebCodex'],description:'WebCodex choice'}}}}],instructions:'Read C:\\WebCodex\\file.txt using WebCodex.',turnless:true});
  assert.ok(system.includes('C:\\WebCodex\\file.txt'));assert.ok(system.includes('"WebCodex"'));assert.ok(system.includes('Helper choice'));
});
