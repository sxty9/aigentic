import { useEffect, useMemo, useState } from 'react';
import { SegmentedControl, Stack, Text, type ServiceApiClient } from '@holisdk/ui';
import type { ModelCatalog } from './types';

// The engine/model/effort picker for the Files/Mail "Ask AI" one-shot panels. The full chat tab
// uses the shared <Chat> from @holisdk/ui (whose own EnginePicker covers machine+model); the two
// reuse the SAME engine + model constants below so they never drift. Design goal: no ambiguous
// "Default" — you always see the concrete model that will run. Model + Effort are shown only where
// they apply.
//
// The engines aigentic can route to, in preference order (Auto first). Exported so the shared
// chat's ChatAdapter builds the SAME machine list without duplicating it.
export const ENGINES = [
  { value: 'choose', label: 'Auto' },
  { value: 'ollama', label: 'Local' },
  { value: 'claude-cli', label: 'Claude CLI' },
  { value: 'claude-api', label: 'Claude API' },
] as const;
// Used until GET /models answers (and if it can't list the static Claude set). Exported so the
// chat adapter falls back to the SAME Claude list.
export const CLAUDE_FALLBACK = [
  { id: 'claude-sonnet-4-6', label: 'Sonnet' },
  { id: 'claude-opus-4-8', label: 'Opus' },
  { id: 'claude-haiku-4-5-20251001', label: 'Haiku' },
];
// Effort applies to both Claude engines — the API (output_config.effort) and the CLI
// (--effort) — but not ollama. "Auto" = send no override, let the model pick; the rest are the
// CLI/API levels low|medium|high|xhigh|max. Exported so the chat's effort control offers the same
// levels.
export const EFFORTS = [
  { value: '', label: 'Auto' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Med' },
  { value: 'high', label: 'High' },
  { value: 'xhigh', label: 'X-High' },
  { value: 'max', label: 'Max' },
] as const;

// usesEffort — effort only reaches the two Claude engines; ollama/Auto ignore it. Exported so the
// chat adapter gates the effort override the same way.
export const usesEffort = (engine: string) => engine === 'claude-cli' || engine === 'claude-api';

export interface Picker {
  engine: string;
  setEngine: (v: string) => void;
  model: string;
  setModel: (v: string) => void;
  effort: string;
  setEffort: (v: string) => void;
  modelOptions: { value: string; label: string }[];
  catalog: ModelCatalog | null;
}

// usePicker owns the picker state + the host-specific model catalog (which ollama models are
// pulled), keeping the selected model valid for the chosen engine.
export function usePicker(apiFor: (id: string) => ServiceApiClient): Picker {
  const [engine, setEngine] = useState<string>('choose');
  const [model, setModel] = useState<string>('');
  const [effort, setEffort] = useState<string>('');
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);

  const modelOptions = useMemo(() => {
    if (engine === 'ollama') return (catalog?.ollama ?? []).map((m) => ({ value: m, label: m }));
    if (engine === 'claude-cli' || engine === 'claude-api')
      return (catalog?.claude ?? CLAUDE_FALLBACK).map((m) => ({ value: m.id, label: m.label }));
    return [];
  }, [engine, catalog]);

  useEffect(() => {
    apiFor('aigentic')
      .get<ModelCatalog>('models')
      .then(setCatalog)
      .catch(() => {});
  }, [apiFor]);

  // Always land on a concrete model for the engine (no ambiguous "Default"), or clear for Auto.
  useEffect(() => {
    setModel((cur) => (modelOptions.some((o) => o.value === cur) ? cur : (modelOptions[0]?.value ?? '')));
  }, [modelOptions]);

  return { engine, setEngine, model, setModel, effort, setEffort, modelOptions, catalog };
}

// pickerFields maps the picker state to the request's optional model/effort overrides.
export function pickerFields(p: Picker): { model?: string; claude?: { effort: string } } {
  const out: { model?: string; claude?: { effort: string } } = {};
  if (p.model) out.model = p.model;
  if (p.effort && usesEffort(p.engine)) out.claude = { effort: p.effort };
  return out;
}

// EnginePicker renders the three controls; Model/Effort appear only where meaningful.
export function EnginePicker({ p, compact }: { p: Picker; compact?: boolean }) {
  return (
    <Stack direction="row" gap={3} align="end" className="flex-wrap">
      <Stack gap={1}>
        {!compact && (
          <Text variant="caption" color="tertiary">
            Engine
          </Text>
        )}
        <SegmentedControl value={p.engine} onChange={p.setEngine} options={ENGINES.map((e) => ({ value: e.value, label: e.label }))} />
      </Stack>

      {p.engine === 'choose' ? (
        <Text variant="caption" color="tertiary" className="pb-2">
          Auto picks the engine &amp; model for you.
        </Text>
      ) : p.modelOptions.length > 0 ? (
        <Stack gap={1}>
          {!compact && (
            <Text variant="caption" color="tertiary">
              Model
            </Text>
          )}
          <SegmentedControl value={p.model} onChange={p.setModel} options={p.modelOptions} />
        </Stack>
      ) : p.engine === 'ollama' ? (
        <Text variant="caption" color="tertiary" className="pb-2">
          No local models pulled on the server.
        </Text>
      ) : null}

      {usesEffort(p.engine) && (
        <Stack gap={1}>
          {!compact && (
            <Text variant="caption" color="tertiary">
              Effort
            </Text>
          )}
          <SegmentedControl value={p.effort} onChange={p.setEffort} options={EFFORTS.map((e) => ({ value: e.value, label: e.label }))} />
        </Stack>
      )}
    </Stack>
  );
}
