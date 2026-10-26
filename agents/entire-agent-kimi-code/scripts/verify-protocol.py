"""Run the upstream compliance suite in a neutral, isolated environment."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

p=argparse.ArgumentParser()
p.add_argument('binary')
args=p.parse_args()
binary=str(Path(args.binary).resolve())
fixtures_dir=Path(__file__).resolve().parent.parent/'testdata'
fixture=json.loads((fixtures_dir/'compliance.json').read_text())
fixture['transcript_analyzer']['session_ref']=str(fixtures_dir/fixture['transcript_analyzer']['session_ref'])
with tempfile.TemporaryDirectory(prefix='kimi-protocol-') as temp:
    temp=Path(temp)
    path=temp/'fixtures.json'
    path.write_text(json.dumps(fixture))
    # The protocol fixture tests use documented byte semantics and deliberately
    # replace HOME. Do not invoke a user's version-manager shim from that home.
    env=dict(os.environ,KIMI_CODE_HOME=str(temp/'kimi-home'),ENTIRE_KIMI_OFFSET_UNIT='bytes')
    result=subprocess.run(['external-agents-tests','verify',binary,'--fixtures',str(path)],cwd=temp,env=env)
    raise SystemExit(result.returncode)
