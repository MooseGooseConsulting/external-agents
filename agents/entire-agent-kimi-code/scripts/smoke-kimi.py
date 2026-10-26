"""Real local lifecycle proof. Requires installed adapter/hooks and authenticated Kimi.

Uses existing authentication in place; never copies credentials or rewrites config.
Run: py -3 scripts/smoke-kimi.py --output <evidence-directory>
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument('--output', required=True)
args = parser.parse_args()
out = Path(args.output).resolve()
out.mkdir(parents=True, exist_ok=True)
repo = Path(tempfile.mkdtemp(prefix='entire-kimi-smoke-'))
env = dict(os.environ)
# Avoid resolving the enclosing coding agent's session instead of the Kimi test.
for key in ('CODEX_SESSION_ID', 'CODEX_THREAD_ID', 'CLAUDE_SESSION_ID', 'AI_AGENT'):
    env.pop(key, None)

def run(*cmd, timeout=180):
    p = subprocess.run(cmd, cwd=repo, env=env, capture_output=True, timeout=timeout)
    if p.returncode:
        raise RuntimeError(f'{cmd[0]} {cmd[1]} exited {p.returncode}: {p.stderr.decode(errors="replace")}')
    return p.stdout

run('git', 'init')
run('git', 'config', 'user.name', 'Kimi Integration Test')
run('git', 'config', 'user.email', 'integration@example.invalid')
run('git', 'commit', '--allow-empty', '-m', 'initial')
run('git', 'switch', '-c', 'kimi-smoke')
run('entire', 'enable', '--agent', 'kimi-code', '--local', '--telemetry=false', '--skip-push-sessions')
prompt = 'Create exactly one file proof.txt containing ENTIRE_KIMI_SMOKE_OK using Write. Do not use git or subagents. Answer SMOKE_READY.'
console = run('kimi', '-p', prompt)
assert (repo/'proof.txt').read_text() == 'ENTIRE_KIMI_SMOKE_OK'
current = json.loads(run('entire', 'session', 'current', '--json'))
(out/'session-current.json').write_text(json.dumps(current, indent=2))
run('git', 'add', 'proof.txt')
run('git', 'commit', '-m', 'verify Kimi checkpoint')
checkpoint = json.loads(run('entire', 'checkpoint', 'explain', 'HEAD', '--json'))
assert checkpoint['files_touched'] == ['proof.txt'], checkpoint
session = checkpoint['sessions'][0]
assert session['agent'] == 'Kimi Code'
assert session['token_usage']['api_call_count'] > 0
sid = session['session_id']
transcript = run('entire', 'checkpoint', 'explain', 'HEAD', '--transcript')
assert prompt.encode() in transcript and b'SMOKE_READY' in transcript
assert b'"name":"Write"' in transcript or b'"name": "Write"' in transcript
compact = run('entire', 'checkpoint', 'explain', 'HEAD', '--full', '--no-pager')
assert b'[User]' in compact and b'[Assistant] SMOKE_READY' in compact
resume = json.loads(run('entire-agent-kimi-code', 'format-resume-command', '--session-id', sid))
assert resume['command'] == f'kimi --session {sid}'
console += run('kimi', '--session', sid, '-p', 'Edit proof.txt to append a newline followed by RESUMED_OK. Use Edit, do not use git or subagents. Answer RESUME_READY.')
assert (repo/'proof.txt').read_text().endswith('\nRESUMED_OK')
run('git', 'add', 'proof.txt')
run('git', 'commit', '-m', 'verify resumed Kimi checkpoint')
resumed = json.loads(run('entire', 'checkpoint', 'explain', 'HEAD', '--json'))
assert resumed['sessions'][0]['session_id'] == sid
resumed_transcript = run('entire', 'checkpoint', 'explain', 'HEAD', '--transcript')
def call_count(data):
    return sum(json.loads(line).get('type') == 'usage.record' for line in data.splitlines() if line.strip())
expected_new_calls = call_count(resumed_transcript) - call_count(transcript)
assert expected_new_calls > 0
assert resumed['sessions'][0]['token_usage']['api_call_count'] == expected_new_calls, (resumed, expected_new_calls)
resume_output = run('entire', 'session', 'resume', 'kimi-smoke')
assert f'kimi --session {sid}'.encode() in resume_output, resume_output
chunks = json.loads(subprocess.run(['entire-agent-kimi-code', 'chunk-transcript', '--max-size', '31'], input=transcript, capture_output=True, check=True).stdout)
joined = subprocess.run(['entire-agent-kimi-code', 'reassemble-transcript'], input=json.dumps(chunks).encode(), capture_output=True, check=True).stdout
assert joined == transcript
(out/'checkpoint.json').write_text(json.dumps(checkpoint, indent=2))
(out/'resumed-checkpoint.json').write_text(json.dumps(resumed, indent=2))
(out/'transcript.jsonl').write_bytes(transcript)
(out/'compact.txt').write_bytes(compact)
(out/'console.txt').write_bytes(console)
(out/'entire-resume.txt').write_bytes(resume_output)
summary = {'repo': str(repo), 'kimi_version': run('kimi','--version').decode().strip(), 'entire_version': run('entire','version').decode().strip(), 'checkpoint': checkpoint['checkpoint_id'], 'session': sid, 'resumed_checkpoint': resumed['checkpoint_id'], 'transcript_sha256': hashlib.sha256(transcript).hexdigest(), 'new_calls_in_resumed_checkpoint': expected_new_calls, 'checks': ['real Write','checkpoint metadata','prompt/tool/assistant transcript','token usage','byte chunk roundtrip','real resumed Edit','incremental token delta','Entire session resume']}
(out/'result.json').write_text(json.dumps(summary, indent=2))
print(json.dumps(summary, indent=2))
