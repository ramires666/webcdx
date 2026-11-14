const fs=require('node:fs');
const assert=require('node:assert/strict');
const {parseCall}=require('./protocol.js');
const tools=JSON.parse(fs.readFileSync(0,'utf8'));
const examples=require('./test-fixtures/reply-v2-examples.json');
for(const example of examples){
  const expected={name:example.name,arguments:example.arguments};
  assert.deepEqual(parseCall(example.text,null,tools),expected,example.name);
  assert.deepEqual(parseCall(JSON.stringify({v:1,...expected}),null,tools),expected,example.name+' legacy');
}
