# DB-022 — performance em disco persistente

Ambiente efetivamente testado: Linux amd64, Go 1.27, AMD Ryzen 7 9800X3D, SQLite modernc v1.39.1. Arquivos em `/home/gabriel/Projects/whiterun-music-bot/.qa-disk`, **ext4 em /dev/nvme0n1p2**, com WAL e synchronous=FULL. A pasta é apenas o TMPDIR de bancos descartáveis; os bancos não usam :memory: nem tmpfs. [Identificação do ambiente](evidence/environment.json).

Campanha de aceite: 100 amostras por operação, catálogos de 100, 1.000, 10.000 e 100.000 músicas, sem Spotify/YouTube. Não há cache da aplicação. Warm preserva o cache SQLite; cold_sqlite executa PRAGMA shrink_memory antes da amostra. O cache do SO **não foi esvaziado** e pode permanecer quente. Assim, o backend é disco persistente, mas não se afirma latência de leitura de mídia a cada lookup.

Quantis por nearest rank; p99 com 100 amostras tem precisão limitada. Cada linha abaixo é a combinação com maior p95 para aquela operação; p50 e p99 pertencem à mesma combinação. Valores em ms:

| Operação | Catálogo | Modo | p50 | p95 | p99 | Meta p95 | Resultado |
|---|---:|---|---:|---:|---:|---:|---|
| track_id | 100000 | cold_sqlite | 0.0088 | 0.0406 | 0.0605 | 10 | PASS |
| playlist_id | 1000 | cold_sqlite | 0.0070 | 0.0096 | 0.0336 | 10 | PASS |
| position | 100000 | cold_sqlite | 0.0205 | 0.0875 | 0.1634 | 10 | PASS |
| playlist_100 | 1000 | cold_sqlite | 0.3569 | 0.5421 | 0.6037 | 50 | PASS |
| playlist_1000 | 100000 | warm | 3.2757 | 5.9205 | 6.7108 | 200 | PASS |
| queue_1000 | 100000 | warm | 3.8283 | 7.7940 | 8.6794 | 300 | PASS |
| concurrent_writes | 1000 | two_handles_four_writers | 5.5263 | 10.3892 | 10.5507 | sem meta | medido |

Todas as metas de leitura/montagem passaram neste hardware. Para comparação, a campanha QA anterior em tmpfs apresentou p95 máximos de 0,0148 ms por track ID, 0,0256 ms por posição, 0,5285 ms para 100 faixas, 4,1947 ms para 1.000 e 4,9034 ms para montar a fila. As condições/cache/carga diferem; não se atribui causalmente toda diferença ao disco.

`concurrent_writes` mede o lote completo de quatro gravações simultâneas por dois handles SQLite reais; inclui contenção e commit WAL. Depois da medição, fecha/reabre os handles, confere as 1.000 posições/metadados atualizados, alimenta o player real e exige PRAGMA integrity_check=ok. O pico físico foi registrado no log como physical_peak_bytes. Não há meta p95 pré-definida para esse lote de escrita.

A fila inclui consulta SQLite, criação do player, inserção, comparação de IDs/ordem/posições e encerramento, sem áudio externo. Limite físico é habilitado também durante os benchmarks.

- [CSV de todas as combinações](evidence/latency-ms.csv).
- [Aceite de consultas e concorrência](evidence/disk-acceptance-benchmark.txt).
- [Matriz completa complementar, incluindo carga de 100.000 posições, inserção/atualização em lote e remoção com rollback](evidence/disk-benchmark-final.txt). Esta matriz foi coletada durante a finalização; as metas foram reexecutadas na campanha de aceite após as correções de alocação. Remoção mede DELETE e rollback, não commit de exclusão.

Reprodução local:

```sh
mkdir -p .qa-disk
# Confirmar que a pasta aponta para armazenamento persistente.
df -T .qa-disk
TMPDIR="$PWD/.qa-disk" go test ./internal/storage -run '^$' \
  -bench 'BenchmarkDB022DiskConcurrency|BenchmarkDB022SQLite/.*/.*/(track_id|playlist_id|position|playlist_100|playlist_1000|queue_1000)$' \
  -benchtime=100x -count=1
```

**Não houve benchmark em Google Cloud/e2-micro.** Os resultados homologam apenas este ambiente local, sem promessa de desempenho ou disponibilidade na VM.
