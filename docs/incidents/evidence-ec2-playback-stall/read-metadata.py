import subprocess,json,datetime
print(json.dumps({'collected_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}))
for a in [['yt-dlp','--version'],['deno','--version']]:
 p=subprocess.run(a,capture_output=True,text=True,timeout=5); print(json.dumps({'tool':a[0],'rc':p.returncode,'version':p.stdout.strip()}))
args=['yt-dlp','--ignore-config','--no-playlist','--no-warnings','--no-progress','--no-cache-dir','--socket-timeout','15','--retries','2','--extractor-retries','2','--js-runtimes','deno','--extractor-args','youtube:player_client=web;player_skip=js;skip=hls,dash,translated_subs','--ignore-no-formats-error','--skip-download','--dump-single-json','--ignore-errors','--','https://www.youtube.com/watch?v=MJNGXnvHYPg']
try:
 p=subprocess.run(args,capture_output=True,text=True,timeout=40)
 d=json.loads(p.stdout) if p.stdout.strip() else {}
 keys=['id','title','duration','duration_string','availability','live_status','age_limit','extractor_key','_type','channel','artist','track']
 print(json.dumps({'probe':'same_metadata_args','rc':p.returncode,'stdout_bytes':len(p.stdout),'stderr_bytes':len(p.stderr),'metadata':{k:d.get(k) for k in keys},'keys':sorted(d.keys()),'diagnostic_classes':[x for x in ['Sign in','bot','PO Token','403','429','No video formats','unavailable'] if x.lower() in p.stderr.lower()]}))
except Exception as e: print(json.dumps({'probe_error_class':type(e).__name__}))
