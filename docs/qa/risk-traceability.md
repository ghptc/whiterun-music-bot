# Matriz de riscos e requisitos

| ID | Prioridade / invariante | Evidência existente | Ampliação planejada |
|---|---|---|---|
| Q01 | P0 ordem, capacidade, ausência de perdas | controls_test, skip_test, collection_test | QualityQueueModel, QualityTwentyProducers |
| Q02 | P0 somente um stream / cancelamento | lifecycle_test, attempt_regression, stall_regression | stress race e teardown sincronizado |
| Q03 | P0 identidade Spotify | fidelity, recording_identity, release_identity, production_regressions | QualityIdentityMatrix |
| Q04 | P0 first confirmed wins | first_confirmed, first_confirmed_process | QualityFirstConfirmed |
| Q05 | P0 Force Play preserva ID | force_test em media/player/discord | manter regressões separadas, mutação isolada |
| Q06 | P0 transação e restart | sqlite_test, existing_database, controls_test | QualitySQLiteRollback, QualitySQLiteRestart |
| Q07 | P0 permissões / ack | commands_test, readiness, cr002 | QualityVoiceBoundary, QualityAckFailure |
| Q08 | P0 subprocessos / áudio | stream_test, ogg_test, pause_test | QualityOgg, integração local existente |
| Q09 | P1 cache TTL e isolamento | cache_test, cache_failure_regression | QualityCacheBoundaries |
| Q10 | P1 UI, artwork, anúncios | quality_test, queue_native, announcements, playback_integrity | contrato de registro, manter budgets existentes |
| Q11 | P0 guild isolation / concorrência | concurrent controls, SQLite isolation | 20 produtores, SQLite concorrente |
| Q12 | P0 recuperação essencial real | testes locais apenas | E2E externo pendente, bloqueio de release |

## Regressões históricas

Auditar em conjunto: cache_failure_regression (falha não cacheada), production_regressions/fidelity-production-regressions (identidade), next-track-stall-hotfix/stall_regression (avanço e stalls), attempt_regression (primeiro frame/estado), failure_notice_regression (anúncio de falha), queue-artwork-hotfix/queue_delivery (UI), first-confirmed-match (seleção), force-play (ID exato). Cada família tem testes existentes; isto não prova que todas as incidências de produção foram catalogadas. Não há tracker externo fornecido. Zero P0 histórico sem teste permanece um gate a validar com responsáveis operacionais.
