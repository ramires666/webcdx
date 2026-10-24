package main

type browserSelectOption struct {
	Label    string `json:"label"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
	Selected bool   `json:"selected"`
}

const browserFormDOM = `
function visible(el){return !!el.getClientRects().length&&getComputedStyle(el).visibility!=='hidden'&&getComputedStyle(el).visibility!=='collapse'}
function fieldContext(el){const group=el.closest('fieldset,[role="group"],form,[role="dialog"]');return (group?.querySelector(':scope > legend')?.innerText||group?.getAttribute('aria-label')||'').trim().slice(0,256)}
function selectOptions(el){return el.tagName==='SELECT'?[...el.options].slice(0,100).map(o=>({label:o.label.slice(0,256),value:o.value.slice(0,256),disabled:!!(o.disabled||o.closest('optgroup')?.disabled),selected:o.selected})):[]}
`

func browserSelectScript(value, by string) string {
	return `(async()=>{` + browserFormDOM + `if(el.tagName!=='SELECT')wcFail('invalid_control_type');if(el.matches(':disabled'))wcFail('element_disabled');if(!visible(el))wcFail('element_hidden');
const input=` + browserJS(value) + `,by=` + browserJS(by) + `,options=[...el.options];
let choices=options.filter(o=>by==='label'?o.label===input:o.value===input);
if(by==='auto'&&!choices.length)choices=options.filter(o=>o.label===input);
if(!choices.length)wcFail('invalid_option_value',undefined,selectOptions(el));
if(choices.length!==1)wcFail('ambiguous_locator',choices.length);
const option=choices[0];if(option.disabled||option.closest('optgroup')?.disabled)wcFail('invalid_option_value',undefined,selectOptions(el));
const selected=option.value,label=option.label;el.value=selected;
el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));
await Promise.race([new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))),new Promise(r=>setTimeout(r,200))]);
if(!el.isConnected||el.value!==selected)wcFail('state_verification_failed');
return {selected_value:selected,selected_label:label,confirmation:'control_value_verified'};
})()`
}

func browserFillScript(text string) string {
	return `if(el.matches(':disabled')||el.getAttribute('aria-disabled')==='true')wcFail('element_disabled');if(!el.getClientRects().length||getComputedStyle(el).visibility==='hidden')wcFail('element_hidden');
if(el.isContentEditable){el.focus();const sel=window.getSelection(),range=document.createRange();range.selectNodeContents(el);sel.removeAllRanges();sel.addRange(range);const text=` + browserJS(text) + `;if(text===''){document.execCommand('delete',false);if(el.textContent==='')el.replaceChildren()}else if(!document.execCommand('insertText',false,text))wcFail('state_verification_failed');}
else{if(!['INPUT','TEXTAREA'].includes(el.tagName)||el.readOnly)wcFail('element_not_editable');if(el.tagName==='INPUT'&&['checkbox','radio','file','hidden','button','submit','reset','image'].includes(el.type))wcFail('invalid_control_type');const proto=el.tagName==='INPUT'?HTMLInputElement.prototype:HTMLTextAreaElement.prototype;Object.getOwnPropertyDescriptor(proto,'value').set.call(el,` + browserJS(text) + `);el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));}`
}
