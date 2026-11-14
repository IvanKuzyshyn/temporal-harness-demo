import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { SaxesParser } from 'saxes';
import yauzl from 'yauzl';
import { Resvg } from '@resvg/resvg-js';

const require = createRequire(import.meta.url);
const EXPANDED = 16777216;
const CHARS = 200000;
const WORD_NS = 'http://schemas.openxmlformats.org/wordprocessingml/2006/main';
const SVG_NS = 'http://www.w3.org/2000/svg';
const empty = () => ({ status: 'completed', text: '', pages: 0, truncated: false });
function fail() {
  throw Error('invalid, unreadable or resource-limited document');
}

function xml(data, onOpen = () => {}, onText = () => {}, onClose = () => {}) {
  const parser = new SaxesParser({ xmlns: true });
  let depth = 0;
  let nodes = 0;
  function validateElement(node) {
    if (++depth > 64 || ++nodes > 200000 || Object.keys(node.attributes).length > 64) fail();
    onOpen(node);
  }
  function closeElement(node) {
    onClose(node);
    --depth;
  }
  parser.on('doctype', fail);
  parser.on('error', fail);
  parser.on('opentag', validateElement);
  parser.on('text', onText);
  parser.on('cdata', onText);
  parser.on('closetag', closeElement);
  parser.write(data).close();
}

function accumulator(out) {
  let count = 0;
  return function appendBoundedText(text) {
    if (!text) return;
    const chars = Array.from(text);
    const room = CHARS - count;
    if (chars.length > room) out.truncated = true;
    out.text += chars.slice(0, room).join('');
    count += Math.min(room, chars.length);
  };
}

async function pdf(data, extract) {
  // Byte-array only. No JS/actions/forms/rendering or remote resources.
  const { getDocument } = await import('pdfjs-dist/legacy/build/pdf.mjs');
  const base = dirname(require.resolve('pdfjs-dist/package.json'));
  const task = getDocument({
    data: new Uint8Array(data), isEvalSupported: false, useWasm: false,
    useSystemFonts: false, disableFontFace: true, stopAtErrors: true,
    standardFontDataUrl: join(base, 'standard_fonts') + '/', verbosity: 1,
  });
  let doc;
  try {
    doc = await task.promise;
  } catch (error) {
    if (error.name === 'PasswordException') {
      return { ...empty(), status: 'unsupported', reason: 'encrypted_pdf' };
    }
    throw error;
  }
  try {
    const out = empty();
    out.pages = doc.numPages;
    out.truncated = doc.numPages > 100;
    out.sections = [];
    const add = accumulator(out);
    for (let i = 1; i <= Math.min(100, doc.numPages); i++) {
      const page = await doc.getPage(i);
      if (extract) {
        const start = Array.from(out.text).length;
        const content = await page.getTextContent({ disableNormalization: false });
        for (const item of content.items) {
          if (typeof item.str === 'string') add(item.str + (item.hasEOL ? '\n' : ' '));
        }
        out.sections.push({ page: i, start, end: Array.from(out.text).length });
      }
      page.cleanup();
    }
    if (extract && !out.text.trim()) {
      out.status = 'unsupported';
      out.reason = 'image_only_or_empty_pdf';
    }
    return out;
  } finally {
    await task.destroy();
  }
}

function readWordDocument(text, extract, add) {
  let inText = 0;
  let root = false;
  function openWordElement(node) {
    if (node.local === 'document' && node.uri === WORD_NS) root = true;
    if (node.local === 't' && node.uri === WORD_NS) inText++;
  }
  function readWordText(value) {
    if (extract && inText) add(value);
  }
  function closeWordElement(node) {
    if (node.local === 't' && node.uri === WORD_NS) inText--;
    if (extract && node.local === 'p') add('\n');
  }
  xml(text, openWordElement, readWordText, closeWordElement);
  if (!root) fail();
}

async function docx(data, extract) {
  const archive = await new Promise((resolve, reject) => {
    yauzl.fromBuffer(data, {
      lazyEntries: true, validateEntrySizes: true, strictFileNames: true,
    }, (error, zip) => error ? reject(error) : resolve(zip));
  });
  const out = empty();
  const add = accumulator(out);
  let total = 0, declared = 0, entries = 0, document = false, types = false;
  const names = new Set();
  function validateContentType(node) {
    if (node.local === 'Override' && node.attributes.PartName?.value === '/word/document.xml' &&
        node.attributes.ContentType?.value === 'application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml') {
      types = true;
    }
  }
  async function readEntry(entry) {
    if (++entries > 1024 || names.has(entry.fileName) || entry.fileName.startsWith('/') ||
        entry.fileName.split('/').includes('..') || (entry.generalPurposeBitFlag & 1)) fail();
    names.add(entry.fileName);
    declared += entry.uncompressedSize;
    if (declared > EXPANDED) fail();
    if (entry.fileName.endsWith('/')) {
      archive.readEntry();
      return;
    }
    const stream = await new Promise((resolve, reject) => {
      archive.openReadStream(entry, (error, result) => error ? reject(error) : resolve(result));
    });
    const chunks = [];
    let size = 0;
    for await (const chunk of stream) {
      total += chunk.length;
      size += chunk.length;
      if (total > EXPANDED) fail();
      if (entry.fileName.endsWith('.xml') || entry.fileName.endsWith('.rels')) chunks.push(chunk);
    }
    if (size !== entry.uncompressedSize) fail();
    if (chunks.length) {
      const text = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks));
      if (entry.fileName === 'word/document.xml') {
        readWordDocument(text, extract, add);
        document = true;
      } else if (entry.fileName === '[Content_Types].xml') {
        xml(text, validateContentType);
      } else {
        xml(text); // Parse bounded XML only. Relationships are NEVER followed.
      }
    }
    archive.readEntry();
  }
  await new Promise((resolve, reject) => {
    function abort(error) {
      archive.close();
      reject(error);
    }
    archive.on('error', abort);
    archive.on('end', resolve);
    archive.on('entry', entry => { readEntry(entry).catch(abort); });
    archive.readEntry();
  });
  if (!document || !types) fail();
  if (extract && !out.text.trim()) {
    out.status = 'unsupported';
    out.reason = 'empty_document';
  }
  return out;
}

function svg(data, render) {
  const text = new TextDecoder('utf-8', { fatal: true }).decode(data);
  // Narrow static-vector subset: no href/use/image/CSS/fonts/filter/animation,
  // foreignObject, entity loading, or resource resolution of any kind.
  const tags = new Set(['svg', 'g', 'path', 'rect', 'circle', 'ellipse', 'line', 'polyline', 'polygon', 'title', 'desc']);
  const attrs = new Set(['width', 'height', 'viewBox', 'x', 'y', 'x1', 'y1', 'x2', 'y2', 'cx', 'cy', 'r', 'rx', 'ry', 'd', 'points', 'fill', 'stroke', 'stroke-width', 'fill-rule', 'stroke-linecap', 'stroke-linejoin', 'opacity', 'fill-opacity', 'stroke-opacity', 'transform', 'xmlns']);
  let root = false, rootWidth = 0, rootHeight = 0;
  function validateSVGAttribute(attribute) {
    if (attribute.name === 'xmlns' && attribute.value === SVG_NS) return;
    if (!attrs.has(attribute.name) || /url\s*\(|https?:|file:|data:|@|[\x00-\x08]/i.test(attribute.value)) fail();
  }
  function validateSVGElement(node) {
    if (node.uri !== SVG_NS || !tags.has(node.local)) fail();
    if (!root) {
      if (node.local !== 'svg') fail();
      const width = Number(node.attributes.width?.value);
      const height = Number(node.attributes.height?.value);
      if (!Number.isFinite(width) || !Number.isFinite(height) || width < 1 || height < 1 ||
          width > 8192 || height > 8192 || width * height > 20000000) fail();
      rootWidth = width;
      rootHeight = height;
      root = true;
    }
    for (const attribute of Object.values(node.attributes)) validateSVGAttribute(attribute);
  }
  xml(text, validateSVGElement);
  if (!root) fail();
  const renderer = new Resvg(text, {
    font: { loadSystemFonts: false },
    fitTo: rootWidth >= rootHeight
      ? { mode: 'width', value: Math.min(2048, Math.ceil(rootWidth)) }
      : { mode: 'height', value: Math.min(2048, Math.ceil(rootHeight)) },
  });
  if (renderer.imagesToResolve().length) fail();
  const image = renderer.render();
  if (image.width > 2048 || image.height > 2048) fail();
  const out = empty();
  if (render) {
    const png = image.asPng();
    if (png.length > 1048576) fail();
    out.raster = png.toString('base64');
  }
  return out;
}

try {
  let size = 0;
  const chunks = [];
  for await (const chunk of process.stdin) {
    size += chunk.length;
    if (size > 1500000) fail();
    chunks.push(chunk);
  }
  const request = JSON.parse(Buffer.concat(chunks));
  const data = Buffer.from(request.data, 'base64');
  if (data.length > 1048576) fail();
  let out;
  if (request.media === 'application/pdf') out = await pdf(data, request.extract === true);
  else if (request.media === 'application/vnd.openxmlformats-officedocument.wordprocessingml.document') out = await docx(data, request.extract === true);
  else if (request.media === 'image/svg+xml') out = svg(data, request.render === true);
  else fail();
  if (!request.extract) out.text = '';
  process.stdout.write(JSON.stringify(out));
} catch (error) {
  process.stderr.write(String(error?.name || 'ParserError') + ': rejected document\n');
  process.exitCode = 65;
}
