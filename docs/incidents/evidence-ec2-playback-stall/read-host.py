import subprocess,json,re,urllib.request
def run(args):
 try: p=subprocess.run(args,capture_output=True,text=True,timeout=35)
 except Exception as e: return {'argv':args,'error':str(e)}
 return {'argv':args,'rc':p.returncode,'out':p.stdout,'err':p.stderr}
for args in [['date','-u'],['uptime'],['free','-m'],['df','-h'],['df','-i'],['last','-x','-n','8'],['systemctl','is-active','docker'],['docker','top','bardao-production-bard-1','-eo','pid,ppid,comm,etime,stat'],['sudo','-n','journalctl','-k','--since','2026-10-25','--no-pager','-g','oom|Out of memory|Killed process'],['sudo','-n','journalctl','-u','docker','--since','2026-10-29 20:47:00','--until','2026-10-29 21:08:00','--no-pager'],['docker','inspect','bardao-production-bard-1','--format','{{json .State}}'],['docker','inspect','bardao-production-bard-1','--format','{{json .HostConfig.RestartPolicy}}'],['docker','events','--since','2026-10-29T20:47:00Z','--until','2026-10-29T21:08:00Z','--filter','container=bardao-production-bard-1','--format','{{.Time}} {{.Action}}'],['git','-C','/opt/services/bardao','rev-parse','HEAD']]:
 print(json.dumps(run(args)))
try:
 opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
 req=urllib.request.Request('http://169.254.169.254/latest/api/token',method='PUT',headers={'X-aws-ec2-metadata-token-ttl-seconds':'60'})
 token=opener.open(req,timeout=3).read().decode()
 req=urllib.request.Request('http://169.254.169.254/latest/dynamic/instance-identity/document',headers={'X-aws-ec2-metadata-token':token})
 d=json.loads(opener.open(req,timeout=3).read())
 print(json.dumps({'instance':{k:d[k] for k in ['instanceId','region','instanceType','availabilityZone','imageId']}}))
except Exception as e: print(json.dumps({'metadata_error':str(e)}))
logs=run(['docker','logs','--timestamps','--since','2026-10-29T20:47:00Z','--until','2026-10-29T21:08:00Z','bardao-production-bard-1'])
for line in (logs['out']+'\n'+logs['err']).splitlines():
 line=re.sub(r'(?:https?|wss?)://[^\s"\\]+','[REDACTED_URL]',line)
 if any(s in line.lower() for s in ['token','authorization','secret','password']): continue
 print(json.dumps({'docker_log':line}))
