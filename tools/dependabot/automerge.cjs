'use strict';

const { execFileSync } = require('node:child_process');
const { readFileSync } = require('node:fs');
const { eligible } = require('./policy.cjs');

function api(method, path, body) {
  const args = ['api', '--method', method, path];
  if (body) args.push('--input', '-');
  return JSON.parse(execFileSync('gh', args, {
    encoding: 'utf8', input: body ? JSON.stringify(body) : undefined,
    timeout: 30000, maxBuffer: 2 * 1024 * 1024,
  }));
}

async function run({ api, event, eventName, repository, dryRun }) {
  if (repository !== 'nkiyohara/corresync') throw new Error('Unexpected repository');
  const prefix = `repos/${repository}`;
  const workflow = await api('GET', `${prefix}/actions/workflows/ci.yml`);
  let numbers;
  if (eventName === 'workflow_run') {
    const run = event.workflow_run;
    if (run.conclusion !== 'success' || run.event !== 'pull_request' ||
        run.workflow_id !== workflow.id || run.head_repository?.full_name !== repository) return [];
    numbers = run.pull_requests.map(pr => pr.number);
  } else if (eventName === 'workflow_dispatch') {
    const number = String(event.inputs?.pull_request ?? '');
    if (!/^[1-9]\d{0,8}$/.test(number)) throw new Error('A valid PR number is required');
    numbers = [Number(number)];
  } else {
    throw new Error('Unsupported event');
  }
  const results = [];
  for (const number of numbers) {
    if (!Number.isSafeInteger(number) || number <= 0) throw new Error('Invalid PR number');
    const pr = await api('GET', `${prefix}/pulls/${number}`);
    // Do not fetch manifests from forks or non-bot PRs, even for a dry run.
    if (pr.state !== 'open' || pr.draft || pr.user?.login !== 'dependabot[bot]' || pr.user?.type !== 'Bot' ||
        pr.base?.ref !== 'main' || pr.base?.repo?.full_name !== repository ||
        pr.head?.repo?.full_name !== repository || !pr.head?.ref?.startsWith('dependabot/go_modules/')) {
      results.push({ number, status: 'manual review' });
      continue;
    }
    if (!/^[a-f0-9]{40}$/.test(pr.head.sha) || !/^[a-f0-9]{40}$/.test(pr.base.sha)) throw new Error('Invalid revision');
    if (eventName === 'workflow_run' && pr.head.sha !== event.workflow_run.head_sha) {
      results.push({ number, status: 'head changed; wait for new CI' });
      continue;
    }
    const files = await api('GET', `${prefix}/pulls/${number}/files?per_page=100`);
    if (files.length !== pr.changed_files || files.some(file => !['go.mod', 'go.sum'].includes(file.filename))) {
      results.push({ number, status: 'manual review' });
      continue;
    }
    async function content(path, sha) {
      const file = await api('GET', `${prefix}/contents/${path}?ref=${sha}`);
      if (file.type !== 'file' || file.encoding !== 'base64' || file.size > 262144 || typeof file.content !== 'string') {
        throw new Error('Unexpected manifest response');
      }
      return Buffer.from(file.content, 'base64').toString('utf8');
    }
    const [beforeMod, afterMod, beforeSum, afterSum] = await Promise.all([
      content('go.mod', pr.base.sha), content('go.mod', pr.head.sha),
      content('go.sum', pr.base.sha), content('go.sum', pr.head.sha),
    ]);
    if (!eligible({ pr, files, beforeMod, afterMod, beforeSum, afterSum, repository })) {
      results.push({ number, status: 'manual review' });
      continue;
    }
    const runs = await api('GET', `${prefix}/actions/workflows/${workflow.id}/runs?head_sha=${pr.head.sha}&event=pull_request&per_page=1`);
    const latest = runs.workflow_runs[0];
    if (!latest || latest.head_sha !== pr.head.sha || latest.status !== 'completed' || latest.conclusion !== 'success') {
      results.push({ number, status: 'wait for successful CI' });
      continue;
    }
    if (dryRun) {
      results.push({ number, status: 'eligible (dry run)' });
      continue;
    }
    // Merge exactly the inspected revision, never arm auto-merge on future pushes.
    // GitHub enforces required checks, up-to-date base and conversation resolution.
    const merged = await api('PUT', `${prefix}/pulls/${number}/merge`, { sha: pr.head.sha, merge_method: 'squash' });
    if (!merged.merged) throw new Error(`PR ${number} was not merged`);
    results.push({ number, status: 'merged', sha: merged.sha });
  }
  return results;
}

if (require.main === module) {
  const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'));
  run({ api, event, eventName: process.env.GITHUB_EVENT_NAME,
    repository: process.env.GITHUB_REPOSITORY,
    dryRun: process.env.GITHUB_EVENT_NAME === 'workflow_dispatch' && event.inputs?.dry_run !== 'false',
  }).then(results => console.log(JSON.stringify(results, null, 2))).catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}

module.exports = { run };
