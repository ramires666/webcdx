package main

import (
	"context"
	"fmt"
)

func (b *browserManager) readBrowserRecords(ctx context.Context, t *browserTab, args map[string]any) (any, error) {
	container, err := optionalString(args, "container_selector")
	if err != nil {
		return nil, err
	}
	selector, err := requiredString(args, "record_selector")
	if err != nil {
		return nil, err
	}
	idAttribute, err := optionalString(args, "id_attribute")
	if err != nil {
		return nil, err
	}
	if idAttribute == "" {
		idAttribute = "id"
	}
	textSelector, err := optionalString(args, "text_selector")
	if err != nil {
		return nil, err
	}
	authorSelector, err := optionalString(args, "author_selector")
	if err != nil {
		return nil, err
	}
	timeSelector, err := optionalString(args, "time_selector")
	if err != nil {
		return nil, err
	}
	attachmentSelector, err := optionalString(args, "attachment_selector")
	if err != nil {
		return nil, err
	}
	if attachmentSelector == "" {
		attachmentSelector = "a[href],img[src]"
	}
	pageSize, err := optionalInt(args, "page_size", 25, 1, 100)
	if err != nil {
		return nil, err
	}
	pageToken, err := optionalString(args, "page_token")
	if err != nil {
		return nil, err
	}
	frameID, _ := optionalString(args, "frame_id")
	responseBudget := 65536
	if b.executor != nil && b.executor.maxResponse > 0 {
		responseBudget = min(responseBudget, b.executor.maxResponse/4)
	}
	revision := browserID("record_revision_")
	expression := browserRecordReadExpression(revision, container, selector, idAttribute, textSelector, authorSelector, timeSelector, attachmentSelector, pageSize, pageToken, responseBudget)
	var result map[string]any
	if err := b.eval(ctx, t, frameID, expression, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func browserRecordReadExpression(revision, container, selector, idAttribute, textSelector, authorSelector, timeSelector, attachmentSelector string, pageSize int, pageToken string, responseBudget int) string {
	return `(()=>{` + browserDOMFailure + browserFormDOM + `
const containerSelector=` + browserJS(container) + `,recordSelector=` + browserJS(selector) + `,idAttribute=` + browserJS(idAttribute) + `;
const textSelector=` + browserJS(textSelector) + `,authorSelector=` + browserJS(authorSelector) + `,timeSelector=` + browserJS(timeSelector) + `,attachmentSelector=` + browserJS(attachmentSelector) + `;
let root=document.body;
if(containerSelector){let groups;try{groups=[...document.querySelectorAll(containerSelector)].filter(visible)}catch(e){wcFail('invalid_selector')}if(!groups.length)wcFail('element_not_found',0);if(groups.length!==1)wcFail('ambiguous_locator',groups.length);root=groups[0]}
let rows;try{rows=[...root.querySelectorAll(recordSelector)].filter(visible)}catch(e){wcFail('invalid_selector')}
if(textSelector||authorSelector||timeSelector||attachmentSelector){try{if(textSelector)root.querySelector(textSelector);if(authorSelector)root.querySelector(authorSelector);if(timeSelector)root.querySelector(timeSelector);if(attachmentSelector)root.querySelector(attachmentSelector)}catch(e){wcFail('invalid_selector')}}
const signature=JSON.stringify([containerSelector,recordSelector,idAttribute,textSelector,authorSelector,timeSelector,attachmentSelector]);
let state=globalThis.__webcodexRecordRead;
const token=` + browserJS(pageToken) + `;
if(token){if(!state||state.document!==document||state.url!==location.href||state.root!==root||state.signature!==signature||state.dirty)wcFail('stale_generation');const parts=token.split(':');if(parts.length!==2||parts[0]!==state.revision||!/^[0-9]+$/.test(parts[1]))wcFail('stale_generation')}
else{if(state?.observer)state.observer.disconnect();state={document,url:location.href,root,signature,revision:` + browserJS(revision) + `,dirty:false,observer:null};state.observer=new MutationObserver(()=>{state.dirty=true});state.observer.observe(root,{subtree:true,childList:true,attributes:true,characterData:true});globalThis.__webcodexRecordRead=state}
if(state.observer?.takeRecords().length)state.dirty=true;if(token&&state.dirty)wcFail('stale_generation');
const boundedRows=rows.slice(0,2000),recordIDs=[],ids=new Set(),truncatedRows=rows.length>2000;
for(const row of boundedRows){const id=(row.getAttribute(idAttribute)||'').trim();if(!id)wcFail('unstable_record');if(ids.has(id))wcFail('ambiguous_record');ids.add(id);recordIDs.push(id)}
const offset=token?Number(token.split(':')[1]):0;if(!Number.isSafeInteger(offset)||offset<0||offset>boundedRows.length)wcFail('stale_generation');
let end=Math.min(offset+` + fmt.Sprint(pageSize) + `,boundedRows.length);const selected=[];
for(let index=offset;index<end;index++){
const row=boundedRows[index],recordID=recordIDs[index],pick=selector=>selector?row.querySelector(selector):null;
let textNode,authorNode,timeNode;try{textNode=pick(textSelector);authorNode=pick(authorSelector);timeNode=pick(timeSelector)}catch(e){wcFail('invalid_selector')}
const rawText=(textNode?textNode.innerText:row.innerText)||'',recordText=rawText.slice(0,4096),recordTruncated=rawText.length>4096;
let attachmentNodes;try{attachmentNodes=[...row.querySelectorAll(attachmentSelector)]}catch(e){wcFail('invalid_selector')}
const attachments=[];for(let i=0;i<Math.min(attachmentNodes.length,32);i++){const el=attachmentNodes[i],stable=el.getAttribute('data-attachment-id')||el.id||'',rawURL=el.tagName==='IMG'?el.currentSrc||el.src:el.href;let safeURL='',filename='';try{const u=new URL(rawURL,location.href);if(['http:','https:'].includes(u.protocol)&&!u.username&&!u.password){safeURL=(u.origin+u.pathname).slice(0,2048);filename=el.download||decodeURIComponent(u.pathname.split('/').pop()||'')}}catch{}attachments.push({attachment_id:(stable||String(i+1)).slice(0,256),id_source:stable?'stable_id':'ordinal',filename:filename.slice(0,256),url:safeURL,media_type:(el.getAttribute('type')||(el.tagName==='IMG'?'image':el.tagName.toLowerCase())).slice(0,64)})}
selected.push({record_id:recordID,text:recordText,author:(authorNode?.innerText||'').trim().slice(0,256),timestamp:(timeNode?.getAttribute('datetime')||timeNode?.innerText||'').trim().slice(0,256),attachments,recordTruncated,attachmentsTruncated:attachmentNodes.length>32})
}
const reasons=[];if(truncatedRows)reasons.push('record_limit');if(selected.some(r=>r.recordTruncated))reasons.push('text_limit');if(selected.some(r=>r.attachmentsTruncated))reasons.push('attachment_limit');
const makeResult=()=>({records:selected.map(r=>({record_id:r.record_id,text:r.text,author:r.author,timestamp:r.timestamp,attachments:r.attachments,text_truncated:r.recordTruncated,attachments_truncated:r.attachmentsTruncated})),dom_revision:state.revision,page_token:token||state.revision+':0',next_page_token:'',has_more:false,total_records:rows.length,collection_complete:false,status:'completed',truncated:truncatedRows||reasons.length>0,truncation_reasons:reasons.slice()});
let result=makeResult();while(new TextEncoder().encode(JSON.stringify(result)).length>` + fmt.Sprint(responseBudget) + `){if(selected.length<=1)wcFail('response_too_large');selected.pop();end--;if(!reasons.includes('response_budget'))reasons.push('response_budget');result=makeResult()}
result.has_more=end<boundedRows.length;result.next_page_token=result.has_more?state.revision+':'+end:'';if(new TextEncoder().encode(JSON.stringify(result)).length>` + fmt.Sprint(responseBudget) + `)wcFail('response_too_large');return result;})()`
}
