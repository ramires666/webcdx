const test=require('node:test');
const assert=require('node:assert/strict');
const {Node,setup}=require('./test-fixtures/studio-dom.cjs');
const {parseCall,analyzeReply}=require('./protocol.js');

const tools=[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path'],additionalProperties:false}}];
const args={path:'W:\\projects\\Игра { [test] }\\"quoted"\\file 😀.txt'};
const raw=JSON.stringify({v:1,name:'read_file',arguments:args});
function body(state){state.chunk.remove();return state.model.children[0];}
function fragmented(parent,value,width){for(let i=0;i<value.length;i+=width)parent.add(new Node('span',{'class':'syntax-'+i},value.slice(i,i+width)));}

test('whole-answer parsing tolerates renderer, nesting, labeling and tokenization changes',t=>{
  let cases=0;
  for(const tag of ['ms-code-block','pre','code','article','section','future-code-widget'])
  for(const layout of ['direct','paragraphs','nested','siblings'])
  for(const label of ['','v','json','Webcodex-call','arbitrary language'])
  for(const width of [1,3,19,10000]){
    const state=setup({draft:'keep my draft'}),content=body(state);
    content.add(new Node('p',{},'Проверю доступные пути.\n'));
    let target=content.add(new Node(tag));
    if(label)target.add(new Node('header',{},label+'\n'));
    target.add(new Node('button',{},raw)); // A copy button/tooltip must not add a second command.
    if(layout==='paragraphs')target=target.add(new Node('p'));
    if(layout==='nested')target=target.add(new Node('ms-text-chunk')).add(new Node('div')).add(new Node('ms-text-chunk'));
    if(layout==='siblings'){
      const midpoint=Math.floor(raw.length/2);
      fragmented(target,raw.slice(0,midpoint),width);
      target=content.add(new Node('another-widget'));
      fragmented(target,raw.slice(midpoint),width);
    }else fragmented(target,raw,width);
    content.add(new Node('footer',{},'\nПосле выполнения сообщу результат.'));
    const before=state.model.textContent;
    const result=state.adapter.inspect();
    assert.equal(result.supported,true);
    assert.deepEqual(parseCall(result.latestModel.text,null,tools),{name:'read_file',arguments:args});
    assert.ok(result.latestModel.sourceText.startsWith('Проверю доступные пути.\n'));
    assert.ok(result.latestModel.sourceText.endsWith('После выполнения сообщу результат.'));
    assert.equal(state.model.textContent,before,'inspection never modifies the original answer');
    assert.equal(state.editor.value,'keep my draft');cases++;
  }
  t.diagnostic(cases+' DOM combinations dispatch the same exact arguments');
});

test('hidden copies and thoughts never become commands or create duplicate commands',()=>{
  const hiddenFactories=[
    ()=>new Node('ms-thought-accordion',{},raw),
    ()=>new Node('ms-thought-chunk',{},raw),
    ()=>new Node('div',{'data-thought':'true'},raw),
    ()=>new Node('div',{'hidden':''},raw),
    ()=>new Node('div',{'aria-hidden':'true'},raw),
    ()=>new Node('div',{'style':'display: none'},raw),
    ()=>new Node('div',{'style':'visibility: hidden'},raw),
    ()=>{const n=new Node('div',{},raw);n.hidden=true;return n;},
    ()=>new Node('div',{'class':'computed-hidden'},raw),
    ()=>{const n=new Node('details',{},raw);n.add(new Node('summary',{},'Thoughts'));return n;},
    ()=>{const n=new Node('details',{open:''},raw);n.add(new Node('summary',{},'Thoughts'));return n;},
  ];
  for(const makeHidden of hiddenFactories)for(const withVisible of [false,true]){
    const state=setup(),content=body(state);
    state.document.defaultView.getComputedStyle=node=>({display:node.attrs.class==='computed-hidden'?'none':'block'});
    content.add(makeHidden());content.add(new Node('div',{},withVisible?raw:'Done.'));
    const result=state.adapter.inspect().latestModel.text;
    assert.equal(analyzeReply(result).kind,withVisible?'call':'none');
  }
});

test('semantic roles survive renamed turn tags while ambiguous roles remain unsupported',()=>{
  for(const mixed of [false,true]){
    const state=setup();if(!mixed)state.user.tag='future-turn';state.model.tag='future-turn';body(state).add(new Node('unknown',{},raw));
    assert.deepEqual(parseCall(state.adapter.inspect().latestModel.text,null,tools),{name:'read_file',arguments:args});
    state.model.children[0].add(new Node('div',{'data-turn-role':'User'},raw));
    assert.equal(state.adapter.inspect().supported,false);
  }
});

test('visible duplicates, broken JSON and nested call data are never promoted to one executable call',()=>{
  for(const value of [raw+'\n'+raw,raw.slice(0,-1),JSON.stringify({result:JSON.parse(raw)}),JSON.stringify([JSON.parse(raw)])]){
    const state=setup();body(state).add(new Node('future-renderer',{},value));
    const text=state.adapter.inspect().latestModel.text;
    if(value.startsWith('{"result"')||value.startsWith('['))assert.equal(parseCall(text,null,tools),null);
    else assert.throws(()=>parseCall(text,null,tools));
  }
});

test('delivery proof in generic markup requires the full expected result and rejects duplicate or nested envelopes',()=>{
  const expected={type:'webcodex-result',operation_id:'op-new',result:{text:'W:\\a { b }\\file.txt'}};
  const payload=JSON.stringify(expected);
  for(const [value,accepted] of [[JSON.stringify(expected,null,2),true],[payload.replace('file.txt','other.txt'),false],[payload+'\n'+payload,false],[payload+'\n{"broken":',false],[JSON.stringify({example:expected}),false]]){
    const state=setup();state.user.children=[];state.user._text='';state.user.attrs['data-turn-role']='User';
    state.user.add(new Node('future-user-content',{},'User · 10:00 PM\n'+value));
    assert.equal(state.adapter.hasDelivery('op-new',payload),accepted,value);
  }
  const state=setup();state.user.children=[];state.user._text='';state.user.attrs['data-turn-role']='User';
  state.user.add(new Node('ms-text-chunk',{},payload));state.user.add(new Node('ms-text-chunk',{},payload));
  assert.equal(state.adapter.hasDelivery('op-new',payload),false,'matching one child must not hide a duplicate result envelope');
  assert.equal(state.adapter.inspect().latestUser.operationId,'');
});
