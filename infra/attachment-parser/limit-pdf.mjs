// Fail-closed allocation instrumentation of the EXACT pinned parser, not a PDF
// parser. DecodeStream allocations cover decompressed text/object streams. The
// sandbox separately bounds all allocations (including native/image structures).
import {readFileSync,writeFileSync} from 'node:fs';
const path='node_modules/pdfjs-dist/legacy/build/pdf.worker.mjs';
let source=readFileSync(path,'utf8');
const start=source.indexOf('class DecodeStream extends BaseStream {');
const end=source.indexOf('  getByte() {',start);
if(start<0||end<start)throw Error('pinned DecodeStream changed');
let block=source.slice(start,end);
const needle='    const buffer2 = new Uint8Array(size);';
if(block.split(needle).length!==2)throw Error('pinned allocator changed');
block=block.replace(needle,`    attachmentExpandedBytes += size - buffer.byteLength;
    if (attachmentExpandedBytes > 16777216) { process.stderr.write('expanded PDF limit\\n'); process.exit(65); }
${needle}`);
source=source.slice(0,start)+'let attachmentExpandedBytes = 0;\n'+block+source.slice(end);
const nativeStart=source.indexOf('  async asyncGetBytesFromDecompressionStream(name) {');
const nativeEnd=source.indexOf('  reset() {',nativeStart);
if(nativeStart<0||nativeEnd<nativeStart)throw Error('pinned native decoder changed');
let native=source.slice(nativeStart,nativeEnd);
const nativeNeedle='        chunks.push(chunk);';
if(native.split(nativeNeedle).length!==2)throw Error('pinned native chunk loop changed');
native=native.replace(nativeNeedle,`        attachmentExpandedBytes += chunk.byteLength;
        if (attachmentExpandedBytes > 16777216) { process.stderr.write('expanded PDF limit\\n'); process.exit(65); }
${nativeNeedle}`);
source=source.slice(0,nativeStart)+native+source.slice(nativeEnd);
writeFileSync(path,source);
