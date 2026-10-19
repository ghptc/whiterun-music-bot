# Performance e soak

Baseline executado em Linux amd64, AMD Ryzen 7 9800X3D, GOMAXPROCS observado pelo benchmark: 8. Cinco amostras, benchmem, sem teto artificial. Evidência: evidence/benchmark.txt.

Snapshot de 1.000 faixas: aproximadamente 303–313 µs/op, 2.359.566–2.359.568 B/op, 4.024 allocs/op. Snapshot de 500: 78–83 µs/op, cerca de 794 KB/op. Esta medição não inclui renderização de componentes, REST, resolução nem áudio. Há aumento expressivo de alocações; não é prova de vazamento nem justificativa para refatorar produção nesta etapa.

## Protocolo de comparação

Rodar cinco amostras baseline/candidato sequencialmente no mesmo runner sem jobs concorrentes; registrar CPU, kernel, Go, imagem e carga. Usar benchstat e distribuição p50/p95/p99 de latências, sem adotar um número isolado como SLA. Primeiras medições desta sessão coexistiram com outros checks: repetir em runner dedicado antes de definir budget de release.

## Soak operacional planejado

1. Sessão dedicada de 8 horas, duas guilds, playlists de 500 posições e 20 usuários; repetir stop/leave, pause/resume, perda de rede e importações.
2. Coletores externos por intervalo registram RSS, heap após GC, CPU, /proc/PID/fd, goroutines, filhos (PID/PPID/grupo), latência de comandos, intervalo entre faixas e resolução por hit/miss.
3. Correlacionar cada ciclo com ID da tentativa e ticket. Após stop/leave e encerramento, todos os processos iniciados devem estar reaped; usar propriedade de ownership, não contagem global de processos do host.
4. Rodar cenário cache frio e quente; comparar guild única e duas guilds. Validar áudio recebido e integridade da fila continuamente.
5. Determinar limiares a partir do baseline estável no ambiente de release. Investigar tendência crescente de recursos após períodos equivalentes de repouso.
6. Guardar amostras brutas e diagnóstico de cada desvio. O teste race repetido não substitui oito horas de soak.

## Harness local opt-in

BARD_QA_SOAK_DURATION=30m go test -race -tags soak ./internal/player -run '^TestQualitySoak$' -count=1 -v -timeout=35m

O workload mantém um player, importa lotes de 500, pausa, replay, skip, shuffle e stop; verifica ordem de transição, capacidade e join do run loop. Registra heap, alocações, goroutines e FD. É transporte virtual, sem processos externos. Smoke de 2s executado: 1.091 ciclos, FD 7→7, goroutines 3→2 após Close. Sem threshold de memória inferido de uma única amostra. Evidência: evidence/soak-smoke.txt.

Status: benchmark e smoke do harness executados; soak prolongado, renderização, CPU e latências externas ainda pendentes.
