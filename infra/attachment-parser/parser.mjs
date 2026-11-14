import {createRequire} from 'node:module';
import {dirname,join} from 'node:path';
import {SaxesParser} from 'saxes';
import yauzl from 'yauzl';
import {Resvg} from '@resvg/resvg-js';
const require=createRequire(import.meta.url);
const EXPANDED=16777216, CHARS=200000;
const empty=()=>({status:'completed',text:'',pages:0,truncated:false});
const fail=()=>{throw Error('invalid, unreadable or resource-limited document');};
function xml(data,onOpen=()=>{},onText=()=>{},onClose=()=>{}){
 const p=new SaxesParser({xmlns:true});let depth=0,nodes=0;
 p.on('doctype',fail);p.on('error',fail);
 p.on('opentag',n=>{if(++depth>64||++nodes>200000||Object.keys(n.attributes).length>64)fail();onOpen(n);});
 p.on('text',onText);p.on('cdata',onText);p.on('closetag',n=>{onClose(n);--depth;});p.write(data).close();
}
function accumulator(out){let count=0;return s=>{if(!s)return;const chars=Array.from(s);const room=CHARS-count;if(chars.length>room)out.truncated=true;out.text+=chars.slice(0,room).join('');count+=Math.min(room,chars.length);};}
async function pdf(data,extract){
 // Byte-array only. No JS/actions/forms/rendering or remote resources.
 const {getDocument}=await import('pdfjs-dist/legacy/build/pdf.mjs');
 const base=dirname(require.resolve('pdfjs-dist/package.json'));
 const task=getDocument({data:new Uint8Array(data),isEvalSupported:false,useWasm:false,useSystemFonts:false,disableFontFace:true,stopAtErrors:true,standardFontDataUrl:join(base,'standard_fonts')+'/',verbosity:1});
 let doc;try{doc=await task.promise;}catch(e){if(e.name==='PasswordException')return {...empty(),status:'unsupported',reason:'encrypted_pdf'};throw e;}
 try{
  const out=empty();out.pages=doc.numPages;out.truncated=doc.numPages>100;out.sections=[];const add=accumulator(out);
  for(let i=1;i<=Math.min(100,doc.numPages);i++){
   const page=await doc.getPage(i);
   if(extract){const start=Array.from(out.text).length;const content=await page.getTextContent({disableNormalization:false});for(const item of content.items){if(typeof item.str==='string')add(item.str+(item.hasEOL?'\n':' '));}out.sections.push({page:i,start,end:Array.from(out.text).length});}
   page.cleanup();
  }
  if(extract&&!out.text.trim()){out.status='unsupported';out.reason='image_only_or_empty_pdf';}
  return out;
 }finally{await task.destroy();}
}
async function docx(data,extract){
 const archive=await new Promise((resolve,reject)=>yauzl.fromBuffer(data,{lazyEntries:true,validateEntrySizes:true,strictFileNames:true},(e,z)=>e?reject(e):resolve(z)));
 const out=empty(),add=accumulator(out);let total=0,declared=0,entries=0,document=false,types=false;const names=new Set();
 await new Promise((resolve,reject)=>{
  const abort=e=>{archive.close();reject(e);};archive.on('error',abort);archive.on('end',resolve);
  archive.on('entry',entry=>{(async()=>{
   if(++entries>1024||names.has(entry.fileName)||entry.fileName.startsWith('/')||entry.fileName.split('/').includes('..')||(entry.generalPurposeBitFlag&1))fail();names.add(entry.fileName);
   declared+=entry.uncompressedSize;if(declared>EXPANDED)fail();
   if(entry.fileName.endsWith('/')){archive.readEntry();return;}
   const stream=await new Promise((res,rej)=>archive.openReadStream(entry,(e,s)=>e?rej(e):res(s)));
   const chunks=[];let size=0;
   for await(const chunk of stream){total+=chunk.length;size+=chunk.length;if(total>EXPANDED)fail();if(entry.fileName.endsWith('.xml')||entry.fileName.endsWith('.rels'))chunks.push(chunk);}
   if(size!==entry.uncompressedSize)fail();
   if(chunks.length){const text=new TextDecoder('utf-8',{fatal:true}).decode(Buffer.concat(chunks));
    if(entry.fileName==='word/document.xml'){
     let inText=0,root=false;xml(text,n=>{if(n.local==='document'&&n.uri==='http://schemas.openxmlformats.org/wordprocessingml/2006/main')root=true;if(n.local==='t'&&n.uri==='http://schemas.openxmlformats.org/wordprocessingml/2006/main')inText++;},s=>{if(extract&&inText)add(s);},n=>{if(n.local==='t'&&n.uri==='http://schemas.openxmlformats.org/wordprocessingml/2006/main')inText--;if(extract&&n.local==='p')add('\n');});if(!root)fail();document=true;
    }else if(entry.fileName==='[Content_Types].xml'){
     xml(text,n=>{if(n.local==='Override'&&n.attributes.PartName?.value==='/word/document.xml'&&n.attributes.ContentType?.value==='application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml')types=true;});
    }else xml(text); // Parse bounded XML only. Relationships are NEVER followed.
   }
   archive.readEntry();
  })().catch(abort);});archive.readEntry();
 });
 if(!document||!types)fail();if(extract&&!out.text.trim()){out.status='unsupported';out.reason='empty_document';}return out;
}
function svg(data){
 const text=new TextDecoder('utf-8',{fatal:true}).decode(data);
 // Deliberately narrow static vector subset; no href, use, image, CSS, fonts,
 // filters, animation, foreignObject, entities or resource resolution.
 const tags=new Set(['svg','g','path','rect','circle','ellipse','line','polyline','polygon','title','desc']);
 const attrs=new Set(['width','height','viewBox','x','y','x1','y1','x2','y2','cx','cy','r','rx','ry','d','points','fill','stroke','stroke-width','fill-rule','stroke-linecap','stroke-linejoin','opacity','fill-opacity','stroke-opacity','transform','xmlns']);
 let root=false,rootWidth=0,rootHeight=0;xml(text,n=>{
  if(n.uri!=='http://www.w3.org/2000/svg'||!tags.has(n.local))fail();
  if(!root){if(n.local!=='svg')fail();const width=Number(n.attributes.width?.value),height=Number(n.attributes.height?.value);if(!Number.isFinite(width)||!Number.isFinite(height)||width<1||height<1||width>8192||height>8192||width*height>20000000)fail();rootWidth=width;rootHeight=height;root=true;}
  for(const a of Object.values(n.attributes)){if(a.name==='xmlns'&&a.value==='http://www.w3.org/2000/svg')continue;if(!attrs.has(a.name)||/url\s*\(|https?:|file:|data:|@|[\x00-\x08]/i.test(a.value))fail();}
 });if(!root)fail();
 const renderer=new Resvg(text,{font:{loadSystemFonts:false},fitTo:rootWidth>=rootHeight?{mode:'width',value:Math.min(2048,Math.ceil(rootWidth))}:{mode:'height',value:Math.min(2048,Math.ceil(rootHeight))}});
 if(renderer.imagesToResolve().length)fail();const image=renderer.render();if(image.width>2048||image.height>2048)fail(); // no persisted/model image in Task8
 return empty();
}
try{
 let size=0,chunks=[];for await(const c of process.stdin){size+=c.length;if(size>1500000)fail();chunks.push(c);}
 const request=JSON.parse(Buffer.concat(chunks));const data=Buffer.from(request.data,'base64');if(data.length>1048576)fail();
 let out;
 if(request.media==='application/pdf')out=await pdf(data,request.extract===true);
 else if(request.media==='application/vnd.openxmlformats-officedocument.wordprocessingml.document')out=await docx(data,request.extract===true);
 else if(request.media==='image/svg+xml')out=svg(data);
 else fail();
 if(!request.extract)out.text='';process.stdout.write(JSON.stringify(out));
}catch(e){process.stderr.write(String(e?.name||'ParserError')+': rejected document\n');process.exitCode=65;}
