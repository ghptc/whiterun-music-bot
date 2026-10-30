# EC2 playback interruption — investigation open

Investigation began 2026-10-30 04:12:50 UTC (01:12:50 São Paulo).
**Production playback has not been restored or verified.** No restart, deployment,
database edit, container deletion, or fault injection was performed.

Atualização: tratamento local do desafio do YouTube implementado e descrito na
seção final deste relatório. **Sem deploy; incidente de produção continua aberto.**

## Finding and classification

The latest recorded playback attempt failed before emitting audio, at
2026-10-29 20:57:07.320532546 UTC. Spotify identity verification rejected the
saved YouTube recording with `MATCH_UNCERTAIN: duration_unknown`. The player
retained its 145,000 ms checkpoint and queue, set `halted=true`, and waited for
`resume`. Docker and SQLite independently record this sequence. This was a
completed failed operation followed by an intentional scheduler wait, rather
than evidence of a blocked FFmpeg process or deadlocked playback worker.

**Updated classification after the live retry: confirmed yt-dlp extraction
failure, with an upstream sign-in/bot challenge.** A strict metadata-only probe
on EC2 returned exit 1, null JSON and diagnostic categories `Sign in` and `bot`.
With production's suppressed-error flags, the same recording returned exit 0
and metadata without duration. The immediate metadata rejection and application
halt are confirmed; see the follow-up below for the additional evidence. The
safety halt is explicit policy, not proof of a deadlock. It remains unknown why
YouTube challenged this environment or whether every failed candidate had the
same upstream condition. There is no evidence establishing Discord, DAVE, UDP,
resource exhaustion, or version mismatch as the cause of this attempt. No
speculative code fix or provider-challenge workaround was made.

The user's report did not supply a failure timestamp. The latest attempt is
therefore the investigation anchor, not an assumption that every reported
historical interruption has the same cause.

## Production identity

| Item | Observed value |
| --- | --- |
| Instance | `i-0efee61618db5edea`, `t3.small`, `sa-east-1c` |
| Identity source | EC2 IMDSv2 instance identity document read on host |
| Host | `ip-172-31-34-223`, SSH alias `aws-prod` |
| Compose project/service | `bardao-production` / `bard` |
| Deployed Compose path | `/opt/services/bardao/compose.yaml` (container labels) |
| Container | `cbfba4c8cd677706c000fa169d9d022cbc838e0807a8d58fd93f339f7fe05893` |
| Name | `bardao-production-bard-1` |
| Image reference | `whiterun-music-bot-bard:latest` |
| Image ID / recorded repository digest | `sha256:7424ca8dd74867eb0e8a79443240cf60f40ac10202d70f64732234887be92aca` |
| Executable SHA-256 | `4adae7bbbe07bf654fa36649b97a4d84240d0e6df71aec31e891d947d4c6cd96` |
| Container created | `2026-10-25T05:36:00.439873207Z` |
| Current start | `2026-10-25T05:37:18.076006982Z` |
| Persistent volume | `bardao-production-data` → `/home/bard/data` |
| Build revision | Unknown: no VCS revision in executable build info or image labels; deploy directory is not a Git checkout |
| Local investigation HEAD | `2642fc24a1c8fad8f5985543550b4f3f23cc24ab` — not asserted to be deployed |

Repository Compose identifies `bard` but contains no EC2 address/deploy mapping.
The existing host SSH configuration supplied the candidate; container labels,
image, application logs and IMDSv2 then confirmed the actual service and EC2
identity. Other containers on the host are unrelated and were not altered.

AWS API inspection with existing profile `cod4-prod` failed after leaving the
sandbox: `Your session has expired. Please reauthenticate using 'aws login'.`
SSH succeeded. AWS status checks, historical CloudWatch CPU/credit/network
metrics and provider events have consequently not been verified.

## Evidence and preservation

- [SQLite/runtime export](evidence-ec2-playback-stall/sqlite-runtime.jsonl):
  events from 24 October onward, current guild states, catalog counts, runtime
  health, and binary marker checks. Overall log DB bounds: 14,830 records,
  `2026-10-20T13:10:46.081421945Z` through
  `2026-10-29T20:58:10.224587651Z`.
- [Host/Docker export](evidence-ec2-playback-stall/host-docker.jsonl): current
  resource readings, OOM search, Docker state/events, process names without
  command arguments, IMDS identity, and timestamped Docker logs for
  **20:47–21:08 UTC on 29 October**, covering at least ten minutes either side
  of the failure. No later SQLite events exist in the observed DB; an empty
  interval is not evidence of successful playback.
- [Executable build info](evidence-ec2-playback-stall/binary-build-info.txt):
  extracted from a read-only copy of the production executable, Go 1.27.3,
  linux/amd64, disgo 0.19.6, godave 0.2.0. No VCS revision embedded.

SQLite was opened with `mode=ro`, using an explicit read transaction per
database. No active database files were copied, checkpointed, migrated, or
edited. Exports are logical sanitized evidence, not restore backups; the two
database reads are not one cross-database atomic snapshot. URLs and sensitive
keys were excluded/redacted. Existing production log retention was unchanged.

## Timeline (UTC; São Paulo = UTC−03)

Guild `698256401922785340` is the affected latest-attempt guild.

| Time | Evidence |
| --- | --- |
| Oct 24 01:00:52.719273 | First audio for “Houdini”, position 15, attempt `1792799989886444037/14`. |
| Oct 24 01:04:39.836762 | “Houdini” reaches EOF successfully: last fully completed track in exported history. |
| Oct 24 01:04:42.885991 | First audio for “Sing For The Moment”, position 16, attempt `1792799989886444037/15`: last recorded track with confirmed audio. |
| Oct 24 01:07:10.913109 | `container_shutdown_requested`; playback subsequently canceled with `terminated signal received`. Who requested that historical shutdown is unknown. |
| Oct 24 01:07:10.916479 | Voice close attributed to `bot_shutdown`, confirmed progress 148,060 ms. FFmpeg/yt-dlp killed after cancellation; no EOF for this track. This predates the present EC2 container. |
| Oct 25 05:37:18 | Current production container starts. No automatic Voice attachment on checkpoint restore is expected. |
| Oct 29 20:57:02.236506 | `state_restored`, 35 saved positions, automatic playback false; new player created on command. |
| Oct 29 20:57:02.899853 | Voice join completes in channel `698641530868727838`; lifecycle state ready. |
| Oct 29 20:57:03.069659 | Attempt `1793307422216540870/1` selects saved position 16, video `MJNGXnvHYPg`. |
| Oct 29 20:57:07.320533 | Attempt ends with `playback_failure`, `duration_unknown`, no cancel cause and terminal `started=false`. No first audio frame for this attempt. |
| Oct 29 20:57:07.320959 | `playback_resume_failed`, checkpoint 145,000 ms, queue preserved. |
| Oct 29 20:57:07.345025 | Scheduler waits `halted`, expected event `resume`; 35 ready entries and 50 pending positions, one active resolution. These are scheduler counters, not necessarily distinct tracks to add together. |
| Oct 29 20:57:18–20:57:57 | New playlist positions 1–3 fail matching; metadata recovery attempts complete without a reliable selection. No recovered audio. |
| Oct 29 20:58:03.843055 | Explicit stop cancellation; queue subsequently empty. |
| Oct 29 20:58:09.926177 | Explicit `command_leave`, queue size zero; Voice closes at 20:58:09.937579. |
| Oct 30 04:16–04:18 | Container healthy, worker responsive, gateway connected, `voice_detached`, pending zero; both persisted guild states have null Current and Queue. |

The exact last transmitted frame timestamp is not recorded historically.
First-frame timestamps and confirmed progress are available; neither proves
audibility or permits inventing an exact last-frame time. The checkpoint
restored on 29 October is 145 s, earlier than the historical shutdown snapshot
of 148.06 s, consistent with periodic checkpointing but not proof of its timing.

## EC2 and Docker observations

Host uptime was 4 days 23 hours 42 minutes at 04:18 UTC. Load average
0.01/0.03/0.00; 1,216 MiB available of 1,905 MiB, no swap. Root filesystem 64%
used, 6.7 GiB available; inode utilization 7%. Container sample: CPU 0.10%,
116.8 MiB / 512 MiB, 11 tasks. This rules out current pressure, not historical
peaks. Current process list contains `docker-init` and `bard`, no FFmpeg or
yt-dlp, appropriate for the now-empty detached player.

`RestartCount=0`, `OOMKilled=false`, running, exit-code field 0, Docker daemon
active, restart policy `unless-stopped`. The exit-code field is not proof of
past clean exits. Kernel journal OOM search since 25 October returned no
entries. Docker journal and retained lifecycle events in the incident window
returned no entries; event retention limits apply. `last` is not installed,
so a complete reboot history was not obtained. A single healthcheck failed at
04:16:06 (`runtime degraded or health probe stale`) and the following probe
succeeded; its exact transient cause was not captured.

SSH and IMDS worked and current gateway health is ready. These do not establish
historical UDP reachability or exclude transient network failures. No audio
pipeline was active in the latest failed attempt according to retained events;
metadata extraction completed rather than hanging. Catalog has 549 entries,
tracks 1,056, history 151, playback events 181, catalog positions 100.

## Why automatic recovery did not restore playback

Source audit is explanatory and is not proof that every local instruction is
identical to the deployed executable:

- FIX-025 resume guard in `internal/player/player.go`: a failed attempt with
  saved offset retains the occurrence and sets `halted=true`, logging
  `playback_resume_failed`. This exact event is present in production.
- Media retries handle transport/network/stall/EOF classes. Metadata match
  rejection is `playback_failure`; it does not enter that bounded retry path.
- REL-024 `cmd/bot/health.go` detects unresponsive workers, stale playing or
  buffering progress, and stuck cancellation. A responsive scheduler waiting
  `halted` is outside those restart conditions. `internal/player/health.go`
  also only flags halted recovery exhaustion when networkAttempts >= 3;
  a metadata resume refusal need not satisfy that condition.
- FIX-025.1 Voice recovery responds to unhealthy transport. Voice had joined
  successfully; the failing attempt did not report a Voice transport error.
  The later close is explicitly attributed to the leave command.
- Docker health status alone never proves audio progress. `unless-stopped`
  restarts exited processes, not a live process intentionally waiting.

Production binary has `playback_checkpoint_delayed`,
`local_readiness_confirmed`, `voice_disconnect_initiated`, and
`process_supervision_triggered`. `watchdog_recovery_triggered` is absent.
The absence of one marker does not prove the whole watchdog is absent.
Live runtime health and other markers demonstrate relevant supervision code;
complete source/artifact equivalence cannot be established without provenance.
The old October 23 report of missing FIX-025 markers does not describe this
artifact. No recovery-attempt event or progress after this metadata halt was
observed before stop/leave.

The <50% policy would retain the 145 s checkpoint (track duration 339.546 s,
approximately 42.7%). Advancing it automatically or weakening identity checks
would change the existing safety policy. No such change was made. Mutex,
canceled-context, dead worker, DAVE and UDP failures remain unsubstantiated
for this attempt; production goroutine stacks were not obtained.

## Recovery recommendation and authorization

The user authorized a playback attempt in channel `698641530868727838`, guild
`698256401922785340`, using “Sing For The Moment”. Authorization does not extend
to restart, deployment, or database editing.

The smallest action is a normal `/play` of the already-associated public video
`https://www.youtube.com/watch?v=MJNGXnvHYPg` from an operator in that Voice
channel. Expected impact: join the authorized channel and play this one track,
including the normal playback announcement. The queue is currently empty due
to the prior stop command; do not claim that restarting can restore it.

This session has SSH but no Discord connector/browser interaction mechanism,
and the inspected application exposes no remote playback control CLI, socket,
or HTTP endpoint. CLI commands are healthcheck, log inspection, Spotify
diagnosis and command synchronization. An operator must trigger the existing
slash-command entry point. No second bot process or fabricated interaction
was used to bypass the running process.

After the operator triggers playback, verify production first_audio_frame,
continued confirmed progress in runtime/checkpoint data, FFmpeg/yt-dlp state,
and human confirmation of audible sound. Observe through EOF and a subsequent
track transition before claiming queue advancement works. Do not run stop or
leave merely to test cleanup. If the direct video fails, capture its bounded
failure before selecting further recovery; no restart is currently justified.

## Local changes, validation and remaining risks

Only incident documentation and sanitized evidence were added. Pre-existing
changes in logstore, resolver, media tests and audit scripts were preserved.
No application defect was conclusively reproduced from a production metadata
fixture, so no speculative patch or implementation-mirroring test was added.
Existing FIX025/REL024/Voice/resume tests were run for policy coverage; they do
not demonstrate production recovery or explain the missing duration.

Validation completed on the local working tree: `go test ./...`,
`go vet ./...`, race detector on `internal/player` and `internal/discord`,
targeted FIX025/REL024/Voice recovery/resume tests, and Docker build all exited
0. Build tag `bard-incident-validation:local` was not deployed. Logs and exit
codes are recorded in the evidence directory, with SHA-256 checksums.
These results include the pre-existing working-tree changes and do not validate
the deployed binary. Outstanding risks:
unknown source revision, unknown duration-loss origin, no historical last-frame
or UDP telemetry, unavailable AWS metrics/status, cleared queue, and no verified
production playback. Follow-up should capture the exact sanitized extraction
schema for the rejected recording and compare parsing against the deployed
artifact before selecting a targeted fix and regression fixture. Do not deploy
the unrelated working-tree edits as an incident fix.

## Live playlist retry — 30 October, 04:23–04:26 UTC

The user retried the same playlist. This attempt is in **guild
`404344088691867649`, channel `921489232374140978`**, unlike the 29 October
attempt. Interaction `1565582010460938350`, collection 1 reserves 50 positions.
Voice join completed at 04:23:59.619645. By the health observation at
04:25:40.254077533, positions 1–6 had failed matching and 44 positions remained,
with `resolution_in_progress`, responsive worker and main gateway ready.
**No `first_audio_frame` or playback attempt was recorded for this retry in
that observation.** Playback restoration is not verified.

The first track's full verification at 04:24:04.573557 returned
`duration_unknown`; other candidates failed credits or artist checks. Position
1 failed with the aggregate `insufficient_metadata` result at 04:24:14.147281.
Positions 2–6 subsequently failed similarly. This is continuing bounded
resolution and queue consumption, not evidence of a frozen worker. Two
`scheduler_stall_detected` warnings at 04:24:44 and 04:25:14 show pending
positions changing from 48 to 46 and queue versions changing; their waiting
duration alone must not be interpreted as a deadlock.

DAVE also reported an unconfirmed commit at 04:24:14.623085 and entered a
degraded state. It explicitly logged `session recovered` at
04:24:29.651812 after 15.028 seconds, epoch 1. The metadata rejection preceded
this DAVE degradation, and matching failures continued after its recovery;
there is no evidence that DAVE caused the missing metadata. This recovery
log alone is not proof of audible output or healthy UDP.

Two bounded metadata-only probes were run inside the existing production
container with no audio download or bot lifecycle action. Both used yt-dlp
`2026.08.19`, Deno `2.5.6`, and public video `MJNGXnvHYPg`. Production metadata
flags returned exit 0 and 12,804 bytes of JSON with the expected video ID,
title, artist and track, **but no duration key**. A second probe removing the
forced web-client extractor override produced the same missing-duration
result. This proves that the extractor output itself can lack duration on
EC2, rather than assuming the Go parser discarded an available duration.
It does not establish whether the ultimate cause is provider behavior,
yt-dlp compatibility, or some other extractor/runtime condition; the client
override alone was not shown causal. No fidelity check was weakened.

Evidence: `retry-first-observation.jsonl`, `retry-second-observation.jsonl`,
`retry-latest-observation.jsonl`, `metadata-probe.jsonl`, and
`metadata-default-probe.jsonl` in the evidence directory. The retry is recent:
the ten-minute post-event observation window is not yet complete. No production
changes were made, and the latest retry's active queue must be preserved.

### Strict probe exposes the upstream challenge

At 04:26:39 UTC a separate metadata-only probe removed `--no-warnings`,
`--ignore-no-formats-error` and `--ignore-errors` from the production arguments.
It returned exit 1, five stdout bytes (`null` plus newline), and 512 stderr
bytes. Only fixed diagnostic categories were exported: **`Sign in`, `bot`**.
No token, cookie, signed URL or raw authentication response was exported.
See `metadata-strict-probe.jsonl`. An earlier collector invocation could not
summarize null JSON; that collector error was fixed before the recorded result.

This confirms a yt-dlp extraction failure with an upstream sign-in/bot
challenge for the selected video on EC2. It narrows the missing-duration
origin beyond Go parsing. Suppression flags allow incomplete metadata to reach
the identity checker, where an upstream extraction problem appears as
`duration_unknown` or aggregate `insufficient_metadata`. The identity checker
then correctly refuses to confirm a match without sufficient evidence.

A bot restart has no demonstrated way to clear this upstream challenge.
Do not weaken duration/identity checks or add unbounded retries. No automated
authentication, cookies, proxy or downloader upgrade was installed. A supported
provider access solution must be selected and validated separately before
deployment. An application change could improve error classification and
suspend provider-wide failure without consuming the remaining playlist, but
the precise production diagnostic fixture and failure scope must be captured
before defining that targeted regression; it would not itself restore media
access. All retry probes were bounded subprocesses independent of the bot and
did not alter its configuration, queue or Voice state.

## Hotfix local — classificação e suspensão do provedor

### Causa confirmada e escopo

Há duas falhas de aplicação no tratamento do bloqueio observado: avisos e erros
suprimidos permitiam aceitar JSON incompleto como extração bem-sucedida; os
caminhos de fila tratavam a recusa do provedor como indisponibilidade individual
e consumiam posições sucessivas. O caminho síncrono de Spotify para faixa avulsa
também descartava a causa original e devolvia `ErrSpotifyMatch`.

O desafio de sign-in/bot permanece confirmado na EC2. Sua causa externa
(reputação do endereço de saída, mudança do serviço, compatibilidade do extrator
ou outra condição) **não foi comprovada**. Este hotfix corrige o tratamento da
recusa, sem tentar contornar autenticação nem garantir acesso ao áudio.

### Classificação e proteção dos diagnósticos

`internal/media/extraction_error.go` introduz erro tipado com categoria, exit
code quando disponível e causa de cancelamento/timeout. Não carrega stderr,
cookies, tokens ou URLs. O resolver classifica o diagnóstico antes de aceitar
JSON, inclusive quando o subprocesso retorna exit 0.

| Categoria | Tratamento |
| --- | --- |
| `youtube_metadata_unavailable` | Dados necessários ausentes; não confundir com autenticação nem aprovar identidade incompleta. |
| `youtube_video_unavailable` | Indisponibilidade da gravação individual; permite o tratamento normal daquela posição. |
| `youtube_auth_required` | Suspender resoluções do provedor na guild e preservar a fila. |
| `youtube_bot_challenge` | Mesmo bloqueio controlado, distinguindo-o de login comum. |
| `youtube_network_timeout` | Timeout de extração, com causa de deadline preservada e recuperação limitada. |
| `youtube_rate_limited` | Suspender e respeitar o backoff, sem consumir a playlist. |
| `youtube_extractor_failure` | Falha do extrator, saída inválida ou erro sem classificação mais específica; falhar sem classificar como mismatch musical. |

As extrações individuais retiram `--ignore-errors` e `--no-warnings`.
**`--ignore-no-formats-error` foi mantido apenas para metadados**, com os avisos
visíveis e inspecionados: a comparação local comprovou que metadados válidos
podem existir sem um formato solicitado disponível. Exigir formato nessa etapa
criaria uma regressão. A listagem plana de playlist YouTube mantém
`--ignore-errors` para entradas privadas/deletadas, mas também inspeciona
diagnósticos antes de aceitar resultados parciais; desafio do provedor interrompe
a operação, inclusive com exit 0. Stderr bruto deixou de ser incluído nas falhas
dessas extrações e no diagnóstico do produtor de áudio.

`discoverSpotify`, seleção de candidatos e o caminho síncrono de coleções
propagam a falha do provedor e interrompem buscas alternativas. Trabalhadores
já admitidos são cancelados; não se inicia uma tentativa para cada posição da
playlist. Uma falha de extração não invalida uma associação persistida sem
evidência de incompatibilidade/indisponibilidade daquela gravação.

### Recuperação controlada

- Circuito por guild, compartilhado pelo worker de áudio, coleções e preparação
  de `/play`. Não há circuito global entre guilds.
- Bloqueio mantém a fila e o checkpoint confirmado. Uma falha na resolução de
  faixas futuras não cancela a música que já está tocando.
- Uma tentativa de recuperação por vez; intervalos de **15, 30 e 60 segundos**
  após as respectivas falhas, com **três tentativas automáticas**. Depois disso,
  a fila continua suspensa até `/resume`. Não há loop de retries infinito.
- Sucesso na verificação de metadados libera o circuito. No áudio, exige-se
  progresso confirmado de cinco segundos ou término normal, preservando a
  proteção existente contra reset do orçamento após apenas um frame.
- Identificador de incidente e autorização de tentativa impedem que uma
  conclusão antiga libere o circuito de uma sessão posterior. Stop/cancelamento
  recolhem os trabalhadores e não ressuscitam a fila antiga.
- Uma notificação de suspensão por incidente. Retries e `/resume` sem sucesso
  mantêm o mesmo incidente; não geram mensagens por faixa. A mensagem de
  provedor não usa o rótulo de faixa indisponível.
- O comando inicial de playlist recebe confirmação de fila suspensa sem ficar
  preso aguardando o provedor. Novos `/play` durante o bloqueio não iniciam
  extração. `/resume` também libera uma fila vazia para uma nova solicitação.
- Nenhum caminho novo executa Leave ou recria Voice. Mantida a política FIX-025:
  abaixo de 50%, reter/retomar offset confirmado; a partir de 50%, avançar
  exatamente uma ocorrência. Retomadas não duplicam histórico ou Now Playing.

O orçamento de recuperação e o circuito são mantidos em memória; fila e
checkpoints continuam usando a persistência existente. Reinício não é um
tratamento para o desafio e a restauração continua exigindo attachment explícito.

### Comparação ambiental

Evidências sanitizadas em [evidence-youtube-provider-hotfix](evidence-youtube-provider-hotfix/).
O host local não tinha `yt-dlp`/Deno no PATH; a comparação foi feita num container
local de validação com os mesmos **yt-dlp 2026.08.19 e Deno 2.5.6** observados na
produção, sem montar dados ou credenciais da EC2. Todas as chamadas eram de
metadados, limitadas a 40 segundos, para `MJNGXnvHYPg`, sem download de áudio.

| Ambiente / horário UTC de 30/10 | Flags / resultado |
| --- | --- |
| EC2, 04:26:39 | Sem supressões: exit 1, JSON null, diagnóstico sign-in/bot. |
| Container local, 05:03:14 | Mesmo probe estrito: exit 1, formato solicitado indisponível. |
| Container local, 05:08:46 | Flags originais: exit 0, duração 340 s, público. |
| Container local, 05:10:54 | Avisos visíveis, sem exigir formato: exit 0, duração 340 s, aviso de formato indisponível. |
| EC2, 05:10:56 | Mesmo probe com avisos visíveis: exit 0, duração ausente, zero formatos; diagnósticos sign-in/bot, sem formatos e formato solicitado indisponível. |

As chamadas não foram simultâneas, e a equivalência do restante do ambiente não
foi comprovada. A diferença observada não demonstra causalidade do IP ou da
versão. O probe mais recente na EC2 foi executado no container existente, sem
alterar seu programa, configuração, fila ou Voice. O novo código não está live.

### Alternativas oficiais e autorizadas

A [YouTube Data API, método videos.list](https://developers.google.com/youtube/data/reference/rest/v3/videos/list)
permite consultar `snippet`, `contentDetails` e `status` por ID. A
[referência de videos](https://developers.google.com/youtube/data/reference/rest/v3/videos)
define duração em `contentDetails.duration`. Isso oferece uma alternativa oficial
para metadados, com credenciais e cotas próprias; não fornece, por si só, uma
solução para a extração de áudio recusada. Dados de título/canal/duração também
não comprovam todos os créditos musicais exigidos pelo matcher atual.

O [guia oficial de início](https://developers.google.com/youtube/data/getting-started)
explica projeto, habilitação da API e credenciais; operações que exigem
autorização do usuário usam OAuth 2.0. Nenhuma credencial foi criada ou alterada.
O [IFrame Player API](https://developers.google.com/youtube/iframe_api_reference)
é uma alternativa oficial de reprodução embutida no navegador, não um substituto
direto do pipeline Opus/Discord desta aplicação — essa conclusão é uma avaliação
da arquitetura descrita na documentação. A
[FAQ do projeto yt-dlp](https://github.com/yt-dlp/yt-dlp/wiki/FAQ)
documenta autenticação por cookies, mas isso não equivale a uma API oficial de
metadados do YouTube nem garante que o desafio será removido. Cookies, proxies,
tokens e upgrades de runtime não foram instalados neste hotfix.

### Regressões e validação

Novos testes em `internal/media/provider_hotfix_test.go` e
`internal/player/provider_hotfix_test.go` cobrem as sete categorias, desafio
mascarado por exit 0 e JSON parcial, sigilo de diagnósticos, metadados válidos sem
formato disponível, propagação na faixa Spotify avulsa, interrupção de buscas e
trabalhadores síncronos, retenção de 123 posições, backoff e orçamento, admissão
serial entre coleções, recuperação após retorno do provedor, áudio atual durante
falha de background, persistência do checkpoint, cancelamento durante tentativa,
fila vazia após falha de preparação, ausência de Leave/spam e política de 50%.
Também reproduzem cancelamento de uma resolução que só termina depois da
recuperação e admissão de coleção com orçamento já esgotado: nenhum dos dois
casos pode consumir posições ou prender a resposta inicial do comando.

Testes antigos que exigiam consumir a fila em desafio do provedor foram
atualizados. Os de vídeo individual indisponível continuam exigindo avanço
normal. O percurso de comandos Discord cobre suspensão, `/resume`, retomada e
avanço subsequente, e a apresentação da notificação de provedor tem regressão.

Resultados finais e logs estão em `evidence-youtube-provider-hotfix`. A árvore
validada inclui alterações locais preexistentes em logstore, mídia e auditoria,
que foram preservadas. **Nenhum deploy automático foi realizado.** Mesmo que
todos os testes passem, áudio real ainda precisa ser confirmado na EC2; a
correção do tratamento não remove a exigência externa de autenticação.

Validação final concluída com exit 0: `go test ./... -count=1`, `go vet ./...`,
`go test -race ./internal/media ./internal/player ./internal/discord -count=1`,
regressões direcionadas e build Docker `bard-provider-hotfix:local`.
[validation.json](evidence-youtube-provider-hotfix/validation.json) registra
resultados e identidade da imagem local; os arquivos de evidência têm SHA-256.

## Preparação do commit e deploy autorizado — 30/10/2026

O usuário autorizou commit e deploy controlado exclusivamente do serviço Bardão,
com backup consistente e rollback preservando dados. A árvore do índice foi
exportada por `git archive` para uma pasta limpa e validada novamente: testes
completos, vet, race de media/player/discord e build Docker concluíram com exit 0.
Assim, a validação do artefato a implantar não depende das alterações locais
preexistentes. Foram excluídos do commit logstore, auto-pilot, auditoria, teste de
telemetria preexistente e o hunk de telemetria em `resolver.go`; o restante da
árvore de trabalho foi preservado. Evidências em
[evidence-youtube-provider-deploy](evidence-youtube-provider-deploy/).

Às 05:29–05:30 UTC, o alvo continuava sendo a instância
`i-0efee61618db5edea`, container original `cbfba4c8cd67`, imagem
`sha256:7424ca8dd74867eb0e8a79443240cf60f40ac10202d70f64732234887be92aca`.
Gateway e SQLite estavam saudáveis, sem reinícios; 6,7 GiB livres no filesystem
e aproximadamente 1,2 GiB de memória disponível. O Compose efetivo contém apenas
`bard` e mapeia o volume lógico `bard-data` para o volume existente
`bardao-production-data`. A última leitura indicava fila vazia, sem áudio ativo;
o estado precisa ser verificado novamente junto do backup e após o deploy.

O deploy usará imagem identificada pelo SHA do commit e registrará também o hash
do executável. Backups ficarão em `/opt/backups/bardao`, fora de
`/opt/services/bardao`, com acesso restrito. Nenhum `down -v`, prune ou migration
foi incluído no procedimento. A homologação de áudio permanece pendente até uma
tentativa real após a implantação.
