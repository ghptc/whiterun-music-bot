# QA-010 — Endurance e confiabilidade operacional

**QA-011: uma hora real executada e integridade aprovada; gate operacional de memória ainda bloqueado.** Maior segmento contínuo: 3.600,141 segundos. Há crescimento sustentado de RSS no binário com race, ainda em triagem; não é diagnóstico confirmado de vazamento de produção. Campanhas 6h/12h não iniciadas nem aprovadas. Ver [resultados QA-011](soak-results.md#qa-011--campanha-real-de-uma-hora). Produção, Spotify Fidelity, Force Play, UI e o harness não foram alterados na QA-011.

## Inventário reutilizado

| Área auditada | Base existente | Incremento QA-010 |
|---|---|---|
| docs/qa, advanced, defect-elimination | inventário, riscos, critérios, cobertura 87,5% histórica, 221 mutantes revisados, D01/D02/D03 | evidência operacional separada; não repetir a auditoria nem reapresentar score antigo inválido |
| Player/fila | modelo independente de 2.048 seeds, 128 linearizações, limites, histórico, loop, cancelamentos | 20 usuários reais em goroutines, duas guilds, 15 operações e snapshots verificados após cada operação |
| Streaming | FFmpeg real, Opus/Ogg, EOF, stall, teardown, PID/helper, fonte encerrada e decoder morto | WAV local → stand-in de yt-dlp → FFmpeg/libopus real → transporte local controlado, por segmento de tempo real |
| Persistência/cache | SQLite/WAL real, migrations, rollback, restart, singleflight/TTL, mappings | ledger contínuo, checkpoints reais, retomada por novo segmento, SIGKILL do writer em quatro estágios |
| Discord/DAVE | REST limiter gerenciado, budgets ACK, permissões, readiness, anúncios, Force Play, Queue V2 | benchmark ACK por TCP local, roteiro de homologação autorizado; nenhum Discord real acessado |
| CI | Quality, advanced-reliability e external-metadata | separar PR curto, integração/cobertura, race; workflow endurance manual; 6h/12h via CLI dedicada |

O transporte real de Discord aguarda readiness DAVE antes de enviar UDP e impõe deadline de escrita. Isso foi inspecionado, mas o transporte local do harness **não exercita negociação DAVE, criptografia, Gateway ou áudio recebido no cliente Discord**.

## Execução opt-in

Linux, Go do go.mod e FFmpeg com libopus. O executável não pede token, não chama serviços externos e não lê .env. O comando `source` implementa somente a fixture local de yt-dlp. O yt-dlp de produção não foi usado nessas sessões.

```sh
go build -race -o /tmp/qa-soak ./cmd/qa-soak
/tmp/qa-soak -out /tmp/qa010-new -duration 1h -playlist 123 -guilds 2 -warm -faults
# Novos diretórios para sessões independentes e ininterruptas:
/tmp/qa-soak -out /tmp/qa010-6h -duration 6h -playlist 500 -guilds 2 -faults
/tmp/qa-soak -out /tmp/qa010-12h -duration 12h -playlist 500 -guilds 2 -warm -faults
```

Os exemplos 6h/12h são planejamento e estão bloqueados após a observação QA011-R01; não representam aprovação nem autorização para ignorar a triagem de memória. Esses comandos são instruções, não resultados executados. Duração usa relógio monotônico real; nenhum synctest ou avanço virtual no soak. Sources de 100ms tornam o workload intensivo em transições/processos e persistência; não representam músicas naturais de três minutos.

`-out` precisa ser um diretório novo. Para recuperar um checkpoint do próprio harness:

```sh
/tmp/qa-soak -out /tmp/qa010-new -resume -duration 1h -playlist 123 -guilds 2 -warm -faults
python3 -B scripts/qa010_report.py --run /tmp/qa010-new --out /tmp/qa010-evidence
```

Retomada exige marker de ownership e configuração compatível. Lock de processo impede duas sessões no mesmo SQLite. Cada invocação é um novo segmento com duração própria: 30m + restart + 30m **não** aprovam 1h contínua. O arquivo qa-only.db, WAL/SHM e marker são o checkpoint local; não são incluídos no exportador de evidências. Guardar esse diretório em volume de QA para retomar, sem copiá-lo sobre banco operacional.

Eventos e recursos são JSONL sincronizados periodicamente; um processo morto pode deixar segmento sem summary, classificado como incompleto. Interrupção não vira PASS. O resumo final é gravado por rename atômico. Isso não garante fsync de diretório contra perda física de energia.

## Oráculos e limites

- Cada entrada aceita tem identidade lógica com segmento/guild/batch/posição, Video ID e ordem esperada independentes do estado interno da fila.
- Cada seleção consome exatamente a próxima posição; cada tentativa precisa de resultado terminal: completed, unavailable, error_interrupted ou shutdown_interrupted.
- accepted = selected + pending; a fila ainda pendente precisa coincidir com o SQLite após Close. Não chamar uma faixa pendente de perdida ou concluída.
- Callback de anúncio exige frame aceito pelo transporte e não pode ocorrer duas vezes para a mesma tentativa. Não equivale a mensagem publicada em Discord real.
- Resume aceita o replay explícito do Current salvo, desde o início. É uma nova tentativa; não há promessa de exatamente uma execução entre checkpoints nem retomada de timestamp.
- Falhas de fonte, transporte no quarto frame e cancelamento interno são reproduzíveis por seed/posição. Outras fronteiras são exercitadas em testes de falha separados, não misturadas silenciosamente no soak.
- O workload sequencial não embaralha sua ordem para manter o oráculo independente. A campanha concorrente e o modelo existente exercitam skip/previous/replay/loop/shuffle/jump etc. A campanha de 20 usuários verifica identidade, metadados, capacidade, isolamento, ausência de streams simultâneos por guild e teardown; não prova linearizabilidade das 20 operações. As seis linearizações do modelo de três operações continuam sendo a evidência específica dessa propriedade.

## Métricas e interpretação

RSS, heap, objetos, alocações, goroutines, FD, threads, filhos diretos e CPU própria/filhos reaped vêm de runtime e /proc do processo. Não contar processos globais do host. Séries, ledger, resumos por guild e percentis aproximados com histogramas fixos não crescem por tentativa em memória; os eventos ficam em disco. Cache memory aparece como null: SearchCache não exporta contabilização própria. Não atribuir o heap total ao cache.

Primeiros segmentos registravam `first_frame` no callback OnStart, após persistência. Esse campo mede seleção→callback, não entrega exata do frame. A instrumentação final acrescenta `delivery_first_frame` no transporte e `announcement_after_frame`; a campanha delivery de 20s mediu ambos. Percentis do harness são limites superiores de buckets em potências de dois; máximos/médias são amostras reais.

Os segmentos maiores retêm objetos do ledger, metadados pendentes e históricos até escrever o relatório; o ponto após cleanup/GC ainda inclui esse estado de diagnóstico. As medições usam race e conviveram com builds/checks: não servem como SLA de produção ou benchmark isolado. Crescimento de RSS não foi convertido em diagnóstico de memory leak.

## Campanhas e CI

```sh
BARD_QA_TRACE_DIR=/tmp/qa010-traces go test -race -tags stress ./internal/player -run '^TestEnduranceTwentyUsers$' -count=3 -v
# SIGKILL somente em writers de SQLite criados pelo próprio teste:
go test -race -tags stress ./internal/storage -run '^TestEnduranceKill' -count=3 -v
go test -race -tags stress ./internal/qasoak -run '^TestEnduranceGuildFootprint$' -count=3 -v
```

Seeds 41 e 20261019 estão no código e nos traces. Os comandos individuais são reproduzíveis; o scheduler não é determinístico. O arquivo de trace por seed guarda a última repetição, enquanto stdout registra as três repetições (12.000 operações). Para guardar cada execução separada, usar um diretório diferente por invocação.

Quality separa `-short`, integração/cobertura e race. Endurance é workflow_dispatch: stress/crash separado de soak 60s/1h. Não foi disparado GitHub Actions nesta chamada. Sessões 6h/12h devem usar runner dedicado e o CLI; não dependem de manter uma chamada do Codex aberta. E2E externo permanece manual, em servidor autorizado.

## Validação e commits

`go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...` e build Compose: resultados finais em [validation.json](evidence/validation.json): todos exit=0; completo 27,43s, race 39,61s, vet 0,37s e build 49,01s nesta execução concorrente. Compose usa `--env-file /dev/null` e DISCORD_TOKEN/DISCORD_APPLICATION_ID sintéticos apenas para interpolação; nenhum container foi iniciado nem volume operacional montado.

- be25f0b — harness, checkpoint, recursos, reconciliação e testes do próprio harness.
- 5e91921 — stress de 20 usuários, crash/WAL, DNS e ACK local.
- 9277e4f — separação dos jobs de CI e endurance opt-in.
- 5d7a3fc — garantir ownership de cleanup desde o início do setup de recuperação.

[Resultados](soak-results.md) · [Recursos/performance](resource-trends.md) · [Falhas](fault-injection.md) · [E2E](discord-e2e-checklist.md) · [Bloqueios](release-blockers.md).

Logs de Docker/benchmarks/build-info com whitespace de terminal têm apresentação .txt normalizada para git diff --check e cópia exata em .raw.json adjacente, incluindo carriage returns/ANSI. Nenhuma falha foi descartada.

## QA-011 — execução existente, sem nova infraestrutura

Foi reutilizado cmd/qa-soak e scripts/qa010_report.py. Builds com e sem race são do mesmo commit 27a49ba. O comando de uma hora usou `-duration 1h -sample 10s -playlist 500 -guilds 2 -audio 1s -warm -faults -seed 20261019`, com duração monotônica integral. Manifesto/hashes/limites/estatísticas systemd e ledger em evidence/qa011/. A campanha sem race de dez minutos é controle diagnóstico, nunca substituta da hora.

Checkpoint: `.qa-runs/qa011-20261020T022653Z/1h`. O diretório é excluído do Git e do contexto de build Docker, contém somente SQLite/fixtures de QA e permanece no disco do workspace para retomada; o exportador copia apenas métricas/ledger, não o banco. Comando de retomada compatível: `.qa-runs/qa011-soak -out .qa-runs/qa011-20261020T022653Z/1h -resume -duration 1h -sample 10s -playlist 500 -guilds 2 -audio 1s -warm -faults -seed 20261019`. Uma retomada sempre gera outro segmento e não prolonga retroativamente o anterior.

A hora executada teve CPUQuota=100% (um núcleo), prioridade nice 10, MemoryMax=2GiB, TasksMax=128, NOFILE=256 e arquivo individual até 512MiB. Rede IP foi bloqueada no serviço. O ambiente foi filtrado para PATH/GOMAXPROCS/GOMEMLIMIT/GORACE; não houve leitura de .env, token, volume operacional ou acesso Discord. Nenhum serviço do bot foi reiniciado/parado. Limites de execução são proteção do host compartilhado, não critérios de performance fabricados.
