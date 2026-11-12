const test=require('node:test');
const assert=require('node:assert/strict');
const {parseCall,analyzeReply,buildSystemInstructions}=require('./protocol.js');
const tools=[{name:'list_directory',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path'],additionalProperties:false}},{name:'exec_command',inputSchema:{type:'object',properties:{cwd:{type:'string'},command:{type:'string'}},required:['cwd','command'],additionalProperties:false}}];
const wrap=raw=>'Просмотрим папки. ```helper-call\n'+raw+'\nCode';
test('the reported inline Markdown Windows path keeps its backslash',()=>{
  const raw=String.raw`{"v":1,"name":"list_directory","arguments":{"path":"W:\algo"}}`;
  assert.equal(parseCall(wrap(raw),null,tools).arguments.path,String.raw`W:\algo`);
});
test('Windows path notation covers drive roots, control-letter folders, Unicode and UNC',()=>{
  for(const path of ['W:\\',String.raw`W:\temp\new\file.txt`,String.raw`W:\algo`,String.raw`C:\Users\admin\работа😀`,String.raw`\\server\share\folder`]){
    const literal='{"v":1,"name":"list_directory","arguments":{"path":"'+path+'"}}';
    assert.equal(parseCall(wrap(literal),null,tools).arguments.path,path);
    assert.equal(parseCall(JSON.stringify({v:1,name:'list_directory',arguments:{path}}),null,tools).arguments.path,path);
  }
  const unicode=String.raw`{"v":1,"name":"list_directory","arguments":{"path":"W:\u005calgo"}}`;
  assert.equal(parseCall(unicode,null,tools).arguments.path,'W:\\algo','valid explicit JSON Unicode escapes remain authoritative');
});
test('jsonrepair restores punctuation and JSON-like notation without rewriting string data',()=>{
  const expected={name:'list_directory',arguments:{path:'W:/algo'}};
  for(const raw of ['{"v":1 "name":"list_directory","arguments":{"path":"W:/algo"}}','{"v":1,"name" "list_directory","arguments":{"path":"W:/algo"}}','{v:1,name:"list_directory",arguments:{path:"W:/algo",},}','{\'v\':1,\'name\':\'list_directory\',\'arguments\':{\'path\':\'W:/algo\'}}','{"v":1,/* comment */"name":"list_directory","arguments":{"path":"W:/algo",},}']){
    assert.deepEqual(parseCall(raw,null,tools),expected,raw);
  }
});
test('recovery never removes unknown command escapes or changes a valid command',()=>{
  const broken=String.raw`{"v":1,"name":"exec_command","arguments":{"cwd":"W:/algo","command":"echo W:\algo"}}`;
  assert.equal(analyzeReply(broken).kind,'error');
  const arguments={cwd:String.raw`W:\temp\new`,command:String.raw`echo W:\temp\new`};
  assert.deepEqual(parseCall(JSON.stringify({v:1,name:'exec_command',arguments}),null,tools),{name:'exec_command',arguments});
});
test('library repair cannot dispatch incomplete, duplicate or schema-invalid calls',()=>{
  for(const raw of ['{"v":1,"name":"list_directory","arguments":{"path":"W:/algo"}','{"v":1,"name":"list_directory","arguments":{"path":"W:/algo","path":"W:/other",}}','{v:1,name:"list_directory",arguments:{path:123,},}','{v:1,name:"list_directory",arguments:{path:"W:/algo",request_key:"fake"}}'])assert.throws(()=>parseCall(raw,null,tools),raw);
});
test('model instructions avoid backslash-dependent Windows paths',()=>{
  assert.match(buildSystemInstructions({tools,turnless:true}),/Windows paths using forward slashes/);
});
