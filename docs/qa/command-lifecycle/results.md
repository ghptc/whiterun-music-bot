# QA-013 — resultados da campanha de lifecycle

Campanha local em **2026-10-20**, código de testes `0c77c4d` (mais documentação e
relatórios posteriores). **Gates automatizados aprovados; release v1.0.0 ainda
não recebe aprovação de E2E Discord real.** Nenhuma alteração em código de
produção, arquitetura ou Spotify Fidelity nesta campanha. Nenhum bot ou volume
de produção foi iniciado, reiniciado ou usado em fault injection.

## Escopo e contagens

- Registro real: **22 comandos**, inventariados com todas as opções em
  [inventory.md](inventory.md) e snapshot JSON serializado do registro.
- **1.452 sequências novas** de pares ordenados: 22×22×3 estados iniciais.
  Cada uma segue até EOF, nova requisição, próximo EOF e stop, comparando o
  oráculo após operações; não apenas a resposta Discord.
- **256 novas sequências de modelo durável × 258 operações = 66.048 operações**.
  O modelo existente agora também verifica geração: 2.048×130 = 266.240 operações.
  Juntos: **2.304 sequências e 332.288 operações de modelo**.
- **22 ciclos SQLite reais**, um por comando: comando→EOF→history→close/reopen→
  summon→history→EOF→stop; artwork, navegação, ledger e guild 2 preservados.
- 44 cenários ACK/reply, 17 de permissão, oito de argumentos inválidos,
  12 sequências críticas longas, seis sequências progressivas e quatro de STARTING.
- Concorrência: dois plays/solicitantes, duas guilds, 48 handlers de queue/mutação,
  64 repetições do contrato EOF/skip. Repetições não são 64 novos contratos.
- Testes novos de rollback após ACK, recuperação após remover fault, falha de
  anúncio e correlação persistida comando→tentativa→transição.
- Na execução completa: **5.228 folhas de teste passaram, cinco opt-ins foram
  pulados**; dessas folhas, **1.894 são QA-013**. Pais de subtestes não são
  somados. Race é nova execução das mesmas folhas, não novos cenários.

As matrizes [state-matrix.md](state-matrix.md) e
[interaction-matrix.md](interaction-matrix.md) separam cobertura nova, existente,
cruzamentos impossíveis, rejeições intencionais e lacunas. Não se declara que
22 comandos × 11 estados × todas opções/interleavings foram esgotados.

## Gates executados

| Gate | Resultado |
|---|---|
| `go test ./... -count=1` (capturado com `-json` e `-coverprofile`) | PASS |
| `go test -race ./... -count=1` (com `-json`) | PASS, zero races reportadas |
| `go vet ./...` | PASS, sem diagnósticos |
| `docker compose build` | PASS; inclui vet e testes no estágio build |
| Modelo independente | 2.048 seeds originais + 256 com estado durável e geração |
| `go test -race -tags=stress ./internal/player -run '^TestEnduranceTwentyUsers$' -count=1 -timeout=120s` | PASS; seeds 41/20261019, 20 usuários, duas guilds, 4.000 operações |
| `go test -race -tags=stress ./internal/storage -run '^TestEnduranceKillDuringSQLiteWriteRecoversCommittedState$' -count=1 -timeout=120s` | PASS; kill em quatro fronteiras SQL, somente DB temporário marcado pelo teste |
| `python3 scripts/qa013_mutations.py` | Baseline PASS; cinco mutantes mortos por assertions, zero builds inválidos/sobreviventes |

Normal e race foram executados sem `-short`. Não há sleeps nos novos cenários
críticos: canais/barreiras e testing/synctest controlam EOF, primeiro frame,
resolução e quiescência. Suítes de processo existentes usam FFmpeg real sobre
fontes simuladas. Os testes de propriedade e fuzzing compartilham o oráculo
independente existente; o modelo não chama helpers de transformação de produção.

## Regressões e defeitos

O P0 conhecido de OBS-001 foi reproduzido com **302 músicas, seis prontas,
295 pendentes e next_pending=true após EOF natural**. A remoção do wakeup em
cópia descartável do código faz TestQA013Production302ShuffleEOF falhar. Outra
fixture injeta a notificação perdida e demonstra o desbloqueio por skip manual.
Ver [production-stall-regression.md](production-stall-regression.md).

A correção funcional já existia em `941a876`; QA-013 não a substitui. Não foi
confirmado outro defeito de produção nesta campanha. Durante construção do
oráculo foram classificados corretamente dois contratos existentes: queue faz
um segundo PATCH limitado para aviso quando a entrega falha; EOF/skip pode
pular uma faixa antes de seu primeiro frame, deixando histórico [a,c]. Essas
correções foram feitas no teste, sem mudar comportamento do player.

Mutantes dirigidos: perda do wakeup, execução após ACK falho, skip default=2,
move que duplica slot e stop que retém fila. **5/5 não representa mutation score
do repositório inteiro**. Resultados em [mutations.json](evidence/mutations.json).
Seeds e bytes de sequências que falharem no novo modelo são persistidos em
BARD_QA_FAILURE_DIR ou /tmp/bard-qa013-failures; nenhum seed novo falhou.

## Fuzzing

Cada alvo rodou com `GOMAXPROCS=2`, `-parallel=2`, `-fuzztime=15s`,
`-timeout=90s` e `-run '^$'` (sem repetir toda a suíte por alvo).

| Alvo | Execuções do engine | Resultado |
|---|---:|---|
| player/FuzzAdvancedQueue | 1.952 | PASS |
| discord/FuzzAdvancedQueueArguments | 20.260 | PASS |
| media/FuzzAdvancedMediaURLs | 263.635 | PASS |
| player/FuzzAdvancedTimestamp | 651.974 | PASS |

Total **937.821** chamadas do engine, incluindo entradas que podem ser rejeitadas
por bounds do harness. Não são somadas a cenários de lifecycle nem contadas como
sequências válidas de modelo. Novos casos interessantes ficam no cache de fuzz
do Go; falhas seriam gravadas no corpus reproduzível do alvo.

## Baseline de performance

Linux amd64, AMD Ryzen 7 9800X3D, Go host 1.27.1-X:nodwarf5, oito CPUs visíveis
aos benchmarks. Três amostras de 200 ms, pacotes executados em sequência após
terminarem as campanhas principais. Medianas; sem gate de latência arbitrário.
Amostras e alocações estão nos dois arquivos de benchmarks em evidence/.

| Medição | Mediana |
|---|---:|
| ACK pelo SDK em TCP localhost | 17,7 µs/op |
| ACK local no handler (help/queue/pause) | 1,89–2,16 µs/op |
| Comando completo, HTTP simulado: help / queue / pause rejeitado sem voz | 4,03 / 8,50 / 4,02 µs/op |
| Enqueue até primeiro frame, Voice em memória | 14,0 µs/op |
| EOF até primeiro frame sucessor pronto | 12,9 µs/op |
| SaveGuild SQLite: vazio / 302 / 1.000 posições | 12,7 µs / 2,81 ms / 9,23 ms |
| Match local sobre fixture / identidade Spotify em cache | 120,4 / 66,4 µs/op |

Esses valores não incluem rede Discord/Spotify/YouTube, DAVE real ou início de
FFmpeg no benchmark. Não representam tempo de resolução externa, audibilidade,
p99 nem limite de produção. Pipeline/cleanup são verificados por invariantes de
serialização, active=0 após stop e testes existentes de subprocessos/descritores;
não se estima custo de longa duração a partir desses microbenchmarks.

## Cobertura e limites

Cobertura de statements da execução instrumentada: **78,6% geral**, Discord
**87,6%**, player **89,4%**, storage **86,1%**, media **91,4%**, logstore **83,3%**.
CLIs auxiliares/soak têm baixa cobertura e reduzem o total; não confundir
statement coverage com cobertura completa de estados ou de causalidade.

Cinco opt-ins não executados: TestExistingDatabaseUpgrade (precisa backup
consistente de teste), TestLiveResolverTiming, TestSpotifyPublicLargeLiveRegression,
TestSpotifyEmbedLiveRegression e TestDefault500MiBSaturation. Caminhos offline,
rollback, cache, retenção representativa e indisponibilidade do log DB passaram.
A campanha não acessou um DB operacional/backup real para suprir esses opt-ins.

**Gates ainda pendentes para aprovação integral da release:** execução real em
guild Discord controlada (ACK/Voice/DAVE/reconexão/audibilidade e anúncios),
aceitação live dos provedores e soak representativo de longa duração. Não houve
E2E Discord real nesta execução. Não há covering array de todos os valores de
opções nem exploração exaustiva dos interleavings. Logging ainda não oferece
before/after completo por comando ou interaction_id em toda mutação; a correlação
validada é a disponível no código, sem inventar eventos ausentes.

## Evidência e reprodução

[summary.json](evidence/summary.json) contém contagens por contrato, resultados por
pacote, skips, amostras e hashes SHA-256 dos logs completos. JSONL normal/race,
build e cobertura detalhada estão no workspace ignorado `.qa-runs/qa013/`;
evidência compacta e reproduzível está versionada em `evidence/`.

```sh
go test ./... -count=1 -json -coverprofile=/tmp/qa013.cover
go test -race ./... -count=1 -json
go vet ./...
docker compose build
python3 scripts/qa013_mutations.py
go test ./internal/player -run '^TestQA013DurableModelSequences$/seed=17$' -count=1
env GOMAXPROCS=2 go test ./internal/player -run '^$' \
  -fuzz '^FuzzAdvancedQueue$' -fuzztime=15s -parallel=2 -timeout=90s
go test ./internal/discord ./internal/player ./internal/storage ./internal/media \
  -run '^$' -bench 'Benchmark(QA013|EnduranceLocalACK|FastMatchRealFixture|CachedSpotifyIdentity)' \
  -benchtime=200ms -count=3
python3 scripts/qa013_report.py --raw-dir .qa-runs/qa013
```

O relatório só resume evidência já salva; não executa comandos nem acessa serviços.
Os benchmarks devem ser repetidos por pacote se for necessário evitar competição
entre pacotes, como feito na coleta desta campanha.
