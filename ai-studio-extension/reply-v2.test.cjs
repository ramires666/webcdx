const test=require('node:test');
const assert=require('node:assert/strict');
const {parseCall,buildSystemInstructions}=require('./protocol.js');
const {Node,Textarea,setup}=require('./test-fixtures/studio-dom.cjs');
const marker='0123456789abcdef0123456789abcdef';
const tools=[
  {name:'write_file',inputSchema:{type:'object',properties:{path:{type:'string'},content:{type:'string'},if_absent:{type:'boolean',const:true}},required:['path','content','if_absent'],additionalProperties:false}},
  {name:'exec_command',inputSchema:{type:'object',properties:{cwd:{type:'string'},command:{type:'string'},argv:{type:'array',items:{type:'string'}},stdin:{type:'string'}},required:['cwd'],oneOf:[{required:['command'],not:{required:['argv']}},{required:['argv'],not:{required:['command']}}],additionalProperties:false}},
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

test('large Python source stays exact in raw stdin while argv stays compact',()=>{
  const source=('title = "quotes \' and \\\\ paths"\n```mermaid\nA --> B\n```\n').repeat(1800);
  assert.ok(source.length>32768);
  const text=frame('exec_command\ncwd: W:/a\nargv: ["python", "-"]',source);
  assert.deepEqual(parseCall(text,null,tools),{name:'exec_command',arguments:{cwd:'W:/a',argv:['python','-'],stdin:source}});
  assert.deepEqual(parseCall(frame('exec_command\ncwd: W:/a\nargv: ["python", "-c", "print(1)"]'),null,tools),{name:'exec_command',arguments:{cwd:'W:/a',argv:['python','-c','print(1)']}});
  assert.throws(()=>parseCall(frame('exec_command\ncwd: W:/a\nargv: ["python", "-"]\nstdin: header value','raw body'),null,tools),/duplicate|Ambiguous/i);
  const noStdinCapability=[{...tools[0]}, {...tools[1],inputSchema:{...tools[1].inputSchema,properties:{cwd:{type:'string'},argv:{type:'array',items:{type:'string'}}},oneOf:[{required:['argv']}]}},tools[2]];
  assert.throws(()=>parseCall(text,null,noStdinCapability),/Unexpected|schema/i);
});

test('identical complete frames execute once and preserve nesting within raw bodies',()=>{
  const text=frame('write_file\npath: W:/a\nif_absent: true','{not JSON}\n'+frame('list_allowed_roots'));
  const state=setup();state.chunk.remove();const pre=state.model.children[0].add(new Node('pre',{},text));
  let model=state.adapter.inspect().latestModel;
  assert.equal(parseCall(model.text,null,tools,model).arguments.content,'{not JSON}\n'+frame('list_allowed_roots'));
  state.model.children[0].add(new Node('pre',{},text));model=state.adapter.inspect().latestModel;
  assert.equal(parseCall(model.text,null,tools,model).arguments.content,'{not JSON}\n'+frame('list_allowed_roots'));
  state.model.children[0].add(new Node('pre',{},text.replace('W:/a','W:/different')));
  model=state.adapter.inspect().latestModel;
  assert.throws(()=>parseCall(model.text,null,tools,model),/Ambiguous/);
  pre.remove();
});

test('foreign markers, ambiguous parameters and missing raw bytes remain local errors',()=>{
  const valid=frame('write_file\npath: W:/a\nif_absent: true','data');
  for(const text of [valid.replaceAll(marker,'b'.repeat(32)),valid.replace('path: W:/a','path: W:/a\npath: W:/b'),valid.replace('if_absent: true','if_absent: yes'),valid.replace('\nEND_'+marker,''),frame('exec_command\ncwd: W:/a\nargv: ["echo"]\nstdin: duplicate','echo bad'),frame('write_file\narguments: {"path":"W:/a","content":"duplicate","if_absent":true}','body')]){
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

test('templates use a concrete tilde fence that preserves nested Markdown documents',()=>{
  const system=buildSystemInstructions({tools,turnless:true,replyFormat:'v2'});
  assert.equal((system.match(/^~~~~~~~~~~~~~~~~text$/gm)||[]).length,tools.length);
  assert.doesNotMatch(system,/^```text$/m);
  const body='# План\n```mermaid\nflowchart LR\n A --> B\n```\n~~~js\nconsole.log("😀");\n~~~\n';
  const raw='Пишу документ.\n\n~~~~~~~~~~~~~~~~text\n'+frame('write_file\npath: W:/plan.md\nif_absent: true',body)+'\n~~~~~~~~~~~~~~~~';
  assert.equal(parseCall(raw,null,tools).arguments.content,body);
  assert.match(system,/returned next offsets/);
  assert.match(system,/write it once with write_file/);
});

test('exact edit source recovers broken outer fences without changing raw bytes',()=>{
  for(const newline of ['\n','\r\n']){
    const body='$content = @\'\n# План 😀\n```mermaid\nflowchart LR\nA --> B\n```\n*literal* \\path\n\'@'.replaceAll('\n',newline);
    const raw=('Пишу.\n\n```text\n'+frame('exec_command\ncwd: W:/project',body)+'\n```\nГотово.').replaceAll(/(?<!\r)\n/g,newline);
    assert.throws(()=>parseCall(raw,null,tools),/missing end/);
    const call=parseCall(raw,null,tools,{sourceBlocks:[{text:raw,kind:'reply'}]});
    assert.equal(call.arguments.command,body);
    assert.throws(()=>parseCall(raw.replace('\nEND_'+marker,''),null,tools,{sourceBlocks:[{text:raw.replace(/\r?\nEND_[a-f0-9]+/,''),kind:'reply'}]}),/missing end/);
  }
  const two='```text\n'+frame('list_allowed_roots')+'\n```\n```text\n'+frame('list_allowed_roots')+'\n```';
  assert.throws(()=>parseCall(two,null,tools,{sourceBlocks:[{text:two,kind:'reply'}]}),/Ambiguous/);
});

test('local edit recovery reads the original reply once and invalidates it when the answer changes',async()=>{
  const state=setup(),role=state.model.children[0];state.chunk.remove();
  const body='# Документ\n```js\nconst x = "😀";\n```\n';
  const raw='Пишу.\n\n```text\n'+frame('write_file\npath: W:/plan.md\nif_absent: true',body)+'\n```';
  const source=role.add(new Node('pre',{},raw.slice(raw.indexOf('HELPER_'),raw.indexOf('```js'))));
  const edit=state.model.add(new Node('button',{'aria-label':'Edit'}));
  let opened=0,input;
  edit.click=()=>{
    opened++;edit.attrs['aria-label']='Stop editing';input=role.add(new Textarea(raw));
    edit.click=()=>{input.remove();edit.attrs['aria-label']='Edit';};
  };
  const before=state.adapter.inspect().latestModel;
  assert.throws(()=>parseCall(before.text,null,tools,before),/missing end/);
  assert.equal(await state.adapter.recoverReplySource(before),true);
  const recovered=state.adapter.inspect().latestModel;
  assert.equal(recovered.sourceRevision,before.sourceRevision);
  assert.equal(parseCall(recovered.text,null,tools,recovered).arguments.content,body);
  assert.equal(input.value,raw);
  assert.equal(state.adapter.inspect().supported,true);
  assert.equal(await state.adapter.recoverReplySource(recovered),true);
  assert.equal(opened,1);
  source.textContent=frame('list_allowed_roots');
  assert.equal(state.adapter.inspect().latestModel.sourceBlocks[0].kind,'code');
});


test('re-emitted raw frames are accepted only when every source byte matches',()=>{
  const raw=frame('write_file\npath: W:/a\nif_absent: true','print("once")\n');
  const sources=Array.from({length:4},(_,id)=>({id,kind:'code',text:raw}));
  const options={sessionMarker:marker,sourceBlocks:sources,sourceRemainder:'I will re-emit the same call cleanly.'};
  assert.equal(parseCall(sources.map(source=>source.text).join('\n'),null,tools,options).arguments.content,'print("once")\n');
  for(const changed of [raw.replace('once','twice'),raw.replace(marker,'f'.repeat(32)),raw+' ',raw.replace('END_'+marker,'')]){
    assert.throws(()=>parseCall(raw,null,tools,{...options,sourceBlocks:[sources[0],{id:9,kind:'code',text:changed}]}));
  }
});


test('explicit last-call recovery selects the final complete frame while automatic parsing rejects alternatives',()=>{
  const first=frame('write_file\npath: W:/draft\nif_absent: true','draft');
  const last=frame('write_file\npath: W:/final\nif_absent: true','final');
  const options={sessionMarker:marker,sourceBlocks:[{kind:'code',text:first},{kind:'code',text:last}]};
  assert.throws(()=>parseCall(first+'\n'+last,null,tools,options),/Ambiguous/);
  assert.equal(parseCall(first+'\n'+last,null,tools,{...options,lastFrame:true}).arguments.path,'W:/final');
  for(const text of [last.replace(marker,'f'.repeat(32)),last.replace('END_'+marker,''),last.replace('if_absent: true','unexpected: true')]){
    assert.throws(()=>parseCall(last,null,tools,{...options,lastFrame:true,sourceBlocks:[options.sourceBlocks[0],{kind:'code',text}]}));
  }
});


test('explicit recovery reads the final header-only call when a malformed fence merged two code widgets',()=>{
  const first=frame('exec_command\ncwd: W:/a\nargv: ["python", "-c", "print(1)"]');
  const last=frame('exec_command\ncwd: W:/a\nargv: ["python", "-c", "print(2)"]');
  const merged=first+'\n~~~~~~~~~~~~~~~~I will read the next part.\n~~~~~~~~~~~~~~~~text\n'+last;
  const options={sessionMarker:marker,sourceBlocks:[{kind:'code',text:merged}]};
  assert.throws(()=>parseCall(merged,null,tools,options));
  assert.deepEqual(parseCall(merged,null,tools,{...options,lastFrame:true}).arguments.argv,['python','-c','print(2)']);
  const raw=frame('write_file\npath: W:/a\nif_absent: true','file text\n'+last);
  assert.equal(parseCall(raw,null,tools,{sessionMarker:marker,lastFrame:true,sourceBlocks:[{kind:'code',text:raw}]}).arguments.content,'file text\n'+last);
});
