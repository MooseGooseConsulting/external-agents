"""Real background/concurrent child capture; existing authentication is used in place."""
import argparse,json,os,subprocess,tempfile
from pathlib import Path

p=argparse.ArgumentParser();p.add_argument('--output',required=True);p.add_argument('--children',type=int,default=2);args=p.parse_args()
out=Path(args.output).resolve();out.mkdir(parents=True,exist_ok=True)
repo=Path(tempfile.mkdtemp(prefix='kimi-background-'))
env=dict(os.environ)
for k in ('CODEX_SESSION_ID','CODEX_THREAD_ID','AI_AGENT'):env.pop(k,None)
def run(*cmd,check=True):
    r=subprocess.run(cmd,cwd=repo,env=env,capture_output=True,timeout=300)
    if check and r.returncode:raise RuntimeError(f'{cmd[0]} exited {r.returncode}: {r.stderr.decode(errors="replace")}')
    return r.stdout
for a in [('init',),('config','user.name','Integration Test'),('config','user.email','integration@example.invalid'),('commit','--allow-empty','-m','initial')]:run('git',*a)
run('entire','enable','--agent','kimi-code','--local','--telemetry=false','--skip-push-sessions')
tasks=' '.join(f'Child {i}: sleep 3 seconds using Bash, then use Write to create child-{i}.txt containing CHILD_{i}_OK; report CHILD_{i}_DONE.' for i in range(args.children))
prompt=f'Use the Agent tool to launch {args.children} coder subagents with run_in_background=true before waiting for any of them. {tasks} While they run, use Write to create parent.txt containing PARENT_OK. Then wait for every child with TaskOutput or the supported task-wait tool. Do not use git. Answer BACKGROUND_DONE after all children finish.'
console=run('kimi','-p',prompt);(out/'console.txt').write_bytes(console)
for i in range(args.children):assert (repo/f'child-{i}.txt').read_text().strip()==f'CHILD_{i}_OK'
assert (repo/'parent.txt').read_text().strip()=='PARENT_OK'
run('git','add',*[f'child-{i}.txt' for i in range(args.children)],'parent.txt');run('git','commit','-m','verify background child capture')
checkpoint=json.loads(run('entire','checkpoint','explain','HEAD','--json'));(out/'checkpoint.json').write_text(json.dumps(checkpoint,indent=2))
sid=checkpoint['sessions'][0]['session_id'];home=Path(env.get('KIMI_CODE_HOME',str(Path.home()/'.kimi-code')))
index=[json.loads(x) for x in (home/'session_index.jsonl').read_text(encoding='utf-8').splitlines()]
native=Path(next(x for x in index if x['sessionId']==sid)['sessionDir']);records=[json.loads(x) for x in (native/'agents/main/wire.jsonl').read_text(encoding='utf-8').splitlines()]
spawn=[x for x in records if x['type']=='subagent.spawned' and x.get('parentAgentId')=='main']
logs=[json.loads(x) for x in (repo/'.entire/logs/entire.log').read_text().splitlines()]
summary={'repo':str(repo),'session':sid,'checkpoint':checkpoint['checkpoint_id'],'spawned':[{'id':x['subagentId'],'background':x.get('runInBackground')} for x in spawn],'tasks':checkpoint.get('tasks',[]),'native_main_turns':sum(x['type']=='turn.prompt' for x in records),'entire_starts':sum(x.get('msg')=='turn-start' and x.get('session_id')==sid for x in logs),'entire_ends':sum(x.get('msg')=='turn-end' and x.get('session_id')==sid for x in logs)}
(out/'result.json').write_text(json.dumps(summary,indent=2));print(json.dumps(summary,indent=2))
assert len(spawn)==args.children and all(x.get('runInBackground') for x in spawn),'Kimi did not execute the requested background scenario'
assert len(checkpoint.get('tasks',[]))==args.children,'child task capture incomplete'
assert all(t.get('transcript_stored') for t in checkpoint['tasks']),'missing child transcript'
assert {t['agent_id'] for t in checkpoint['tasks']} == {s['subagentId'] for s in spawn},'task identities differ from native children'
for task in checkpoint['tasks']:
    child=[json.loads(x) for x in (native/'agents'/task['agent_id']/'wire.jsonl').read_text(encoding='utf-8').splitlines()]
    calls=[x['usage'] for x in child if x['type']=='usage.record']
    assert task['token_usage']['api_call_count']==len(calls),'child API calls counted incorrectly'
    for field,native_field in [('input_tokens','inputOther'),('output_tokens','output'),('cache_read_tokens','inputCacheRead'),('cache_creation_tokens','inputCacheCreation')]:
        assert task['token_usage'][field]==sum(x.get(native_field,0) for x in calls),'child token attribution mismatch'
assert summary['entire_starts']==summary['native_main_turns']==summary['entire_ends'],'child hooks corrupted parent turn boundaries'
