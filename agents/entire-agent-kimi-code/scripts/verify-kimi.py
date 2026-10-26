import json, os, pathlib, subprocess, sys, time

root = pathlib.Path(__file__).resolve().parent
if len(sys.argv) > 1 and sys.argv[1] == 'capture':
    event = json.load(sys.stdin)
    out = pathlib.Path(os.environ['KIMI_PROBE_OUT'])
    out.mkdir(parents=True, exist_ok=True)
    (out / (sys.argv[2] + '-' + str(time.time_ns()) + '.json')).write_text(json.dumps(event), encoding='utf-8')
    sys.exit(0)

repo = root / 'kimi-probe-repo'
repo.mkdir(exist_ok=True)
for args in [('init',), ('config','user.name','Integration Test'), ('config','user.email','integration@example.invalid'), ('commit','--allow-empty','-m','initial')]:
    subprocess.run(['git',*args],cwd=repo,check=True,capture_output=True)
out = root / 'kimi-captures'
os.environ['KIMI_PROBE_OUT'] = str(out)
home = pathlib.Path(os.environ.get('KIMI_CODE_HOME', str(pathlib.Path.home()/'.kimi-code')))
config = home/'config.toml'
original = config.read_bytes()
events = ['SessionStart','UserPromptSubmit','TurnStarted','Stop','StopFailure','Interrupt','PreCompact','PostCompact','SessionEnd','SubagentStart','SubagentStop']
addition = '\n# BEGIN temporary Entire research probe\n'
for event in events:
    command = f'py -3 "{__file__}" capture {event}'
    addition += '\n[[hooks]]\nevent = '+json.dumps(event)+'\ncommand = '+json.dumps(command)+'\ntimeout = 10\n'
installed = original + addition.encode()
config.write_bytes(installed)
try:
    r = subprocess.run(['kimi','-p','Create exactly one file named kimi-integration-probe.txt containing ENTIRE_KIMI_PROBE_20261025. Use the Write tool. Do not run git or spawn subagents. Then answer PROBE_DONE.'],cwd=repo,capture_output=True,timeout=180)
    (root/'probe-console.txt').write_bytes(r.stdout+r.stderr)
    print('Kimi exit:',r.returncode,'capture files:',len(list(out.glob('*.json'))))
finally:
    if config.read_bytes() != installed:
        raise RuntimeError('Config changed concurrently; retaining probe for safe manual reconciliation')
    config.write_bytes(original)
    print('Original configuration restored byte-for-byte')
