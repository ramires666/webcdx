const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {pathToFileURL}=require('node:url');
const {spawn}=require('node:child_process');

test('real Chrome DOM preserves raw attachments across rendered code widget boundaries',{
  skip:process.env.WEBCODEX_BROWSER_REAL_TEST!=='1'||process.platform!=='win32',timeout:45000
},async t=>{
  const parent=path.resolve(__dirname,'..','tmp');fs.mkdirSync(parent,{recursive:true});
  const temp=fs.mkdtempSync(path.join(parent,'real-studio-dom-'));
  const executable=process.env.WEBCODEX_TEST_CHROME_PATH||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
  assert.ok(fs.existsSync(executable),'installed Chrome is required for this explicit browser test');
  const fixture=path.join(temp,'fixture.html');
  const scripts=['protocol.js','studio-adapter.js'].map(name=>'<script src="'+pathToFileURL(path.join(__dirname,name)).href+'"></script>').join('');
  fs.writeFileSync(fixture,`<!doctype html><meta charset="utf-8"><title>Helper DOM regression</title>
<style>.widget {display:grid} .language {display:block} pre {white-space:pre-wrap}</style>
<ms-prompt-input-wrapper><textarea></textarea></ms-prompt-input-wrapper>
<ms-chat-turn data-turn-role="User">Save this file</ms-chat-turn>
<ms-chat-turn data-turn-role="Model" id="answer"></ms-chat-turn>
<button aria-label="Run">Run</button><output id="check"></output>${scripts}<script>
try {
  const answer=document.getElementById('answer');
  const tools=[{name:'write_file',inputSchema:{type:'object',properties:{path:{type:'string'},content:{type:'string'},if_absent:{const:true}},required:['path','content','if_absent'],additionalProperties:false}}];
  const metadata=JSON.stringify({v:1,name:'write_file',arguments:{path:'example.html',if_absent:true},content_source:'file_1'});
  const content='<!doctype html>\\n<p title="quotes">Привет 😀</p>\\nconst path = "W:\\\\Python";\\n';
  const attachment='<<<HELPER_FILE_BEGIN:file_1>>>\\n'+content+'\\n<<<HELPER_FILE_END:file_1>>>';
  const adapter=WebCodexStudio.createAdapter(document,{hostname:'aistudio.google.com'});
  let cases=0;
  for(const widgetTag of ['ms-code-block','article','future-widget'])
  for(const headerTag of ['header','div','span'])
  for(const codeTag of ['pre','code','div'])
  for(const br of [false,true]) {
    answer.replaceChildren();
    for(const [label,value]of [['Helper-call',metadata],['Helper-file',attachment]]) {
      const widget=document.createElement(widgetTag);widget.className='widget';
      const header=document.createElement(headerTag);header.className='language';header.textContent=label;widget.append(header);
      const copy=document.createElement('button');copy.textContent='Copy '+value;widget.append(copy);
      const source=document.createElement(codeTag);
      if(br)value.split('\\n').forEach((line,i)=>{if(i)source.append(document.createElement('br'));const token=document.createElement('span');token.textContent=line;source.append(token);});
      else source.textContent=value;
      widget.append(source);answer.append(widget);
    }
    if(cases===0&&WebCodexProtocol.analyzeReply(answer.textContent).error!=='Ambiguous or damaged Helper file delimiters')throw Error('fixture must reproduce the reported flattened-DOM error');
    const before=answer.innerHTML,snapshot=adapter.inspect();
    if(!snapshot.supported)throw Error(snapshot.error);
    const call=WebCodexProtocol.parseCall(snapshot.latestModel.text,null,tools);
    if(call?.arguments.content!==content||answer.innerHTML!==before)throw Error('source mismatch '+JSON.stringify({widgetTag,headerTag,codeTag,br}));
    cases++;
  }
  document.getElementById('check').textContent='PASS:'+cases;
} catch(error) {document.getElementById('check').textContent='FAIL:'+error.message;}
</script>`,'utf8');
  const child=spawn(executable,['--headless','--disable-gpu','--no-first-run','--no-default-browser-check','--disable-background-networking','--user-data-dir='+path.join(temp,'profile'),'--dump-dom',pathToFileURL(fixture).href],{windowsHide:true,stdio:['ignore','pipe','ignore']});
  t.after(()=>{
    if(child.exitCode===null&&child.signalCode===null)child.kill();
    assert.equal(path.dirname(temp),parent);assert.ok(path.basename(temp).startsWith('real-studio-dom-'));
    fs.rmSync(temp,{recursive:true,force:true,maxRetries:5,retryDelay:100});
  });
  let output='';child.stdout.on('data',chunk=>{output+=chunk;});
  const code=await new Promise((resolve,reject)=>{child.once('error',reject);child.once('exit',resolve);});
  assert.equal(code,0);
  const result=/<output id="check">([^<]*)<\/output>/.exec(output)?.[1];
  assert.equal(result,'PASS:54');t.diagnostic('54 combinations passed in installed Chrome with an isolated temporary profile');
});
