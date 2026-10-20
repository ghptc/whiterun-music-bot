# PERF-017 — startup de playback

Data: 2026-10-20. Base: `498e5e4`, inicialmente sem mudanças locais e igual a origin/main após fetch. Implementação local, sem deploy, sem executar comandos Discord nem modificar produção.

## Resultado e alcance

O maior custo observado nos três inícios reais persistidos é a preparação Spotify da coleção (3,51–7,05 s), seguida de matching/revalidação (1,11–3,23 s) e extração/entrada de áudio (~1,8–2,3 s). A conexão Voice ocorre antes da preparação, acrescentando 0,73–0,99 s. Resposta/ACK não é início de áudio.

Foram implementadas duas mudanças de performance pontuais:

1. `-page_duration 20000` na saída Ogg/Opus do FFmpeg. Evita esperar a página padrão de 1 s em fontes que chegam lentamente. Codec libopus, estéreo, 48 kHz, 96 kbps CBR e frames de 20 ms permanecem iguais.
2. Catálogo de coleção gravado em uma transação SQLite, com um orçamento total de 200 ms para esse cache opcional. Antes havia uma transação e um orçamento de 200 ms por faixa. Mesma whitelist, preservação de artwork e semântica de identidade; rollback integral em erro. Filas, histórico e eventos mantêm sua persistência existente.

A melhoria grande comprovada é no ensaio de entrada em tempo real: mediana até primeiro pacote Opus **1504,04 → 23,30 ms** (8 amostras por configuração). Isso não equivale a uma melhoria de 1,48 s em todos os /play: nos seis streams históricos o intervalo entre primeira entrada e primeiro Opus já era apenas ~38–59 ms, pois bytes chegavam em rajadas. Não há nova medição ponta a ponta no Discord após as mudanças; metas p95 de 2/5/1 s continuam não demonstradas.

## Caminho crítico e dados persistidos

Cópias de `bard.db` e `bard-logs.db` do volume Docker local, montado somente para leitura; nenhum banco bruto ou token incluído no repositório. Serviço local já estava parado. Banco de logs: 19 comandos, três /play de coleções, seis streams; catálogo: 476 identidades.

Fluxo: recebimento → ACK HTTP → conexão Voice/DAVE quando ausente → Spotify API ou fallback público/Embed com validação de paginação/revisão → restauração/gravação do catálogo → reserva da fila → matching prioritário da primeira faixa → busca/mapping e verificação de candidato → commit ordenado → verificação de identidade antes de tocar → yt-dlp streaming → marker de extração completa → primeiro byte → início FFmpeg → primeira entrada → primeira saída Ogg → primeiro Opus → speaking/pacing → WriteFrame → primeiro envio Discord.

`Timing` já distingue duração de etapa de idade do pedido. `first_discord_send` registra submissão UDP, não confirmação de audibilidade. `first_audio_frame` ocorre após WriteFrame. Para músicas subsequentes, request_elapsed inclui tempo na fila e não deve ser tratado como startup.

| Observação histórica | Playlist 302 A | Playlist 302 B | Playlist 123 |
|---|---:|---:|---:|
| Voice join | 731,62 ms | 819,62 ms | 994,15 ms |
| Preparação completa da coleção | 3794,11 ms | 3510,00 ms | 7053,33 ms |
| Primeira resolução | 1110,56 ms | 1431 ms | 3227 ms |
| Pipeline até primeiro frame | 2054,80 ms | 1851,22 ms | 2044,93 ms |
| Pedido até primeiro envio UDP | 8092,63 ms | 8523,01 ms | 13762,28 ms |

As duas primeiras usaram API Spotify e mapping encontrado (com revalidação quando lease expirou). A terceira recebeu 403 na API, usou fallback e matching sem mapping. Um mapping encontrado não significa fast path fresco. Nas seis reproduções: primeira entrada 1806,67–2258,11 ms após início da pipeline; primeira saída 1834,85–2293,34 ms; primeiro Opus 1844,42–2306,89 ms. Catálogo antigo fez 727 writes individuais de 0,593–2,193 ms durante esses pedidos. Logs de ACK usam campos próprios, e não fornecem amostras comparáveis de todas as etapas para cada cenário pedido.

## Experimentos comparativos

### FFmpeg

Reprodução: `python3 scripts/perf017_audio.py`. FFmpeg local real, seno 48 kHz, entrada em tempo real, mesma configuração de encoding. Cronômetro começa antes de iniciar o processo e termina ao ler a primeira página de áudio, ignorando OpusHead/OpusTags. O ensaio não inclui yt-dlp, rede nem Discord. O teste `TestStreamerRealtimeFirstFrame` também usa a pipeline real Streamer com um produtor FFmpeg em tempo real e Voice de teste: sem a flag falhou em 1548,77 ms; com a flag passou três execuções. Demais testes de processo/EOF/cancelamento continuam existentes.

| Configuração | n | Mediana primeiro áudio | Faixa |
|---|---:|---:|---:|
| Página padrão | 8 | 1504,04 ms | 1503,37–1509,47 ms |
| Página de 20 ms | 8 | 23,30 ms | 18,26–23,97 ms |

Sem p95/p99 com oito amostras. Evidência completa: `evidence/perf017-audio.jsonl`.

### Catálogo

Reprodução: `go test ./internal/storage -run '^$' -bench BenchmarkCatalogCollection -benchtime=300ms -count=3`. Mesmo banco temporário por sub-benchmark, repetição de atualizações com SQLite WAL, mesmas faixas. Compara o método individual anterior com lote, ambos no mesmo binário; inclui serialização e SQL. Disco/cache local, não representa fsync do host de produção.

| Faixas | Antes, tempo/op | Depois, tempo/op | Alocações antes → depois |
|---:|---:|---:|---:|
| 10 | 153,0–153,9 µs | 145,0–145,4 µs | 190 → 207 |
| 302 | 4,676–4,726 ms | 4,297–4,329 ms | 5740 → 6049 |

Ganho local modesto (~8% para 302). Benefício adicional: uma persistência atômica em vez de 302 commits e limite total sob contenção. Não extrapolar para ganhos de segundos. Evidência: `evidence/perf017-catalog.txt`. Teste cobre rollback e preservação de artwork quando metadados novos não contêm capa.

### Cache yt-dlp

Contêiner da imagem runtime existente, yt-dlp 2026.08.19, sem tokens/volumes de produção. Seis extrações alternadas do mesmo vídeo público, `--skip-download --print id`, três por modo. Mesmo conjunto de flags de rede/runtime, retries desabilitados no probe, timeout externo de 30 s. Cache habilitado em diretório mantido entre chamadas do mesmo contêiner, limitado por tmpfs a 16 MB e descartado ao terminar. Não foram persistidas URLs assinadas.

Sem cache: 1926,8 / 1950,5 / 2106,8 ms. Com cache: 2003,1 / 1786,4 / 1839,2 ms. Todas retornaram o ID esperado. **Nenhum arquivo de cache foi criado.** Três observações e variação da rede não sustentam um benefício causal. Mantido `--no-cache-dir`; não foi introduzido diretório persistente em produção. Não é benchmark de bytes de áudio.

## Matriz solicitada e lacunas

| Cenário | Evidência disponível |
|---|---|
| Spotify cache hit fresco | Cache/deduplicação/provas cobertos na suíte; sem novas amostras ponta a ponta |
| Spotify mapping encontrado, revalidação | Dois inícios reais de coleção acima; não equivalem a hit fresco |
| Spotify miss | Um início real com fallback, acima; sem percentis |
| YouTube Force Play / normal | Regressões existentes de seleção exata, metadata e streaming; sem medições Discord reais novas |
| Playlist 10 | Benchmark comparativo de persistência; preparação externa completa não medida |
| Playlist 302 | Duas preparações reais, benchmark local de persistência; sem importação real nova |
| Voice conectado | Três streams subsequentes históricos, pipeline 2,10–2,31 s; idade total do pedido excluída |
| Voice desconectado | Três joins históricos 0,73–0,99 s; sem nova conexão |
| Transição pronta | Cobertura funcional existente; sem novas amostras ponta a ponta para meta de 1 s |

Não simulamos números de latência externa a partir de mocks. Uma sessão controlada de Discord com credenciais e canal de teste continua necessária para completar a matriz e medir antes/depois ponta a ponta, sem testar destrutivamente em produção.

## Hipóteses preservadas ou descartadas

* Trabalho duplicado: metadata de matching e extração de formatos para streaming têm finalidades distintas. SearchCache já deduplica; proofs frescos evitam VerifySpotify redundante; mappings têm lease de 10 minutos. Não removemos revalidação nem armazenamos JSON completo de extração/URLs assinadas para reaproveitamento inseguro.
* Primeira faixa: MatchLimiter mantém quatro slots totais, três reserváveis por background; CollectionJob confirma a primeira antes de iniciar as demais. Workers continuam três e janela lógica seis, recalculada em mutações (inclui shuffle). FIX-014 e testes de wakeup permanecem intactos.
* Prefetch: já existe matching das próximas posições lógicas. Não abrimos streams paralelos; assinatura/expiração e cancelamento tornariam essa mudança maior que a evidência disponível.
* Voice em paralelo: serialização observada, mas não alterada nesta entrega; exige tratar corrida com leave, attach, requester/channel e geração antes de aceitar fila. Não se reivindica ganho nessa etapa.
* Flags agressivas de probing/buffer/codec: não usadas; poderiam comprometer formatos. Mudança limita-se à página do muxer, verificada com FFmpeg real.
* Renderer: ~60 µs para 123 entradas; nenhuma micro-otimização ou alteração de orçamento foi justificada. Correções de artwork estão na origem, descritas em `../ui/queue-rendering-investigation.md`.

## CPU, memória e custo

Ensaio local de encoding de 10 s: CPU acumulada dos filhos 63,49 → 66,36 ms; uma amostra por modo, insuficiente para regressão estatística. Ogg produzido 121173 → 134403 bytes (+10,9% de overhead de páginas na pipe interna). Discord continua recebendo os mesmos tipos de pacotes Opus de 20 ms; esse overhead Ogg não vai para Voice. Peak RSS acumulado de filhos foi 45928/47888 KiB, uma marca d'água do experimento inteiro, não medição isolada da RAM do bot. Nenhuma conclusão quantitativa de memória em produção é possível.

Catálogo 302: ~718 KB/op → ~723 KB/op (+~5 KB) e +309 alocações, em troca da transação única. Não há novo cache de áudio, processos permanentes ou streams antecipados. O lote mantém a única conexão SQLite ocupada por uma transação; orçamento de 200 ms limita a espera desse cache opcional.

## Verificação

`go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...` e `docker compose build` executados com sucesso. Testes QA-013, QA-014 e FIX-014 existentes incluídos nas suítes normais, sem iniciar campanha de endurance/milhões de execuções. Regressões novas limitadas a artwork, Unicode, página de áudio em tempo real e catálogo atômico. A construção Docker valida FFmpeg/libopus e não inicia o serviço. Logs de execução locais em `/tmp/perf017-*.log`; dados brutos de produção não versionados.

Commits separados por UI e performance, identificáveis por `git log --oneline --grep=PERF-017`. Nenhum deploy, push ou trabalho em /lyrics.
