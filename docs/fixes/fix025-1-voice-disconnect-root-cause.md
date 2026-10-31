# FIX-025.1 — investigação das saídas de Voice

Data: 23/10/2026. Base: `6a848cd`. **Homologação pendente. Sem deploy.**

## Resultado objetivo

Foram identificados **seis incidentes de transporte/reprodução** no histórico disponível, além de **12 solicitações locais de `leave` e sete desligamentos de processo**. Essas contagens não são somáveis como saídas físicas: shutdown pode chamar Leave novamente quando o bot já saiu. Os logs não permitem contar todas as remoções visuais do canal nem atribuir cada solicitação antiga de Leave a comando, limpeza de canal vazio ou shutdown.

Em quatro incidentes, o **monitor local iniciou comprovadamente o cancelamento do áudio**, após observar o Gateway principal indisponível. Em dois, o primeiro sinal registrado foi **EOF do websocket Voice**; a origem física do rompimento (Discord, rede ou infraestrutura) não é identificável. Não há evidência que permita atribuir os seis a um mesmo responsável externo.

**O container ativo não contém as mudanças relevantes do FIX-025.** A cópia do executável contém a mensagem antiga de recuperação e não contém `watchdog_recovery_triggered`, `playback_checkpoint_delayed`, `local_readiness_confirmed` nem a mensagem sobre progresso confirmado presentes em `6a848cd`. O executável não incorpora revisão VCS; não é possível nomear seu commit exato. O incidente novo de 23/10 repete o comportamento anterior, portanto não demonstra regressão da correção não implantada. SHA-256 e presença dos marcadores estão em [runtime-evidence.json](fix025-1/runtime-evidence.json).

Não foi identificada uma nova causa que justifique alterar especulativamente a política de recuperação de `6a848cd`. Esta entrega mantém suas correções e adiciona rastreabilidade nos pontos reais de fechamento, evidência reproduzível e regressões direcionadas. **Não se declara o problema resolvido em produção.**

## Fontes, abrangência e limitações

- SQLite `bard-logs.db`: **13.146 registros**, de `2026-10-20T13:10:46.081421945Z` a `2026-10-23T23:42:19.396318055Z`, consultados integralmente em cópia local `mode=ro`, sem LIMIT. WAL de logs observado com zero bytes. Não houve alteração do banco vivo.
- `docker compose logs --timestamps --no-color bard`: todo o histórico ainda retido, aproximadamente 3,5 MB. A retenção Docker pode ter eliminado registros antigos; ausência no arquivo não exclui um evento anterior.
- SQLite operacional: cópia de main DB **e WAL**, lida localmente. Arquivos copiados sequencialmente, não constituem snapshot histórico atômico. As duas guilds estavam sem Current/fila na leitura posterior ao `/stop` e Leave. O estado copiado não contém `ResumeOffset`; não há checkpoint de áudio histórico capaz de recompor o percentual dos incidentes.
- Docker inspect: processo ativo desde `2026-10-23T10:33:42.983671976Z`, saudável, `OOMKilled=false`, `RestartCount=0`; término anterior registrado às `2026-10-23T02:54:07.546059039Z`. Esses campos não são histórico completo de OOM/restarts.
- Docker events, intervalo 20/10 a 23/10 23:45 UTC, filtros oom/die/restart/start/stop do container: **nenhum evento retornado**. Histórico do daemon é limitado e não exclui eventos perdidos em reinício.
- Código do player, monitor Voice, supervisão REL-024, Streamer, disgo/godave local e relatório anterior FIX-025. Eventos DAVE/MLS e conclusão de FFmpeg/yt-dlp foram examinados junto com os eventos Gateway, não tratados automaticamente como causas.

[historical-evidence.json](fix025-1/historical-evidence.json) contém hashes das fontes, contagens, timelines SQLite e Docker de **±120 segundos para cada incidente**, e timelines SQLite para cada solicitação Leave/shutdown. A janela solicitada pode ultrapassar o início/fim da evidência disponível; em particular, o último banco termina às 23:42:19, antes de completar os 120 segundos posteriores. O coletor não inventa registros para preencher lacunas. Os logs são esparsos: início da faixa pode estar fora da janela; tentativa, título e posição no fim correlacionam a ocorrência.

Reprodução da análise: `python3 scripts/investigate-voice-lifecycle.py /tmp/bard-logs.db /tmp/docker.log /tmp/evidence.json`. O exportador usa campos permitidos e classes fixas de erro; não exporta payload Gateway, token, SessionID Discord, chave MLS/DAVE, URL assinada ou stderr bruto. O ID de sessão usado abaixo pertence ao player, não à autenticação Discord.

## Timeline dos incidentes

Horários exatos abaixo em UTC; São Paulo = UTC−03. A primeira coluna usa a observação de saúde; quando há um evento causal anterior, seu horário também é informado.

Guild A: `404344088691867649`, canal correlacionado pelos eventos MLS `921489232374140978`. Guild B: `698256401922785340`, canal MLS `1469316350546808866`. MLS não prova presença contínua no canal; os eventos antigos de saúde não registravam seu ID diretamente.

| Observação UTC | Guild / player / tentativa / posição | Faixa e estado | Iniciador e resposta | Retomada |
|---|---|---|---|---|
| 20/10 18:10:27.801933011 | A / `1792518614004675889` / 5 / 2 | Love The Way You Lie; áudio interrompido, categoria voice_send | Voice EOF/1006 às 18:10:27.666003791; reabertura rejeitada com **4006** às 18:10:27.762047902. Responsável pela ruptura desconhecido; sessão invalidada pelo servidor. Versão antiga continua tentando outras faixas com voice_speaking. | Não demonstrada antes do shutdown às 18:11:18.397434425. |
| 20/10 19:59:12.231862839 | A / `1792522128036085600` / 16 / 6 | Bitch Please II; circuito Voice aberto e faixa retida | `voice_session_lost` EOF/1006 às 19:59:12.074256398. Nova sessão MLS às 19:59:14.147305956. Sem prova de quem rompeu a conexão. | Saúde pronta em ~2 s; primeiro áudio às 19:59:17.853498251, **5,779 s** após o EOF. |
| 20/10 21:16:11.231519955 | A / `1792522128036085600` / 37 / 4 | Master Of Puppets; cancel_reason voice transport unavailable | Monitor local, main_ready=false, cache conhecido/mesmo canal. Gateway principal volta em ~1 s. Primeira reabertura recebe already opened or closed. | Primeiro áudio às 21:16:34.263561844: **23,032 s** após detecção. |
| 22/10 01:08:55.692794977 | B / `1792629263190533464` / 10 / 10 | We Want Some P--sy; fila retida | Monitor local, main_ready=false, mesmo canal; cancelamento às 01:08:55.694558245. Principal volta em 1 s. Primeira recuperação falha localmente às 01:08:57.811325016, depois timeout de 15 s. | Recuperação concluída às 01:09:14.899490090; primeiro áudio às 01:09:17.853143038: **22,160 s**. |
| 22/10 02:08:38.692884793 | B / `1792629263190533464` / 30 / 31 | Pump It; fila retida | Mesmo padrão local. Falha already opened or closed às 02:08:40.701210921; segunda tentativa funciona. | Pronta às 02:08:57.574808439; áudio às 02:09:00.695233599: **22,002 s**. Leave local posterior às 02:09:06.504417049. |
| 23/10 23:41:33.882502979 | B / `1792797760406721174` / 6 / 44 | Indika; fila com 93 entradas retidas | Monitor abre circuito às 23:41:33.882587512. FFmpeg e yt-dlp têm exit=0, cancelamento Voice; não iniciam a falha. Principal volta às 23:41:34.882274861. Reabertura falha às 23:41:35.763238443; segunda tentativa funciona. | Pronta às 23:41:53.058579490; áudio às 23:41:55.905553190: **22,023 s**. Stop às 23:42:15.235693207 e Leave às 23:42:19.127155250 são posteriores. |

O progresso confirmado, percentual exato, estado UDP e prontidão DAVE no instante de cada ruptura são **desconhecidos**. `elapsed_ms` mede duração da tentativa, inclui resolução/buffering e não equivale a áudio transmitido. Não foi usado para decidir retroativamente a política de 50%. Estado Voice antigo é booleano composto com o principal: `voice_ready=false` nesses quatro casos não comprova fechamento do websocket Voice.

As 12 solicitações locais de Leave têm seus horários exatos e IDs no JSON; incluem duplicações de cleanup após shutdown. Sem origem lógica antiga, não são classificadas como comandos legítimos nem como saídas indevidas. Os sete shutdowns têm suas próprias janelas; não coincidem com o início dos quatro incidentes de monitor.

## Sessões prolongadas e periodicidade

A sessão A entrou às 18:49:13 de 20/10, teve incidentes às 19:59:12 e 21:16:11, e ainda iniciou faixa às 21:55:23: mais de três horas, com interrupções. Sessão B entrou às 00:34:23 de 22/10; segundo incidente às 02:08:38, mais de 94 minutos depois. A ocorrência de 23/10 aconteceu cerca de 19 minutos depois da entrada às 23:22:41; portanto não depende de 60 minutos de reprodução.

22:08 e 23:08 locais de 21/10 correspondem a 01:08 e 02:08 UTC de 22/10. Intervalo exato entre detecções: **59 min 43 s**. O intervalo A de 19:59 a 21:16 é aproximadamente 77 minutos. Existem também EOFs do Gateway principal fora de reprodução (22/10 10:24:01 e 23/10 14:18:42, 16:29:27 e 20:55:53 UTC), sem evidência de periodicidade uniforme.

Não foi localizada rotina que execute Leave após uma hora. Expiração de mappings/catálogo é 24 h; backoff de limpeza pode atingir uma hora, mas não chama Leave. Heartbeats usam intervalo negociado; o watchdog Voice verifica disponibilidade e tolerância de cinco segundos no FIX-025. Supervisor REL-024 observa travamento funcional, não duração de sessão. **Não se confirmou renovação horária, timeout de FFmpeg ou Spotify como causa.**

## Causas confirmadas e hipóteses

Confirmado: o monitor antigo cancela áudio por indisponibilidade breve do principal; há quatro cancelamentos correlacionados imediatamente com main_ready=false. A recuperação antiga recria transporte e seu caminho de código executa Close antes de nova negociação. A assinatura desses logs e a ausência dos marcadores comprovam que a proteção FIX-025 não está no executável ativo. A origem lógica do cancelamento está comprovada; a chamada exata que produziu cada saída visual não foi registrada historicamente e permanece inferida pelo caminho de código, não por um log individual de Disconnect.

Confirmado: houve duas rupturas Voice com EOF/1006 e uma rejeição 4006. Uma sessão rejeitada como inválida não pode ser mantida artificialmente. Não se identifica se o EOF inicial veio do Discord ou da infraestrutura.

Hipóteses abertas: atraso no eco de Leave pode fechar o objeto de conexão recém-criado; o FIX-025 tem reprodução dessa corrida, mas os payloads históricos não permitem atribuí-la a cada falha de reabertura. Origem da reconexão breve do principal nos quatro eventos também não foi registrada com código/razão. Não há prova de DAVE, heartbeat, UDP, SQLite, FFmpeg, yt-dlp, Spotify, panic, deadlock, OOM ou watchdog de processo como iniciadores desses quatro eventos. Ausência de logs não exclui defeitos não observados.

## Auditoria dos caminhos de código

| Caminho / callers | Condição e efeito |
|---|---|
| `commands.go` → `guild.leave` → `GuildPlayer.Leave` → `transport.Close` → `voiceConn.Close` | `/leave` autorizado; cancela busca/jobs/recuperação, limpa fila e fecha conexão. |
| `checkEmpty` → `guild.leave` | Canal sem ouvintes humanos por dois minutos, verificado pelo worker apenas quando cache/saúde confiáveis. Política existente de permanência; não é timeout da música. |
| `Bot.Close` → `GuildPlayer.Close` → `Leave`, depois Close idempotente | Shutdown do bot/processo. Cancela o contexto raiz e aguarda workers. Falha ao restaurar store na criação da guild também fecha o player ainda sem transporte anexado. |
| `playbackTransport.Recover` → Close antigo / Close candidato | Só após autorização de canal e falha de reutilização: Gateway inválido, UDP marcado quebrado ou confirmação de prontidão falhou. Preserva fila. Falha/cancelamento de Open fecha candidato com timeout de cleanup. |
| comando summon/play → Open/Attach falho → Close candidato | Limpeza de entrada que não se completou; transporte anterior não é anexado novamente. |
| `voiceConn.HandleVoiceStateUpdate` → disgo handler → Gateway.Close | Remoção real após entrada confirmada invalida Voice; eco anterior à entrada é filtrado. Ator administrativo não consta do payload observado. |
| `incidentGateway.listen`, heartbeat, Open cancelado → Gateway.Close | Encerra websocket inválido; `gatewayLost` fecha UDP/DAVE **locais**, sem emitir Leave de guild. Scheduler negocia substituição quando autorizado. |
| scheduler/`SuspendVoice`/watchdog de player | Cancela contexto da faixa/recupera; não fecha conexão saudável. Falhas de áudio reiniciam mídia localmente e mantêm fila/progresso. |
| Stop, SkipN, Clear, resetSearches, cancelJobsLocked | Cancelam os contextos pertinentes e invalidam gerações; Stop limpa fila, Skip avança seleção, Clear remove próximas. Não são chamadas de Leave. |
| `monitorRuntime` → callback em `cmd/bot/main.go` | Travamento funcional persistente e orçamento de restart permitem `os.Exit(1)`; log process_supervision_triggered. Sinais de término passam pelo shutdown; falha de inicialização também retorna erro/exit. Não há esse evento iniciando os incidentes. |
| Streamer / subprocessos | Contextos, EOF, panic supervisionado e deadlines matam/recolhem FFmpeg/yt-dlp e fecham pipes. `StreamError` separa mídia e Voice. Não chamam Leave. |

Conexão e fila são deliberadamente recursos distintos. As proteções de `6a848cd` continuam: principal independente do Voice, grace de cinco segundos, confirmação de transporte saudável, rejeição de sessão inválida, filtragem de eco antigo, recuperação limitada e progresso por frame confirmado.

## Alterações nesta entrega

- Contexto de fechamento carrega origem lógica sem alterar cancelamento/deadline. Caminhos de comando, canal vazio, shutdown, falha de entrada e recriação registram motivos distintos.
- `voice_disconnect_requested` registra snapshot do player **antes** de limpar estado: incidente, guild, tentativa, título/posição, progresso confirmado, duração, fila, pausa e suspensão. `voice_disconnect_initiated` registra Close efetivo uma vez, canal, origem e transição de estado.
- `voice_disconnect_remote` identifica VoiceStateUpdate removendo o bot após a entrada; registra explicitamente que o ator é desconhecido. Não confunde isso com um comando administrativo comprovado. Eco de saída solicitado localmente recebe local_close_echo, sem ser atribuído como desconexão remota; há regressão específica para essa distinção.
- `voice_gateway_closed` passa a incluir código remoto quando disponível, estado, canal e correlação; códigos 4006/4009 também emitem `voice_session_invalidated`.
- `watchdog_voice_action`, `voice_lifecycle_state_changed` e eventos `voice_recovery_started/completed/failed` tornam observável a decisão do monitor e do scheduler. Eventos de recuperação iniciada/concluída antes chamados genericamente recovery_started/completed agora têm nomes específicos de Voice; consumidores devem acompanhar os novos nomes.
- Snapshot/callback usam mutex e não guardam tokens/SessionID Discord. Há fallback de correlação local antes de Attach. Eventos não aplicáveis não inventam progresso, código remoto ou principal saudável. O canal está nos eventos de conexão/watchdog; o scheduler não possui canal Discord e é correlacionado pelo incidente/guild.
- Sem log por frame/heartbeat saudável. Mantidas retenção SQLite **500 MiB** e rotação Docker **3 × 10 MB**. Alteração local anterior em logstore preservada e excluída do commit.

Não houve nova mudança de política ou timeout arbitrário. Mantida a regra FIX-025: abaixo de 50% retomar offset confirmado; a partir de 50% avançar exatamente uma ocorrência; preservar fila e não duplicar notificações, com Stop/Skip/Pause invalidando ou preservando as operações conforme o comando.

## Validação e cobertura

Os novos testes FIX0251 verificam três horas virtuais com transições e falhas FFmpeg/yt-dlp, três horas de observações do monitor com reconexões do principal/cache indisponível, erro de Spotify no contrato do resolver, timeout de resolução com relógio controlado, grace Voice, corrida EOF/watchdog, cancelamento do contexto e log de Close exatamente uma vez. Nenhum depende de produção ou força reconexão real.

| Requisito | Teste direcionado novo ou regressão existente |
|---|---|
| 1–2. Transições/fila sem Leave | `TestFIX0251ThreeHoursWithoutVoluntaryLeave`, `TestFIX0251ThreeHoursMainGatewayOutagesPreserveVoice` |
| 3–4. Falhas FFmpeg/yt-dlp | Mesmo teste de três horas; fixtures reais em `TestVoiceStreamFailureReapsRealAudioProcesses` e `TestPipelineSoak125Transitions` |
| 5. Falha Spotify | `TestFIX0251ResolverFailurePreservesActiveVoice/spotify_unavailable` (injeção no contrato de resolução, sem chamar serviço real) |
| 6. Watchdog ativo | `TestFIX0251ThreeHoursMainGatewayOutagesPreserveVoice`, `TestFIX0251VoiceOutageGraceAndRecoveryDoesNotLeave` |
| 7. Timeout resolver | `TestFIX0251ResolverFailurePreservesActiveVoice/resolution_timeout` |
| 8. Voice temporariamente indisponível | Teste de grace e `TestVoiceUnavailableBeforeAndDuringPlaybackRecoversAfterFiveSeconds` |
| 9. Sessão invalidada | `TestIncidentGatewayFreshIdentifyAndNoBackgroundReconnect/4006`, `TestFIX025InvalidGatewayClosesUDPWithoutLeavingGuild` |
| 10. Perda real/reconexão | `TestVoiceRecoveryReplacesConnectionAndProvesReadiness`, `TestFIX025BrokenUDPRecreatesSessionButRemovalCannotRejoin` |
| 11. Stop/Skip/Pause durante recovery | `TestVoiceStopAndSkipDuringRecovery`, `TestFIX025PauseDuringRecovery` |
| 12. EOF/watchdog | `TestFIX0251EOFRacesVoiceWatchdogWithoutDoubleAdvance` |
| 13. Mais de duas horas | Três horas virtuais no scheduler (`testing/synctest`) e no monitor (`now` controlado); não equivale a três horas de Voice real |
| 14. Fila/progresso | `TestFIX025ConfirmedProgressPolicy123Occurrences`, `TestFIX025CrashCheckpointRequiresAttachment`, testes persistentes de checkpoint no pacote storage |

Resultados finais e hashes dos logs de validação: [validation.json](fix025-1/validation.json). Foram solicitados `go test ./...`, `go vet ./...`, race nos pacotes discord/player, build Docker isolado e execução específica FIX0251. A validação da árvore de trabalho inclui mudanças locais preexistentes, preservadas fora do commit. Não foi criada campanha genérica adicional.

## Riscos e aceite pendente

Não foi executada sessão real prolongada: o executável corrigido não foi implantado, nem se entrou em Voice para testes nesta investigação. Sem deploy automático. Testes locais não homologam o requisito final.

A próxima sessão real autorizada precisa usar o artefato atualizado, confirmar ausência de saídas locais indevidas e correlacionar qualquer fechamento pelos novos eventos. Permanecem desconhecidos a origem das reconexões do principal e o responsável físico pelos EOFs. Não existem métricas históricas suficientes para excluir vazamentos ou perda UDP/DAVE silenciosa. Perda real de autorização impede reentrada automática; recuperação tem orçamento finito e mantém fila suspensa quando esgotado. O progresso confirma envio, não audição. Checkpoints podem repetir alguns segundos após crash.

**FIX-025.1 só pode ser homologado após sessão real prolongada sem saída inesperada atribuível ao Bardão.**
