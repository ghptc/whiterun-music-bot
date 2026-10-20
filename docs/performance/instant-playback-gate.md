# PERF-017 Addendum — Instant Playback Performance Gate

Data: 2026-10-20. Base anterior: `5b378eb`. Resultado: **gate determinístico PASS; homologação de performance real FAIL / NÃO APROVADA**. Nenhum limite foi relaxado. Foram 4.400 primeiros envios determinísticos; no canal autorizado, 54 tentativas e 51 envios reais. Não houve deploy ou alteração de comandos publicados/dados de produção.

## Executar e bloquear regressões

```sh
python3 scripts/test_instant_playback_gate.py
python3 scripts/instant_playback_gate.py --run --output .qa-runs/perf017/deterministic.json
```

O comando retorna exit code diferente de zero se um teste falhar, faltar cenário/timestamp, houver menos de 100 amostras, ou p95 ultrapassar o limite. O workflow `Instant playback performance gate / deterministic-ttfa` roda em push e pull request, requer FFmpeg/libopus real e guarda relatório + JSONL bruto como artifact. A proteção da branch deve exigir esse check no GitHub; configuração remota de branch protection não foi alterada.

Limites: hit p95 ≤ 2000 ms; miss ≤ 5000 ms; primeira faixa quente em playlists 302/500 ≤ 2000 ms; EOF para próxima faixa pronta ≤ 1000 ms. Crescimento adicional de p95 entre playlist quente de 10 e 500: no máximo **250 ms**, orçamento explícito para cópia/persistência de metadados, mantendo também o limite absoluto de 2 s. As 499 buscas restantes são bloqueadas por canais controlados: implementar resolução síncrona de toda a coleção faz o teste falhar por ausência de frame. Não usamos delays artificiais para produzir tempos favoráveis.

O avaliador é fail-closed: amostras vazias, matriz incompleta, timestamps inválidos, comandos sem áudio e evidência simulada apresentada como real são rejeitados. Quantis são nearest-rank, com p50/p95/p99/máximo. Testes negativos verificam os limites, inclusive transporte lento e evidência insuficiente. O gate por guild também verifica p95 em cada guild, sem esconder starvation no agregado.

## O que é medido

`TTFA = transport_sent_monotonic_ns - received_monotonic_ns`. Envio aceito pelo transporte UDP não comprova que o áudio foi ouvido.

Estímulo no dispatcher real `commandAt`, ACK da fixture, CollectionResolver, SearchCache, SQLite real em arquivo, MatchLimiter, CollectionJob, validação Spotify, Streamer, FFmpeg/libopus real, parser Ogg, pacing e adaptador `transport.WriteFrame`. O clock é `time.Time` com componente monotônico; persistimos offsets em nanossegundos relativos à entrada do comando, sem subtrair relógios UTC. `NewTimingAt` ancora os logs de produção no mesmo início do comando e inclui `request_elapsed_ns`/`elapsed_ns`.

Offsets obrigatórios: `received`, `ack`, `metadata`, `first_resolved`, `extraction`, `ffmpeg_started`, `opus`, `transport_sent`. Eventos correspondentes incluem `spotify_metadata_available`, `play_preparation_complete`, `resolver_complete`, `stream_resolve_complete`, `ffmpeg_start`, `first_opus_packet`, `first_discord_send`. Cache hit é observado antes de retornar metadados; nenhuma rede é necessária para medir a fila. `Queued` não encerra a medição. No gate de transição, a origem é o EOF real do stream anterior, e não um novo comando.

O yt-dlp determinístico é um executável controlado: devolve metadata capturada de Whistle/Flo Rida ou bytes Opus sintéticos. Ele não é um yt-dlp de rede. FFmpeg, pipes, processos e transport adapter são reais. ACK, conexão Voice e UDP são simulados e rotulados. Metadata Spotify de 1/16/10/50/123/302/500 é determinística. A capa/identidade das demais entradas não são fabricadas como prova musical: elas ficam pendentes e bloqueadas até cancelamento.

Cache frio: SearchCache e catálogo/mappings novos; o resolver executa descoberta e confirmação controladas. Quente: metadata/primeiro match preparados fora do cronômetro, sem guardar áudio ou URL assinada. Após restart: reabrimos SQLite e descartamos o cache em memória antes de um novo /play; isso mede nova reprodução com catálogo/mappings persistentes, não tempo de boot do SO nem reconstrução de uma fila de 500 restaurada. QA-014 continua cobrindo a recuperação funcional da fila. No caso shuffle, a seleção inicial aguarda um canal, a fila pendente é embaralhada e a pipeline é liberada; o TTFA inclui essa intervenção. O caso progressivo confirma que a importação ainda existe e as posições restantes estão pendentes no primeiro envio.

## Baseline determinístico versionado

Ambiente: AMD Ryzen 7 9800X3D, 8 CPUs disponíveis, Linux x86-64, Go 1.27.1, FFmpeg n9.0.2, GOMAXPROCS padrão. Rede externa desativada logicamente por fixtures; sem Discord real. Cada linha simples tem 100 execuções; carga tem 100 rodadas simultâneas, com 100 amostras por guild. A campanha completa levou aproximadamente 212 s. O JSON inclui hardware, configuração e digest dos fontes Go da campanha; foi produzido no working tree do addendum antes de criar os commits. Não é um SLA de produção.

| Cenário | Cache | Faixas | Envios | p50 ms | p95 ms | p99 ms | Máximo ms | Resultado |
|---|---|---:|---:|---:|---:|---:|---:|---|
| spotify-single | cold | 1 | 100 | 27.84 | 31.03 | 33.75 | 43.26 | PASS |
| spotify-single | warm | 1 | 100 | 24.86 | 26.47 | 27.22 | 27.29 | PASS |
| youtube-single | cold | 1 | 100 | 25.85 | 28.16 | 29.53 | 31.80 | PASS |
| youtube-single | warm | 1 | 100 | 24.41 | 26.26 | 26.42 | 26.81 | PASS |
| spotify-album | cold | 16 | 100 | 29.31 | 32.17 | 34.37 | 34.62 | PASS |
| spotify-album | warm | 16 | 100 | 26.49 | 28.88 | 32.91 | 33.10 | PASS |
| playlist-10 | cold | 10 | 100 | 29.07 | 31.21 | 33.42 | 34.39 | PASS |
| playlist-10 | warm | 10 | 100 | 25.47 | 27.58 | 28.36 | 29.88 | PASS |
| playlist-50 | cold | 50 | 100 | 31.92 | 34.74 | 35.43 | 37.97 | PASS |
| playlist-50 | warm | 50 | 100 | 28.79 | 31.85 | 32.86 | 38.30 | PASS |
| playlist-123 | cold | 123 | 100 | 35.91 | 39.21 | 41.82 | 44.70 | PASS |
| playlist-123 | warm | 123 | 100 | 33.56 | 35.33 | 36.15 | 36.35 | PASS |
| playlist-302 | cold | 302 | 100 | 46.77 | 51.02 | 53.37 | 54.53 | PASS |
| playlist-302 | warm | 302 | 100 | 43.70 | 53.06 | 61.96 | 131.87 | PASS |
| playlist-500 | cold | 500 | 100 | 57.16 | 61.59 | 69.91 | 72.56 | PASS |
| playlist-500 | warm | 500 | 100 | 57.03 | 63.48 | 68.77 | 74.51 | PASS |
| youtube-force | cold | 1 | 100 | 24.90 | 27.46 | 28.58 | 32.52 | PASS |
| youtube-force | warm | 1 | 100 | 23.72 | 25.63 | 26.70 | 28.74 | PASS |
| voice-disconnected | cold | 1 | 100 | 27.26 | 29.52 | 31.81 | 35.77 | PASS |
| voice-disconnected | warm | 1 | 100 | 24.07 | 26.76 | 27.34 | 27.35 | PASS |
| after-restart | cold | 1 | 100 | 27.28 | 29.71 | 31.67 | 32.14 | PASS |
| after-restart | warm | 1 | 100 | 24.89 | 28.06 | 29.47 | 31.04 | PASS |
| after-shuffle | cold | 50 | 100 | 31.86 | 35.36 | 36.33 | 39.15 | PASS |
| after-shuffle | warm | 50 | 100 | 29.71 | 34.27 | 35.28 | 43.95 | PASS |
| progressive-active | cold | 500 | 100 | 58.61 | 63.83 | 67.11 | 67.25 | PASS |
| progressive-active | warm | 500 | 100 | 57.69 | 63.26 | 66.13 | 69.11 | PASS |
| load-2-guilds | warm | 500 | 200 | 87.49 | 95.58 | 99.42 | 102.12 | PASS |
| load-5-guilds | warm | 500 | 500 | 157.18 | 224.54 | 233.69 | 238.56 | PASS |
| load-10-guilds | warm | 500 | 1000 | 251.65 | 425.94 | 440.77 | 488.56 | PASS |
| ready-transition | ready | 2 | 100 | 25.22 | 27.34 | 29.13 | 32.02 | PASS |

Os casos de carga usam um Bot compartilhado, quatro permissões de busca, um MatchLimiter global e primeira gravação compartilhada em cache: são playlists simultâneas do mesmo primeiro recording, para testar reserva/prioridade/contensão e deduplicação. Não são dez caches isolados nem dez servidores Discord reais. Os workers não foram aumentados. O delta de p95 10 → 500 foi **35.90 ms**, abaixo dos 250 ms definidos. Há custo linear legítimo para metadata/snapshots; não se reivindica complexidade O(1) para toda a preparação.

## Falha encontrada e correção

Antes da correção, cinco guilds simultâneas esgotavam as quatro permissões globais de busca. O quinto comando recebia uma resposta de ocupado e não enviava áudio; o gate falhou após 8 s. Evidência: `evidence/instant-playback-admission-before.json`.

A admissão agora aguarda uma das mesmas quatro vagas, com deadline de 5 s e cancelamento pelo contexto da busca. Stop/leave continuam invalidando o pedido e a geração; a mensagem de cancelamento foi preservada após uma regressão detectada pela suíte. Não há aumento de MatchLimiter, workers ou prefetch. Regressões com synctest verificam espera, liberação e cancelamento sem sleeps arbitrários. Depois: 5 e 10 guilds enviaram áudio dentro de seus p95 de aproximadamente 225 e 426 ms no controle. Sob carga externa que ocupe as vagas por mais de 5 s, a resposta explícita de ocupado continua possível; o gate não confunde essa resposta com sucesso.

## Homologação com provedores e Discord reais

Foi usado o canal Voice autorizado `921489232374140978` na guild `404344088691867649`, em contêiner isolado, com bancos temporários. A conexão foi encerrada ao término. Imagem runtime do projeto (Debian bookworm, yt-dlp 2026.08.19, Deno 2.5.6, FFmpeg/libopus do runtime); o binário de teste contém o código do addendum. Token e credenciais não estão em artifacts; arquivos temporários de ambiente foram removidos. Nenhuma sincronização de slash commands ou mensagem de anúncio foi feita.

O probe usa interação sintética no dispatcher e ACK/edição REST simulados; Spotify, YouTube, FFmpeg, gateway Discord, sessão DAVE e envio UDP são reais. Isso mede o trecho local até o envio real, mas não substitui evidência de ACK REST de uma interação real. O gate de homologação exige `ack_transport=discord-rest` e `dave_verified=true`; estes probes **não podem aprovar a release**, mesmo que seus tempos fossem menores.

Existem três observações por modo, insuficientes para p95/p99 de release. Para cumprir o registro, a tabela mostra nearest-rank calculado; com n=3, p95/p99 são o máximo observado, sem significado de SLA. O primeiro cenário frio inclui uma conexão nova e duas já conectadas: seu máximo não deve ser usado como p95 de Voice já conectado. As demais amostras reutilizam a conexão; cold/warm se refere ao cache de metadata/matching, não a um cache de bytes de áudio. Spotify individual quente foi o único caso quente com três envios abaixo de 2 s (máximo 1989,78 ms); isso é uma observação favorável, ainda insuficiente para homologar p95.

Contagens reais foram verificadas separadamente em `evidence/instant-playback-source-counts.jsonl`: álbum com 10 faixas, playlists com 10/50/123/302/500. Fontes: [playlist 10](https://open.spotify.com/playlist/3tfeDqgxoMDd5sCmefEtx8), [50](https://open.spotify.com/playlist/6dADHYLaAAQ61tUZJyIljF), [123](https://open.spotify.com/playlist/35m4wmHSlcS2RNsafX4dEg), [302](https://open.spotify.com/playlist/6BL3uZVI7Q41TtpsLbmmLt), [500](https://open.spotify.com/playlist/0OdkLDK5wUdzAd2OT03hQz), [álbum](https://open.spotify.com/album/7eLwoxxWs6lfkVYJGkGNbk). São fontes mutáveis: o harness atualizado verifica o total retornado, em vez de confiar no nome da playlist. O JSON bruto desta primeira campanha deixou Count=0 como placeholder do álbum; a consulta independente confirmou dez faixas, e o harness atual usa/verifica dez. Os timestamps originais foram preservados.

| Cenário real | Cache | Faixas verificadas | Envios/tentativas | p50 ms | p95* ms | p99* ms | Máximo ms | Release |
|---|---|---:|---:|---:|---:|---:|---:|---|
| youtube-single | cold | 1 | 3/3 | 3582.22 | 15805.93 | 15805.93 | 15805.93 | FAIL |
| youtube-single | warm | 1 | 3/3 | 2088.35 | 2307.73 | 2307.73 | 2307.73 | FAIL |
| youtube-force | cold | 1 | 3/3 | 3238.45 | 3287.17 | 3287.17 | 3287.17 | FAIL |
| youtube-force | warm | 1 | 3/3 | 2281.25 | 2543.14 | 2543.14 | 2543.14 | FAIL |
| spotify-single | cold | 1 | 3/3 | 5315.94 | 6585.65 | 6585.65 | 6585.65 | FAIL |
| spotify-single | warm | 1 | 3/3 | 1788.28 | 1989.78 | 1989.78 | 1989.78 | FAIL |
| spotify-album | cold | 10 | 3/3 | 5067.94 | 5338.03 | 5338.03 | 5338.03 | FAIL |
| spotify-album | warm | 10 | 3/3 | 2746.57 | 2926.18 | 2926.18 | 2926.18 | FAIL |
| playlist-10 | cold | 10 | 3/3 | 5532.28 | 6158.05 | 6158.05 | 6158.05 | FAIL |
| playlist-10 | warm | 10 | 3/3 | 2934.53 | 3133.40 | 3133.40 | 3133.40 | FAIL |
| playlist-50 | cold | 50 | 3/3 | 7029.84 | 7250.84 | 7250.84 | 7250.84 | FAIL |
| playlist-50 | warm | 50 | 3/3 | 3532.65 | 3554.95 | 3554.95 | 3554.95 | FAIL |
| playlist-123 | cold | 123 | 3/3 | 9433.30 | 9912.66 | 9912.66 | 9912.66 | FAIL |
| playlist-123 | warm | 123 | 3/3 | 6620.81 | 7315.23 | 7315.23 | 7315.23 | FAIL |
| playlist-302 | cold | 302 | 3/3 | 8187.60 | 8330.59 | 8330.59 | 8330.59 | FAIL |
| playlist-302 | warm | 302 | 3/3 | 4999.28 | 5094.16 | 5094.16 | 5094.16 | FAIL |
| playlist-500 | cold | 500 | 3/3 | 53831.70 | 56636.88 | 56636.88 | 56636.88 | FAIL |
| playlist-500 | warm | 500 | 0/3 | — | — | — | — | FAIL |

A playlist de 500 quente falhou três vezes no warmup de metadados, antes de existir primeiro frame. São falhas, não amostras de 0 ms. Uma reprodução adicional de metadata levou 42,53 s e retornou API Spotify 403, seguida de fallback público interrompido por HTTP 504 durante hidratação de 400 faixas. Nenhuma faixa foi publicada sem completude. Essa reprodução confirma uma falha externa compatível com o warmup; os três erros originais não guardaram seu status HTTP específico. O erro sanitizado está em `evidence/instant-playback-metadata-diagnostic.json`. Não repetimos indefinidamente as chamadas externas para tentar obter PASS.

### Gargalos e orçamento por etapa

Medianas observadas, separadas por caso (ms; sem somar medianas como se fossem um sample único):

| Cenário | Metadata | Matching | Extração stream | Extração → FFmpeg* | FFmpeg → Opus | Opus → UDP |
|---|---:|---:|---:|---:|---:|---:|
| spotify-single/cold | 332.41 | 3087.19 | 1933.49 | 31.59 | 33.44 | 0.10 |
| spotify-single/warm | 0.10 | 0.03 | 1724.01 | 24.75 | 30.40 | 0.08 |
| playlist-123/cold | 4907.54 | 2779.32 | 1466.09 | 27.55 | 28.90 | 0.08 |
| playlist-123/warm | 4755.34 | 2.65 | 1795.07 | 21.80 | 28.31 | 0.08 |
| playlist-302/cold | 3469.69 | 2786.45 | 1631.04 | 150.65 | 28.19 | 0.08 |
| playlist-302/warm | 3211.39 | 4.60 | 1825.10 | 22.70 | 28.89 | 0.09 |
| playlist-500/cold | 48668.10 | 3081.55 | 2046.20 | 60.95 | 29.10 | 0.09 |

*Extração → FFmpeg inclui a espera pelo primeiro byte após o marker yt-dlp; não é uma medição pura do custo de fork/exec. A rede fica embutida em Spotify/yt-dlp, e o protocolo interno do yt-dlp não permite atribuir DNS/TLS/download separadamente com estes traces. Não inventamos essa decomposição. Em condições normais, FFmpeg → Opus foi ~28–33 ms e Opus → UDP ~0,07–0,12 ms. No primeiro início, Opus → UDP foi **11270,92 ms**: o log real registrou commit DAVE não confirmado, reset e recuperação cerca de 15 s após a conexão. Não removemos a barreira DAVE para atender uma meta.

Para 500 faixas frias, só metadata levou 45,84–51,65 s; o fallback completo hidrata 400 entradas fora do prefixo Embed com quatro workers e valida a revisão da playlist antes de publicar a coleção. O matching das 499 restantes não impede o primeiro áudio; a **preparação síncrona da metadata completa** é o gargalo real. Para 302 quentes, metadata mediana de 3,21 s já ultrapassa a meta de TTFA de 2 s antes da extração do áudio. Isso explica por que fixtures de metadata pronta passam e os provedores reais não.

Orçamento proposto para estudar o alvo de 2 s, **sem mudar o gate**: metadata ≤100 ms, match/cache ≤100 ms, extração/primeiro byte ≤1600 ms, FFmpeg/Opus ≤100 ms, transporte conectado/DAVE pronto ≤100 ms. A implementação atual não garante esses suborçamentos. Coleções grandes exigiriam uma decisão específica sobre cache de manifesto/revisão, preservando verificação de completude e Spotify Fidelity. Miss de matching observado ~2,5–3,1 s mais extração de stream ~1,5–2,2 s pode exceder os 5 s; isso precisa ser tratado como falha externa observada, não como PASS de resposta rápida.

O probe separado de provedores sem Discord (`instant-playback-provider-observations.json`) também executou yt-dlp real: três amostras por modo de YouTube normal/Force e Spotify individual. Esses valores não são misturados com a tabela determinística ou com os envios Voice reais.

## CPU, memória, goroutines e subprocessos

A campanha completa registra CPU SELF do processo Go (Go + SQLite), alocações acumuladas, heap/goroutines amostrados durante atividade e após cleanup, e pico de processos instrumentados. CPU SELF exclui filhos FFmpeg/yt-dlp, portanto não é CPU total da pipeline. Alocação cumulativa não é memória residente; amostras de heap não são RSS nem um pico contínuo de produção.

| Controle | Envios | CPU SELF acumulada ms | Alocação acumulada MB | Heap amostrado máximo MB | Goroutines amostradas máximas | Goroutines após cleanup | Subprocessos máximos | Matching máximo |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Spotify quente | 100 | 419.0 | 103.9 | 2.82 | 16 | 3 | 2 | 1 |
| Playlist 500 quente | 100 | 6208.8 | 1882.4 | 6.43 | 20 | 4 | 2 | 3 |
| 10 guilds, 500 cada | 1000 | 48149.6 | 17423.2 | 22.18 | 291 | 3 | 6 | 4 |

Cada fixture cancela os jobs, aguarda player/processos e exige matching/subprocessos zerados. A margem máxima após cleanup é de quatro goroutines além do início para runtime/GC; o pico ativo é registrado separadamente. O MatchLimiter não ultrapassou quatro slots e não houve aumento de workers. O probe adicional sob race fez 200 envios (100 por guild), sem race: p95 instrumentado de 1668,92 ms, heap amostrado de 6,68 MB e 27 goroutines; ao drenar, três goroutines como no início. Essa evidência está separada em `instant-playback-resource-race.json`, sem misturar seus tempos com o baseline sem instrumentação.

## Benchmarks reproduzíveis

```sh
go test ./internal/discord -run '^$' -bench '^BenchmarkInstant' -benchmem -benchtime=100ms -count=3
```

Inclui resolução com cache, sem cache, preparação de playlists, startup do Streamer, comando até primeiro frame e EOF até próximo frame pronto. Evidência: `evidence/instant-playback-benchmarks.txt`. Resolução controlada quente ~2,04–2,10 µs; fria ~1,93–1,98 ms. São fixtures de processo, não números de YouTube real. Na transição, `eof_to_frame_ns/op` e o timer começam após notificação causal de EOF; a espera pela duração da faixa anterior fica fora da métrica.

O baseline anterior da admissão e o atual são versionados; diferenças pequenas de tempo entre execuções não são reivindicadas como otimizações. A mudança comprovada é passar de rejeição sem áudio sob cinco solicitações para envio nas dez guilds simuladas. A otimização anterior de páginas Opus é documentada em `playback-startup.md`, com seu experimento antes/depois independente.

## Homologação reproduzível e decisão de release

`Instant playback real homologation` é manual, no environment `external-tests`. Pode avaliar um JSONL versionado de evidências ou abrir o canal explicitamente informado para probes isolados, sem deploy. Exige credenciais configuradas como secrets. O teste `TestInstantPlaybackDiscordLive` requer `BARD_DISCORD_LIVE=1`, `BARD_LIVE_GUILD`, `BARD_LIVE_CHANNEL`; verifica que o destino é Voice, usa somente bancos temporários e não chama Start/SyncCommands. `BARD_LIVE_SAMPLES` permite 1–100; o default de três é investigação, não aprovação estatística. A coleta por tentativa tem deadline de 75 s e cancelamento, incluindo warmup, para evitar bloqueio indefinido.

```sh
python3 scripts/instant_playback_gate.py --kind real --input evidence.jsonl --output real-gate.json
```

O gate real exige a matriz completa, 100 amostras por cenário, DAVE confirmado e ACK REST real, além dos mesmos limites. A campanha autorizada aqui tem Voice/DAVE real, mas ACK simulado, três amostras por cenário básico e nenhuma medição real de restart/shuffle/EOF/carga multiguild. As demais guilds não foram autorizadas. Assim, **release de performance real NÃO APROVADA**. A entrega do gate está concluída; não se amplia automaticamente a campanha até mascarar a variabilidade. O próximo passo de homologação é uma sessão de interações Discord reais, após tratar os gargalos de metadata/extração e a recuperação inicial DAVE. A suíte unitária passar não libera esta aprovação.

## Validação

Testes direcionados de admissão/cancelamento, avaliador e campanha determinística passaram. `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...` e `docker compose build` passaram após corrigir a mensagem de cancelamento identificada pela suíte existente. O gate extenso fica no job dedicado, não é repetido em toda execução normal de go test. Regressões QA-013/QA-014/FIX-014 existentes continuam incluídas. Houve um probe de race direcionado ao harness de carga, separado do resultado determinístico. Sem fuzzing, mutation testing genérico ou campanha de milhões de execuções.

Commits separados: correção de admissão/timestamps `2920a29`; benchmarks/gates/relatório no commit com assunto `test(performance): enforce instant playback TTFA and homologation gates (PERF-017)`.
