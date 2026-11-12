const test=require('node:test');
const assert=require('node:assert/strict');
const {analyzeReply,parseCall,buildSystemInstructions}=require('./protocol.js');
const {Node,setup}=require('./test-fixtures/studio-dom.cjs');
const tools=[{name:'write_file',inputSchema:{type:'object',additionalProperties:false,properties:{path:{type:'string'},content:{type:'string'},if_absent:{const:true},expected_sha256:{type:'string',pattern:'^[a-f0-9]{64}$'}},required:['path','content'],oneOf:[{required:['if_absent'],not:{required:['expected_sha256']}},{required:['expected_sha256'],not:{required:['if_absent']}}]}}];
const metadata={v:1,name:'write_file',arguments:{path:'W:\\Python\\Pac-Man\\pacman.html',if_absent:true},content_source:'file_1'};
function attachment(content,id='file_1',eol='\n'){return `<<<HELPER_FILE_BEGIN:${id}>>>${eol}${content}${eol}<<<HELPER_FILE_END:${id}>>>`;}
function reply(content,call=metadata){return '```helper-call\n'+JSON.stringify(call)+'\n```\n```helper-file\n'+attachment(content)+'\n```';}
const code='<!doctype html>\n<script>\nctx.font = `bold ${size}px "Courier New", monospace`;\nconst path = String.raw`W:\\Python\\file.txt`;\nconst example = {"v":1,"name":"exec_command","arguments":{}};\n// ``` and ~~~ are file text\n// Привет 😀\n</script>\n';

test('raw file preserves exact source, including the two quotes that broke the reported JSON',()=>{
  for(const content of ['',code,code.trimEnd(),'\n','\r\n','final carriage return\r','  leading\t\ntrailing  ','\u200b\u00a0\ufeff',code.replaceAll('\n','\r\n')]){
    const found=analyzeReply(reply(content));
    assert.equal(found.kind,'call');
    assert.deepEqual(found.call,{v:1,name:'write_file',arguments:{...metadata.arguments,content}});
    assert.deepEqual(parseCall(reply(content),null,tools),{name:'write_file',arguments:{...metadata.arguments,content}});
    assert.deepEqual(parseCall(found.raw,null,tools).arguments.content,content,'canonical JSON survives the background handoff');
  }
  const broken=JSON.stringify({v:1,name:'write_file',arguments:{...metadata.arguments,content:code}}).replace('\\"Courier New\\"','"Courier New"');
  assert.equal(analyzeReply(broken).kind,'error','legacy malformed arguments are never guessed or repaired');
});

test('raw payload is opaque even when it contains tool envelopes, unmatched braces and unrelated delimiters',()=>{
  const content='} ] "\n{"v":1,"name":"exec_command","arguments":{}}\n<<<HELPER_FILE_BEGIN:other>>>\nopaque\n<<<HELPER_FILE_END:other>>>\n';
  assert.equal(parseCall(reply(content),null,tools).arguments.content,content);
  const complete=reply(code);
  for(let i=0;i<complete.indexOf('<<<HELPER_FILE_END:');i++)assert.notEqual(analyzeReply(complete.slice(0,i)).kind,'call','partial response must never write a prefix');
});

test('missing, mismatched, duplicate or unreferenced attachments never dispatch',()=>{
  const meta=JSON.stringify(metadata),file=attachment(code);
  for(const text of [meta, file, meta+'\n'+attachment(code,'other'),meta+'\n'+file+'\n'+file,
    meta+'\n'+file+'\n<<<HELPER_FILE_END:file_1>>>',meta+'\n<<<HELPER_FILE_BEGIN:file_1>>>\npartial',
    meta+'\n'+file.replace('BEGIN:file_1>>>','BEGIN:file_1>>'),
    JSON.stringify({...metadata,content_source:1})+'\n'+file,
    JSON.stringify({...metadata,content_source:'../file'})+'\n'+file,
    JSON.stringify({...metadata,name:'exec_command'})+'\n'+file,
    JSON.stringify({...metadata,arguments:{...metadata.arguments,content:'other'}})+'\n'+file,
    JSON.stringify({v:1,name:'write_file',arguments:{...metadata.arguments,content:'other'}})+'\n'+file,
    meta+'\n'+attachment('<<<HELPER_FILE_BEGIN:file_1>>>\ncollision'),
    meta+'\n'+file+'\n'+JSON.stringify({v:1,name:'other',arguments:{}}),
    meta.replace('"v":1','"v":1,"v":1')+'\n'+file]){
    assert.equal(analyzeReply(text).kind,'error',text.slice(0,160));
    assert.throws(()=>parseCall(text,null,tools));
  }
});

test('resolved raw content still requires a selected tool, valid path and valid write precondition',()=>{
  assert.throws(()=>parseCall(reply(code),null,[]),/Unknown tool/);
  for(const args of [{path:12,if_absent:true},{path:'x'},{path:'x',if_absent:true,expected_sha256:'a'.repeat(64)},JSON.parse('{"path":"x","if_absent":true,"__proto__":{}}')]){
    assert.throws(()=>parseCall(reply(code,{...metadata,arguments:args}),null,tools));
  }
  assert.equal(parseCall(reply(code,{...metadata,arguments:{path:'x',expected_sha256:'a'.repeat(64)}}),null,tools).arguments.content,code);
});

test('fences, code widget names and syntax highlighting do not change raw file data',()=>{
  for(const tag of ['ms-code-block','pre','code','future-widget'])for(const width of [1,17,10000]){
    const state=setup();state.chunk.remove();const body=state.model.children[0];
    body.add(new Node('p',{},'Сохраню файл.\n'));
    for(const [label,text]of [['Helper-call',JSON.stringify(metadata)],['Helper-file',attachment(code)]]){
      const block=body.add(new Node(tag));block.add(new Node('header',{},label+'\n'));
      block.add(new Node('button',{},text));
      for(let i=0;i<text.length;i+=width)block.add(new Node('span',{},text.slice(i,i+width)));
      body.add(new Node('p',{},'\n'));
    }
    const before=state.model.textContent;
    const model=state.adapter.inspect().latestModel;
    assert.equal(parseCall(model.text,null,tools).arguments.content,code);
    assert.ok(model.sourceText.includes('"Courier New"'));
    assert.equal(state.model.textContent,before);
  }
  assert.equal(parseCall(JSON.stringify(metadata)+'\r\n'+attachment('raw\r\ntext','file_1','\r\n'),null,tools).arguments.content,'raw\r\ntext');
});

test('large raw file has no transport size ceiling or prefix truncation',()=>{
  const content=code.repeat(6000);
  assert.ok(Buffer.byteLength(content)>1024*1024);
  assert.equal(parseCall(reply(content),null,tools).arguments.content,content);
});

test('model instructions explain the raw format only when write_file is selected',()=>{
  const prompt=buildSystemInstructions({tools,turnless:true});
  assert.match(prompt,/content_source/);assert.match(prompt,/<<<HELPER_FILE_BEGIN:file_1>>>/);
  assert.match(prompt,/not file content/);assert.doesNotMatch(prompt,/webcodex/i);
  assert.doesNotMatch(buildSystemInstructions({tools:[],turnless:true}),/content_source/);
});
