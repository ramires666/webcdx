package main

import (
	"context"
	"fmt"
)

func (b *browserManager) scrollBrowserContainer(ctx context.Context, t *browserTab, args map[string]any) (any, error) {
	containerSelector, err := requiredString(args, "container_selector")
	if err != nil {
		return nil, err
	}
	recordSelector, _ := optionalString(args, "record_selector")
	idAttribute, _ := optionalString(args, "id_attribute")
	if idAttribute == "" {
		idAttribute = "id"
	}
	if len(idAttribute) > 64 || idAttribute == "" {
		return nil, toolErr("invalid_arguments", "id_attribute must be a short attribute name")
	}
	x, err := optionalInt(args, "x", 0, -100000, 100000)
	if err != nil {
		return nil, err
	}
	y, err := optionalInt(args, "y", 0, -100000, 100000)
	if err != nil {
		return nil, err
	}
	frame, _ := optionalString(args, "frame_id")
	ctx = context.WithValue(ctx, browserUserGesture{}, true)
	expression := `(()=>{` + browserDOMFailure + browserFormDOM + `
const containerSelector=` + browserJS(containerSelector) + `,recordSelector=` + browserJS(recordSelector) + `,idAttribute=` + browserJS(idAttribute) + `;
let containers;try{containers=[...document.querySelectorAll(containerSelector)].filter(visible)}catch(e){wcFail('invalid_selector')}
if(!containers.length)wcFail('element_not_found',0);if(containers.length!==1)wcFail('ambiguous_locator',containers.length);
const container=containers[0];
function rows(){let found;try{found=recordSelector?[...container.querySelectorAll(recordSelector)]:[]}catch(e){wcFail('invalid_selector')}
if(found.length>2000)return {error_code:'limit_reached'};
const ids=[];for(const row of found){const id=row.getAttribute(idAttribute);if(id==null||id.length===0||id.length>256)return {error_code:'unstable_record'};if(ids.includes(id))return {error_code:'ambiguous_record'};ids.push(id)}
return {found,ids}}
const beforeRows=rows();if(beforeRows.error_code)return {error_code:beforeRows.error_code};
const beforeTop=container.scrollTop,beforeLeft=container.scrollLeft,beforeIds=new Set(beforeRows.ids),rect=container.getBoundingClientRect();
let mutated=false,observer=null,timer=null,scrollHandler=null,recordError='';
const wait=new Promise(resolve=>{
let finished=false;const done=()=>{if(finished)return;finished=true;if(observer)observer.disconnect();if(timer!==null)clearTimeout(timer);if(scrollHandler)container.removeEventListener('scroll',scrollHandler);resolve()};
observer=new MutationObserver(()=>{mutated=true;if(recordSelector){const current=rows();if(current.error_code){recordError=current.error_code;done();return}if(current.ids.length!==beforeRows.ids.length||current.ids.some((id,index)=>id!==beforeRows.ids[index])||current.ids.some(id=>!beforeIds.has(id)))done()}else done()});observer.observe(container,{subtree:true,childList:true,attributes:true,characterData:true});
scrollHandler=()=>{if(!recordSelector&&(Math.abs(container.scrollTop-beforeTop)>0.5||Math.abs(container.scrollLeft-beforeLeft)>0.5))done()};container.addEventListener('scroll',scrollHandler,{passive:true});
timer=setTimeout(done,2000);
});
container.scrollBy({left:` + fmt.Sprint(x) + `,top:` + fmt.Sprint(y) + `,behavior:'instant'});
return wait.then(()=>{if(recordError)return {error_code:recordError};const afterRows=rows();if(afterRows.error_code)return {error_code:afterRows.error_code};
const top=container.scrollTop,left=container.scrollLeft,maxTop=Math.max(0,container.scrollHeight-container.clientHeight),maxLeft=Math.max(0,container.scrollWidth-container.clientWidth);
const visible=[];for(const row of afterRows.found){const r=row.getBoundingClientRect();if(r.bottom>rect.top&&r.top<rect.bottom&&r.right>rect.left&&r.left<rect.right)visible.push(row.getAttribute(idAttribute))}
const added=afterRows.ids.filter(id=>!beforeIds.has(id)),newIds=added.slice(0,100),edgeTolerance=4;
return {scroll_top:top,scroll_left:left,scroll_moved:Math.abs(top-beforeTop)>0.5||Math.abs(left-beforeLeft)>0.5,
dom_changed:mutated||beforeRows.ids.length!==afterRows.ids.length||added.length>0,
first_id:visible.length?visible[0]:'',last_id:visible.length?visible[visible.length-1]:'',visible_count:visible.length,
new_ids:newIds,at_scroll_start:top<=edgeTolerance&&left<=edgeTolerance,at_scroll_end:top>=maxTop-edgeTolerance&&left>=maxLeft-edgeTolerance,
history_end_verified:false,status:'completed',truncated:added.length>100};});})()`
	var result map[string]any
	if err := b.eval(ctx, t, frame, expression, &result); err != nil {
		return nil, err
	}
	if code, _ := result["error_code"].(string); code != "" {
		switch code {
		case "unstable_record":
			return nil, toolErr("unstable_record", "container records need stable nonempty identifiers")
		case "ambiguous_record":
			return nil, toolErr("ambiguous_record", "container records have duplicate identifiers")
		case "limit_reached":
			return nil, toolErr("limit_reached", "container contains more than 2000 tracked records")
		default:
			return nil, toolErr("browser_failed", "container scroll failed")
		}
	}
	return result, nil
}
