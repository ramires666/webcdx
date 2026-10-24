package main

func browserSnapshotExpression(id, container string) string {
	return `(()=>{` + browserDOMFailure + browserDOM + `
let root=document.body;
const selector=` + browserJS(container) + `;
if(selector){let groups;try{groups=[...document.querySelectorAll(selector)].filter(visible)}catch(e){wcFail('invalid_selector')}if(!groups.length)wcFail('element_not_found',0);if(groups.length!==1)wcFail('ambiguous_locator',groups.length);root=groups[0];all.length=0;all.push(root);walk(root)}
const id=` + browserJS(id) + `,found=all.filter(el=>role(el)&&visible(el)),nodes=found.slice(0,300),text=root?.innerText||'',reasons=[];
globalThis.__webcodex={id,nodes,document,url:location.href};
globalThis.__webcodexGeneration??=id;
if(all.length>=10000)reasons.push('node_limit');if(found.length>300)reasons.push('element_limit');if(text.length>12000)reasons.push('text_limit');
const elements=nodes.map((el,i)=>({element_id:String(i),role:role(el),name:name(el),tag:el.tagName.toLowerCase(),disabled:el.matches(':disabled')||el.getAttribute('aria-disabled')==='true',href:el.tagName==='A'?el.href.split(/[?#]/)[0]:'',value:el.type==='password'?'':String(el.value??(el.isContentEditable?el.innerText:'')).slice(0,256),context:fieldContext(el),...(el.tagName==='SELECT'?{options:selectOptions(el),options_truncated:el.options.length>100||[...el.options].some(o=>o.label.length>256||o.value.length>256)}:{})}));
if(elements.some(e=>e.options_truncated))reasons.push('options_limit');
return {snapshot_id:id,document_generation:globalThis.__webcodexGeneration,ready_state:document.readyState,url:location.href,title:document.title,viewport_width:innerWidth,viewport_height:innerHeight,text:text.slice(0,12000),elements,counts:{dom_visited:all.length,elements_found:found.length,elements_returned:elements.length},truncation_reasons:reasons,truncated:reasons.length>0};})()`
}

func snapshotReason(result map[string]any, reason string) {
	reasons, ok := result["truncation_reasons"].([]string)
	if !ok {
		reasons = []string{}
		for _, raw := range result["truncation_reasons"].([]any) {
			if s, ok := raw.(string); ok {
				reasons = append(reasons, s)
			}
		}
	}
	for _, existing := range reasons {
		if existing == reason {
			return
		}
	}
	result["truncation_reasons"] = append(reasons, reason)
	result["truncated"] = true
}
