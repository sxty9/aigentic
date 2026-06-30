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
}

// The single aigentic result, shared by all four kinds.
export interface Result {
  output: string;
  engine?: string;
  model?: string;
  usage?: { inputTokens?: number; outputTokens?: number; totalTokens?: number; truncated?: boolean };
  context?: { path: string; ref?: string; bytes?: number; skipped?: string }[];
  decision?: { picked: string; complexity?: string; reason?: string; source: string; fallback?: boolean };
}

// A prizm Response: Header₀ + opaque Data₀. For aigentic, Data is a Result.
export interface RunResponse {
  header: { kind: string; id?: string };
  data: Result;
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
