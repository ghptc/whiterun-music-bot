# DB-022-FINAL — fechamento local

21/10/2026. **Os dois bloqueadores do DB-022 foram resolvidos no ambiente testado: Linux/amd64, ext4, NVMe local.** Não houve deploy, escrita no banco de produção nem mensagens no Discord de produção. Não houve teste na Google Cloud/e2-micro.

## Enforcement físico

`storage.Open` agora usa um VFS de cota sobre o VFS `unix` do modernc SQLite **v1.39.1**, com limite padrão e máximo de **10.737.418.240 bytes (10 GiB)** por banco operacional. `OpenWithPhysicalQuota` aceita limites menores, a partir de 512 KiB, para testes. O limite fica persistido; reabrir com `Open` não o aumenta silenciosamente. Valores acima de 10 GiB são recusados.

A unidade de contabilização é `max(tamanho arredondado para bloco, st_blocks×512)` de cada arquivo: banco principal, `-wal`, `-shm`, `-journal`, `.quota` e `.quota-lock`. Abrange também fila, histórico, índices e outros registros do mesmo banco operacional. Não depende do tamanho lógico das linhas nem da observação posterior ao commit.

Antes de `xWrite`, extensão de `xTruncate` e `xShmMap`, o VFS adquire mutex e `flock` entre processos, mede os arquivos e reserva o crescimento. A projeção soma **crescimento do tamanho lógico aos blocos já alocados**: usar apenas o máximo dos dois subestimava metadados de extents. A alocação exata `fallocate(KEEP_SIZE)` ocorre sob o mesmo lock, antes do `pwrite`, evitando a prealocação especulativa do ext4. SIZE_HINT/CHUNK_SIZE não podem criar alocações fora do controle; mmap do banco fica desabilitado e temporários/subjournals são mantidos em memória. Abertura de outros arquivos via este VFS é recusada.

O orçamento inclui reserva conservadora para copiar WAL para o principal durante checkpoint e **64 KiB de margem para metadados de alocação**. Assim, o limite pode impedir uma transação antes que os arquivos visíveis somem 10 GiB. `PhysicalUsage.Files` é o uso físico observado; `Reserved` inclui recuperação/margem e orienta admissão/alertas. Não são a mesma medida. Checkpoint pode gastar sua reserva; leituras e operações sobre bytes já existentes não precisam reservar novamente toda a capacidade.

Um pedido que não cabe retorna `SQLITE_FULL` **antes da alocação recusada**. SQLite continua responsável por commit, rollback, locking e recuperação WAL. Não há limpeza automática de músicas. O VFS mede também o resultado das operações e registra pico físico para detectar violações da invariável nos testes; essa verificação posterior é uma defesa adicional, não o mecanismo de enforcement.

Não foi necessário mover o catálogo ou alterar migrations SQL: os mesmos arquivos e tabelas existentes continuam válidos. Na primeira abertura são criados os arquivos de controle; verifica-se espaço antes de alocá-los. Arquivos legados que já não deixam espaço para controle/margem são recusados sem remover dados. Para esse caso de upgrade, é preciso disponibilizar espaço/um backup consistente antes de migrar; não se promete aceitar um banco legado já acima do teto.

Os logs continuam em seu SQLite próprio e no VFS anterior, com limite existente de **524.288.000 bytes (500 MiB)**. O VFS musical não é registrado como default global. A regressão satura a admissão musical e depois grava/fecha/consulta métricas do logstore real: um evento confirmado, zero falhas e nenhum aumento no contador físico musical.

## Alertas e recuperação do controle

Os alertas físicos usam o pipeline Discord existente, com ranking de canais e adaptador REST disgo real. O estado necessário cabe em **dois registros fixos de 4 KiB**, com sequência, CRC e fsync; a criação também sincroniza o diretório. Não é necessário crescer uma tabela SQL quando o banco chega à capacidade.

- Warning ao atingir 90% do orçamento de arquivos e recuperação.
- Crítico ao atingir 100% desse orçamento ou quando a próxima alocação é recusada. O texto informa bytes usados/reservados; não afirma que o tamanho corrente dos arquivos já é exatamente 10 GiB.
- Rearmamento abaixo de 85%; novo cruzamento gera novo episódio.
- Lease, destino, tentativas e backoff persistidos; nonce estável para retries.
- Estado confirmado não é reenviado após reinício. Não há promessa de entrega exatamente uma vez entre SQLite/arquivo de controle e API externa.

Uma escrita rasgada no registro de controle mais novo é recuperada pelo registro anterior, mantendo a cota configurada e as posições persistidas. Se ambos os registros estiverem inválidos, a abertura falha de forma segura; não adivinha uma cota maior nem apaga o catálogo. `.quota` deve acompanhar o backup do banco. Não remover `.quota-lock` com processos em execução.

## Campanha física real em disco

[Evidência completa das regressões](evidence/regression.txt), executada com TMPDIR no ext4 do projeto, sem criar arquivos de 10 GiB:

| Cenário | Evidência/resultado |
|---|---|
| Cota 1 MiB e leitor prendendo WAL | Crescimento recusado; pico observado entre operações 512.000 bytes; playlist de 10 posições continuou legível e alimentou o player |
| Escritas concorrentes após bloqueio | Nenhum arquivo contabilizado acima de 1 MiB; rollback/fechamento/reabertura preservaram dados |
| Lote de 2 MiB sob cota 512 KiB | Rejeitado atomicamente; playlist anterior intacta; crítico persistido; uso físico 249.856 bytes na campanha registrada |
| Três processos reais, cota 2 MiB | Escritas/commits concorrentes e checkpoints reais, dados anteriores preservados e nenhuma violação de cota |
| Repetição do cenário de processos | **100 execuções / 300 processos** sem falha; [log](evidence/physical-process-stress.txt) |
| SIGKILL com WAL pendente | WAL real de 16.512 bytes; commit anterior recuperado, transação interrompida revertida, 100 posições preservadas e FK/integridade válidas |
| Registro de controle rasgado | Limite de 1 MiB e 10 posições recuperados pelo registro anterior |
| Discord HTTP real local | Warning 90%, crítico 100%, falha de entrega, retry após reabertura com mesmo nonce, confirmação sem repetição, recuperação abaixo de 85% e novo warning |
| Logstore independente | Evento de log persistido após recusa musical, sem consumir cota musical |

Foi encontrada uma falha real durante a implementação: a projeção ignorava a diferença entre blocos de metadados já alocados e o crescimento do arquivo; uma escrita podia ultrapassar a cota em 4 KiB. [Falha preservada](evidence/initial-allocation-failure.txt). A correção usa projeção aditiva, alocação exata antes do pwrite e margem para metadados. Os asserts físicos foram mantidos, incluindo comparação de `Files` e pico com a cota. O teste entre processos foi repetido 100 vezes após a correção.

## Performance e integridade existente

[Medições p50/p95/p99 e comparação com QA](performance.md). Bancos reais no ext4 de `/dev/nvme0n1p2`, WAL, synchronous=FULL; sem cache da aplicação. As metas passaram para catálogos de 100, 1.000, 10.000 e 100.000 músicas. O cache do SO não foi esvaziado: isto não é medição de cold disk.

A campanha de concorrência usa dois handles e quatro gravações simultâneas por amostra, depois fecha/reabre e verifica metadados, 1.000 posições, montagem de fila e `integrity_check=ok`. Não há meta inventada para latência de escrita concorrente.

A playlist pública de homologação foi importada novamente em 21/10/2026: **123/123 posições persistidas e reconstruídas, zero chamadas Spotify no cache hit**, com o novo VFS. [Execução real](evidence/spotify-live.txt). Fonte de áudio e Discord Voice real não foram necessários nem homologados por este teste de catálogo.

CRUD, migrations, exclusões, concorrência, rollback, reinício, alertas lógicos existentes e alertas físicos passaram nas regressões. Não foram alterados asserts existentes para tolerar corrupção, perda de posições ou acesso Spotify no cache hit.

As verificações foram executadas na worktree atual. Alterações locais preexistentes em media/logstore/scripts foram preservadas e não fazem parte deste commit.

Verificações:

- `go test ./... -count=1`: [aprovado](evidence/go-test.txt).
- `go vet ./...`: [aprovado, saída vazia](evidence/go-vet.txt).
- `go test -race ./internal/storage ./internal/media ./internal/player ./internal/discord ./internal/logstore -count=1`: [aprovado](evidence/go-race.txt).

## Escopo da garantia e operação

O mecanismo é de admissão no VFS da aplicação, não uma cota administrativa do volume inteiro. Todas as conexões de escrita do banco operacional precisam usar `storage.Open`; não executar em paralelo binário antigo, GUI ou CLI SQLite que contorne este VFS. Symlinks de arquivo/hard links são recusados; diretórios são canonicalizados para que aliases compartilhem o mesmo lock/cota. Arquivos arbitrários colocados pelo operador no diretório não são parte do catálogo contabilizado.

O suporte é **Linux com ext4 ou tmpfs, blocos de até 4 KiB e alocação exata suportada**. Geometria bigalloc/COW/network não foi validada; tipos de filesystem desconhecidos, blocos maiores e geometria de controle incompatível são recusados. A homologação de persistência usa ext4, não tmpfs. A ponte ABI usa estruturas do unix VFS da versão fixada do modernc; atualização da dependência exige repetir a campanha física e de race antes de liberar.

A cota cobre arquivos e seus blocos, incluindo espaço de recuperação reservado; não cobre overhead global do filesystem, snapshots externos ou alterações por outros programas. Falha global do dispositivo, EIO e corrupção de ambos os registros de controle continuam erros operacionais: não se afirma que um limite de aplicação substitui recuperação do hardware.

Antes de atualizar uma instância real, parar escritores antigos e fazer backup consistente do banco/sidecars, mantendo `.quota` quando já existir. Não copiar apenas o principal com WAL ativo. Esta entrega não executou esse procedimento em produção.

Base técnica: [interface VFS SQLite](https://sqlite.org/c3ref/io_methods.html) e [controles de arquivo/prealocação](https://www.sqlite.org/c3ref/c_fcntl_begin_atomic_write.html). Os números acima são medições locais, **não homologação Google Cloud**.
