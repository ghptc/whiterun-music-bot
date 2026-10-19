# Bard Fast Match: diagnóstico, arquitetura e medições

Execução: 18/10/2026. Baseline desta mudança: `47e8e6b`. As fixtures de produção
já existiam no repositório, com proveniência descrita em
[fidelity-production-regressions.md](fidelity-production-regressions.md).
Não houve deploy, sessão de voz ou E2E no Discord.

## Diagnóstico e limites da evidência

`docker compose ps` não encontrou containers neste daemon. Não há log da tentativa
histórica que permita identificar o erro exato de Little Bad Girl. Seu Spotify ID
`55VzNhbcf0Gxf6qRCUAV01` é aceito tanto no baseline quanto nesta alteração.
O código anterior permite reproduzir o problema de apresentação: um stream envia
um frame, anuncia Now Playing, falha e chama `announceSkip`, que imprime Skipped.
Também chamava a mesma função para resolução antecipada de posições da coleção.
Portanto Skipped, sozinho, não comprova salto de playback nem erro de associação.
A nova regressão de apresentação falha no baseline com esse texto.

Whistle e My House têm rejeições por ambiguidade reproduzidas em `81e10a5`, conforme
investigação anterior. O baseline **desta** mudança já as aceita. Não atribuímos
uma nova redução de falsos negativos à otimização atual. Memories não tem fixture
histórica; Right Round e Good Feeling aparecem em pools e extrações incidentais,
mas não há a identidade Spotify original e o pool da tentativa rejeitada.
Esses três incidentes permanecem sem causa comprovada. Inventar uma identidade
Spotify a partir de um upload não resolveria essa lacuna.

Cada slot tem collection ID, índice interno e posição original. Resoluções
concorrentes publicam prefixos ordenados, sem anexar resultados atrasados à fila.
O matcher preserva a identidade Spotify; agora o coordenador também rejeita
retornos com outro Spotify ID. Cancelamento remove slots, cancela buscas e impede
commit tardio. A gravação de falhas verifica novamente se a posição ainda é desejada.
Não foi encontrada evidência de que uma posição tenha recebido o resultado de outra
no corpus disponível. Há regressões de cancelamento, ordem e callbacks obsoletos.

## Caminho de resolução

1. O SearchCache compartilhado consulta metadados e coalesce misses concorrentes,
   com cancelamento independente por solicitante. TTL existente: dez minutos.
2. No miss, consulta SQLite por chave `spotify:<TrackID>:<hash de identidade/política>`.
   A coluna identity é chave primária. A posição e o solicitante não integram a chave;
   metadados/versionamento continuam protegendo contra reutilização incompatível.
   A mudança de formato invalida naturalmente as chaves antigas, que expiram.
3. Mapeamentos persistentes retêm-se por 24 horas. Extrações completas recebem
   `CheckedAt` local; o provedor não pode definir esse timestamp. Durante os mesmos
   dez minutos do cache existente, reuso exige nova comparação dos metadados com
   a identidade solicitada. Timestamp ausente, futuro ou antigo exige extração.
   Revalidação válida renova o mapeamento; metadados incompatíveis o invalidam.
4. No miss persistente, consulta YouTube Music. O primeiro candidato ranqueado passa
   por extração completa. Só há retorno antecipado com lançamento independentemente
   corroborado: fonte compatível, artista/créditos/título/versão/duração válidos e
   ISRC correspondente ou lançamento auto-gerado com álbum/track/créditos exatos.
   O delta máximo desse caminho continua sendo dois segundos. Não há threshold novo.
5. Sem prova suficiente, busca vídeo e segue o seletor completo existente. IDs já
   extraídos não são extraídos novamente. Contradição explícita de ISRC no pool
   desabilita o retorno antecipado. Grupos de lançamentos conflitantes continuam
   incertos. O fast pass não transforma score, posição na busca ou badge em identidade.
6. Mantidos os tetos de quatro buscas, oito verificações únicas, dez resumos por
   busca, quatro candidatos por rodada, 15 s por subprocesso e 90 s por resolução.
   Falta de prova termina com MatchError rastreável; não há loop ilimitado.

O resultado carrega uma prova interna imutável de metadados, ligada ao vídeo.
Antes do stream, `VerifySpotify` confere URL/ID, idade e identidade novamente.
Uma prova fresca evita outra chamada de metadados. Ela não é serializada na fila;
restauração durável exige nova verificação. O stream continua extraindo formatos
atuais e pode falhar por remoção, região, autenticação ou rate limit. A janela de
validade é uma concessão explícita de frescor, não garantia de disponibilidade.
Nunca são persistidas URLs assinadas de áudio.

Prefetch e concorrência preexistentes foram mantidos: três resoluções por coleção,
janela de seis posições, limite global de quatro com reserva de capacidade para
primeira faixa. Não foram criados workers adicionais, heap ou nova infraestrutura.

## Estado e anúncios

`PENDING → RESOLVING → READY → STARTING → PLAYING → FINISHED`.
`FAILED` e `CANCELED` são terminais separados. A fila expõe Pending/Resolving/Ready;
o snapshot atual expõe Starting/Playing; eventos e logs registram os terminais por
sequência. Essas fases descrevem tentativas, sem manter histórico ilimitado em RAM.

Now Playing só é emitido depois de `WriteFrame` retornar sucesso. O callback deve
corresponder à sequência atual, ter contexto ativo e não ter sido anunciado antes.
Uma falha de resolução produz Unavailable; falha antes do primeiro frame produz
Playback failed to start; falha posterior produz Playback interrupted. Nenhuma
delas é apresentada como skip solicitado. O callback legado `SetOnSkip` continua
transportando falhas, agora com State, FailureStage e Sequence; consumidores devem
usar esses campos. Nome, Spotify ID e posição original são preservados no Discord.
Falhas de coleção são registradas uma vez por slot; eventos de início, uma vez por
sequência. O worker REST continua limitado e assíncrono, sem garantia de entrega.

## Resultado dos replays sem rede

| Faixa | Buscas/verificações antes | Depois | Resultado |
| --- | ---: | ---: | --- |
| Little Bad Girl | 2/4 | 2/4 | mesmo vídeo correto |
| Whistle | 2/4 | 1/1 | mesmo vídeo correto |
| My House | 4/5 | 4/5 | mesmo vídeo correto |
| Pumpkin Edit | 2/1 | 1/1 | mesmo vídeo correto |

Total: buscas 10 → 8; verificações 14 → 11. São chamadas de metadados, sem a
extração adicional do stream. Os quatro positivos passam antes e depois (0/4 falsos
negativos). Covers, remixes e artista incorreto não vencem a alternativa correta;
contradições conhecidas continuam rejeitadas. Nenhum falso positivo conhecido nas
fixtures executadas; isso não mede taxa de erro em tráfego real.

Cache fresco: zero chamadas yt-dlp de metadados, inclusive após reabrir SQLite.
Cold start pode usar até 12 chamadas de metadados, mais extração de stream. Prova
expirada antes do playback pode acrescentar uma revalidação. Cache hit não elimina
rede e processos necessários ao áudio. Hit rate de produção não foi medido.

As medições brutas ficam em [metrics/fast-match](metrics/fast-match). O microbenchmark
usa o lançamento real Whistle como fixture, sem rede, disco SQLite ou áudio.
`BenchmarkCachedSpotifyIdentity` mede resolução em RAM mais verificação por prova
fresca: aproximadamente 66 µs/op, 105 kB/op, 1.597 alocações/op. É tempo de CPU do
caminho de metadados, não tempo de início de áudio.

Medição isolada de 10.000 decisões, binários compilados previamente, Linux/AMD
Ryzen 7 9800X3D, Go 1.27, GOMAXPROCS 8:

| Métrica do microbenchmark | Baseline | Atual |
| --- | ---: | ---: |
| Tempo por decisão | 245.783 ns | 195.607 ns |
| Bytes alocados por decisão | 411.173 | 312.862 |
| Alocações por decisão | 6.192 | 4.723 |
| CPU total user + system | 2,886 s | 2,250 s |
| Pico RSS do processo | 26.100 KiB | 31.580 KiB |

A redução observada é ~20% no tempo de decisão e ~24% nos bytes alocados. RSS
não melhorou nesta execução (+5,35 MiB); inclui runtime Go e coletor de lixo.
Uma execução isolada não demonstra comportamento de memória sob carga contínua.
Para repetir: `go test -c -o /tmp/bard-media.test ./internal/media` e
`python scripts/measure-matcher.py /tmp/bard-media.test`. Execute separadamente
em cada checkout; a medição não inclui compilação. JSONs preservam a execução
original; três rodadas padrão de benchmark também foram realizadas.

O teste de 123 posições, com dependências simuladas de 10 ms, mediu primeiro frame
falso em 20,42 ms no baseline e 20,45 ms depois. A melhoria desse teste contra o
modo atômico (~330 ms) já existia antes desta alteração. Não há nova melhoria
mensurável de transição entre faixas nesse teste. `/play → primeiro frame` real e
intervalo real entre faixas permanecem não medidos. Os eventos `first_discord_send`
(request_elapsed_ms) e `next_track_start` (next_track_delay_ms), ligados a sequência,
Spotify ID e posição, permitem medi-los em uma sessão real. Envio UDP não prova
audibilidade no cliente. Serviço externo impede promessa de reprodução instantânea.

## Regressões e reprodução

A suíte cobre os quinze cenários solicitados, combinando fixtures reais positivas e
mutações adversariais explicitamente sintéticas:

| Cenário | Testes principais |
| --- | --- |
| Cache hit e reinício SQLite | TestFreshPersistentMappingAvoidsMetadataProcessAcrossReopen, TestIdentityLeaseBoundariesAndTrackBinding |
| Cache expirado | TestValidatedMappingReusedAndInconsistentVideoInvalidated, TestSearchCacheExpiryBoundAndRequestIsolation |
| Primeiro correto | TestFastMatchStopsAfterCorroboratedRelease |
| Primeiro incorreto, segundo correto | TestMisleadingFirstSummaryRequiresSecondVerification |
| Uploads equivalentes / gravações diferentes | TestRecordingGroups, TestFastDiscoveryDoesNotHideKnownConflictingRecordings |
| Delta de 2 s, cover, remix | TestFastMatchAdversarialAndSecondCandidate, TestEquivalentUploadsDoNotBypassVersionChecks |
| Playlist 123 / ordem | TestProgressiveFirstStartsWith122Unresolved, TestProgressiveOutOfOrderContiguousPrefix |
| Skip durante resolução / callback tardio | TestProgressiveSkipFiftyWithBoundedPrefetch, TestProgressiveCancellation, TestStaleStartCannotAnnounceCurrentAttempt |
| Now Playing seguido de falha | TestStartedAttemptFailureIsInterrupted, TestFailureAnnouncementsDistinguishResolutionAndInterruptedPlayback |
| Falha de stream | TestSourceSurvivesAudioEOFCleanup, TestAudioStallAndDecoderAfterEOF |
| Múltiplos usuários | TestProgressiveConcurrentReservationsAndGuildIsolation, TestCoalescedSearchHasIndependentCancellation |
| Associação de Spotify ID | TestCollectionCannotCommitAnotherSpotifyIdentity |

Comandos de validação: `go test ./... -count=1`, `go test -race ./... -count=1`,
`go vet ./...`. Benchmarks: `go test ./internal/media -run '^$' -bench
'Benchmark(FastMatchRealFixture|CachedSpotifyIdentity)' -benchmem -count=3`.

Para demonstrar os defeitos anteriores, exportou-se `git archive 47e8e6b` para
`/tmp/bard-fast-baseline`, copiando a fixture helper `fastFixture`, o teste
`TestFastMatchStopsAfterCorroboratedRelease` e `BenchmarkFastMatchRealFixture` para
um arquivo de teste com imports context/encoding/json/os/testing, além do arquivo
`internal/discord/failure_notice_regression_test.go` completo. Ambos os testes
falham nesse checkout: duas buscas em vez de uma, e falha apresentada como Skipped.
Os mesmos testes passam nesta alteração. As outras correções não são atribuídas
ao baseline sem reprodução correspondente.
