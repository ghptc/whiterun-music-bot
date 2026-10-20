# Sessões realmente executadas

As sessões QA-010 da tabela inicial são locais, com FFmpeg/libopus real, producer controlado, SQLite real e race detector. A seção QA-011 distingue a hora com race do controle sem race. Datas UTC estão nos segmentos; não há relógio virtual. Arquivos de configurações, eventos, recursos, resumos e análise estão em evidence/<campanha>/<segmento>/.

| Campanha | Tempo real | Guilds | Playlist por batch | Selecionadas | Anúncios locais | Concluídas | Pendentes no checkpoint | Transições entre tentativas |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| smoke quente + falhas | 12,170 s | 2 | 123 | 60 | 57 | 52 | 186 | 58 |
| 123 quente + falhas | 60,174 s | 2 | 123 | 284 | 272 | 249 | 208 | 282 |
| retomada da campanha 123 | 20,252 s | 2 | checkpoint | 71 | 68 | 64 | 139 | 69 |
| 500 frio + falhas | 120,634 s | 2 | 500 | 194 | 186 | 169 | 806 | 192 |
| entrega do primeiro frame, frio | 20,059 s | 1 | 123 | 68 | 65 | 58 | 55 | 67 |

Cada contagem de seleção é uma tentativa identificada, não uma música única entre reinícios. Não somar pendentes ou accepted entre a sessão 123 e sua retomada: são checkpoints da mesma fila. A campanha 500 aceitou 1.000 posições, mas **não reproduziu as 500 de cada guild até o fim**; 806 posições permaneceram preservadas. A campanha 123 completou o primeiro batch de cada guild e aceitou um segundo.

## Resultados terminais (sem confundir com skip de usuário)

| Segmento | completed | unavailable | error_interrupted | shutdown_interrupted |
|---|---:|---:|---:|---:|
| 123 / 60s | 249 | 12 | 22 | 1 |
| retomada / 20s | 64 | 2 | 4 | 1 |
| 500 / 120s | 169 | 8 | 16 | 1 |
| delivery / 20s | 58 | 3 | 6 | 1 |

Cancelamentos internos injetados contam como erro de streaming; encerramento do segmento conta como interrupção de shutdown. Esses segmentos não executam skip de usuário. Skip, previous, loop, replay, pause/resume, remove/move, shuffle, jump, seek, clear e stop foram exercitados no stress/API e no modelo existente, com finalidades distintas.

O exportador reconciliou seleção, anúncio, terminal e os contadores finais; o oráculo online verificou ordem/identidade e a fila persistida. Nenhuma perda/duplicação inesperada, paralisação por falha individual, processo filho restante ou corrupção de fila foi detectada nessas sessões. Isso não aprova estabilidade de horas.

## Metas prolongadas e status atual

| Meta | Resultado |
|---|---|
| 1 hora contínua | QA-011 executou 3.600,141s; integridade PASS, gate de memória bloqueado |
| 6 horas contínuas | PENDENTE |
| 12 horas contínuas | PENDENTE |
| Sessão audível Discord/DAVE real | PENDENTE |
| Playlist 500 integralmente drenada por guild | QA-011 drenou três lotes de 500 posições por guild, com resultados classificados |
| Perdas externas prolongadas durante horas | PENDENTE |

## Experimentos rejeitados e limitações

O piloto inicial de 8,017s terminava PASS sem selecionar nenhuma faixa: cada subprocesso instrumentado com race aguardava o atraso padrão de saída e o tempo acabou durante metadados. Esse piloto **não é aprovação**. Seu JSON original foi preservado em evidence/pilot-invalid, e qa010_report.py o rejeita com `vacuous playback evidence` (pilot-rejected.txt). A fixture agora usa GORACE=atexit_sleep_ms=0 apenas no producer; o harness exige seleção, áudio e accounting não vazio. TestSoakEvidenceRejectsVacuousAndLostTracks protege a regra.

O vet inicial falhou porque a tabela de testes copiava guildRun com mutex. A fixture passou a usar ponteiros; vet e Docker foram repetidos. Saídas e validation anteriores preservados como before-fixture-fix-*. Nenhuma produção foi modificada para tornar os testes verdes.

O binário usado nas campanhas 123/500 precede a extração do validador e/ou o acréscimo de métricas de entrega. Hashes distintos estão em evidence/soak-binary*.sha256. A instrumentação final foi exercitada na campanha delivery. Nenhum defeito novo de produção foi confirmado; as duas correções acima são do harness/fixture.

Não houve reprodução de falha flaky nas campanhas repetidas. Isso não demonstra ausência global de flakiness. Nenhum teste vermelho foi omitido dos relatórios.

## QA-011 — campanha real de uma hora

**Concluída em tempo real; integridade aprovada; aprovação operacional bloqueada por crescimento sustentado de RSS ainda em triagem.** Não tratar o campo status=pass do harness como aprovação de todos os critérios do programa.

- Início: 2026-10-20 02:27:20,495 UTC; término systemd: 03:27:20 UTC; duração medida pelo harness **3.600,141040267s**, cleanup 122,10ms, exit=0.
- Serviço: qa011-1h-20261020t022653z.service. Binário com race, áudio local de 1s, duas guilds, lote 500, cache com priming/hits, faults, seed 20261019.
- 4.000 posições aceitas; 3.900 selecionadas e encerradas; 3.484 completed, 140 unavailable, 274 error_interrupted e duas shutdown_interrupted. 100 posições pendentes preservadas (50/guild).
- 3.760 callbacks de início após áudio, 195.963 frames entregues, 3.898 transições entre tentativas.
- 414 seleções de recuperação após falha; 140 cadeias de falhas voltaram a completed. Nenhuma cadeia de falhas ficou sem recuperação ao final.
- Três lotes completos de 500 posições drenados em cada guild; o quarto lote ficou parcialmente pendente. Não converter unavailable/error em música concluída com sucesso.
- Nenhuma seleção duplicada, anúncio órfão/duplicado, terminal ausente, corrupção de ordem, perda silenciosa ou deadlock observados. Fila pendente confrontada com SQLite pelo harness e eventos reconciliados pelo exportador existente.

Evidências: [manifesto](evidence/qa011/execution.json), [estado final systemd](evidence/qa011/1h-systemd-final.txt), [reconciliação](evidence/qa011/1h/segments.json) e [revisão de recursos](evidence/qa011/1h-resource-review.json). Raw resources/events/summary permanecem no subdiretório do segmento em evidence/qa011/1h/.

### Classificação das campanhas QA-011

| Campanha | Estado | Resultado |
|---|---|---|
| 1h com race | CONCLUÍDA | duração e integridade aprovadas; gate operacional de memória bloqueado |
| Controle 10m sem race | CONCLUÍDA | 600,029s; integridade PASS; 1.148 selecionadas, 1.022 completed, 852 pendentes; diagnóstico, não substitui 1h |
| 6h | PENDENTE / NÃO INICIADA | condição de aprovação operacional da hora ainda não satisfeita |
| 12h | PENDENTE / NÃO INICIADA | mesma condição; nenhuma aprovação antecipada |
| Interrompidas nesta QA-011 | NENHUMA | os dois segmentos terminaram no tempo integral, exit=0; ver manifesto e estados systemd |

A sessão 1h não foi truncada nem retomada para compor sua duração. Checkpoint conservado em .qa-runs/qa011-20261020T022653Z/1h; retomar cria novo segmento. Não houve Discord real ou escrita em banco operacional.

Controle sem race: iniciado em 2026-10-20 08:18:17,492 UTC, encerrado às 08:28:17 UTC, 600,029097983s reais. Mesma seed, lote, áudio, guilds, cache/faults e limites; executado posteriormente, não simultaneamente nem sob carga de host garantidamente idêntica. Houve 42 unavailable, 82 error_interrupted e duas shutdown_interrupted, além das 1.022 conclusões. Recursos voltaram a três goroutines/dez FD/zero filhos; RSS de cleanup 18.591.744 bytes. O controle é diagnóstico curto, não uma hora sem instrumentação. Nenhuma sessão foi interrompida nesta QA-011; 6h e 12h permanecem não iniciadas.
