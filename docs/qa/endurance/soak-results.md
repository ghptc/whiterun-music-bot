# Sessões realmente executadas

Todas abaixo são locais, com FFmpeg/libopus real, producer controlado, SQLite real e race detector. Datas UTC estão nos segmentos; não há relógio virtual. Arquivos de configurações, eventos, recursos, resumos e análise estão em evidence/<campanha>/<segmento>/.

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

## Sessões não executadas

| Meta | Resultado |
|---|---|
| 1 hora contínua | PENDENTE |
| 6 horas contínuas | PENDENTE |
| 12 horas contínuas | PENDENTE |
| Sessão audível Discord/DAVE real | PENDENTE |
| Playlist 500 integralmente drenada por guild | PENDENTE |
| Perdas externas prolongadas durante horas | PENDENTE |

## Experimentos rejeitados e limitações

O piloto inicial de 8,017s terminava PASS sem selecionar nenhuma faixa: cada subprocesso instrumentado com race aguardava o atraso padrão de saída e o tempo acabou durante metadados. Esse piloto **não é aprovação**. Seu JSON original foi preservado em evidence/pilot-invalid, e qa010_report.py o rejeita com `vacuous playback evidence` (pilot-rejected.txt). A fixture agora usa GORACE=atexit_sleep_ms=0 apenas no producer; o harness exige seleção, áudio e accounting não vazio. TestSoakEvidenceRejectsVacuousAndLostTracks protege a regra.

O vet inicial falhou porque a tabela de testes copiava guildRun com mutex. A fixture passou a usar ponteiros; vet e Docker foram repetidos. Saídas e validation anteriores preservados como before-fixture-fix-*. Nenhuma produção foi modificada para tornar os testes verdes.

O binário usado nas campanhas 123/500 precede a extração do validador e/ou o acréscimo de métricas de entrega. Hashes distintos estão em evidence/soak-binary*.sha256. A instrumentação final foi exercitada na campanha delivery. Nenhum defeito novo de produção foi confirmado; as duas correções acima são do harness/fixture.

Não houve reprodução de falha flaky nas campanhas repetidas. Isso não demonstra ausência global de flakiness. Nenhum teste vermelho foi omitido dos relatórios.
