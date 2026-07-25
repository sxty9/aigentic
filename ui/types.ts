// Shapes exchanged with the backend under /api/services/aigentic/.

export interface Info {
  service: string;
  version: string;
  user: string;
  isAdmin: boolean;
  kinds: string[];
}

// The single aigentic request header (Header₁), shared by all four kinds and carried in a
// prizm Request's opaque Data₀.
export interface AigenticRequest {
  prompt: string;
  paths?: string[];
  // Caller-supplied file contents (no server fs access) — e.g. the Files app reads the user's
  // private share and passes the bytes here. Text rides in `content`; images/PDFs ride as
  // base64 in `content` with a `mediaType` (image/png, application/pdf, …).
  inline?: { path: string; content: string; mediaType?: string }[];
  outputFormat?: string;
  model?: string;
  maxTokens?: number;
  claude?: { effort?: string };
  choose?: { force?: string };
  // Let the model ask a structured multiple-choice question, answerable by clicking options in the
  // bubble (à la Claude Code). When set, a question surfaces on Result.ask; see the aigentic backend.
  interactive?: boolean;
}

// A structured question the model posed on an interactive turn — rendered as clickable options in
// the chat bubble. Mirrors the aigentic backend's Ask shape (and @holistic/ui's AskChoice props).
export interface AskOption {
  label: string;
  description?: string;
}
export interface AskQuestion {
  header?: string;
  question: string;
  options: AskOption[];
  multiSelect?: boolean;
}
export interface Ask {
  questions: AskQuestion[];
}

// The single aigentic result, shared by all four kinds.
export interface Result {
  output: string;
  engine?: string;
  model?: string;
  usage?: { inputTokens?: number; outputTokens?: number; totalTokens?: number; truncated?: boolean };
  context?: { path: string; ref?: string; bytes?: number; skipped?: string }[];
  decision?: { picked: string; complexity?: string; reason?: string; source: string; fallback?: boolean };
  ask?: Ask;
}

// A prizm Response: Header₀ + opaque Data₀. For aigentic, Data is a Result.
export interface RunResponse {
  header: { kind: string; id?: string };
  data: Result;
}

// Models the Ask AI / chat picker can offer per engine (GET /models): a static Claude list
// (used by claude-cli + claude-api) and the locally-pulled ollama models (used by the local
// engine). ollama is empty when the daemon can't reach it.
export interface ModelCatalog {
  claude: { id: string; label: string }[];
  ollama: string[];
}

// One model ollama currently holds resident (GET /ollama/status, from /api/ps). Drives the chat's
// staged progress ("loading into VRAM…" vs "generating…") and the live residency readout (VRAM,
// keep-alive time left, loaded context window) — so the user sees why the SSD spins up and how long
// a local model stays warm on the GPUs.
export interface LoadedModel {
  name: string;
  sizeBytes: number; // total resident size (VRAM + any CPU-offloaded layers)
  vramBytes: number; // bytes actually on the GPU(s)
  fullyOnGpu: boolean; // vram covers the whole model (no CPU spill)
  contextLength: number; // num_ctx the resident instance was loaded with (a change forces a reload)
  expiresAt: string; // RFC3339 keep-alive unload time ("" if none)
  expiresInSec: number; // seconds until keep-alive unload, at fetch time
}
export interface OllamaStatus {
  models: LoadedModel[];
}

// Handoff from the Files "Ask AI" dialog to the full aigentic chat tab: the answer is dropped
// in localStorage under CHAT_SEED_KEY, then the dashboard switches to aigentic and a new chat
// opens pre-seeded with this exchange. Kept text-only (no bulky inline blobs) so it fits.
export const CHAT_SEED_KEY = 'aigentic.chat.seed';
export interface ChatSeed {
  prompt: string;
  answer: string;
  engine?: string;
  model?: string;
  folder?: string;
}

// Non-secret status of an Anthropic API key (global /secret or per-user /mykey). The key value
// never crosses back; only a masked hint (sk-ant-…last4) and its source ('user' = the caller's
// own, 'store' = the shared/admin key, 'env' = the bootstrap).
export interface SecretStatus {
  configured: boolean;
  source?: 'user' | 'store' | 'env';
  hint?: string;
}

// Non-secret status of a per-user Claude subscription link (/claude). The token never crosses
// back; only whether it's linked and a masked hint.
export interface TokenStatus {
  linked: boolean;
  hint?: string;
}
