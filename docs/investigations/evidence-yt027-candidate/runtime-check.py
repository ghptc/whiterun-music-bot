import socket,json,subprocess,importlib.metadata
import yt_dlp
from yt_dlp.extractor.youtube import YoutubeIE
from yt_dlp.extractor.youtube.jsc._director import initialize_jsc_director
socket.socket.connect=lambda *a,**k:(_ for _ in ()).throw(AssertionError('offline'))
opts=yt_dlp.parse_options(['--ignore-config','--no-cache-dir','--js-runtimes','deno:/usr/local/bin/deno']).ydl_opts
opts.update({'quiet':True,'cachedir':False,'no_warnings':True})
with yt_dlp.YoutubeDL(opts,auto_init=False) as dl:
 ie=YoutubeIE(dl)
 ie._jsc_director=initialize_jsc_director(ie)
 available={name:p.is_available()for name,p in ie._jsc_director.providers.items()}
 p=subprocess.run(['/usr/local/bin/deno','eval','--no-config','--no-lock','console.log(6*7)'],capture_output=True,text=True,timeout=5)
 assert p.returncode==0 and p.stdout.strip()=='42'
 assert any('deno' in k.lower() and v for k,v in available.items()),available
 print(json.dumps({'yt_dlp':importlib.metadata.version('yt-dlp'),'ejs':importlib.metadata.version('yt-dlp-ejs'),'providers_available':available,'local_deno_eval':True,'external_calls':0,'youtube_challenge_executed':False}))
