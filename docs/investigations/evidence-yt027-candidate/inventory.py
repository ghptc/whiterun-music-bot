import importlib.metadata,json,subprocess,hashlib,pathlib,platform

def sha(path):return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
def out(args):return subprocess.check_output(args,text=True).strip()
dpkg={}
for line in out(['dpkg-query','-W','-f=${Package}\t${Version}\n']).splitlines():
 key,value=line.split('\t',1);dpkg[key]=value
result={'pip_check':out(['/opt/yt-dlp/bin/python','-m','pip','check']),'architecture':platform.machine(),'os_release':pathlib.Path('/etc/os-release').read_text(),'python':platform.python_version(),'python_packages':{d.metadata['Name'].lower().replace('_','-'):d.version for d in importlib.metadata.distributions()},'debian_packages':dpkg,'component_sha256':{p:sha(p)for p in ['/usr/local/bin/bard','/usr/local/bin/deno','/usr/bin/ffmpeg','/opt/bard-validation/validation.flac']},'yt_dlp_version':out(['yt-dlp','--version']),'deno_version':out(['deno','--version']).splitlines()[0],'ffmpeg_version':out(['ffmpeg','-version']).splitlines()[0]}
print(json.dumps(result))
