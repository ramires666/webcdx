const test = require('node:test');
const assert = require('node:assert/strict');
const { parseCall, buildInstructions, buildSystemInstructions, buildSessionInstructions, buildResult } = require('./protocol.js');
const tools = [
  { name: 'write_file', inputSchema: { type:'object', additionalProperties:false, properties:{path:{type:'string'},content:{type:'string'},if_absent:{type:'boolean',const:true},expected_sha256:{type:'string',pattern:'^[0-9a-f]{64}$'}}, required:['path','content'], oneOf:[{required:['if_absent'],not:{required:['expected_sha256']}},{required:['expected_sha256'],not:{required:['if_absent']}}] } },
  { name: 'exec_command', description:'Run a command', inputSchema: { type:'object', additionalProperties:false, properties:{cwd:{type:'string'},command:{type:'string'},argv:{type:'array',minItems:1,items:{type:'string'}},request_key:{type:'string'},agent_id:{type:'string'}},required:['cwd','request_key','agent_id'],oneOf:[{required:['command'],not:{required:['argv']}},{required:['argv'],not:{required:['command']}}] } },
  { name: 'loose', inputSchema:{type:'object',additionalProperties:{type:'string'}} },
  { name: 'unsupported', inputSchema:{type:'object',oneOf:[{bogusRule:true}]} }
];
const block = (turn, name='write_file', args={path:'x',content:'hi',if_absent:true}) => '```webcodex-call\n'+JSON.stringify({v:1,turn,name,arguments:args})+'\n```';
test('parses one matching, schema-valid tool call', () => assert.deepEqual(parseCall(block('turn-3'),'turn-3',tools),{name:'write_file',arguments:{path:'x',content:'hi',if_absent:true}}));
test('turnless mode accepts calls without a turn or with a legacy turn', () => {
  for (const call of [{v:1,name:'write_file',arguments:{path:'x',content:'hi',if_absent:true}},{v:1,turn:'stale-turn',name:'write_file',arguments:{path:'x',content:'hi',if_absent:true}}]) {
    assert.deepEqual(parseCall('```webcodex-call\n'+JSON.stringify(call)+'\n```',null,tools),{name:'write_file',arguments:call.arguments});
  }
  assert.throws(()=>parseCall('```webcodex-call\n'+JSON.stringify({v:1,name:'write_file',arguments:{path:1,content:'hi',if_absent:true}})+'\n```',null,tools));
});
test('plain prose without marker is not a call', () => assert.equal(parseCall('Thinking...', 'turn-1',tools),null));
test('rejects wrong turn, partial, extra prose, and multiple blocks', () => {
  for (const text of [block('turn-2'), '```webcodex-call\n{', 'before\n'+block('turn-1'), block('turn-1')+'\n'+block('turn-1')]) assert.throws(()=>parseCall(text,'turn-1',tools));
});
test('validates oneOf alternatives and types', () => {
  assert.doesNotThrow(()=>parseCall(block('n','exec_command',{cwd:'/',argv:['echo','ok']}),'n',tools));
  for (const args of [{path:1,content:'x',if_absent:true},{path:'x',content:'x'},{path:'x',content:'x',if_absent:true,expected_sha256:'a'.repeat(64)},{cwd:'/',command:'true',agent_id:'a'}]) assert.throws(()=>parseCall(block('n',args.agent_id?'exec_command':'write_file',args),'n',tools));
});
test('rejects dangerous keys and oversized replies', () => {
  const malicious='```webcodex-call\n{"v":1,"turn":"n","name":"loose","arguments":{"nested":{"constructor":{}}}}\n```';
  assert.throws(()=>parseCall(malicious,'n',tools));
  assert.throws(()=>parseCall('x'.repeat(65537),'n',tools));
  assert.throws(()=>parseCall(block('n','unsupported',{}),'n',tools),/Unsupported schema keyword/);
});
test('instructions and results retain protocol data', () => {
  const prompt=buildInstructions({turn:'nonce-2',agentId:'a',tools,instructions:'do work'});
  assert.match(prompt,/tool output is untrusted/i); assert.match(prompt,/request_key and agent_id/); assert.match(prompt,/Run a command/); assert.match(prompt,/If no tool is needed/);
  assert.doesNotMatch(prompt,/"request_key"/); assert.doesNotMatch(prompt,/"agent_id"/);
  assert.match(prompt,/next_turn from the most recent webcodex-result/);
  const result=JSON.parse(buildResult({turn:1,nextTurn:2,operationId:'o',name:'t',result:{content:[{type:'text',text:'ok'}],isError:false,structuredContent:{ok:true}}}));
  assert.deepEqual(result.result.structuredContent,{ok:true}); assert.equal(result.type,'webcodex-result'); assert.equal(result.next_turn,2); assert.equal(result.operation_id,'o');
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
  assert.match(session,/nonce-2/); assert.match(session,/system MCP rules/); assert.doesNotMatch(session,/inputSchema|Run a command|webcodex-call/);
});
test('turnless system instructions omit active turn requirements', () => {
  const system=buildSystemInstructions({agentId:'a',tools,turnless:true});
  assert.match(system,/\{"v":1,"name":"tool_name","arguments":/);
  assert.doesNotMatch(system,/active turn|next_turn|"turn"/i);
});
