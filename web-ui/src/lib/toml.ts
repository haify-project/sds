/**
 * Helpers for reading drbd-reactor promoter TOML in the HA OCF agent editor.
 */

/**
 * Extract the items of the `start = [ ... ]` array from arbitrary TOML.
 * Handles multi-line arrays and both quote styles. Returns null if no start
 * array is found.
 */
export function extractStartArrayItems(content: string): string[] | null {
  // Locate `start` assignment (start of line, optional whitespace).
  const startMatch = content.match(/^[ \t]*start[ \t]*=[ \t]*\[/m);
  if (!startMatch || startMatch.index === undefined) return null;

  const openIndex = content.indexOf('[', startMatch.index);
  // Scan to the matching closing bracket, respecting quotes.
  let depth = 0;
  let inQuote: '"' | "'" | null = null;
  let endIndex = -1;
  for (let i = openIndex; i < content.length; i++) {
    const ch = content[i];
    if (inQuote) {
      if (ch === inQuote) inQuote = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      inQuote = ch;
    } else if (ch === '[') {
      depth++;
    } else if (ch === ']') {
      depth--;
      if (depth === 0) {
        endIndex = i;
        break;
      }
    }
  }
  if (endIndex === -1) return null;

  const inner = content.slice(openIndex + 1, endIndex);
  // Extract quoted strings ("..." or '...').
  const items: string[] = [];
  const itemRegex = /"((?:[^"\\]|\\.)*)"|'([^']*)'/g;
  let m: RegExpExecArray | null = itemRegex.exec(inner);
  while (m) {
    const raw = m[1] !== undefined ? m[1].replace(/\\(.)/g, '$1') : m[2];
    const trimmed = raw.trim();
    if (trimmed) items.push(trimmed);
    m = itemRegex.exec(inner);
  }
  return items;
}

/**
 * Render a configured OCF agent as its promoter `start[]` line:
 *   ocf:<provider>:<name> <instance> k=v k='v v' ...
 * Values containing whitespace or commas are single-quoted. Empty params are
 * skipped. Mirrors how the backend composes the start array.
 */
export function ocfAgentToStartLine(agent: {
  provider: string;
  name: string;
  instance: string;
  params: Record<string, string>;
}): string {
  const head = `ocf:${agent.provider}:${agent.name} ${agent.instance}`;
  const paramStr = Object.entries(agent.params)
    .filter(([, value]) => value !== undefined && value !== '')
    .map(([key, value]) => {
      const v = String(value);
      if (v.includes(' ') || v.includes(',')) {
        return `${key}='${v}'`;
      }
      return `${key}=${v}`;
    })
    .join(' ');
  return paramStr ? `${head} ${paramStr}` : head;
}
