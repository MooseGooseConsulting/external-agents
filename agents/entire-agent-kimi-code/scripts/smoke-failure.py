"""Prove real Kimi StopFailure against a local deliberately failing provider.

No real authentication, no production config changes, no remote API requests.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

if len(sys.argv)>1 and sys.argv[1]=='capture':
    Path(os.environ['KIMI_FAILURE_CAPTURE']).write_bytes(sys.stdin.buffer.read())
    raise SystemExit(0)

class Failure(BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length',0)))
        self.send_response(400)
        self.send_header('Content-Type','application/json')
        self.end_headers()
        self.wfile.write(b'{"error":{"message":"Intentional Entire integration test failure","type":"invalid_request_error","code":"integration_test"}}')
    def log_message(self,*args): pass

root=Path(tempfile.mkdtemp(prefix='kimi-failure-'))
repo=root/'repo';repo.mkdir()
home=root/'home';home.mkdir()
server=ThreadingHTTPServer(('127.0.0.1',0),Failure)
threading.Thread(target=server.serve_forever,daemon=True).start()
config=f'''default_model = "failure-test"
[providers.failure]
type = "openai"
base_url = "http://127.0.0.1:{server.server_port}/v1"
api_key = "integration-test-only"
[models.failure-test]
provider = "failure"
model = "failure-test"
max_context_size = 32768
'''
(home/'config.toml').write_text(config)
env=dict(os.environ,KIMI_CODE_HOME=str(home),KIMI_FAILURE_CAPTURE=str(root/'StopFailure.json'))
for k in ('CODEX_SESSION_ID','CODEX_THREAD_ID','AI_AGENT'):env.pop(k,None)
def run(*cmd,check=True):
    return subprocess.run(cmd,cwd=repo,env=env,capture_output=True,check=check,timeout=90)
try:
    for args in [('init',),('config','user.name','Integration Test'),('config','user.email','integration@example.invalid'),('commit','--allow-empty','-m','initial')]:run('git',*args)
    run('entire','enable','--agent','kimi-code','--local','--telemetry=false','--skip-push-sessions')
    cmd=f'"{sys.executable}" "{Path(__file__).resolve()}" capture'
    with (home/'config.toml').open('a') as f:f.write('\n[[hooks]]\nevent = "StopFailure"\ncommand = '+json.dumps(cmd)+'\ntimeout = 10\n')
    result=run('kimi','-p','Reply with FAILURE_TEST.',check=False)
    (root/'console.txt').write_bytes(result.stdout+result.stderr)
    capture=json.loads((root/'StopFailure.json').read_text())
    assert capture['hook_event_name']=='StopFailure',capture
    # Observational hooks can outlive prompt-mode exit. Await the actual
    # lifecycle transition, retaining a bounded failure if dispatch is broken.
    deadline=time.monotonic()+15
    while True:
        logs=[json.loads(x) for x in (repo/'.entire/logs/entire.log').read_text().splitlines()]
        if any(x.get('from')=='active' and x.get('to')=='idle' for x in logs):break
        if time.monotonic()>=deadline:break
        time.sleep(.05)
    assert any(x.get('event')=='TurnEnd' for x in logs),logs
    assert any(x.get('from')=='active' and x.get('to')=='idle' for x in logs),logs
    state=json.loads((repo/'.git/entire-sessions'/f"{capture['session_id']}.json").read_text())
    assert state['phase']=='idle',state
    print(json.dumps({'evidence':str(root),'kimi_exit':result.returncode,'hook':capture['hook_event_name'],'session':capture['session_id'],'entire_turn_closed':True},indent=2))
finally:
    server.shutdown()
