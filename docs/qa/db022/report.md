# DB-022-QA — relatório de integridade, performance e armazenamento

Data: 21/10/2026. **Aceite global: BLOQUEADO.** Catálogo e alertas possuem evidência funcional, mas a cota física de 10 GiB ainda não é garantida. Não houve deploy, alteração do banco de produção nem envio ao Discord de produção.

## Implementação e schema

Migrations 001–005 aplicadas em SQLite real, com `foreign_keys=ON`, WAL e transações. O catálogo existente permanece em `music_catalog`; artistas múltiplos, álbum, ISRC, versão/título, explicit opcional e artwork permanecem no JSON de metadados. Não foram criadas entidades normalizadas separadas para artistas/álbuns.

A migration 004 adiciona `catalog_playlists`, `catalog_positions`, FK CASCADE da playlist, FK RESTRICT da música, identidade da ocorrência por ordinal e unicidade da posição; índices de referência da faixa e busca artista/título. Triggers impedem IDs NULL, aceitos indevidamente pelo comportamento SQLite de TEXT PRIMARY KEY na tabela original. Nenhuma coluna de produção foi removida.

A migration 005 adiciona contador transacional de bytes UTF-8, limite padrão 10 GiB, estado de thresholds, outbox durável e atividade por interação. Triggers mantêm o contador e rejeitam crescimento excedente através de CHECK; rollback restaura dados, uso e alertas. O detector observa apenas o estado final de operações em lote: uma substituição de playlist não rearma alertas por causa de quedas intermediárias no uso.

A inspeção usa `PRAGMA table_info`, `PRAGMA foreign_key_check`, `sqlite_master` e `EXPLAIN QUERY PLAN`. Um banco real com migrations 001/002 e dados preexistentes é atualizado, reaplicado e comparado. Alterações add/rename/drop/rebuild de campos são exercitadas somente em tabelas descartáveis, incluindo índice UNIQUE, índices explícitos e rollback de DDL. Rebuild de produção futuro deve ser transacional, copiar todas as colunas mantidas e recriar índices/constraints. Uma falha anterior ao commit reverte tudo; após commit, recuperação requer backup consistente ou migration corretiva, sem apagar o arquivo do operador.

## CRUD, exclusão e recuperação

Testes reais cobrem inserção, leitura, atualização, reinício, Unicode, arrays de artistas, metadados opcionais NULL/nil, títulos com versão e ocorrências duplicadas. Importação incompleta é rejeitada atomicamente. Leituras concorrentes observam snapshots completos enquanto a mesma playlist é substituída por múltiplos importadores.

Excluir playlist remove suas ocorrências e preserva músicas compartilhadas. Excluir uma posição preserva outra ocorrência da mesma faixa. Música referenciada é protegida por FK; música órfã só é excluída por chamada explícita `DeleteOrphanTrack`, nunca automaticamente. Lacunas de posições originais são preservadas após exclusão, sem renumerar artificialmente a fonte.

Um subprocesso é morto com título alterado e posições removidas em transação não confirmada. Reabertura recupera as 100 posições e o título anterior, sem violações de FK. Há teste de leituras após rejeição por cota e reinício. Dois handles SQLite também disputam a última vaga: exatamente uma inserção é confirmada, sete são rejeitadas e o contador termina exatamente em 100%. Exclusão e reconstrução concorrentes em handles distintos nunca expõem snapshots parciais. Não há claim de cobertura exaustiva de todas as intercalações possíveis de múltiplos processos.

## Playlist de homologação e fila

Fonte real: https://open.spotify.com/playlist/35m4wmHSlcS2RNsafX4dEg

Importação pública completa em 21/10/2026: **123/123 posições**, páginas verificadas de 50 + 50 + 23 e enriquecimento de 23 metadados da cauda. O teste importa os metadados reais e usa o mesmo caminho de persistência do resolver; fecha/reabre SQLite, substitui o provider por um cliente que falha se chamado e reconstrói a playlist. Compara contagem, ordem, IDs, títulos/versões, artistas, álbum, posições e totais. A fila real do player recebe as faixas e é comparada novamente, sem fonte de áudio.

**Resultado: 123 persistidas, 123 reconstruídas, zero chamadas Spotify no cache hit.** Não há deduplicação por track ID na tabela de ocorrências; o teste determinístico adicional verifica IDs repetidos em posições diferentes. SQLite reconstrói metadados sem rede. Localizadores/disponibilidade de áudio e playback real continuam dependendo da fonte externa.

Snapshots completos não expiram com o cache de faixas de 24 horas. Para atualizar explicitamente a fonte, use importação completa/`SavePlaylist`; o resolver retorna o snapshot existente. Importações progressivas com `MetadataPending` e coleções sem identidade Spotify completa não substituem snapshots completos: é necessário concluir uma importação de metadados para persistir a playlist integralmente.

Foi encontrado e corrigido um defeito em `-race`: o timeout opcional de 200 ms podia descartar a gravação da playlist. A gravação completa agora usa o deadline da importação e propaga erros; uma falha de leitura do snapshot não provoca fallback silencioso ao Spotify. [Falha inicial preservada](evidence/race-initial-failure.txt), [regressão repetida três vezes](evidence/race-regression.txt), [homologação real](evidence/spotify-live.txt).

## Performance

[Medições e limitações](performance.md), com CSV de p50/p95/p99 e evidências brutas para 100, 1.000, 10.000 e 100.000 músicas. Todas as metas p95 solicitadas passaram neste ambiente. São arquivos SQLite reais em tmpfs, sem cache da aplicação; não são medições cold disk nem validação do armazenamento persistente de produção. Deve-se repetir a campanha no hardware/volume de referência antes do aceite final de performance.

## Cota: resultado e bloqueador físico

**Aprovado somente para payload lógico contabilizado.** Conta bytes UTF-8 de ID/JSON das faixas, ID/título/URL das playlists e IDs/inteiros das posições. Não contabiliza páginas livres, índices, cabeçalhos SQLite, WAL/SHM, estado da fila/histórico, atividade de comandos nem outbox. Logs usam o `logstore` separado, sem consumir esse contador. Nenhuma escrita autorizada nesses registros contabilizados pode superar o limite lógico: a constraint é mantida na mesma transação SQLite, inclusive em concorrência. Alterar a cota abaixo do uso falha, sem remoção automática.

O teste reduz a cota ao tamanho efetivamente serializado de uma música, **479 bytes**. Uma atualização que aumenta o título e oito inserções concorrentes são rejeitadas; o registro original continua legível e a rejeição sobrevive ao reinício. Entretanto, o WAL já ocupa **160.712 bytes**, acima da cota lógica. Esta é uma reprodução explícita do problema físico, não uma simulação de tamanho nem uma aprovação do requisito de 10 GiB.

**Permanece pendente implementar e verificar isolamento/enforcement físico**, incluindo SQLite principal, WAL, SHM, journaling e arquivos de manutenção. O banco atual compartilha catálogo e estado operacional; limite de páginas do banco principal ou verificação de `stat` após commit não satisfazem a garantia. É necessário volume dedicado com cota imposta pelo filesystem/container e separação dos logs, ou um VFS que recuse alocações sob a mesma reserva atômica. O ambiente de destino ainda precisa ser especificado. Não foi criado arquivo de 10 GiB, montado filesystem nem alterada configuração de host. Até isso existir e ser testado com cotas pequenas, **DB-022 não está concluído e continua bloqueador da v1.0**.

## Alertas e seleção de canais

O pipeline é real: writes SQLite → triggers/thresholds → outbox SQLite → ranking persistido → verificações REST → `CreateMessage` do adaptador disgo real contra servidor HTTP local. O teste verifica Authorization, rota, texto, nonce e enforce_nonce. Não envia mensagens ao Discord de produção.

Abaixo de 90%: zero POSTs. Aos 90%: um warning. Aos 100%: um evento crítico e rejeição do próximo crescimento. A primeira entrega crítica falha no servidor; tentativa pendente e backoff sobrevivem à reabertura. Retry usa o mesmo nonce; confirmação impede novos envios. Abaixo de 85%, um novo episódio permite outro warning. Resultado capturado: um warning inicial, uma tentativa crítica falha, um retry crítico e um warning após recuperação.

Comandos reconhecidos/confirmados em guild com canal identificado são deduplicados pelo interaction ID. Ranking: janela `(agora−30 dias, agora]`, maior atividade primeiro, desempate por guild ID e channel ID. O teste utiliza múltiplas guilds, canal removido (404), canal sem Send Messages por overwrite, canal elegível de maior atividade, eventos na borda da janela e no futuro, empate e ausência de candidatos. Permissões efetivas consideram @everyone, roles, administrador, overwrites e timeout do membro; canais de texto/news são elegíveis. Threads não são selecionadas.

Lease e backoff são persistidos antes de I/O; um claim condicional impede dois workers de obter o mesmo evento simultaneamente. O destino escolhido é preservado para recuperação de respostas ambíguas, enquanto continuar elegível. Logs estruturados incluem event_id, threshold, used_bytes, limit_bytes e attempt. Bot inicia monitor a cada 15 segundos, com uma tentativa por Poll, timeout de 10 segundos e backoff até uma hora. Eventos históricos pendentes não são descartados automaticamente.

**Não há garantia exatamente uma vez.** Crash entre sucesso HTTP e confirmação SQLite pode repetir uma tentativa; nonce estável com enforce_nonce reduz duplicação dentro da janela suportada pelo Discord, sem garantir deduplicação infinita. Reeleição de canal após perda de acesso também impede uma garantia transacional entre canais. Smoke de Discord de homologação não executado: não houve configuração e autorização explícitas de um canal de destino.

## Execução

- `go test ./... -count=1`: aprovado; [evidência](evidence/go-test.txt).
- `go vet ./...`: aprovado; [evidência](evidence/go-vet.txt), saída vazia significa sucesso.
- `go test -race ./... -count=1`: aprovado; [evidência](evidence/go-race.txt).
- Testes DB-022 com nomes e planos: [evidência focada](evidence/focused.txt).
- Importação pública opt-in: `DB022_SPOTIFY_LIVE=1 go test ./internal/storage -run TestDB022HomologationPlaylistLive -v -count=1`.
- Benchmark: `go test ./internal/storage -run '^$' -bench BenchmarkDB022SQLite -benchtime=100x -count=1`.

Dez testes DB-022 focados passaram; o caso de homologação pública passou separadamente. Logs foram normalizados somente quanto a espaços finais e finais de linha, preservando resultados e métricas.

Testes de integração externa opcionais existentes e helper subprocesso são pulados na execução padrão; a importação pública foi executada separadamente. O banco de produção não foi usado; o teste de upgrade é contra fixtures reais isoladas. O teste opcional de backup de produção continua exigindo `BARD_TEST_EXISTING_DB` com backup consistente, nunca o arquivo aberto do serviço.

As falhas encontradas não foram mascaradas: NULL de identidade, perda de snapshots por timeout e rearmamento intermediário foram corrigidos. A garantia física e a validação de disco de referência continuam pendentes e impedem aceite global.
