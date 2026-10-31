# DB-022-QA — medições SQLite

Execução em 21/10/2026: Linux amd64, Go 1.27, AMD Ryzen 7 9800X3D, 8 CPUs disponíveis. Bancos reais em diretórios temporários no `/tmp` (tmpfs). Estes números **não validam latência de disco persistente do ambiente de produção**.

Volumes: 100, 1.000, 10.000 e 100.000 músicas. Cada combinação tem 100 amostras, quantis por nearest rank; p99 com 100 observações tem precisão limitada. Sem cache da aplicação e sem Spotify/YouTube. Warm conserva o page cache SQLite; cold_sqlite executa `PRAGMA shrink_memory` antes da amostra, fora da medição. O cache do SO não foi esvaziado: não se afirma cold disk.

A tabela mostra a combinação de maior p95 para cada operação, com p50 e p99 da mesma combinação, em milissegundos. Todos os resultados estão em [CSV](evidence/latency-ms.csv).

| Operação | Catálogo | Cache | p50 ms | p95 ms | p99 ms | Meta p95 ms | Resultado |
|---|---:|---|---:|---:|---:|---:|---|
| track_id | 1000 | cold_sqlite | 0.0118 | 0.0148 | 0.0338 | 10 | PASS |
| playlist_id | 100 | cold_sqlite | 0.0069 | 0.0128 | 0.0399 | 10 | PASS |
| position | 100000 | cold_sqlite | 0.0191 | 0.0256 | 0.0329 | 10 | PASS |
| playlist_100 | 1000 | cold_sqlite | 0.3513 | 0.5285 | 0.5691 | 50 | PASS |
| playlist_1000 | 100000 | cold_sqlite | 3.4288 | 4.1947 | 4.4349 | 200 | PASS |
| queue_1000 | 1000 | cold_sqlite | 4.5517 | 4.9034 | 5.2747 | 300 | PASS |
| batch_insert_100 | 1000 | cold_sqlite | 7.2321 | 7.8308 | 8.2996 | — | sem meta |
| batch_update_100 | 100000 | cold_sqlite | 8.4334 | 8.8506 | 9.2842 | — | sem meta |
| metadata_update | 100000 | cold_sqlite | 0.1061 | 0.2082 | 0.3625 | — | sem meta |
| remove_position | 100000 | cold_sqlite | 0.0540 | 0.1302 | 0.1500 | — | sem meta |
| normalized_artist_title | 1000 | cold_sqlite | 0.0128 | 0.0139 | 0.0178 | — | sem meta |
| full_playlist | 100000 | cold_sqlite | 344.5953 | 430.3529 | 470.6019 | — | sem meta |

`full_playlist` carrega todas as posições (até 100.000), sem impor a meta de playlists de 1.000 a esse volume. `queue_1000` inclui consulta SQLite, criação do player real, inserção, comparação de ordem/IDs/posições e encerramento, sem transporte de áudio. `batch_insert_100` insere 100 IDs ausentes; limpeza da fixture fica fora da medição. `batch_update_100` atualiza 100 IDs existentes (nome original `batch_100` no primeiro log). `remove_position` mede exclusão SQL real e rollback para restaurar a fixture, não um commit durável. Busca normalizada usa `lower` nativo SQLite (case folding ASCII), não remoção de acentos/case folding Unicode.

Planos críticos devem conter `USING INDEX` ou `USING COVERING INDEX`; ver [testes focados](evidence/focused.txt). Carregamento usa um JOIN ordenado, sem N+1. As metas passaram neste ambiente; repetir no volume persistente do hardware de referência é necessário.

Reprodução:

```sh
go test ./internal/storage -run '^$' -bench BenchmarkDB022SQLite -benchtime=100x -count=1
```

Evidências brutas: [matriz](evidence/benchmark-sqlite.txt), [inserções](evidence/benchmark-insert.txt), [fila com comparação](evidence/benchmark-queue.txt).
