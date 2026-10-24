package main

import (
	"context"
	"unicode/utf8"
)

func (b *browserManager) fillNative(ctx context.Context, t *browserTab, args map[string]any) (any, error) {
	text, err := optionalString(args, "text")
	if err != nil {
		return nil, err
	}
	if _, ok := args["text"]; !ok {
		return nil, toolErr("invalid_arguments", "text is required")
	}
	if utf8.RuneCountInString(text) > 10000 {
		return nil, toolErr("invalid_arguments", "text must not exceed 10000 characters")
	}
	frameID, _ := optionalString(args, "frame_id")
	locator, err := b.locator(t, args)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, browserUserGesture{}, true)

	prepare := `(()=>{` + locator + browserFormDOM + `
if(el.matches(':disabled')||el.getAttribute('aria-disabled')==='true')wcFail('element_disabled');
if(!visible(el))wcFail('element_hidden');
if(el.type==='password')return {password:true};
const input=el.tagName==='INPUT'&&['text','search','url','tel','email'].includes(el.type);
const textarea=el.tagName==='TEXTAREA';
const editable=el.isContentEditable===true;
if(!input&&!textarea&&!editable)wcFail('invalid_control_type');
if((input||textarea)&&el.readOnly||editable&&el.getAttribute('aria-readonly')==='true')wcFail('element_not_editable');
const rect=el.getBoundingClientRect(),root=el.getRootNode();
if(rect.width>0&&rect.height>0&&rect.right>0&&rect.bottom>0&&rect.left<innerWidth&&rect.top<innerHeight){
const x=Math.max(0,Math.min(innerWidth-1,rect.left+rect.width/2)),y=Math.max(0,Math.min(innerHeight-1,rect.top+rect.height/2));
const pointRoot=root===document?document:root,hit=pointRoot.elementFromPoint?.(x,y);
if(hit&&hit!==el&&!el.contains(hit))wcFail('element_obscured');
}
el.focus({preventScroll:true});
if(input||textarea)el.setSelectionRange(0,el.value.length);
else{const selection=window.getSelection(),range=document.createRange();range.selectNodeContents(el);selection.removeAllRanges();selection.addRange(range)}
return {password:false,kind:editable?'contenteditable':'value'};
})()`
	var prepared struct {
		Password bool   `json:"password"`
		Kind     string `json:"kind"`
	}
	if err := b.eval(ctx, t, frameID, prepare, &prepared); err != nil {
		return nil, err
	}
	if prepared.Password {
		return map[string]any{"status": "waiting_user", "reason": "password_entry"}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := t.commandFrame(ctx, frameID, "Input.insertText", map[string]any{"text": text}, nil); err != nil {
		return nil, err
	}

	verify := `(()=>{` + locator + `const expected=` + browserJS(text) + `,actual=`
	if prepared.Kind == "contenteditable" {
		verify += `el.innerText`
	} else {
		verify += `el.value`
	}
	verify += `;return String(actual).replace(/\r\n/g,'\n')===String(expected).replace(/\r\n/g,'\n')})()`
	var verified bool
	if err := b.eval(ctx, t, frameID, verify, &verified); err != nil {
		return nil, err
	}
	if !verified {
		return nil, toolErr("state_verification_failed", browserFailureMessage("state_verification_failed"))
	}
	return map[string]any{"confirmation": "control_value_verified"}, nil
}
