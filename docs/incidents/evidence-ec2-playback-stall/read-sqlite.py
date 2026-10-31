import sqlite3,json,datetime,pathlib
def utc(t): return datetime.datetime.fromtimestamp(t/1e9,datetime.timezone.utc).isoformat()
def clean(v):
    if isinstance(v,dict):
        return {k:clean(x) for k,x in v.items() if not any(s in k.lower() for s in ('url','token','secret','session_id','stderr','payload','stack','cookie','endpoint'))}
    if isinstance(v,list): return [clean(x) for x in v]
    if isinstance(v,str) and ('http' in v.lower() or 'wss:' in v.lower()): return '[redacted URL-bearing value]'
    return v
c=sqlite3.connect('file:/home/bard/data/bard-logs.db?mode=ro',uri=True)
c.execute('BEGIN')
rows=c.execute('select id,timestamp_ns,event,guild_id,payload from logs where timestamp_ns >= ?', (int(datetime.datetime(2026,10,24,tzinfo=datetime.timezone.utc).timestamp()*1e9),)).fetchall()
print(json.dumps({'kind':'bounds','value':c.execute('select count(*),min(timestamp_ns),max(timestamp_ns) from logs').fetchone()}))
for i,t,e,g,p in rows:
    print(json.dumps({'kind':'log','id':i,'utc':utc(t),'event':e,'guild':g,'data':clean(json.loads(p))},ensure_ascii=False))
c.rollback()
c=sqlite3.connect('file:/home/bard/data/bard.db?mode=ro',uri=True)
c.execute('BEGIN')
for g,s in c.execute('select guild_id,state from guilds'):
    v=json.loads(s)
    print(json.dumps({'kind':'guild_state','guild':g,'value':clean(v)},ensure_ascii=False))
for table in ('music_catalog','tracks','history','playback_events','catalog_positions'):
    print(json.dumps({'kind':'count','table':table,'count':c.execute('select count(*) from '+table).fetchone()[0]}))
c.rollback()
print(json.dumps({'kind':'health','value':clean(json.loads(pathlib.Path('/tmp/bard-runtime-health.json').read_text()))}))
b=pathlib.Path('/usr/local/bin/bard').read_bytes()
for m in ('watchdog_recovery_triggered','playback_checkpoint_delayed','local_readiness_confirmed','voice_disconnect_initiated','process_supervision_triggered'):
    print(json.dumps({'kind':'binary_marker','marker':m,'present':m.encode() in b}))
