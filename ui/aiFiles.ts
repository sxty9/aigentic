// Pure (no-JSX, no-DOM) helpers shared across every aigentic AI surface — the folder + single-file
// "Ask AI" panels and the chat tab: file→inline-part encoding, answer cleaning, and readability.
import type { FileEntry } from '@holistic/ui';

// An inline file part sent to the backend: text rides in `content`; images/PDFs ride as base64 in
// `content` with a `mediaType` (image/png, application/pdf, …).
export type InlinePart = { path: string; content: string; mediaType?: string };

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

// aiReadable — can a single previewed file actually be sent to the AI? Text/markdown ride inline;
// PDFs and web images (png/jpeg/gif/webp) ride as base64. Everything else (audio, video, svg and
// other exotic image types, unknown binaries) has nothing the model can read, so no button shows.
export function aiReadable(entry: FileEntry): boolean {
  if (entry.viewer === 'text' || entry.viewer === 'markdown') return true;
  const mime = entry.mime ?? '';
  if (entry.viewer === 'pdf' || mime === 'application/pdf') return true;
  if (entry.viewer === 'image' && /^image\/(png|jpeg|gif|webp)$/.test(mime)) return true;
  return false;
}

// Web image types a Claude model reads as vision (same set as aiReadable). On a local (ollama) run
// these have nothing the model can read, so the backend lists them by name only.
const IMAGE_RE = /^image\/(png|jpeg|gif|webp)$/;
// Extensions treated as text when the browser reports no (or a generic) MIME type — common for
// source and config files dragged straight from a desktop.
const TEXT_EXT_RE = /\.(txt|md|markdown|json|ya?ml|toml|ini|conf|cfg|csv|tsv|log|xml|html?|css|jsx?|tsx?|go|rs|py|rb|java|kt|c|h|cpp|cc|hpp|sh|bash|zsh|sql|env|tex)$/i;

// fileToInline turns a browser File (drag-drop or the attach button) into an inline part, reusing the
// SAME text-vs-media rules as the Files "Ask AI" flow: text rides in `content`; png/jpeg/gif/webp
// images and PDFs ride as base64 with a mediaType; anything else becomes a name-only attachment
// (counted, not read). Best-effort — an unreadable file yields null.
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
