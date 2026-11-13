const test=require('node:test');
const assert=require('node:assert/strict');
const {createHash}=require('node:crypto');
const {prepare}=require('./attachments.js');
const hash=value=>createHash('sha256').update(value).digest('hex');
test('a legacy text read becomes an exact native-file payload and compact metadata',async()=>{
  const text='print("Привет 😀")\r\n'+('line\n'.repeat(20000));
  const result={isError:false,structuredContent:{path:'W:\\algo\\agents.py',content:text,sha256:hash(text),size:Buffer.byteLength(text),offset:1,next_offset:20002,truncated:false}};
  const prepared=await prepare({name:'read_file',result});
  assert.equal(prepared.attachments.length,1);const file=prepared.attachments[0];
  assert.equal(Buffer.from(file.data,'base64').toString('utf8'),text);assert.equal(file.name,'agents.py');
  assert.equal(file.sha256,hash(text));assert.ok(JSON.stringify(prepared.result).length<1500);
  assert.equal(prepared.result.structuredContent.content,undefined);
  assert.equal(result.structuredContent.content,text,'preparation never mutates the original operation result');
});
test('binary embedded MCP resources keep PDF bytes, filename and MIME type',async()=>{
  const bytes=Buffer.from('%PDF-1.7\n\x00\xffbinary','latin1'),sha256=hash(bytes),uri='urn:helper:sha256:'+sha256;
  const attachment={name:'report.pdf',mimeType:'application/pdf',size:bytes.length,sha256,uri};
  const result={isError:false,structuredContent:{path:'W:/report.pdf',attachment},content:[{type:'resource',resource:{uri,mimeType:'application/pdf',blob:bytes.toString('base64')}}]};
  const prepared=await prepare({name:'read_file',result});
  assert.deepEqual(Buffer.from(prepared.attachments[0].data,'base64'),bytes);
  assert.equal(prepared.attachments[0].mimeType,'application/pdf');
  assert.ok(!JSON.stringify(prepared.result).includes(bytes.toString('base64')));
  await assert.rejects(prepare({name:'read_file',result:{...result,structuredContent:{attachment:{...attachment,size:99},path:'W:/report.pdf'}}}),/checksum|size/);
});
test('read ranges are identified as fragments; disabled and failed reads remain untouched',async()=>{
  const result={structuredContent:{path:'W:/data.py',content:'second\n',offset:2,next_offset:3,truncated:true}};
  assert.equal((await prepare({name:'read_file',result})).attachments[0].name,'data.py.lines-2-2.txt');
  for(const value of [{name:'read_file',enabled:false,result},{name:'exec_command',result},{name:'read_file',result:{...result,isError:true}}])assert.deepEqual(await prepare(value),{result:value.result,attachments:[]});
});
