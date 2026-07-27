// Messages for the aigentic service. Registered on import (see index.tsx), exactly like every
// other Holistic service — aigentic routes ALL user-facing text through the shared @holistic/ui
// i18n core rather than hardcoding strings. en-US is the canonical source language; de is shipped
// complete (it also recovers the German that used to be hardcoded in the chat surface). ja is left
// to the nightly translation run and falls back to en-US until then.
import { registerMessages, type MessageVars } from '@holistic/ui';

const count = (v: MessageVars) => Number(v.count ?? 0);

registerMessages({
  'en-US': {
    'service.aigentic': 'Aigentic',
    'aigentic.admin': 'admin',
    'aigentic.tab.chat': 'Chat',
    'aigentic.tab.connect': 'Connect AI',
    'aigentic.loading': 'Loading…',
    'aigentic.remove': 'Remove',
    'aigentic.attach': 'Attach',
    'aigentic.attachCount': (v) => `Attach (${count(v)})`,

    // Connect tab — service panel
    'aigentic.connect.serviceTitle': 'Service',
    'aigentic.connect.serviceIntro': (v) =>
      `Link your own Claude below to use the paid engines as yourself. Without it, chat falls back to the free local engine. Processors: ${v.kinds}`,
    'aigentic.connect.serviceLoadError': 'Could not load service info.',

    // Shared Anthropic-key labels (own key + admin shared key)
    'aigentic.key.configured': 'configured',
    'aigentic.key.notConfigured': 'not configured',
    'aigentic.key.placeholder': 'sk-ant-…',
    'aigentic.key.replacePlaceholder': 'paste a new key to replace (sk-ant-…)',
    'aigentic.key.save': 'Save key',
    'aigentic.key.replace': 'Replace key',
    'aigentic.key.saveError': 'Could not save key',
    'aigentic.key.removeError': 'Could not remove key',

    // Admin shared fallback key (GlobalKeyPanel)
    'aigentic.key.sharedTitle': 'Shared fallback key (admin)',
    'aigentic.key.sharedIntro':
      'Optional shared Anthropic key used only by users who haven’t linked their own. Without it, un-linked users fall back to the free local engine. Stored server-side (0600).',
    'aigentic.key.sharedSaved': 'Shared key saved',
    'aigentic.key.sharedRemoved': 'Shared key removed',

    // Own Anthropic key (ApiKeySlot)
    'aigentic.mykey.title': 'Your Anthropic API key',
    'aigentic.mykey.own': 'your key',
    'aigentic.mykey.shared': 'using shared key',
    'aigentic.mykey.intro':
      'Bills the paid claude-api engine to your own Anthropic Console account. Create one at console.anthropic.com → API Keys. Stored server-side, never shown again.',
    'aigentic.mykey.saved': 'Your API key was saved',
    'aigentic.mykey.removed': 'Your API key was removed',

    // Claude subscription (ClaudeSlot)
    'aigentic.claude.title': 'Your Claude subscription',
    'aigentic.claude.linked': 'linked',
    'aigentic.claude.notLinked': 'not linked',
    'aigentic.claude.intro':
      'Uses your Claude Pro/Max subscription for the claude-cli engine — no API cost. One-time setup: on a computer where the claude CLI is installed and you can sign into your Claude account (your laptop/desktop — not this server), run the command below, sign in, then paste the sk-ant-oat… token it prints (valid about a year).',
    'aigentic.claude.tokenPlaceholder': 'sk-ant-oat…',
    'aigentic.claude.link': 'Link Claude',
    'aigentic.claude.replaceToken': 'Replace token',
    'aigentic.claude.unlink': 'Unlink',
    'aigentic.claude.linkedToast': 'Claude subscription linked',
    'aigentic.claude.unlinkedToast': 'Claude subscription unlinked',
    'aigentic.claude.linkError': 'Could not link Claude',
    'aigentic.claude.unlinkError': 'Could not unlink',

    // Ask AI panels (folder + single file) + shared answer chrome
    'aigentic.askAi': 'Ask AI',
    'aigentic.emptyResponse': '(empty response)',
    'aigentic.continueInChat': 'Continue in chat →',
    'aigentic.contextTruncated': 'context truncated',
    'aigentic.runError': 'AI request failed',
    'aigentic.scope.folderSelected': (v) =>
      `Folder “${v.cwd}” — ${count(v)} selected item(s) (folders are included recursively; images & PDFs are read by Claude models, other files are listed).`,
    'aigentic.scope.folderAll': (v) =>
      `Folder “${v.cwd}” — all items (folders are included recursively; images & PDFs are read by Claude models, other files are listed).`,
    'aigentic.scope.file': (v) => `“${v.name}” — text is read inline; images & PDFs are read by Claude models.`,
    'aigentic.prompt.folder': 'Summarize these files.',
    'aigentic.prompt.file': 'Summarize this file.',
    'aigentic.placeholder.folder': 'Ask the AI about these files…',
    'aigentic.placeholder.file': 'Ask the AI about this file…',
    'aigentic.note.gathering': 'Gathering files…',
    'aigentic.note.noFiles': 'No files in this folder',
    'aigentic.note.noneRead': 'Could not read any file',
    'aigentic.note.sending': (v) => `Sending ${v.read} file${Number(v.read) === 1 ? '' : 's'} (${v.total} total) to the AI…`,
    'aigentic.note.reading': 'Reading file…',
    'aigentic.note.fileUnreadable': 'This file can’t be read by the AI',
    'aigentic.note.asking': 'Asking the AI…',

    // Chat surface
    'aigentic.chat.newChat': 'New chat',
    'aigentic.chat.searchChats': 'Search chats…',
    'aigentic.chat.noMatches': 'No matching chats.',
    'aigentic.chat.noChats': 'No chats yet.',
    'aigentic.chat.deleteChat': 'Delete chat',
    'aigentic.chat.emptyTitle': 'Ask anything',
    'aigentic.chat.emptyDesc': 'Pick an engine below — Auto chooses for you — then start chatting.',
    'aigentic.chat.dropTitle': (v) => `${count(v)} file${count(v) === 1 ? '' : 's'} not attached`,
    'aigentic.chat.dropHint': 'Too many or too large (max. 25 files, 24 MB).',
    'aigentic.chat.fromFiles': 'From Files',
    'aigentic.chat.send': 'Send',
    'aigentic.chat.failed': 'Chat failed',
    'aigentic.chat.inputPlaceholder':
      'Message the AI…  (Enter to send, Shift+Enter for a new line — drag in or attach files)',
    // ollama residency readout + staged-progress labels
    'aigentic.chat.stage.thinking': 'Thinking…',
    'aigentic.chat.stage.choosing': 'Choosing engine & model…',
    'aigentic.chat.stage.generating': 'Generating response…',
    'aigentic.chat.stage.loading': 'Loading model into VRAM… (from SSD)',
    'aigentic.chat.warm': 'warm',
    'aigentic.chat.cold': 'cold',
    'aigentic.chat.partlyCpu': ' · partly CPU',
    'aigentic.chat.residencyWarm': (v) => `${v.name} · ${v.vram} VRAM · unloads in ${v.time} · ctx ${v.ctx}`,
    'aigentic.chat.residencyCold': (v) => `${v.name} · loads from SSD on next send`,

    // Files picker (attach from the Holistic share)
    'aigentic.filesPicker.title': 'Attach from Files',

    // Engine / model / effort picker
    'aigentic.picker.engine': 'Engine',
    'aigentic.picker.model': 'Model',
    'aigentic.picker.effort': 'Effort',
    'aigentic.picker.autoHint': 'Auto picks the engine & model for you.',
    'aigentic.picker.noLocal': 'No local models pulled on the server.',
    'aigentic.engine.auto': 'Auto',
    'aigentic.engine.local': 'Local',
    'aigentic.engine.cli': 'Claude CLI',
    'aigentic.engine.api': 'Claude API',
    'aigentic.effort.auto': 'Auto',
    'aigentic.effort.low': 'Low',
    'aigentic.effort.med': 'Med',
    'aigentic.effort.high': 'High',
    'aigentic.effort.xhigh': 'X-High',
    'aigentic.effort.max': 'Max',
  },

  de: {
    'service.aigentic': 'Aigentic',
    'aigentic.admin': 'Admin',
    'aigentic.tab.chat': 'Chat',
    'aigentic.tab.connect': 'KI verbinden',
    'aigentic.loading': 'Lädt…',
    'aigentic.remove': 'Entfernen',
    'aigentic.attach': 'Anhängen',
    'aigentic.attachCount': (v) => `Anhängen (${count(v)})`,

    'aigentic.connect.serviceTitle': 'Dienst',
    'aigentic.connect.serviceIntro': (v) =>
      `Verbinde unten dein eigenes Claude, um die kostenpflichtigen Engines als du selbst zu nutzen. Ohne Verbindung greift der Chat auf die kostenlose lokale Engine zurück. Prozessoren: ${v.kinds}`,
    'aigentic.connect.serviceLoadError': 'Dienstinformationen konnten nicht geladen werden.',

    'aigentic.key.configured': 'konfiguriert',
    'aigentic.key.notConfigured': 'nicht konfiguriert',
    'aigentic.key.placeholder': 'sk-ant-…',
    'aigentic.key.replacePlaceholder': 'neuen Schlüssel zum Ersetzen einfügen (sk-ant-…)',
    'aigentic.key.save': 'Schlüssel speichern',
    'aigentic.key.replace': 'Schlüssel ersetzen',
    'aigentic.key.saveError': 'Schlüssel konnte nicht gespeichert werden',
    'aigentic.key.removeError': 'Schlüssel konnte nicht entfernt werden',

    'aigentic.key.sharedTitle': 'Gemeinsamer Ersatzschlüssel (Admin)',
    'aigentic.key.sharedIntro':
      'Optionaler gemeinsamer Anthropic-Schlüssel, den nur Nutzer verwenden, die keinen eigenen verbunden haben. Ohne ihn greifen nicht verbundene Nutzer auf die kostenlose lokale Engine zurück. Serverseitig gespeichert (0600).',
    'aigentic.key.sharedSaved': 'Gemeinsamer Schlüssel gespeichert',
    'aigentic.key.sharedRemoved': 'Gemeinsamer Schlüssel entfernt',

    'aigentic.mykey.title': 'Dein Anthropic-API-Schlüssel',
    'aigentic.mykey.own': 'dein Schlüssel',
    'aigentic.mykey.shared': 'gemeinsamer Schlüssel',
    'aigentic.mykey.intro':
      'Rechnet die kostenpflichtige claude-api-Engine über dein eigenes Anthropic-Console-Konto ab. Erstelle einen Schlüssel auf console.anthropic.com → API Keys. Serverseitig gespeichert, wird nie wieder angezeigt.',
    'aigentic.mykey.saved': 'Dein API-Schlüssel wurde gespeichert',
    'aigentic.mykey.removed': 'Dein API-Schlüssel wurde entfernt',

    'aigentic.claude.title': 'Dein Claude-Abo',
    'aigentic.claude.linked': 'verbunden',
    'aigentic.claude.notLinked': 'nicht verbunden',
    'aigentic.claude.intro':
      'Nutzt dein Claude-Pro/Max-Abo für die claude-cli-Engine — ohne API-Kosten. Einmalige Einrichtung: Führe auf einem Computer, auf dem die claude-CLI installiert ist und du dich bei deinem Claude-Konto anmelden kannst (dein Laptop/Desktop — nicht dieser Server), den Befehl unten aus, melde dich an und füge dann den ausgegebenen sk-ant-oat…-Token ein (etwa ein Jahr gültig).',
    'aigentic.claude.tokenPlaceholder': 'sk-ant-oat…',
    'aigentic.claude.link': 'Claude verbinden',
    'aigentic.claude.replaceToken': 'Token ersetzen',
    'aigentic.claude.unlink': 'Trennen',
    'aigentic.claude.linkedToast': 'Claude-Abo verbunden',
    'aigentic.claude.unlinkedToast': 'Claude-Abo getrennt',
    'aigentic.claude.linkError': 'Claude konnte nicht verbunden werden',
    'aigentic.claude.unlinkError': 'Trennen fehlgeschlagen',

    'aigentic.askAi': 'KI fragen',
    'aigentic.emptyResponse': '(leere Antwort)',
    'aigentic.continueInChat': 'Im Chat fortsetzen →',
    'aigentic.contextTruncated': 'Kontext gekürzt',
    'aigentic.runError': 'KI-Anfrage fehlgeschlagen',
    'aigentic.scope.folderSelected': (v) =>
      `Ordner „${v.cwd}“ — ${count(v)} ausgewählte(s) Objekt(e) (Ordner werden rekursiv einbezogen; Bilder & PDFs werden von Claude-Modellen gelesen, andere Dateien werden aufgelistet).`,
    'aigentic.scope.folderAll': (v) =>
      `Ordner „${v.cwd}“ — alle Objekte (Ordner werden rekursiv einbezogen; Bilder & PDFs werden von Claude-Modellen gelesen, andere Dateien werden aufgelistet).`,
    'aigentic.scope.file': (v) => `„${v.name}“ — Text wird inline gelesen; Bilder & PDFs werden von Claude-Modellen gelesen.`,
    'aigentic.prompt.folder': 'Fasse diese Dateien zusammen.',
    'aigentic.prompt.file': 'Fasse diese Datei zusammen.',
    'aigentic.placeholder.folder': 'Frag die KI zu diesen Dateien…',
    'aigentic.placeholder.file': 'Frag die KI zu dieser Datei…',
    'aigentic.note.gathering': 'Dateien werden gesammelt…',
    'aigentic.note.noFiles': 'Keine Dateien in diesem Ordner',
    'aigentic.note.noneRead': 'Keine Datei konnte gelesen werden',
    'aigentic.note.sending': (v) => `${v.read} Datei${Number(v.read) === 1 ? '' : 'en'} (${v.total} insgesamt) werden an die KI gesendet…`,
    'aigentic.note.reading': 'Datei wird gelesen…',
    'aigentic.note.fileUnreadable': 'Diese Datei kann von der KI nicht gelesen werden',
    'aigentic.note.asking': 'KI wird gefragt…',

    'aigentic.chat.newChat': 'Neuer Chat',
    'aigentic.chat.searchChats': 'Chats durchsuchen…',
    'aigentic.chat.noMatches': 'Keine passenden Chats.',
    'aigentic.chat.noChats': 'Noch keine Chats.',
    'aigentic.chat.deleteChat': 'Chat löschen',
    'aigentic.chat.emptyTitle': 'Frag irgendetwas',
    'aigentic.chat.emptyDesc': 'Wähle unten eine Engine — „Auto“ wählt für dich — und beginne zu chatten.',
    'aigentic.chat.dropTitle': (v) => `${count(v)} Datei${count(v) === 1 ? '' : 'en'} nicht angehängt`,
    'aigentic.chat.dropHint': 'Zu viele oder zu groß (max. 25 Dateien, 24 MB).',
    'aigentic.chat.fromFiles': 'Aus Files',
    'aigentic.chat.send': 'Senden',
    'aigentic.chat.failed': 'Chat fehlgeschlagen',
    'aigentic.chat.inputPlaceholder':
      'Nachricht an die KI…  (Enter zum Senden, Umschalt+Enter für neue Zeile — Dateien reinziehen oder anhängen)',
    'aigentic.chat.stage.thinking': 'Denkt nach …',
    'aigentic.chat.stage.choosing': 'Wählt Engine & Modell …',
    'aigentic.chat.stage.generating': 'Generiert Antwort …',
    'aigentic.chat.stage.loading': 'Lädt Modell in den VRAM … (von SSD)',
    'aigentic.chat.warm': 'warm',
    'aigentic.chat.cold': 'kalt',
    'aigentic.chat.partlyCpu': ' · teils CPU',
    'aigentic.chat.residencyWarm': (v) => `${v.name} · ${v.vram} VRAM · entlädt in ${v.time} · ctx ${v.ctx}`,
    'aigentic.chat.residencyCold': (v) => `${v.name} · lädt beim nächsten Senden von der SSD`,

    'aigentic.filesPicker.title': 'Aus Files anhängen',

    'aigentic.picker.engine': 'Engine',
    'aigentic.picker.model': 'Modell',
    'aigentic.picker.effort': 'Aufwand',
    'aigentic.picker.autoHint': 'Auto wählt Engine & Modell für dich.',
    'aigentic.picker.noLocal': 'Keine lokalen Modelle auf dem Server geladen.',
    'aigentic.engine.auto': 'Auto',
    'aigentic.engine.local': 'Lokal',
    'aigentic.engine.cli': 'Claude CLI',
    'aigentic.engine.api': 'Claude API',
    'aigentic.effort.auto': 'Auto',
    'aigentic.effort.low': 'Niedrig',
    'aigentic.effort.med': 'Mittel',
    'aigentic.effort.high': 'Hoch',
    'aigentic.effort.xhigh': 'X-Hoch',
    'aigentic.effort.max': 'Max',
  },
});
