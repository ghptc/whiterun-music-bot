# QA-009 — revisão de mutação

O score QA-008 foi retirado: `--test-cpu=2` faz Gremlins 0.6.0 gerar um argumento único `-cpu 2`; go test falha sem executar a suíte e o executor conta exit=1 como KILLED. A reprodução está em evidence/gremlins-test-cpu-invalid.txt. As campanhas mutation/ e mutation-final/ ainda têm essa configuração e **não são evidência de detecção**.

Configuração corrigida: GOMAXPROCS=2, sem --test-cpu. Cada execução preserva comando, diff do mutante e saída em JSONL. Nenhuma compilação inválida, setup failure ou timeout foi contabilizado como detectado no resultado final. O runner retorna 1 quando existe LIVED, mesmo se posteriormente revisado como equivalente; essa falha operacional é preservada, não convertida em exit=0.

| Campanha final | Detectados | Sobreviventes equivalentes | Não cobertos / inválidos / timeout |
|---|---:|---:|---:|
| [player](evidence/mutation-player-final/player.txt) | 27 | 2 | 0 |
| [fidelity](evidence/mutation-fidelity-reviewed/fidelity.txt) | 106 | 1 | 0 |
| [storage](evidence/mutation-storage-final/storage.txt) | 37 | 0 | 0 |
| [permissions](evidence/mutation-permissions-reviewed/permissions.txt) | 48 | 0 | 0 |

Total: 218/221 detectados (98.64%, score bruto); três equivalentes explicitamente revisados. Score ajustado: 218/218, apenas neste escopo de arquivos e operadores. Isto não demonstra ausência de defeitos nem mede todas as invariantes da aplicação.

## Equivalentes

- `controls.go:34:7`: Move(i,i): os dois ramos copiam slices vazios e preservam a mesma entrada.
- `queue.go:67:81`: IDs dos imports são distintos: progress.ID é a chave única de p.jobs; < e <= ordenam igualmente registros distintos. Replay isolado também passou.
- `spotify_rank.go:211:97`: Com artists vazio, o bloco anterior já atribui Artist=1 à mesma identidade de canal; com artists não vazio, a conjunção permanece falsa. Alterar t.Artist != vazio não muda o resultado.

## Os 43 originalmente não cobertos

42 classificados como teste insuficiente, agora detectados; um equivalente. Nenhum inválido ou inconclusivo neste conjunto. Linhas históricas preservadas; spotify_rank.go ganhou quatro linhas antes dos pontos posteriores à linha 115. A falha do executor invalida os 178 KILLED antigos, mas não altera a identificação estática dos 43 pontos sem cobertura.

| Campanha | Local QA-008 | Local atual | Resultado | Classificação | Proteção comportamental |
|---|---|---|---|---|---|
| player | controls.go:141:24 | 141:24 | KILLED | Teste insuficiente (corrigido) | TestHardeningSeekValidationPreservesPlayback |
| player | controls.go:141:40 | 141:40 | KILLED | Teste insuficiente (corrigido) | TestHardeningSeekValidationPreservesPlayback |
| player | queue.go:67:81 | 67:81 | LIVED | Equivalente | TestHardeningConcurrentImportSnapshotOrder + comparator-independent.txt |
| fidelity | spotify_rank.go:99:28 | 99:28 | KILLED | Teste insuficiente (corrigido) | TestHardeningSpotifyIdentityEvidence |
| fidelity | spotify_rank.go:186:45 | 190:45 | KILLED | Teste insuficiente (corrigido) | TestHardeningUnrelatedChannelCannotSupplyPrimaryArtist |
| fidelity | spotify_rank.go:186:129 | 190:129 | KILLED | Teste insuficiente (corrigido) | TestHardeningSpotifyIdentityEvidence |
| fidelity | spotify_rank.go:190:98 | 194:98 | KILLED | Teste insuficiente (corrigido) | TestHardeningSpotifyIdentityEvidence |
| fidelity | spotify_rank.go:255:23 | 259:23 | KILLED | Teste insuficiente (corrigido) | TestHardeningSpotifyIdentityEvidence |
| fidelity | spotify_rank.go:275:11 | 279:11 | KILLED | Teste insuficiente (corrigido) | TestHardeningDiscoveryDeduplicationPreservesRicherIdentity |
| fidelity | spotify_rank.go:282:18 | 286:18 | KILLED | Teste insuficiente (corrigido) | TestHardeningDiscoveryDeduplicationPreservesRicherIdentity |
| fidelity | spotify_rank.go:282:38 | 286:38 | KILLED | Teste insuficiente (corrigido) | TestHardeningDiscoveryDeduplicationPreservesRicherIdentity |
| fidelity | spotify_rank.go:296:16 | 300:16 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:301:91 | 305:91 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:302:15 | 306:15 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:305:15 | 309:15 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:306:101 | 310:101 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:311:17 | 315:17 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:311:35 | 315:35 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:311:116 | 315:116 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:315:16 | 319:16 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:323:17 | 327:17 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:326:17 | 330:17 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:331:17 | 335:17 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:347:11 | 351:11 | KILLED | Teste insuficiente (corrigido) | TestHardeningDiscoveryPreservesExtractorFailure |
| fidelity | spotify_rank.go:347:29 | 351:29 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:352:9 | 356:9 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:352:30 | 356:30 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:356:14 | 360:14 | KILLED | Teste insuficiente (corrigido) | TestHardeningMappingTrustAndFailureRecovery |
| fidelity | spotify_rank.go:412:29 | 416:29 | KILLED | Teste insuficiente (corrigido) | TestHardeningVerificationInfrastructureErrorsRemainActionable |
| storage | sqlite.go:152:9 | 152:9 | KILLED | Teste insuficiente (corrigido) | TestHardeningSettingsReadDistinguishesAbsentAndUnavailable |
| storage | sqlite.go:155:9 | 155:9 | KILLED | Teste insuficiente (corrigido) | TestHardeningSettingsReadDistinguishesAbsentAndUnavailable |
| storage | sqlite.go:180:16 | 180:16 | KILLED | Teste insuficiente (corrigido) | TestHardeningLegacyMappingShapeIsNotCacheHit |
| permissions | commands.go:123:31 | 123:31 | KILLED | Teste insuficiente (corrigido) | TestHardeningAuthorizedCommandOutcomes |
| permissions | commands.go:167:11 | 167:11 | KILLED | Teste insuficiente (corrigido) | TestHardeningVoiceJoinAndAttachFailures |
| permissions | commands.go:176:58 | 176:58 | KILLED | Teste insuficiente (corrigido) | TestHardeningVoiceJoinAndAttachFailures |
| permissions | commands.go:220:10 | 220:10 | KILLED | Teste insuficiente (corrigido) | TestHardeningAuthorizedCommandOutcomes |
| permissions | commands.go:244:23 | 244:23 | KILLED | Teste insuficiente (corrigido) | TestHardeningPlayFailureResponseAndCancellation |
| permissions | commands.go:246:56 | 246:56 | KILLED | Teste insuficiente (corrigido) | TestHardeningPlayFailureResponseAndCancellation |
| permissions | commands.go:301:60 | 301:60 | KILLED | Teste insuficiente (corrigido) | TestHardeningProgressiveFirstPositionNotice |
| permissions | commands.go:304:21 | 304:21 | KILLED | Teste insuficiente (corrigido) | TestHardeningProgressiveFirstPositionNotice |
| permissions | commands.go:310:22 | 310:22 | KILLED | Teste insuficiente (corrigido) | TestHardeningYouTubeCollectionSkippedNotice |
| permissions | commands.go:340:10 | 340:10 | KILLED | Teste insuficiente (corrigido) | TestHardeningAuthorizedCommandOutcomes |
| permissions | commands.go:361:12 | 361:12 | KILLED | Teste insuficiente (corrigido) | TestHardeningAuthorizedCommandOutcomes |

## Falhas intermediárias preservadas

mutation-verified usa o executor corrigido com seleção reduzida de testes: revelou sobreviventes reais. mutation-complete usa todos os Test*, revelando 14 sobreviventes e um timeout. O timeout sqlite.go:92 ocorreu porque o mutante abandona uma transação sem rollback e o preparo da fixture aguardava outra conexão sem contexto. O preparo agora usa contexto e ExecContext: mutation-storage-final detecta a alteração com falha explícita, sem timeout.

mutation-permissions-final falhou já no baseline: uma nova fixture tentava usar o hook Spotify Search numa resolução YouTube; substituída por executável yt-dlp controlado, sem alterar produção. mutation-permissions-reviewed passou no baseline e detectou todos os mutantes. Os relatórios intermediários e suas saídas individuais continuam preservados.

Reprodução: `python3 -B scripts/qa008_mutation.py --campaign <player|fidelity|storage|permissions> --test-pattern '^Test' --evidence-dir /tmp/qa009-mutation-review`. Os quatro relatórios finais podem usar seleções diferentes: player-final usa a seleção rápida padrão, depois de uma campanha completa, enquanto os demais usam todos os Test*.
