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
  outputFormat?: string;
  model?: string;
  maxTokens?: number;
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

// Non-secret status of the admin-managed Anthropic API key (GET/POST /secret). The key value
// itself never crosses back; only a masked hint (sk-ant-…last4) and its source do.
export interface SecretStatus {
  configured: boolean;
  source?: 'store' | 'env';
  hint?: string;
}
