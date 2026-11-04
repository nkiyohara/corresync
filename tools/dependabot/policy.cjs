'use strict';

// Keep the routine-go-patches group in .github/dependabot.yml in sync.
const allowedModules = new Set([
  'charm.land/huh/v2',
  'charm.land/lipgloss/v2',
  'github.com/alecthomas/kong',
  'github.com/google/uuid',
  'github.com/mattn/go-shellwords',
  'github.com/pelletier/go-toml/v2',
  'github.com/posener/complete',
  'github.com/teambition/rrule-go',
  'go.yaml.in/yaml/v3',
]);

function patchChanges(before, after) {
  const oldLines = before.split('\n');
  const newLines = after.split('\n');
  if (oldLines.length !== newLines.length) return null;
  const changes = new Map();
  const requirement = /^(\s+)(\S+)(\s+)v([1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(\s*(?:\/\/ indirect)?\s*)$/;
  for (let i = 0; i < oldLines.length; i++) {
    if (oldLines[i] === newLines[i]) continue;
    const old = requirement.exec(oldLines[i]);
    const next = requirement.exec(newLines[i]);
    if (!old || !next || !allowedModules.has(old[2]) || changes.has(old[2])) return null;
    // Only the patch number may change. Reject directives, new dependencies,
    // prereleases, pseudo-versions, v0 releases and indirect dependency churn.
    for (const field of [1, 2, 3, 4, 5, 7]) {
      if (old[field] !== next[field]) return null;
    }
    if (BigInt(next[6]) <= BigInt(old[6])) return null;
    changes.set(old[2], { before: `v${old[4]}.${old[5]}.${old[6]}`, after: `v${next[4]}.${next[5]}.${next[6]}` });
  }
  return changes.size ? changes : null;
}

function parseSums(text) {
  const sums = new Map();
  for (const line of text.trimEnd().split('\n')) {
    const match = /^(\S+) (\S+) (h1:[A-Za-z0-9+/]{43}=)$/.exec(line);
    if (!match) return null;
    const key = `${match[1]} ${match[2]}`;
    if (sums.has(key)) return null;
    sums.set(key, match[3]);
  }
  return sums;
}

function allowedSums(before, after, changes) {
  const old = parseSums(before);
  const next = parseSums(after);
  if (!old || !next) return false;
  for (const [source, target, direction] of [[old, next, 'before'], [next, old, 'after']]) {
    for (const [key, hash] of source) {
      if (target.has(key)) {
        if (target.get(key) !== hash) return false;
        continue;
      }
      const [name, version] = key.split(' ');
      const change = changes.get(name);
      if (!change || (version !== change[direction] && version !== `${change[direction]}/go.mod`)) return false;
    }
  }
  return true;
}

function eligible({ pr, files, beforeMod, afterMod, beforeSum, afterSum, repository }) {
  if (pr.state !== 'open' || pr.draft || pr.user?.login !== 'dependabot[bot]' || pr.user?.type !== 'Bot' ||
      pr.base?.ref !== 'main' || pr.base?.repo?.full_name !== repository ||
      pr.head?.repo?.full_name !== repository || !pr.head?.ref?.startsWith('dependabot/go_modules/')) return false;
  if (!files.length || files.length !== pr.changed_files ||
      !files.some(file => file.filename === 'go.mod') ||
      files.some(file => file.status !== 'modified' || !['go.mod', 'go.sum'].includes(file.filename))) return false;
  const changes = patchChanges(beforeMod, afterMod);
  return changes !== null && allowedSums(beforeSum, afterSum, changes);
}

module.exports = { allowedModules, patchChanges, eligible };
