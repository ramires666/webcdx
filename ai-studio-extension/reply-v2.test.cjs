const test=require('node:test');
const assert=require('node:assert/strict');
const {parseCall}=require('./protocol.js');
const {Node,setup}=require('./test-fixtures/studio-dom.cjs');
const marker='0123456789abcdef0123456789abcdef';
const tools=[
  {name:'write_file',inputSchema:{type:'object',properties:{path:{type:'string'},content:{type:'string'},if_absent:{type:'boolean',const:true}},required:['path','content','if_absent'],additionalProperties:false}},
  {name:'exec_command',inputSchema:{type:'object',properties:{cwd:{type:'string'},command:{type:'string'},argv:{type:'array',items:{type:'string'}}},required:['cwd'],oneOf:[{required:['command'],not:{required:['argv']}},{required:['argv'],not:{required:['command']}}],additionalProperties:false}},
  {name:'list_allowed_roots',inputSchema:{type:'object',properties:{},additionalProperties:false}}
];
const frame=(header,body)=>'HELPER_'+marker+' '+header+(body===undefined?'':'\n---\n'+body)+'\nEND_'+marker;

test('text frames preserve raw data and all newline endings exactly',()=>{
  for(const body of ['', ' ', '\n', '\r\n', 'code "quoted" \\n\r\n', 'END_'+marker+'\nHELPER_'+marker+' write_file\n{}', '```text\n'+frame('list_allowed_roots')+'\n```']){
    const text=frame('write_file\npath: W:\\folder: name\\a.txt\nif_absent: TRUE',body);
    const parsed=parseCall(text,null,tools,{sessionMarker:marker});
    assert.deepEqual(parsed,{name:'write_file',arguments:{path:'W:\\folder: name\\a.txt',if_absent:true,content:body}});
    assert.deepEqual(parseCall('Prose.\n\n````text\n'+text+'\n````\nDone.',null,tools),parsed);
  }
});

test('CRLF framing and structured argv remain exact',()=>{
  const text=frame('exec_command\ncwd: W:/a\nargv: ["cmd.exe", "/c", "echo a:b"]');
  assert.deepEqual(parseCall(text.replaceAll('\n','\r\n'),null,tools),{name:'exec_command',arguments:{cwd:'W:/a',argv:['cmd.exe','/c','echo a:b']}});
});

test('independent source boundaries reject two equal calls and preserve nesting within raw bodies',()=>{
  const text=frame('write_file\npath: W:/a\nif_absent: true','{not JSON}\n'+frame('list_allowed_roots'));
  const state=setup();state.chunk.remove();const pre=state.model.children[0].add(new Node('pre',{},text));
  let model=state.adapter.inspect().latestModel;
  assert.equal(parseCall(model.text,null,tools,model).arguments.content,'{not JSON}\n'+frame('list_allowed_roots'));
  state.model.children[0].add(new Node('pre',{},text));model=state.adapter.inspect().latestModel;
  assert.throws(()=>parseCall(model.text,null,tools,model),/Ambiguous/);
  pre.remove();
});

test('foreign markers, ambiguous parameters and missing raw bytes remain local errors',()=>{
  const valid=frame('write_file\npath: W:/a\nif_absent: true','data');
  for(const text of [valid.replaceAll(marker,'b'.repeat(32)),valid.replace('path: W:/a','path: W:/a\npath: W:/b'),valid.replace('if_absent: true','if_absent: yes'),valid.replace('\nEND_'+marker,''),frame('exec_command\ncwd: W:/a\nargv: ["echo"]','echo bad'),frame('write_file\narguments: {"path":"W:/a","content":"duplicate","if_absent":true}','body')]){
    assert.throws(()=>parseCall(text,null,tools,{sessionMarker:marker}));
  }
  const unmarked=valid.replaceAll('_'+marker,'');
  assert.throws(()=>parseCall(unmarked,null,tools,{sessionMarker:marker}));
  assert.equal(parseCall(unmarked,null,tools,{sessionMarker:marker,allowUnmarked:true}).arguments.content,'data');
});

test('a complete code source can recover a missing end only for a bodyless frame',()=>{
  const text='HELPER_'+marker+' list_allowed_roots';
  assert.throws(()=>parseCall(text+'\n',null,tools));
  assert.deepEqual(parseCall(text+'\n',null,tools,{sourceBlocks:[{text:text+'\n',kind:'code'}]}),{name:'list_allowed_roots',arguments:{}});
});

test('structured repair rejects duplicate and dangerous keys before merging arguments',()=>{
  for(const arguments of ["{'cwd':'a','cwd':'b','argv':['echo']}",'{"__proto__":{"cwd":"a"},"argv":["echo"]}','{"cwd":"a","argv":["echo"],"constructor":{}}']){
    assert.throws(()=>parseCall(frame('exec_command\narguments: '+arguments),null,tools));
  }
  assert.equal({}.cwd,undefined);
});

test('large raw bodies bypass metadata repair limits without truncation',()=>{
  const content='😀 "quoted" \\n\r\n'.repeat(150000);
  assert.equal(parseCall(frame('write_file\npath: W:/a\nif_absent: true',content),null,tools).arguments.content,content);
});

test('extension-owned fields apply at the argument boundary while nested data keeps its schema',()=>{
  const selected=[{name:'nested',inputSchema:{type:'object',properties:{request_key:{type:'string'},data:{type:'object',properties:{delivery:{type:'string'},request_key:{type:'string'}},required:['delivery','request_key'],additionalProperties:false}},required:['request_key','data'],additionalProperties:false}}];
  const args={data:{delivery:'ordinary data',request_key:'nested value'}};
  assert.deepEqual(parseCall(frame('nested\narguments: '+JSON.stringify(args)),null,selected).arguments,args);
  assert.throws(()=>parseCall(frame('nested\narguments: '+JSON.stringify({...args,request_key:'injected'})),null,selected));
});
