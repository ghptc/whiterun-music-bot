import json,socket,inspect,hashlib,types,copy,dataclasses,urllib.parse
from unittest.mock import patch
import yt_dlp
from yt_dlp.extractor.youtube import YoutubeIE
from yt_dlp.extractor.youtube._base import INNERTUBE_CLIENTS
from yt_dlp.extractor.youtube import _video as video
from yt_dlp.version import __version__
# This runner is offline: an accidental socket connection is a test failure.
network_attempts=[]
def forbidden(*args,**kwargs):
 network_attempts.append(True);raise AssertionError('network forbidden in offline matrix')
socket.socket.connect=forbidden;socket.create_connection=forbidden
class Logger:
 def debug(self,*a):pass
 def warning(self,*a):pass
 def error(self,*a):pass

def make(profile):
 args=['--ignore-config','--no-playlist','--no-cache-dir','--js-runtimes','deno','--skip-download','--ignore-no-formats-error']
 ea={'production':'youtube:player_client=web;player_skip=js;skip=hls,dash,translated_subs','allow_js':'youtube:player_client=web;skip=hls,dash,translated_subs','defaults':'youtube:skip=hls,dash,translated_subs'}[profile]
 opts=yt_dlp.parse_options(args+['--extractor-args',ea]).ydl_opts
 opts.update({'quiet':True,'logger':Logger(),'cachedir':False})
 dl=yt_dlp.YoutubeDL(opts,auto_init=False);ie=YoutubeIE(dl)
 ie.__dict__['_jsc_director']=types.SimpleNamespace(providers={'fixture':types.SimpleNamespace(is_available=lambda:True)},bulk_solve=lambda requests:[])
 ie._skipped_webpage_data=set()
 ie._extract_player_url=lambda *a,**k:'https://fixture.invalid/player.js'
 ie._download_ytcfg=lambda *a,**k:{}
 ie.fetch_po_token=lambda *a,**k:None
 ie._download_player_url=lambda *a,**k:'https://fixture.invalid/player.js'
 ie._extract_visitor_data=lambda *a,**k:'fixture-visitor'
 ie._extract_data_sync_id=lambda *a,**k:None
 captured=[]
 def response(client,video_id,**kwargs):
  captured.append({'client':client,'player_url':kwargs.get('player_url')})
  return {'videoDetails':{'videoId':video_id,'title':'Synthetic Song','lengthSeconds':'210'},'playabilityStatus':{'status':'OK'},'streamingData':{'formats':[]}}
 ie._extract_player_response=response
 return dl,ie,captured

rows=[]
for profile in ['production','allow_js','defaults']:
 dl,ie,captured=make(profile)
 clients=ie._get_requested_clients('https://www.youtube.com/watch?v=abcdefghijk',{},False)
 # Replay every selected client using deterministic player responses; no network.
 prs,player_url=ie._extract_player_responses(clients,'abcdefghijk','','web',{},False)
 details=prs[0]['videoDetails']
 counts={}
 for kind in ['plain','signature_cipher','n_parameter']:
  fmt={'itag':18,'mimeType':'video/mp4; codecs="avc1.42001E, mp4a.40.2"','bitrate':100000,'width':320,'height':180,'quality':'tiny','audioQuality':'AUDIO_QUALITY_LOW','approxDurationMs':'210000'}
  if kind=='plain':fmt['url']='https://fixture.invalid/media?x=1'
  elif kind=='signature_cipher':fmt['signatureCipher']=urllib.parse.urlencode({'url':'https://fixture.invalid/media?x=1','s':'abc','sp':'sig'})
  else:fmt['url']='https://fixture.invalid/media?x=1&n=fixture-n'
  # No real JS/attestation is solved. Inject canned synthetic cache answers.
  ie._load_player_data_from_cache=lambda kind,*a,**k:[0,1,2] if kind=='sigfuncs' else 'fixture-solved-n'
  sd={video.STREAMING_DATA_FETCH_GVS_PO_TOKEN:lambda **k:None,video.STREAMING_DATA_IS_PREMIUM_SUBSCRIBER:False,video.STREAMING_DATA_PLAYER_TOKEN_PROVIDED:False,video.STREAMING_DATA_CLIENT_NAME:'web',video.STREAMING_DATA_AVAILABLE_AT_TIMESTAMP:None,'formats':[fmt]}
  result=list(ie._extract_formats_and_subtitles('abcdefghijk',[{'streamingData':sd}],player_url,'not_live',210))
  counts[kind]=sum(isinstance(x,dict) and 'url'in x for x in result)
 expected={'plain':1,'signature_cipher':0 if profile=='production'else 1,'n_parameter':0 if profile=='production'else 1}
 assert counts==expected,(profile,counts,expected)
 assert details['lengthSeconds']=='210'
 rows.append({'profile':profile,'version':__version__,'requested_clients':clients,'player_api_mock_calls':len(captured),'queried_client_names':[c['client']for c in captured],'player_js_url_retained':bool(captured[0]['player_url']),'title_available':bool(details.get('title')),'duration_seconds':int(details['lengthSeconds']),'synthetic_format_counts':counts,'network_attempts':len(network_attempts),'real_js_solved':False})
 dl.close()
policies={}
for name in ['web','mweb','visionos','tv_samsung']:
 if name not in INNERTUBE_CLIENTS:continue
 c=INNERTUBE_CLIENTS[name]
 policies[name]={'require_js_player':c.get('REQUIRE_JS_PLAYER'),'supports_cookies':c.get('SUPPORTS_COOKIES'),'player_policy':str(c.get('PLAYER_PO_TOKEN_POLICY')),'gvs_policies':{str(k):str(v)for k,v in c.get('GVS_PO_TOKEN_POLICY',{}).items()}}
# Upstream #17703 input shape: synthetic HTML matching the documented change.
dl, ie, captured = make('allow_js')
initial_data = {'metadata': {'fixture': 'metadata-present'}, 'contents': {'fixture': True}}
html_cases = {
 'traditional': '<script>var ytInitialData = '+json.dumps(initial_data)+';</script>',
 'dedicated_script': '<script id="yt-initial-data" type="application/json">'+json.dumps(initial_data)+'</script>',
}
initial_results={name:ie.extract_yt_initial_data('abcdefghijk',html,fatal=False)==initial_data for name,html in html_cases.items()}
assert initial_results['traditional']
assert initial_results['dedicated_script'] == (__version__ != '2026.08.19')
dl.close()
print(json.dumps({'initial_data_fixtures':initial_results,'version':__version__,'rows':rows,'policies':policies,'method_hashes':{name:hashlib.sha256(inspect.getsource(getattr(YoutubeIE,name)).encode()).hexdigest()for name in ['_extract_player_responses','_extract_formats_and_subtitles','_real_extract']},'default_clients':YoutubeIE._DEFAULT_CLIENTS,'network_attempts':len(network_attempts)}))
