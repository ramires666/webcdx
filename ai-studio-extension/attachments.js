(function(root){
  'use strict';
  const base64=bytes=>{let value='';for(let i=0;i<bytes.length;i+=32768)value+=String.fromCharCode(...bytes.subarray(i,i+32768));return btoa(value);};
  const digest=async bytes=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',bytes)),value=>value.toString(16).padStart(2,'0')).join('');
  async function prepare({name,result,enabled=true}){
    if(!enabled||name!=='read_file'||!result||result.isError)return{result,attachments:[]};
    let meta=result.structuredContent;
    if(!meta)for(const item of result.content||[])if(item.type==='text')try{const value=JSON.parse(item.text);if(value&&typeof value==='object'&&typeof value.path==='string')meta=value;}catch{}
    if(!meta||typeof meta.path!=='string')return{result,attachments:[]};
    let bytes,data,fileMeta=meta.attachment;
    if(fileMeta){
      const resources=(result.content||[]).filter(item=>item.type==='resource'&&item.resource?.uri===fileMeta.uri);
      if(resources.length!==1||typeof resources[0].resource.blob!=='string')throw Error('File attachment resource is missing or ambiguous');
      data=resources[0].resource.blob;
      const binary=atob(data);bytes=Uint8Array.from(binary,char=>char.charCodeAt(0));
      if(bytes.byteLength!==fileMeta.size||await digest(bytes)!==fileMeta.sha256)throw Error('File attachment checksum or size does not match');
    }else{
      if(typeof meta.content!=='string')return{result,attachments:[]};
      bytes=new TextEncoder().encode(meta.content);data=base64(bytes);
      const sha256=await digest(bytes);
      let filename=meta.path.split(/[\\/]/).pop()||'file.txt';
      if(meta.offset>1||meta.truncated)filename+='.lines-'+(meta.offset||1)+'-'+((meta.next_offset||1)-1)+'.txt';
      fileMeta={name:filename,mimeType:'text/plain',size:bytes.byteLength,sha256,uri:'urn:helper:sha256:'+sha256};
    }
    if(typeof fileMeta.name!=='string'||!fileMeta.name||/[\\/\x00-\x1f]/.test(fileMeta.name))throw Error('Invalid attachment filename');
    const {content,attachment,...summary}=meta;
    const descriptor={...fileMeta,id:fileMeta.sha256,encodedLength:data.length};
    const compact={...summary,attachment:{name:descriptor.name,mimeType:descriptor.mimeType,size:descriptor.size,sha256:descriptor.sha256}};
    return{result:{isError:false,structuredContent:compact,content:[{type:'text',text:JSON.stringify(compact)}]},attachments:[{...descriptor,data}]};
  }
  const api={prepare};
  if(typeof module!=='undefined'&&module.exports)module.exports=api;
  root.WebCodexAttachments=api;
})(typeof globalThis!=='undefined'?globalThis:this);
