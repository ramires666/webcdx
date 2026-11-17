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
<style>${fs.readFileSync(path.join(__dirname,'panel.css'),'utf8')} .widget {display:grid} .language {display:block} pre {white-space:pre-wrap}</style>
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
  const currentRun=document.querySelector('button[aria-label="Run"]');
  currentRun.removeAttribute('aria-label');currentRun.textContent='Run Ctrl';currentRun.disabled=true;
  if(adapter.inspect().generating||adapter.inspect().controlsPending)throw Error('completed Run Ctrl answer was considered generating');
  currentRun.setAttribute('aria-label','Run');currentRun.textContent='Run';currentRun.disabled=false;cases++;
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
  const manualEditor=document.querySelector('textarea');manualEditor.value='My next request';
  if(!adapter.prepareSessionMessage(liveMarker,{force:true})||!manualEditor.value.startsWith('[Helper session '+liveMarker+']')||!manualEditor.value.endsWith('My next request'))throw Error('manual marker did not preserve the request in an existing chat');
  const manualDraft=manualEditor.value;
  if(!adapter.prepareSessionMessage(liveMarker,{force:true})||manualEditor.value!==manualDraft)throw Error('manual marker was duplicated');
  manualEditor.value='';cases++;
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
  answer.replaceChildren();
  const ticks=String.fromCharCode(96).repeat(3),recoveryMarker='a'.repeat(32);
  const rawBody='# План 😀\\n'+ticks+'js\\nconst x = "quoted";\\n'+ticks+'\\n';
  const rawReply='Сохраняю.\\n\\n'+ticks+'text\\nHELPER_'+recoveryMarker+' write_file\\npath: W:/plan.md\\nif_absent: true\\n---\\n'+rawBody+'\\nEND_'+recoveryMarker+'\\n'+ticks;
  const brokenCode=document.createElement('pre');brokenCode.textContent=rawReply.slice(rawReply.indexOf('HELPER_'),rawReply.indexOf(ticks+'js'));
  const editReply=document.createElement('button');editReply.setAttribute('aria-label','Edit');answer.append(brokenCode,editReply);
  let edits=0;
  editReply.addEventListener('click',()=>{
    if(editReply.getAttribute('aria-label')==='Edit'){
      edits++;editReply.setAttribute('aria-label','Stop editing');const input=document.createElement('textarea');input.value=rawReply;answer.append(input);
    }else{answer.querySelector('textarea').remove();editReply.setAttribute('aria-label','Edit');}
  });
  const brokenReply=adapter.inspect().latestModel;
  if(!await adapter.recoverReplySource(brokenReply))throw Error('edit source recovery failed');
  const exactReply=adapter.inspect().latestModel;
  if(WebCodexProtocol.parseCall(exactReply.text,null,tools,exactReply).arguments.content!==rawBody||exactReply.sourceRevision!==brokenReply.sourceRevision||answer.querySelector('textarea'))throw Error('edit source bytes or restored UI changed');cases++;
  if(!await adapter.recoverReplySource(exactReply)||edits!==1)throw Error('edit source recovery repeated');cases++;
  brokenCode.textContent='ordinary reply';
  if(adapter.inspect().latestModel.sourceBlocks.some(block=>block.kind==='reply'))throw Error('stale edit source was reused');cases++;
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
  const removeGrounding=document.createElement('button');removeGrounding.setAttribute('aria-label','Remove Grounding with Google Search');prompt.append(removeGrounding);
  const input=document.createElement('input');input.type='file';prompt.append(input);
  const chip=document.createElement('span');prompt.append(chip);
  input.addEventListener('change',()=>{chip.textContent=input.files[0]?.name||'';run.disabled=true;const progress=document.createElement('div');progress.setAttribute('role','progressbar');prompt.append(progress);setTimeout(()=>{progress.remove();run.disabled=false;},25);});
  const source='native file Привет 😀\\r\\nwith bytes';const file=new File([source],'readme.txt',{type:'text/plain'});
  if(!adapter.stage('small result metadata'))throw Error('file metadata staging failed');
  const removeAttachment=document.createElement('button');removeAttachment.setAttribute('aria-label','Remove attachment');prompt.append(removeAttachment);
  if((await adapter.attachFiles([file],{key:'blocked-file',isCurrent:()=>true})).ok||input.files.length)throw Error('existing attachment removal control was ignored');removeAttachment.remove();cases++;
  const upload=await adapter.attachFiles([file],{key:'native-file',isCurrent:()=>true});
  if(!upload.ok||await input.files[0].text()!==source)throw Error('native FileList upload changed content');
  let repeatedUploads=0;input.addEventListener('change',()=>repeatedUploads++);
  if(!adapter.stage('small result metadata')||!(await adapter.attachFiles([file],{key:'native-file',isCurrent:()=>adapter.ownsDraft('small result metadata')})).ok||repeatedUploads)throw Error('resuming an owned upload repeated the transfer or lost the draft');cases++;
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
  adapter.releaseFiles('native-drop');chip.textContent='';
  const copyright=document.createElement('section');copyright.id='copyright-acknowledgement-dialog';copyright.setAttribute('role','dialog');
  const agree=document.createElement('button');agree.textContent='Agree to the copyright acknowledgement';copyright.append(agree);document.body.append(copyright);
  let agreements=0;agree.addEventListener('click',()=>{agreements++;copyright.remove();});
  const unknown=document.createElement('section');unknown.setAttribute('role','dialog');const unknownAgree=agree.cloneNode(true);unknown.append(unknownAgree);document.body.append(unknown);
  let unknownAgreements=0;unknownAgree.addEventListener('click',()=>unknownAgreements++);
  if((await adapter.attachFiles([file],{key:'cancelled-modal',isCurrent:()=>false})).ok||agreements)throw Error('cancelled upload accepted a modal');cases++;
  const acknowledged=await adapter.attachFiles([file],{key:'copyright-file',isCurrent:()=>true});
  if(!acknowledged.ok||agreements!==1||unknownAgreements)throw Error('upload acknowledgement was missed or another dialog was accepted');
  adapter.releaseFiles('copyright-file');unknown.remove();cases++;
  const choices=document.createElement('div');choices.className='choices';choices.style.width='320px';
  for(let i=0;i<14;i++){
    const label=document.createElement('label'),input=document.createElement('input');input.type=i===0?'radio':'checkbox';
    label.append(input,document.createTextNode('Tool '+i+' — detailed function description that must remain visible in the full list.'));choices.append(label);
  }
  document.body.append(choices);
  if(choices.clientHeight<=150||choices.scrollHeight>choices.clientHeight||getComputedStyle(choices).maxHeight!=='none'||choices.querySelector('input').getBoundingClientRect().width>32)throw Error('tool choices were compressed into a nested scroll area');cases++;
  for(const dialogRole of ['group','dialog']){
  const systemDialog=document.createElement('mat-dialog-container');systemDialog.setAttribute('role',dialogRole);systemDialog.setAttribute('aria-label','System instructions');
  const closeSystem=document.createElement('button');closeSystem.setAttribute('aria-label','Close panel');closeSystem.addEventListener('click',()=>systemDialog.remove());
  const systemBody=document.createElement('ms-system-instructions'),systemHeading=document.createElement('h2'),systemEditor=document.createElement('textarea');
  systemHeading.textContent='System instructions';systemEditor.placeholder='Optional tone and style instructions for the model';systemEditor.value='Preserve my rules.';
  systemBody.append(systemHeading,systemEditor);systemDialog.append(closeSystem,systemBody);document.body.append(systemDialog);
  if(!await adapter.stageSystemInstructions('Helper test rules')||systemDialog.isConnected||!systemEditor.value.includes('Preserve my rules.')||!systemEditor.value.includes('Helper test rules'))throw Error('system instructions dialog stayed open or lost user rules');cases++;
  }
  for(const roleButton of [false,true]){
    const opener=document.createElement(roleButton?'div':'button');
    if(roleButton)opener.setAttribute('role','button');
    const icon=document.createElement('mat-icon');icon.textContent='edit_note';
    const caption=document.createElement('span');caption.textContent='System instructions';
    const arrow=document.createElement('span');arrow.className='material-symbols-outlined';arrow.textContent='chevron_right';
    opener.append(icon,caption,arrow);document.body.append(opener);
    const systemDialog=document.createElement('ms-system-instructions'),systemEditor=document.createElement('textarea');
    systemEditor.placeholder='Optional tone and style instructions for the model';systemEditor.value='Keep personal rules.';
    const close=document.createElement('button');close.setAttribute('aria-label','Close');close.addEventListener('click',()=>systemDialog.remove());
    systemDialog.append(systemEditor,close);opener.addEventListener('click',()=>document.body.append(systemDialog));
    const draft=document.querySelector('ms-prompt-input-wrapper textarea').value;
    if(!await adapter.stageSystemInstructions('Helper icon test')||systemDialog.isConnected||!systemEditor.value.includes('Keep personal rules.')||!systemEditor.value.includes('Helper icon test'))throw Error('system instructions opener with icons was not recognized: '+adapter.getSystemStageError());
    if(document.querySelector('ms-prompt-input-wrapper textarea').value!==draft)throw Error('system instruction staging changed the chat draft');
    opener.remove();cases++;
  }
  const restoredUser=document.createElement('ms-chat-turn');restoredUser.setAttribute('data-turn-role','User');
  const fileCard=document.createElement('ms-prompt-chunk');fileCard.className='file-chunk';fileCard.textContent='Generated File [November 14].plain';
  const filePreview=document.createElement('pre');filePreview.textContent=JSON.stringify({type:'helper-result',operation_id:'file-preview'});fileCard.append(filePreview);
  const textChunk=document.createElement('ms-prompt-chunk');textChunk.className='text-chunk';textChunk.textContent='[Helper session '+liveMarker+']\\nSession marker\\n[/Helper session]\\n';
  const resultCode=document.createElement('ms-code-block'),label=document.createElement('header'),resultSource=document.createElement('pre');
  const expectedResult=JSON.stringify({type:'helper-result',operation_id:'restored-result',result:{ok:true}});
  label.textContent='Helper-result';resultSource.textContent=expectedResult;resultCode.append(label,resultSource);textChunk.append(resultCode);restoredUser.append(fileCard,textChunk);document.body.append(restoredUser);
  if(adapter.inspect().latestUser.operationId!=='restored-result'||!adapter.hasDelivery('restored-result',expectedResult)||adapter.hasDelivery('file-preview'))throw Error('file metadata or session context hid the delivered result');cases++;
  textChunk.append(resultCode.cloneNode(true));
  if(adapter.inspect().latestUser.operationId||adapter.hasDelivery('restored-result',expectedResult))throw Error('duplicate user control sources were accepted');cases++;
  restoredUser.remove();
  const collapsedUser=document.createElement('ms-chat-turn');collapsedUser.setAttribute('data-turn-role','User');
  const collapsedWidget=document.createElement('ms-code-block'),nativeButton=document.createElement('button'),collapsedRegion=document.createElement('div');
  const collapsedExpected=JSON.stringify({type:'helper-result',operation_id:'collapsed-result',result:{ok:true}});
  nativeButton.setAttribute('aria-label','Collapse code snippet');
  const collapsedSource=document.createElement('pre');collapsedSource.textContent=collapsedExpected;collapsedRegion.append(collapsedSource);
  const unrelatedHidden=document.createElement('span');unrelatedHidden.style.display='none';unrelatedHidden.textContent='unrelated hidden text';
  collapsedWidget.append(nativeButton,collapsedRegion,unrelatedHidden);collapsedUser.append(collapsedWidget);document.body.append(collapsedUser);
  let nativeClicks=0;nativeButton.addEventListener('click',()=>{nativeClicks++;if(nativeButton.getAttribute('aria-label')==='Collapse code snippet'){nativeButton.setAttribute('aria-label','Expand code snippet');collapsedRegion.setAttribute('aria-hidden','true');collapsedRegion.style.display='none';}else{nativeButton.setAttribute('aria-label','Collapse code snippet');collapsedRegion.removeAttribute('aria-hidden');collapsedRegion.style.display='';}});
  if(adapter.inspect().latestUser.operationId!=='collapsed-result'||adapter.collapseDelivery('collapsed-result',collapsedExpected)!==1)throw Error('native result did not collapse after exact delivery proof');
  let collapsedSnapshot=adapter.inspect();
  if(collapsedSnapshot.latestUser.operationId!=='collapsed-result'||!collapsedSnapshot.latestUser.text.includes('collapsed-result')||!adapter.hasDelivery('collapsed-result',collapsedExpected)||collapsedSnapshot.latestUser.text.includes('unrelated hidden text'))throw Error('native collapse hid result proof or preserved unrelated hidden text');
  nativeButton.click();
  if(adapter.collapseDelivery('collapsed-result',collapsedExpected)!==0||nativeClicks!==2||nativeButton.getAttribute('aria-label')!=='Collapse code snippet')throw Error('manual re-expansion was automatically collapsed again');
  collapsedSnapshot=adapter.inspect();
  if(collapsedSnapshot.latestUser.operationId!=='collapsed-result'||!adapter.hasDelivery('collapsed-result',collapsedExpected))throw Error('manual re-expansion changed result proof');cases++;
  collapsedUser.remove();
  for(const width of [360,768,1200]){
    document.body.style.width=width+'px';document.body.style.maxWidth='100%';
    answer.replaceChildren();answer.style.display='flex';
    const article=document.createElement('article');article.style.flex='1';article.style.minWidth='0';article.textContent='Ответ модели с длинным текстом и разметкой. '.repeat(25);answer.append(article);
    const before=article.getBoundingClientRect(),original=answer.innerHTML;
    adapter.annotateContext();
    const note=document.querySelector('[data-webcodex-context]'),after=article.getBoundingClientRect();
    if(!note||note.previousElementSibling!==answer||answer.innerHTML!==original||Math.abs(before.width-after.width)>0.1||note.getBoundingClientRect().top<answer.getBoundingClientRect().bottom||note.scrollWidth>note.clientWidth)throw Error('context note changed the answer layout at width '+width);
    adapter.annotateContext();if(document.querySelectorAll('[data-webcodex-context]').length!==1)throw Error('context note duplicated');cases++;
  }
  answer.replaceChildren();
  const dialogue=document.createElement('p');dialogue.textContent='I will check this carefully.';
  const helperWidget=document.createElement('ms-code-block'),helperButton=document.createElement('button'),helperRegion=document.createElement('div'),helperCode=document.createElement('pre');
  const helperMarker='b'.repeat(32),helperRaw='HELPER_'+helperMarker+' read_file\\npath: notes.txt\\nEND_'+helperMarker;
  helperButton.setAttribute('aria-label','Collapse code snippet');helperCode.textContent=helperRaw;helperRegion.append(helperCode);helperWidget.append(helperButton,helperRegion);
  const exampleWidget=document.createElement('ms-code-block'),exampleButton=document.createElement('button'),exampleCode=document.createElement('pre');
  exampleButton.setAttribute('aria-label','Collapse code snippet');exampleCode.textContent='Example only: '+helperRaw;exampleWidget.append(exampleButton,exampleCode);
  answer.append(dialogue,helperWidget,exampleWidget);
  helperButton.addEventListener('click',()=>{helperButton.setAttribute('aria-label','Expand code snippet');helperRegion.setAttribute('aria-hidden','true');helperRegion.style.display='none';});
  const helperBefore=adapter.inspect().latestModel;
  if(!adapter.collapseCall(helperBefore))throw Error('accepted Helper frame did not collapse');
  const helperAfter=adapter.inspect().latestModel;
  if(helperAfter.sourceRevision!==helperBefore.sourceRevision||helperAfter.text!==helperBefore.text||helperAfter.renderedSource!==helperBefore.renderedSource||!helperAfter.sourceBlocks.some(block=>block.text===helperRaw)||exampleButton.getAttribute('aria-label')!=='Collapse code snippet')throw Error('Helper collapse changed identity or touched an unrelated example');
  if(adapter.collapseCall(helperBefore))throw Error('Helper source was clicked twice after native collapse');
  helperButton.click();helperButton.setAttribute('aria-label','Collapse code snippet');helperRegion.removeAttribute('aria-hidden');helperRegion.style.display='';
  if(adapter.collapseCall(helperBefore)||helperButton.getAttribute('aria-label')!=='Collapse code snippet')throw Error('manual Helper expansion was automatically collapsed');cases++;
  const historyResult=document.createElement('ms-chat-turn');historyResult.setAttribute('data-turn-role','User');
  const historyWidget=document.createElement('ms-code-block'),historyButton=document.createElement('button'),historyRegion=document.createElement('div');
  historyButton.setAttribute('aria-label','Collapse code snippet');
  const historyCode=document.createElement('pre');historyCode.textContent=JSON.stringify({type:'helper-result',operation_id:'saved-history',name:'read_file',result:'saved bytes'});
  historyRegion.append(historyCode);historyWidget.append(historyButton,historyRegion);historyResult.append(historyWidget);document.body.append(historyResult);
  historyButton.addEventListener('click',()=>{historyButton.setAttribute('aria-label','Expand code snippet');historyRegion.style.display='none';});
  const refreshedAdapter=WebCodexStudio.createAdapter(document,{hostname:'aistudio.google.com'});
  if(refreshedAdapter.collapseChat()!==2||helperButton.getAttribute('aria-label')!=='Expand code snippet'||historyButton.getAttribute('aria-label')!=='Expand code snippet')throw Error('saved chat did not fold in both directions after reload');
  if(refreshedAdapter.inspect().latestUser.operationId!=='saved-history'||exampleButton.getAttribute('aria-label')!=='Collapse code snippet')throw Error('history folding changed delivery proof or ordinary code');
  helperButton.setAttribute('aria-label','Collapse code snippet');helperRegion.removeAttribute('aria-hidden');helperRegion.style.display='';
  historyButton.setAttribute('aria-label','Collapse code snippet');historyRegion.style.display='';
  if(refreshedAdapter.collapseChat()!==0||refreshedAdapter.collapseChat({force:true})!==2)throw Error('manual history folding failed or automatic scan undid manual expansion');cases++;
  historyResult.remove();
  const widthPane=document.createElement('div');widthPane.style.width='1000px';
  const widthTurn=document.createElement('ms-chat-turn');widthTurn.setAttribute('data-turn-role','Model');widthTurn.style.cssText='display:block;width:100%;max-width:600px;margin:0 auto';widthTurn.textContent='Width geometry';
  const widthComposer=document.createElement('ms-prompt-input-wrapper');widthComposer.style.cssText='display:block;width:100%;max-width:600px;margin:0 auto';
  const innerComposer=document.createElement('div');innerComposer.style.cssText='max-width:500px;width:100%;margin:0 auto';
  const innerEditor=document.createElement('textarea');innerComposer.append(innerEditor);widthComposer.append(innerComposer);
  widthPane.append(widthTurn,widthComposer);document.body.append(widthPane);
  for(const value of [0,10,50,100]){
    adapter.setChatWidth(value);
    const expected=600+4*value;
    if(Math.abs(widthTurn.getBoundingClientRect().width-expected)>1||Math.abs(widthComposer.getBoundingClientRect().width-expected)>1)throw Error('chat width geometry failed at '+value);
  }
  if(Math.abs(innerComposer.getBoundingClientRect().width-1000)>1)throw Error('inner composer width remained constrained at Max');
  widthPane.style.width='320px';adapter.setChatWidth(100,true);
  if(widthTurn.getBoundingClientRect().width>321||widthComposer.getBoundingClientRect().width>321)throw Error('chat width overflowed a narrow pane');
  adapter.setChatWidth(0);widthPane.style.width='1000px';
  if(Math.abs(widthTurn.getBoundingClientRect().width-600)>1||widthTurn.style.maxWidth!=='600px'||widthTurn.textContent!=='Width geometry')throw Error('default chat width or text was not restored');
  widthPane.remove();cases++;
  const rawInputPayload=(String.fromCharCode(34,39,92,10)+'Unicode Привет raw document').repeat(2000);
  const rawInputMarker='c'.repeat(32);
  const rawInputFrame=['HELPER_'+rawInputMarker+' exec_command','cwd: W:/work','argv: ["python","-"]','---',rawInputPayload,'END_'+rawInputMarker].join(String.fromCharCode(10));
  const rawInputPre=document.createElement('pre');rawInputPre.textContent=rawInputFrame;answer.replaceChildren(rawInputPre);
  const rawInputTools=[{name:'exec_command',inputSchema:{type:'object',properties:{cwd:{type:'string'},argv:{type:'array',items:{type:'string'}},stdin:{type:'string'}},required:['cwd','argv'],additionalProperties:false}}];
  const rawInputModel=adapter.inspect().latestModel;
  const rawInputCall=WebCodexProtocol.parseCall(rawInputModel.text,null,rawInputTools,rawInputModel);
  if(rawInputCall.arguments.stdin!==rawInputPayload||rawInputCall.arguments.argv.join('|')!=='python|-')throw Error('large raw stdin source was changed in rendered DOM');cases++;
  const editingWrapper=document.createElement('ms-autosize-textarea'),editingSource=document.createElement('textarea');editingSource.value=rawInputFrame;editingWrapper.append(editingSource);answer.append(editingWrapper);
  const editingDraft=document.querySelector('ms-prompt-input-wrapper textarea').value;
  const editingSnapshot=adapter.inspect();
  if(!editingSnapshot.supported||!editingSnapshot.editingReply||editingSnapshot.generating||adapter.stage('must wait'))throw Error('reply editor was confused with composer or generation');
  if(document.querySelector('ms-prompt-input-wrapper textarea').value!==editingDraft)throw Error('reply editing changed composer draft');
  editingSource.hidden=true;if(adapter.inspect().editingReply)throw Error('hidden reply editor blocked conversation');
  editingWrapper.remove();if(adapter.inspect().editingReply)throw Error('closed reply editor blocked conversation');cases++;
  const ownDraft=document.querySelector('textarea').value;let preserved=false;
  document.addEventListener('click',event=>{if(event.target===run){adapter.prepareSessionMessage(liveMarker);preserved=document.querySelector('textarea').value===ownDraft;}},true);
  if(!await adapter.submit(ownDraft)||!preserved)throw Error('Run capture modified the owned result');cases++;
  document.getElementById('check').textContent='PASS:'+cases;
} catch(error) {document.getElementById('check').textContent='FAIL:'+error.message;}
})();
</script>`,'utf8');
  const inline=/<script>([\s\S]*?)<\/script>/.exec(fs.readFileSync(fixture,'utf8'))?.[1];
  assert.ok(inline,'browser fixture script is required');
  new (require('node:vm').Script)(inline);
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
  assert.equal(result,'PASS:106',output.slice(-3000));t.diagnostic('106 scenarios passed in installed Chrome, including manual marker reinsertion, system instruction buttons with icons and ARIA roles, accepted Helper-call collapse with stable source identity and manual re-expansion, collapsed-result proof retention, context-note geometry, exact edit-source recovery, delivery sources, owned Run capture, upload resumption, scoped acknowledgement and nested dialog close');
});
