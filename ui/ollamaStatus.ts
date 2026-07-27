import { useEffect, useState } from 'react';
import type { ServiceApiClient } from '@holistic/ui';
import type { LoadedModel, OllamaStatus } from './types';

// Match a resident model name to a picker selection. ollama tags are "name:tag"; an empty want
// matches anything (so a zero-config auto-detected model still lights up the readout).
function matches(resident: string, want: string): boolean {
  if (!want) return true;
  return resident === want || resident.split(':')[0] === want.split(':')[0];
}

// Staged-progress token while a reply is pending — honest, derived from /api/ps (is the model
// already resident, or does the SSD have to load it first?). Semantic, not a display string: the
// view maps it to a localized label, so this pure-logic hook stays language-free.
export type OllamaStage = 'thinking' | 'choosing' | 'generating' | 'loading';

export interface OllamaLive {
  stage: OllamaStage | null; // null when idle
  // Live residency readout for the local model (ollama engine only): warm/cold, VRAM, keep-alive
  // seconds remaining, loaded context window. null when no local model is in play.
  residency: { name: string; hot: boolean; loaded: LoadedModel | null; secondsRemaining: number } | null;
}

// useOllama mirrors ollama's live residency (GET /ollama/status → /api/ps) for the chat. It derives
// the staged-progress label while a reply is pending — "loading into VRAM (from SSD)" until the model
// is resident, then "generating" — and a live keep-alive countdown for the residency readout. It
// polls fast while busy (to catch the load→generate flip) and slow while idle (to keep the countdown
// honest). Claude engines involve no ollama, so they get a plain "thinking" label and no readout.
export function useOllama(api: ServiceApiClient, opts: { engine: string; model: string; busy: boolean }): OllamaLive {
  const { engine, model, busy } = opts;
  const local = engine === 'ollama';
  const relevant = local || engine === 'choose';
  const [snap, setSnap] = useState<{ models: LoadedModel[]; at: number } | null>(null);
  const [, tick] = useState(0); // 1s ticker so the countdown moves between polls

  useEffect(() => {
    if (!relevant) {
      setSnap(null);
      return;
    }
    let alive = true;
    let timer: ReturnType<typeof setTimeout>;
    const loop = async () => {
      try {
        const s = await api.get<OllamaStatus>('ollama/status');
        if (alive) setSnap({ models: s.models ?? [], at: Date.now() });
      } catch {
        // best-effort — keep the last snapshot, show no readout
      }
      if (alive) timer = setTimeout(loop, busy ? 1200 : 7000);
    };
    void loop();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [api, relevant, busy]);

  useEffect(() => {
    if (!relevant) return;
    const id = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [relevant]);

  let stage: OllamaStage | null = null;
  if (busy) {
    if (engine === 'claude-cli' || engine === 'claude-api') stage = 'thinking';
    else if (engine === 'choose') stage = 'choosing';
    else stage = snap?.models.some((m) => matches(m.name, model)) ? 'generating' : 'loading';
  }

  let residency: OllamaLive['residency'] = null;
  if (local && snap) {
    const name = model || snap.models[0]?.name || '';
    if (name) {
      const loaded = snap.models.find((m) => matches(m.name, name)) ?? null;
      const elapsed = (Date.now() - snap.at) / 1000;
      residency = {
        name,
        hot: !!loaded,
        loaded,
        secondsRemaining: loaded ? Math.max(0, Math.round(loaded.expiresInSec - elapsed)) : 0,
      };
    }
  }

  return { stage, residency };
}
