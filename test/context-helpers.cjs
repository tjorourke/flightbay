const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/ambient.js'),'utf8');
module.exports=source.slice(source.indexOf('const amOpts ='),source.indexOf('function amInvalidate()'));
