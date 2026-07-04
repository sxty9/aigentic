// Helpers shared by the two "Ask AI" panels: the folder-scope one (Files toolbar) and the
// single-file one mounted in the shared FilePreview (Files + Mail attachments).
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

// cleanOutput strips the context tags the backend wraps files in, before rendering.
export function cleanOutput(s: string): string {
  return s
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
