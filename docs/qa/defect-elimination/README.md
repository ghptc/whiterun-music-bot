# QA-009 — Defect Elimination & Mutation Hardening

Estado: regressões locais e investigação dos três defeitos confirmadas; validação final aprovada. A release permanece bloqueada pelos gates externos e pelas metas ainda não demonstradas. Nenhuma homologação Discord/Voice real foi realizada. UI congelada, dependências e arquitetura preservadas.

## Causas confirmadas e correções

| Defeito | Causa e impacto demonstrado | Correção e proteção permanente |
|---|---|---|
| D01 — duração inválida | Conversão direta de float64 para time.Duration aceitava negativos e fazia overflow. NaN em Candidate construído também podia resultar em MATCH_CONFIRMED. Metadados JSON não permitem NaN, portanto esse caso é de objeto construído/cache, não de JSON válido do provedor. | Validação na extração, playlist e ranking: finita, não negativa e representável. Zero continua significando duração desconhecida/live. Frações válidas preservadas. Entradas ruins de playlist são ignoradas com contabilização. TestDefectNegativeExtractorDuration, TestDefectExtractorDurationBoundaries, TestDefectPlaylistDoesNotPublishMalformedDuration, TestDefectFidelityRejectsInvalidDuration e FuzzAdvancedDurationValidity. |
| D02 — Opus vazio | Lacing Ogg zero gerava pacote vazio e o callback tratava-o como áudio iniciado. Streamer enviava pacote vazio mais cinco frames de silêncio, retornando sucesso; após áudio parcial enviava sete frames no fixture. | Rejeição antes do callback. TestDefectEmptyOpusPacket e TestDefectEmptyOpusNeverStartsStream validam primeiro frame e erro após frame parcial, contagem de frames e encerramento de Speaking. Não se afirma validação completa do codec Opus nem que FFmpeg íntegro produza esse fluxo. |
| D03 — worker DisGo após Close | DisGo v0.19.6 inicia ticker sem cancelamento/Stop/join; Close mantém bucketsMu adquirido, e o worker fica bloqueado em doCleanup. Confirmado após término do prazo de Close e três períodos completos de manutenção, depois de observar uma limpeza anterior concluída. | RateLimiter local injetado pela extensão pública do SDK, mantendo política de buckets/headers/retries da versão fixada. Cancelamento e join do worker, drenagem limitada pelo contexto sem segurar o lock do mapa, Close idempotente e rejeição de novas requisições após fechamento. Não houve upgrade nem edição do módulo em cache. |

A mitigação D03 exige um adaptador de aproximadamente 300 linhas porque o worker da dependência não é exportado e não pode ser encerrado por um wrapper. Licença Apache-2.0 preservada em licenses/disgo-Apache-2.0.txt. O adaptador deve ser removido quando uma implementação upstream com essas garantias for adotada e validada. O SDK sem injeção continua reproduzindo o defeito.

Na investigação, duas races também foram reproduzidas: atualizações globais de cooldown em buckets distintos e leitura dos campos de bucket no log antes de adquirir o lock. O adaptador sincroniza o global e lê os campos após adquirir o lock. O primeiro caso foi reproduzido diretamente no SDK; o segundo foi detectado no código copiado para o adaptador e corrigido antes do commit. Evidências: upstream-global-race.txt e limiter-shared-bucket-before.txt.

Não foi demonstrado crescimento contínuo de goroutines numa sessão com um único cliente vivo. O vazamento confirmado é de ciclo de vida: criar/fechar clientes deixa seus workers ativos. Os testes de correção cobrem cliente REST real com limiter injetado, três períodos virtuais após Close, pedidos em andamento, cancelamento de waiters, prazo de Close, 20 workers no mesmo bucket, cooldown global e por rota, parsing de headers malformados e falha na construção do bot. Os testes de ACK virtual passaram a usar o adaptador de produção, substituindo o limiter falso.

## Evidências antes/depois

- [strong-regressions-before.txt](evidence/strong-regressions-before.txt): D01 e D02 falham antes das correções, incluindo overflow, NaN, playlist e transporte local.
- [duration-opus-after.txt](evidence/duration-opus-after.txt): regressões D01/D02 aprovadas sob race, 20 repetições.
- [upstream-persistence.txt](evidence/upstream-persistence.txt): falha explícita em tempo real após Close, seu deadline e três períodos; stack do próprio worker. A inspeção do código confirma ausência de qualquer condição de término e lock retido.
- [upstream-persistence-virtual-timeout.txt](evidence/upstream-persistence-virtual-timeout.txt): experimento inicial inválido para essa prova; sync.Mutex não produz bloqueio durável reconhecido por synctest, e o harness esgotou seu timeout. Não conta como prova do vazamento; substituído pela observação real acima.
- [upstream-global-race.txt](evidence/upstream-global-race.txt): relatório de race diretamente no SDK.
- [limiter-shared-bucket-before.txt](evidence/limiter-shared-bucket-before.txt): race nos logs do adaptador antes de corrigir o acesso.
- [limiter-final.txt](evidence/limiter-final.txt): regressões do adaptador sob race, dez repetições; teste adicional de falha de construção validado dez vezes e integrado à suíte completa.

O baseline de produção anterior às correções é 43addc9. Para repetir D01/D02 antes da correção, use um checkout descartável desse commit e copie somente os arquivos atuais defect_regression_test.go (media/player) e duration_regression_test.go, executando o filtro TestDefect. Não copie as alterações de produção. As falhas esperadas estão registradas acima.

As reproduções upstream são diagnósticos **intencionalmente vermelhos**, isolados pela tag qa009_upstream_probe e executados em processos separados. Não são testes críticos flaky omitidos: a dependência permanece defeituosa, e a aplicação usa outro limiter. D01/D02 e as regressões do adaptador estão na suíte padrão.

```sh
go test -race -tags qa009_upstream_probe ./internal/discord -run '^TestUpstreamLimiterPersistsAcrossCleanupPeriods$' -count=1 -timeout=10s
go test -race -tags qa009_upstream_probe ./internal/discord -run '^TestUpstreamGlobalCooldownRace$' -count=1 -timeout=10s
go test -race ./internal/media ./internal/player -run '^TestDefect' -count=20
go test -race ./internal/discord -run '^TestDefect' -count=10
```

## SQLite: integridade e resiliência separadas

Nenhuma alteração em storage/sqlite.go, migrations ou constraint json_valid(candidate).

- TestHardeningMappingConstraintPreservesPreviousRecord: UPDATE com JSON inválido deve falhar; leitura seguinte retorna exatamente o candidato anterior, incluindo metadados.
- TestHardeningLegacyMappingShapeIsNotCacheHit: UPDATE para `[]`, JSON sintaticamente válido mas incompatível com Candidate. Somente o SQLite temporário do teste recebe essa corrupção simulada. LoadMapping retorna erro e hit=false.
- TestHardeningSettingsReadDistinguishesAbsentAndUnavailable: distingue ausência, valor gravado e banco fechado. Os testes do resolver também simulam erro de leitura, revalidação, falha de escrita/invalidação, cancelamento e recuperação sem confiar no cache inválido.

A falha original de TestHardeningCorruptMappingIsNotCacheHit foi um erro da fixture: a constraint impediu corretamente a gravação. Não era defeito de produção.

O teste avançado de interrupção de transação teve apenas o preparo limitado por contexto e ExecContext. Um mutante que abandona uma transação deixava o preparo aguardando outra conexão indefinidamente. O timeout da campanha foi preservado, investigado e substituído por falha comportamental explícita na campanha final.

## Mutação e qualidade da evidência

O score sistemático QA-008 de 178 detectados/zero sobreviventes foi **invalidado**. Gremlins 0.6.0 recebia --test-cpu=2 e gerava um único argumento `-cpu 2`; o comando falhava antes dos testes e exit=1 era classificado como KILLED. A reprodução independente do comparador revelou a inconsistência. O runner agora usa somente GOMAXPROCS, preserva comando/diff/saída de cada execução e rejeita falhas de infraestrutura/timeout em sua validação.

Resultado verificado no escopo original: **218 detectados, três equivalentes, zero não cobertos, inválidos ou inconclusivos**. Score bruto 98,64%; ajustado 218/218 após revisão explícita dos equivalentes. Os 43 antigos NOT COVERED estão individualmente classificados: 42 lacunas de teste corrigidas e um equivalente. Não se atribui esse score ao repositório inteiro. [Relatório e tabela dos 43](mutation.md), [dados de todos os 221](evidence/mutation-review.json).

As execuções com LIVED terminam com status 1 no runner; isso foi mantido mesmo para equivalentes revisados. Relatórios intermediários não foram apagados nem reinterpretados como sucesso. Nove mutações dirigidas históricas são uma evidência distinta; o score sistemático inválido não as substitui.

## Validação final

Comandos e códigos reais em [validation.json](evidence/validation.json); stdout e stderr preservados separadamente. Três logs com whitespace incompatível com git diff --check têm apresentação normalizada e cópia exata em arquivos .raw.json adjacentes; nenhum conteúdo de falha foi descartado.

| Verificação | Resultado | Tempo de parede nesta execução |
|---|---|---:|
| go test ./... -count=1 | PASS | 28,5 s |
| go test -race ./... -count=1 | PASS | 39,1 s |
| go vet ./... | PASS | 0,6 s |
| cobertura atomic com -coverpkg=./... | PASS | 28,6 s |
| modelos/linearização/imports com race, count=20 | PASS | saída em model-stress.txt |
| oito campanhas nativas de fuzz, 15 s/target, dois workers | PASS | 2.549.578 execuções |

Campanhas de fuzz/mutação e stress são opt-in e não executam como parte de go test diário. As regressões determinísticas ficam na suíte padrão; -short continua omitindo as 2.048 sequências longas existentes. Não há baseline controlado de CI suficiente para atribuir um delta de latência a estas mudanças ou definir thresholds.

Tempos medidos com execuções concorrentes no mesmo ambiente, não são thresholds de CI. Fuzz usa GOMEMLIMIT=256MiB por processo como limite **soft**, não limite total rígido. Nenhum crash novo encontrado; seis seeds do novo target de duração e corpus histórico permanecem na suíte. Inputs interessantes adicionais gerados pelo fuzz ficam no cache nativo; não se afirma que todos foram promovidos a corpus do repositório.

A execução de cobertura aprovou **3.267 folhas**, contra 3.165 da QA-008: acréscimo líquido de 102, incluindo a promoção das reproduções à suíte padrão. Repetições, milhões de execuções de fuzz e passos internos dos modelos não são somados como cenários independentes. O modelo completo continua com 2.048 sequências/266.240 operações e 128 seeds de linearização; foi reexecutado integralmente em full/race/cobertura. O stress adicional fixa BARD_QA_MODEL_SEED=20261019, mais linearização/imports, por 20 repetições sob race.

Quatro skips explícitos: TestExistingDatabaseUpgrade (fixture de banco opcional), TestLiveResolverTiming, TestSpotifyPublicLargeLiveRegression e TestSpotifyEmbedLiveRegression. Nenhum foi contado como PASS. Não se observou flakiness nos testes locais repetidos; isso não prova ausência global de flakiness.

## Cobertura e pendências de release

Go cover statements global: **86,2% → 87,5%** (4.067/4.647 statements). Denominador cresceu com o adaptador; não houve exclusão de produção/cmd. Perfil em evidence/coverage.out, funções em evidence/coverage-functions.txt. Não é cobertura exclusiva unitária: inclui as integrações locais da suíte existente. Não foi repetida a cobertura separada do binário nesta etapa.

| Pacote | QA-008 | QA-009 |
|---|---:|---:|
| cmd/bot | 1,2% | 1,2% |
| cmd/profile | 0,0% | 0,0% |
| internal/discord | 81,1% | 86,6% |
| internal/media | 92,7% | 92,9% |
| internal/player | 89,0% | 89,4% |
| internal/storage | 85,4% | 86,1% |

Ainda pendentes: 90% global; 95% nos caminhos críticos com análise de branches/condições; homologação E2E Discord/DAVE/Voice com áudio recebido e reconexão reais; crash físico de processo durante commit/WAL; security gate anterior de stdlib com toolchain oficial. Não foram realizados testes destrutivos em produção. O adaptador requer manutenção enquanto a dependência original permanecer sem encerramento apropriado. Nada nesta etapa declara a v1.0 pronta.

## Commits e reprodução dos relatórios

- 368338a — validação de duração e regressões.
- 2ae4ee0 — rejeição de Opus vazio e regressões de streaming.
- 404aa27 — lifecycle REST, sincronização e diagnóstico isolado upstream.
- 10647a9 — integridade/resiliência SQLite separadas.
- 81ebe25 — regressões de fila, identidade, recuperação e permissões.
- 0303268 — lacunas de mutação, limites de capacidade e preparo SQLite limitado.
- 3bd77fd — correção do executor de mutação e evidência individual.
- 151c603 — arquivo das execuções e reconciliação automática dos resultados.

`python3 -B scripts/qa009_report.py` reconcilia os relatórios finais, verifica que cada sobrevivente está explicitamente explicado e gera a revisão dos 221 mutantes e a matriz dos 43. Os comandos de cada campanha estão nos respectivos *-run.json. Consultar mutation.md antes de interpretar exit=1 dos experimentos.
