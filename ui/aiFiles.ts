// Pure (no-JSX, no-DOM) helpers shared across every aigentic AI surface — the folder + single-file
// "Ask AI" panels and the chat tab: the ONE file→inline-part classifier, the ONE Holistic-fs reader,
// browser-File encoding, answer cleaning, and readability. Keeping these here is what lets every
// surface reuse the same rules instead of re-deriving them.
import type { FileEntry, ServiceApiClient, TextPayload } from '@holistic/ui';

// An inline file part sent to the backend: text rides in `content`; images/PDFs ride as base64 in
// `content` with a `mediaType` (image/png, application/pdf, …).
export type InlinePart = { path: string; content: string; mediaType?: string };

// Web image types a Claude model reads as vision. On a local (ollama) run these have nothing the
// model can read, so the backend lists them by name only.
const IMAGE_RE = /^image\/(png|jpeg|gif|webp)$/;
// Extensions treated as text when the browser reports no (or a generic) MIME type — common for
// source and config files dragged straight from a desktop.
const TEXT_EXT_RE = /\.(txt|md|markdown|json|ya?ml|toml|ini|conf|cfg|csv|tsv|log|xml|html?|css|jsx?|tsx?|go|rs|py|rb|java|kt|c|h|cpp|cc|hpp|sh|bash|zsh|sql|env|tex)$/i;

// encodePath encodes a virtual fs path for the shared fs/* query endpoints (fs/list, fs/text, fs/raw).
export const encodePath = (p: string): string => encodeURIComponent(p);

// bytesToBase64 base64-encodes raw bytes in the browser, chunked to avoid call-stack limits on
// large files (String.fromCharCode(...bigArray) overflows the argument stack).
export function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}

// How a Holistic file rides to the AI: text inline, image/PDF as base64, everything else name-only.
export type InlineKind = 'text' | 'image' | 'pdf' | 'other';

// classifyEntry is the ONE place the text-vs-image-vs-pdf-vs-other rule lives for a Holistic file
// entry (its viewer + mime). Every surface that reads or gates a file — aiReadable, the folder and
// single-file "Ask AI" panels, the chat's Files picker — decides from this, so the rules never drift.
export function classifyEntry(entry: Pick<FileEntry, 'viewer' | 'mime'>): { kind: InlineKind; mediaType: string } {
  const mime = entry.mime ?? '';
  if (entry.viewer === 'text' || entry.viewer === 'markdown') return { kind: 'text', mediaType: '' };
  if (entry.viewer === 'pdf' || mime === 'application/pdf') return { kind: 'pdf', mediaType: 'application/pdf' };
  if (entry.viewer === 'image' && IMAGE_RE.test(mime)) return { kind: 'image', mediaType: mime };
  return { kind: 'other', mediaType: mime || 'application/octet-stream' };
}

// aiReadable — can a single previewed file actually be sent to the AI? (Anything classifyEntry does
// not map to 'other'.) Drives whether the file-level "Ask AI" button shows.
export function aiReadable(entry: FileEntry): boolean {
  return classifyEntry(entry).kind !== 'other';
}

// readEntryInline is the ONE reader that turns a Holistic file entry into an inline part over the
// shared fs/* endpoints — fs/text for text, fs/raw for image/PDF bytes. Used by BOTH the Files
// "Ask AI" folder traversal and the chat's Files picker, so there is no parallel read path: the
// daemon stays fs-free and the privileged share client hands over the bytes. `client` is any
// fs-capable service client (the Files app's own, or samba's). null when nothing readable.
export async function readEntryInline(client: ServiceApiClient, entry: FileEntry): Promise<InlinePart | null> {
  const { kind, mediaType } = classifyEntry(entry);
  try {
    if (kind === 'text') {
      const p = await client.get<TextPayload>(`fs/text?path=${encodePath(entry.path)}`);
      return p?.content ? { path: entry.path, content: p.content, mediaType: '' } : null;
    }
    if (kind === 'image' || kind === 'pdf') {
      const res = await client.raw(`fs/raw?path=${encodePath(entry.path)}`);
      return { path: entry.path, content: bytesToBase64(new Uint8Array(await res.arrayBuffer())), mediaType };
    }
    // Other types: counted only (named in the prompt by the backend), not read.
    return { path: entry.path, content: '', mediaType };
  } catch {
    return null;
  }
}

// cleanAnswer normalises a model reply for display: it drops the leading "Assistant:" the
// transcript framing can echo, strips the <file>/<attachment> context tags the backend wraps
// files in, collapses runs of blank lines, and trims. It is the ONE cleaner for AI answers —
// shared by the "Ask AI" panels (single-shot replies) and the chat (transcript replies + seeds)
// so the two never drift. Idempotent, so a double-clean (panel → chat handoff) is a no-op.
export function cleanAnswer(s: string): string {
  return s
    .replace(/^\s*Assistant:\s*/i, '')
    .replace(/<\/?(file|attachment)\b[^>]*>/g, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

// fileToInline turns a browser File (drag-drop or the attach button) into an inline part, reusing the
// SAME text-vs-media rules as the Files "Ask AI" flow: text rides in `content`; png/jpeg/gif/webp
// images and PDFs ride as base64 with a mediaType; anything else becomes a name-only attachment
// (counted, not read). A dropped OS file has no Holistic viewer, so it classifies by MIME/extension
// rather than via classifyEntry. Best-effort — an unreadable file yields null.
export async function fileToInline(file: File): Promise<InlinePart | null> {
  const mime = file.type || '';
  try {
    if (IMAGE_RE.test(mime)) {
      return { path: file.name, content: bytesToBase64(new Uint8Array(await file.arrayBuffer())), mediaType: mime };
    }
    if (mime === 'application/pdf' || /\.pdf$/i.test(file.name)) {
      return { path: file.name, content: bytesToBase64(new Uint8Array(await file.arrayBuffer())), mediaType: 'application/pdf' };
    }
    if (mime.startsWith('text/') || (mime === '' && TEXT_EXT_RE.test(file.name))) {
      return { path: file.name, content: await file.text(), mediaType: '' };
    }
    return { path: file.name, content: '', mediaType: mime || 'application/octet-stream' };
  } catch {
    return null;
  }
}
