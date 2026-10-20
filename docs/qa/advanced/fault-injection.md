# Fault injection e limites

| Risco | Injeção / oráculo | Evidência |
|---|---|---|
| SQLite interrompido | função SQL registrada em conexão nova, trigger após escrita; cancelar contexto no ponto exato; comparar duas guilds após rollback | sqlite-interruption.txt |
| Estágios de transação | UPDATE guild, DELETE history, INSERT history, INSERT tracks | quatro cenários, dez repetições sob race |
| SQLite indisponível | testes QA anteriores de conexão fechada, query_only, arquivo corrompido e contexto cancelado mantidos | suíte completa |
| FFmpeg encerrado | FFmpeg real, WAV local de 20s para backpressure; SIGKILL somente no PID do decoder após primeiro frame | ffmpeg.txt, cinco repetições sob race |
| Processos restantes | após erro de decoder, kill(pid,0) deve retornar ESRCH para os dois líderes pertencentes ao teste | mesmos testes; helpers descendentes nos testes anteriores |
| yt-dlp incompleto | executável local com JSON truncado, ID ausente e vídeo substituído; erro sem URL/VideoID parcial | network.txt e suíte final |
| HTTP 429 | servidor TCP/HTTP local retorna 429; classificação preservada e nenhuma faixa parcial | network.txt |
| Rede interrompida | hijack/close e Content-Length truncado no servidor local | network.txt |
| Timeout / cancelamento de rede | requisição real bloqueada até cancelamento observado pelo servidor | network.txt |
| Discord indisponível | REST do SDK com 503, conexão abortada, prazo expirado, 429 dentro/fora do budget | discord-faults.txt, 30 repetições sob race |
| Transição / cancelamento | modelo com falha parcial, EOF e comandos; barreiras de teardown anteriores preservadas | model.txt, linearization.txt e stress.txt |

No teste HTTP real, toda autoridade Spotify é reescrita para httptest.Server;
qualquer outra autoridade é rejeitada. Credenciais são fixtures. SQLite usa apenas
t.TempDir. Cada execução de processos tem contexto limitado e PIDs próprios.

Toxiproxy foi avaliado e não adotado: as conexões atuais são locais e controláveis;
hijack, truncamento e cancelamento reproduzem os defeitos desejados sem outro serviço.
Isso não cobre jitter, perda de pacotes, half-open TCP prolongado ou comportamento
de TLS externo. Toxiproxy/netem pode agregar valor na homologação dedicada desses
riscos, sem apontar para produção.

Cancelamento de transação após escrita não equivale a SIGKILL do bot no meio do
commit nem a crash de filesystem. Teste de reboot/WAL continua pendente.
