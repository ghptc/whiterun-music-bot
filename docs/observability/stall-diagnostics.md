# Diagnóstico de stalls

Primeiro localizar guild, instante UTC e EOF; correlacionar attempt/sequence,
coleção, geração e slot original. Um EOF normal não indica erro de decoder.
`transitioning` é uma decisão intermediária, não um diagnóstico de deadlock.

| Condição | Evidência / próximo evento esperado |
|---|---|
| Próxima pronta | scheduler_advance → playback_start → yt-dlp → FFmpeg → primeiro Opus → primeiro envio Voice |
| Próxima pendente sem resolução ativa | resolver_window_wakeup e resolução/commit; ausência pode indicar notificação perdida |
| Resolvendo | track_resolution_complete → resolution_position_commit → scheduler_wakeup |
| Provider bloqueado | provider_recovery / resume explícito; não alertar como stall de scheduler |
| Pausado | playback_pause_changed; resume ou cancelamento; scheduler não seleciona sucessora durante track ativo |
| Vazia / voice detached | queue_insert / voice_attach; sem alarme |
| Geração antiga | queue_generation_rejected ou resolution_commit_discarded; não esperar commit ressuscitar posição removida |
| Stop/cancelamento | playback_cancel_requested, wakeup, término de workers; sem skip automático |

Um timer por espera registra WARN após 30 segundos em cabeça pendente/resolvendo. Uma cabeça que ficou pronta sem notificação
é registrada como notification_missing; o timer continua sem avançá-la.
É limiar diagnóstico, não SLA: uma resolução saudável de até 90 segundos também
pode emitir aviso. O aviso informa condição, tempo, active_resolutions, cabeça,
contagens, último motivo notificado, próximo evento esperado e goroutines.
Não cancela processos nem faz skip/retry. Não repete para a mesma condição sem
mudança e não emite heartbeat. Pause, provider bloqueado, voz ausente e fila
vazia ficam fora do alerta. A falta de notificação é uma hipótese a confirmar
com resolver_window_wait + mutação + ausência de resolver_window_wakeup.

## CLI e SQL

```sh
docker compose exec -T bard bard logs --since 1h --event scheduler_wait
docker compose exec -T bard bard logs --since 1h --event scheduler_stall_detected
docker compose exec -T bard bard logs --guild GUILD_ID --last 10000
# Host: go run ./cmd/bot logs --db /path/bard-logs.db --last 10000
```

CLI combina filtros com AND. Exporte antes da retenção expulsar o incidente.
Consulte banco com `sqlite3 -readonly /path/bard-logs.db`; nunca use immutable
para arquivo vivo com WAL. Leitores devem ser curtos para não bloquear checkpoint.
A tabela se chama `logs` (schema em internal/logstore/store.go).

```sql
SELECT id, event, level, guild_id, collection_id, position,
       json_extract(payload, '$.sequence') AS sequence,
       json_extract(payload, '$.playback_attempt_id') AS attempt,
       json_extract(payload, '$.queue_generation') AS generation,
       json_extract(payload, '$.wait_reason') AS wait_reason,
       json_extract(payload, '$.expected_event') AS expected
FROM logs
WHERE guild_id = 'GUILD_ID'
ORDER BY timestamp_ns, id LIMIT 10000;

SELECT event, count(*) AS events FROM logs GROUP BY event ORDER BY events DESC;

SELECT id, event, payload FROM logs
WHERE event IN ('queue_mutation','shuffle_completed','resolver_window_wait',
 'resolver_window_wakeup','resolution_position_commit','scheduler_stall_detected')
ORDER BY timestamp_ns, id LIMIT 10000;
```

Valide o nome da tabela e colunas com `.schema` antes de usar outra versão.
Veja [logging-gaps.md](logging-gaps.md) para limites históricos e de correlação.

## Validação

Regressão com relógio virtual reproduz EOF com janela adormecida após Move;
shuffle durante resolução conserva identidades. Testes sem rede verificam wait,
reason, wakeup, cause, commit, stale generation, EOF pronto/pendente, provider,
pause, stop, aviso prolongado e volume estável em idle. Saturação da fila de
diagnósticos verifica admissão sem espera pelo writer. Suites existentes de
collection/stream/lifecycle cobrem timeout, cancelamento, teardown e concorrência;
logstore cobre redaction, DB indisponível, falhas de escrita, concorrência e
admissão não bloqueante. Isso não constitui E2E de playback Discord em produção.


## Matriz de cobertura solicitada

| Requisito | Evidência automatizada |
|---|---|
| 1–4 espera, motivo, wakeup, causa | TestSchedulerDiagnosticDecisions, TestSchedulerBlockingConditions |
| 5 resolução concluída | TestResolutionCommitAndStaleGenerationDiagnostics e testes progressivos de collection |
| 6 geração obsoleta | TestResolutionCommitAndStaleGenerationDiagnostics |
| 7 shuffle durante resolução | TestShuffleWhileResolutionActiveKeepsSlotIdentity; TestReorderedPendingHeadWakesSleepingResolver/shuffle |
| 8–9 EOF pendente/pronto | TestEOFReadyAndPendingDiagnostics; TestReorderedPendingHeadWakesSleepingResolver/move |
| 10–11 provider, pause | TestPauseAndProviderDoNotReportStall |
| 12–13 stop/cancelamento | TestSchedulerDiagnosticDecisions, TestProgressiveCancellation, TestCollectionPerPositionTimeoutAndCancellation |
| 14 stall prolongado | TestSchedulerDiagnosticDecisions, TestStallObserverDoesNotAdvanceWithoutNotification |
| 15 volume | TestSchedulerDiagnosticDecisions (cinco minutos virtuais sem novos eventos idle) |
| 16 correlação | TestPlaybackAttemptCorrelation (mesmo attempt no stream, início, fim, terminal, transição) |
| 17 redaction | TestRedactionCredentialsPrivateMessagesAndProviderBuffers (logstore) |
| 18 falha DB | TestUnavailableAndFullDiskRecoverWithoutConsoleLoss, TestWALReaderStallsPersistenceNotProducer |
| 19 concorrência | TestConcurrentDiagnosticPressure, TestConcurrentWritesBatchAndFinalFlush e race detector |
| 20 não bloqueio | TestDiagnosticAdmissionDoesNotWaitForHandler, TestBlockedDiagnosticSinkDoesNotBlockPlayback |

O teste de não bloqueio cobre o novo caminho assíncrono; o destino console dos
emissores legados continua síncrono, conforme a auditoria. As suites de logstore
são as existentes, executadas novamente; não são 20 testes novos de integração.

Validação local em 2026-10-20: `go test ./... -count=1`,
`go test -race ./... -count=1` e `go vet ./...` passaram. As regressões de
reordenação e shuffle com workers ativos também passaram com race detector.
A regressão Move falhou antes da correção com `current=nil` após EOF e passou
após acordar os coordenadores. Fixtures antigas foram ajustadas para proteger
inicialização de fila com mutex e finalizar writers antes de ler bytes.Buffer.
Não houve deploy, reinício da produção ou teste de audibilidade no Discord.
