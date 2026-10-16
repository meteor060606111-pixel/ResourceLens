import {readFileSync,statSync} from 'node:fs';
import {resolve,dirname,extname,relative} from 'node:path';
import {gzipSync} from 'node:zlib';

const root=resolve('dist'),seen=new Set();
function visit(file){
  if(seen.has(file))return;
  if(!file.startsWith(root+'/'))throw Error('Phone asset escapes dist');
  seen.add(file);
  if(!['.html','.js','.webmanifest'].includes(extname(file)))return;
  const source=readFileSync(file,'utf8');
  const paths=extname(file)==='.html'
    ? [...source.matchAll(/(?:src|href)="([^"]+)"/g)].map(m=>m[1])
    : extname(file)==='.webmanifest'
      ? JSON.parse(source).icons.map(icon=>icon.src)
      : [...source.matchAll(/(?:from|import)\s*["'](\.[^"']+)["']/g)].map(m=>m[1]);
  for(const path of paths){
    if(/^(?:https?:|data:)/.test(path))throw Error('Phone must not depend on a remote asset');
    visit(path.startsWith('/')?resolve(root,'.'+path):resolve(dirname(file),path));
  }
}
visit(resolve(root,'phone.html'));
const files=[...seen];
const code=files.filter(file=>['.html','.js','.css'].includes(extname(file)));
const bytes=files.reduce((n,file)=>n+statSync(file).size,0);
const codeBytes=code.reduce((n,file)=>n+statSync(file).size,0);
const gzipBytes=code.reduce((n,file)=>n+gzipSync(readFileSync(file)).length,0);
if(codeBytes>160*1024||bytes>256*1024)throw Error(`Phone exceeds size budget: code ${codeBytes}, total ${bytes}`);
console.log(JSON.stringify({phoneCodeBytes:codeBytes,phoneCodeGzipBytes:gzipBytes,phoneAllAssetsBytes:bytes,budgetCodeBytes:160*1024,budgetAllBytes:256*1024,files:files.map(file=>relative(root,file))}));
