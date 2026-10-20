# Fault injection e recuperação

Nenhum teste usou serviço compartilhado, credencial real, banco operacional ou falha destrutiva contra produção. O único SIGKILL novo é no PID de writer pertencente ao teste, com marker em t.TempDir. Nenhum código de produção foi alterado.

| Fronteira | Mecanismo e ponto | Evidência e resultado | Limite |
|---|---|---|---|
| yt-dlp | stand-in retorna erro antes de áudio em posições seedadas; extrator retorna JSON incompleto em testes existentes | soak events: unavailable e seleção seguinte na ordem; TestAdvancedIncompleteExtractorMetadata repetido | não valida comportamento do yt-dlp real no YouTube |
| FFmpeg | decoder real morto após primeiro frame, PIDs próprios; stall/EOF/helper dos testes existentes | TestAdvancedFFmpegKilledAfterFirstFrame, TestAudioStallAndDecoderAfterEOF, TestExitedSourceLeaderCannotLeakPipeHoldingHelper, três repetições sob race | falha dirigida separada do soak; não injetada em todos os segmentos |
| Transporte local | erro no quarto frame; frames anteriores aceitos | error_interrupted, avanço na ordem e cleanup | sem UDP/DAVE real |
| Contexto | cancelamento no quarto frame; término real de segmento; cancelamento de resolução existente | ledger distingue error_interrupted de shutdown_interrupted; nenhum terminal ausente | cancelamento interno não é skip do usuário |
| SQLite indisponível/escrita interrompida | conexão fechada/query_only, trigger e cancelamento após escrita | testes existentes de rollback/indisponibilidade, TestAdvancedInterruptedSQLiteTransaction repetido | query_only não equivale a ENOSPC |
| SQLite crash real de processo | SIGKILL após UPDATE guild, DELETE history, INSERT history ou INSERT tracks dentro da transação | TestEnduranceKillDuringSQLiteWriteRecoversCommittedState: 4 estágios × 3 repetições com race; duas guilds, histórico, artwork, settings, mapping e integrity_check preservados | kill do processo writer, não reboot físico nem perda de energia no fsync/commit |
| Cache/mapping | erro de leitura, stale, write/invalidation failure e shape legada isolada | TestHardeningMappingTrustAndFailureRecovery e TestHardeningLegacyMappingShapeIsNotCacheHit; constraint json_valid mantida | cache em memória por byte não instrumentado; não houve corrupção do SQLite de produção |
| DNS | DialContext do HTTP retorna net.DNSError controlado | TestEnduranceProviderDNSFailureIsNotPartialSuccess: sem coleção parcial e causa preservada, três repetições | sem consulta DNS real; não mede retries/latência do resolver do SO |
| TCP/HTTP provedor | servidor local fecha conexão, trunca resposta, retorna 429 ou aguarda cancelamento | TestAdvancedSpotifyNetworkFaults, três repetições | autoridades externas reescritas para localhost; sem falha em Internet real |
| Discord HTTP | 429 dentro/fora do budget, 503, timeout, conexão abortada; ACK real por TCP local no benchmark | TestAdvancedDiscordAckSchedules repetido; BenchmarkEnduranceLocalACK mede somente sucesso local | budgets usam relógio virtual em teste separado; não representam duração do soak nem homologação Discord |
| Force Play | suites de URL exata, falha sem substituição e contrato de slash | TestForce*, TestForced* na suíte completa; filtro TestForce repetido em fault-regressions.txt; IDs sintéticos no soak preservados | sem invocar slash/YouTube real no soak |

## Integridade sob comandos concorrentes

TestEnduranceTwentyUsers usa 20 goroutines, duas guilds e seeds 41/20261019. Cada usuário executa 100 operações, com entradas válidas/inválidas e estados que mudam enquanto concorrem. Três repetições: 12.000 operações reais na API Go. Trace final por seed em evidence/stress/; todas as repetições estão em stress.txt. A reprodução de sequência não fixa o scheduler.

Operações: enqueue/play, skip múltiplo, pause/resume, previous, remove, move, shuffle, jump, replay, loop, seek, clear, stop e snapshot. O play controlado entrega o primeiro frame para criar histórico real do player. Após cada operação, snapshot consistente deve respeitar capacidade, identidade/metadados e isolamento. Um contador de ownership detecta streams sobrepostos e Close precisa juntar os loops. Erros de comando esperados são gravados, não tratados como corrupção.

Esse teste não calcula sozinho a ordem correta de todas as transformações simultâneas. Os modelos/linearização independentes QA-008 foram preservados e reexecutados em go test completo/race; eles continuam cobrindo semântica de skip, previous, loop e conservação da fila após quiescência. Nenhuma mudança do player para acomodar o stress.

## Reinício

TestPendingCollectionRestartRestoresMetadataWithoutVoice interrompe uma resolução bloqueada em barreira, fecha o player, exige término do job e restaura a metadata pendente/artwork/mapping/settings. O player restaurado permanece sem voz e não aceita enqueue antes de Attach. A retomada real do harness reabriu o SQLite do segmento 123 e executou 20,252s adicionais com fila conservada.

History/current recovery é regido pela política existente: Current salvo pode reiniciar do começo após reconexão explícita, removendo a última entrada de histórico já marcada para não duplicar navegação. Não há retomada de timestamp. Essa propriedade também é coberta por TestQualityDurableRecovery e pelos testes existentes de restart/histórico.

## Falhas encontradas no trabalho QA

| ID | Severidade | Causa confirmada | Regressão/correção |
|---|---|---|---|
| QA010-H01 | alta para validade da evidência; sem impacto no bot | piloto permitia PASS sem áudio e atraso padrão de saída de subprocessos race consumia o tempo | teste do validador rejeita no-work/never-audible; producer usa atexit_sleep_ms=0; exportador também rejeita piloto; smoke com áudio passou |
| QA010-H02 | baixa, bloqueio de build QA | range copiava estrutura com mutex na tabela de accounting | fixture usa ponteiros; falha vet/Docker anterior preservada e validação repetida |

Nenhum defeito novo de produção foi confirmado nos cenários executados. Não houve alteração de produção silenciosa nem mutante restaurado parcialmente. Falhas físicas de disco, netem prolongado, comportamento real de provedor e reconexão DAVE continuam pendentes; não foram substituídos por mocks declarados como E2E.

## QA-011 — resultados após uma hora

O mesmo harness foi executado por 3.600,141s reais com 140 indisponibilidades de fonte e 274 interrupções de transporte/contexto. O ledger registrou 414 avanços após falha e 140 cadeias recuperadas até uma faixa completed. As duas interrupções finais são shutdown planejado, não erros ocultos nem skips de usuário. Não houve cadeia de falhas sem recuperação, terminal perdido ou filho próprio remanescente após cleanup.

Nova observação QA011-R01: RSS cresceu sustentadamente no binário com race, com heap Go baixo e estável. Severidade: bloqueio da aprovação operacional/continuidade 6h–12h; impacto em produção ainda não confirmado. Causa raiz não estabelecida; overhead documentado de race é hipótese consistente e foi confrontado com controle de dez minutos sem race (RSS baixo, sem tendência comparável). Reprodução: comando original e hash em evidence/qa011/execution.json; séries em evidence/qa011/1h/. Nenhuma correção de produção proposta como confirmada e nenhuma refatoração realizada. Não há reprodução de defeito funcional novo a converter em regressão antes da triagem desse consumo.
