const test = require('node:test');
const assert = require('node:assert/strict');
const {parseCall} = require('./protocol.js');
const corpus = require('./test-fixtures/reply-corpus.json');

const tools = [
  {name:'read_file', inputSchema:{type:'object', additionalProperties:false, properties:{path:{type:'string'}}, required:['path']}},
  {name:'write_file', inputSchema:{type:'object', additionalProperties:false, properties:{path:{type:'string'}, content:{type:'string'}, if_absent:{const:true}}, required:['path','content','if_absent']}}
];

for (const example of corpus) test(`reply corpus ${example.category} ${example.id}`, () => {
  if (example.error) assert.throws(() => parseCall(example.text, null, tools));
  else assert.deepEqual(parseCall(example.text, null, tools), example.expected);
});
