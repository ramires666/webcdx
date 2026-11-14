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
  const scripts=['vendor/json-tools.js','protocol.js','studio-adapter.js'].map(name=>'<script src="'+pathToFileURL(path.join(__dirname,name)).href+'"></script>').join('');
  fs.writeFileSync(fixture,`<!doctype html><meta charset="utf-8"><title>Helper DOM regression</title>
<style>.widget {display:grid} .language {display:block} pre {white-space:pre-wrap}</style>
<ms-prompt-input-wrapper><textarea></textarea></ms-prompt-input-wrapper>
<ms-chat-turn data-turn-role="User">Save this file</ms-chat-turn>
<ms-chat-turn data-turn-role="Model" id="answer"></ms-chat-turn>
<button aria-label="Run">Run</button><output id="check"></output>${scripts}<script>
(async()=>{try {
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
  const user=document.querySelector('ms-chat-turn[data-turn-role="User"]');user.replaceChildren();
  answer.replaceChildren();
  const raw='HELPER_marker write_file\\r\\npath: W:/a.txt\\r\\n---\\r\\n  "😀" \\\\n\\r\\n\\r\\nEND_marker';
  for(let i=0;i<2;i++){
    const pre=document.createElement('pre'),code=document.createElement('code');
    code.textContent=raw;pre.append(code);answer.append(pre);
  }
  const sources=adapter.inspect().latestModel;
  if(sources.sourceBlocks.length!==2||sources.sourceBlocks.some(block=>block.text!==raw)||sources.sourceBlocks[0].id===sources.sourceBlocks[1].id)throw Error('code source boundaries or CRLF changed');
  if(adapter.inspect().latestModel.sourceRevision!==sources.sourceRevision)throw Error('unchanged source revision drifted');cases++;
  const liveMarker='0123456789abcdef0123456789abcdef';
  const userHeader=document.createElement('h3');userHeader.textContent='User 6:40 PM';
  const userChunk=document.createElement('ms-prompt-chunk');userChunk.className='text-chunk';
  const paragraph=document.createElement('p');
  for(const line of ['[Helper session '+liveMarker+']','Session marker for this chat is '+liveMarker+'. Follow the Helper system instructions.','[/Helper session]']){
    if(paragraph.childNodes.length)paragraph.append(document.createElement('br'));
    paragraph.append(document.createTextNode(line));
  }
  userChunk.append(paragraph);user.append(userHeader,userChunk);
  if(adapter.inspect().chatMarkers.join(',')!==liveMarker)throw Error('rendered user header or BR hid the session marker');cases++;
  for(const tag of ['pre','code','div'])for(const br of [false,true])for(const body of ['','line\\n','line\\r\\n']){
    answer.replaceChildren();
    const source=document.createElement(tag);if(tag==='div')source.setAttribute('role','code');
    const value='HELPER_'+liveMarker+' write_file\\npath: W:/test.txt\\nif_absent: TRUE\\n---\\n'+body+'\\nEND_'+liveMarker;
    if(br)value.split('\\n').forEach((line,i)=>{if(i)source.append(document.createElement('br'));const token=document.createElement('span');token.textContent=line;source.append(token);});
    else source.textContent=value;
    answer.append(source);const current=adapter.inspect().latestModel;
    const parsed=WebCodexProtocol.parseCall(current.text,null,tools,{...current,sessionMarker:liveMarker});
    if(parsed.arguments.content!==body)throw Error('v2 data changed '+JSON.stringify({tag,br,body}));cases++;
  }
  const resultWidget=document.createElement('ms-code-block'),resultHeader=document.createElement('header'),resultBody=document.createElement('pre');
  resultHeader.textContent='Helper-result';resultBody.textContent=JSON.stringify({type:'helper-result',operation_id:'op-visible',result:{ok:true}});
  resultWidget.append(resultHeader,resultBody);user.append(resultWidget);
  if(adapter.inspect().latestUser.operationId!=='op-visible')throw Error('result header hid the operation id');cases++;
  answer.replaceChildren();const prose=document.createElement('p');
  prose.textContent='Просмотрим папки. '+String.fromCharCode(96).repeat(3)+'helper-call\\n'+ '{"v":1,"name":"list_directory","arguments":{"path":"W:'+String.fromCharCode(92)+'algo"}}';
  answer.append(prose);const emptyWidget=document.createElement('ms-code-block');emptyWidget.textContent='Code';answer.append(emptyWidget);
  const recovered=WebCodexProtocol.parseCall(adapter.inspect().latestModel.text,null,[{name:'list_directory',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path'],additionalProperties:false}}]);
  if(recovered?.arguments.path!=='W:'+String.fromCharCode(92)+'algo')throw Error('inline Markdown Windows path changed');cases++;
  const prompt=document.querySelector('ms-prompt-input-wrapper'),run=document.querySelector('button[aria-label="Run"]');
  const input=document.createElement('input');input.type='file';prompt.append(input);
  const chip=document.createElement('span');prompt.append(chip);
  input.addEventListener('change',()=>{chip.textContent=input.files[0]?.name||'';run.disabled=true;const progress=document.createElement('div');progress.setAttribute('role','progressbar');prompt.append(progress);setTimeout(()=>{progress.remove();run.disabled=false;},25);});
  const source='native file Привет 😀\\r\\nwith bytes';const file=new File([source],'readme.txt',{type:'text/plain'});
  if(!adapter.stage('small result metadata'))throw Error('file metadata staging failed');
  const upload=await adapter.attachFiles([file],{key:'native-file',isCurrent:()=>true});
  if(!upload.ok||await input.files[0].text()!==source)throw Error('native FileList upload changed content');
  adapter.releaseFiles('native-file');if(input.files.length!==0)throw Error('owned file input was not released');cases++;
  const human=new DataTransfer();human.items.add(new File(['human'],'human.txt'));input.files=human.files;
  const blocked=await adapter.attachFiles([file],{key:'other-file',isCurrent:()=>true});
  if(blocked.ok||input.files[0].name!=='human.txt')throw Error('existing human attachment was replaced');cases++;
  input.value='';chip.textContent='';const humanCard=document.createElement('span');humanCard.className='file-chip';humanCard.textContent='human.pdf';prompt.append(humanCard);
  const protectedCard=await adapter.attachFiles([new File(['new'],'new.txt')],{key:'other-card',isCurrent:()=>true});
  if(protectedCard.ok||!humanCard.isConnected)throw Error('existing uploaded file card was not protected');humanCard.remove();cases++;
  input.remove();chip.textContent='';let dropped;
  prompt.addEventListener('drop',event=>{dropped=event.dataTransfer.files[0];chip.textContent=dropped.name;run.disabled=false;});
  const drop=await adapter.attachFiles([new File([source],'drop.txt',{type:'text/plain'})],{key:'native-drop',isCurrent:()=>true});
  if(!drop.ok||await dropped.text()!==source)throw Error('native drag and drop transfer failed');cases++;
  document.getElementById('check').textContent='PASS:'+cases;
} catch(error) {document.getElementById('check').textContent='FAIL:'+error.message;}
})();
</script>`,'utf8');
  const child=spawn(executable,['--headless','--disable-gpu','--no-first-run','--no-default-browser-check','--disable-background-networking','--virtual-time-budget=3000','--user-data-dir='+path.join(temp,'profile'),'--dump-dom',pathToFileURL(fixture).href],{windowsHide:true,stdio:['ignore','pipe','ignore']});
  t.after(()=>{
    if(child.exitCode===null&&child.signalCode===null)child.kill();
    assert.equal(path.dirname(temp),parent);assert.ok(path.basename(temp).startsWith('real-studio-dom-'));
    fs.rmSync(temp,{recursive:true,force:true,maxRetries:5,retryDelay:100});
  });
  let output='';child.stdout.on('data',chunk=>{output+=chunk;});
  const code=await new Promise((resolve,reject)=>{child.once('error',reject);child.once('exit',resolve);});
  assert.equal(code,0);
  const result=/<output id="check">([^<]*)<\/output>/.exec(output)?.[1];
  assert.equal(result,'PASS:80');t.diagnostic('80 scenarios passed in installed Chrome, including native FileList and drag-and-drop upload');
});
