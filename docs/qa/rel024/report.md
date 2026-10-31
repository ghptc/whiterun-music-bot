# REL-024 — recuperação operacional e acabamento dos cards

Data: 21/10/2026. Base: `83e1f9a` (DB-022). Validação local Linux/amd64, Go 1.27.3, FFmpeg/libopus e Docker. Sem deploy, acesso ao banco de produção ou mensagens no Discord de produção.

**Resultado:** campanha local aprovada nos cenários abaixo. A recuperação Discord/Voice de ponta a ponta e o boot da VPS continuam sujeitos à homologação real. Não se declara alta disponibilidade, recuperação de infraestrutura prolongadamente indisponível ou desempenho da VM.

## 1. Lacunas concretas encontradas

| Lacuna | Correção |
|---|---|
| Scheduler diagnosticava uma notificação perdida, mas permanecia esperando para sempre | Reavaliação após 30 s; retoma apenas a posição já pronta e acorda o coordenador de resolução |
| Processo vivo não provava responsividade ou progresso de áudio | Snapshot por guild, primeiro frame/progresso efetivo, identificação de pausa, buffering, cancelamento, fila vazia e suspensão |
| Dependência bloqueada podia bloquear também a própria sonda | Uma única sonda em voo, prazo de 2 s; supervisor independente da conclusão da sonda |
| Compose não tinha healthcheck funcional, rotação stdout ou limites de recursos | `bard healthcheck`, recursos configuráveis, rotação e saída supervisionada do processo |
| Timeouts/DNS antes do áudio descartavam posições sucessivas | Preservação da mesma posição, duas novas tentativas com backoff e jitter; suspensão segura ao esgotar o orçamento |
| Falha transitória podia invalidar correspondência musical válida | Timeouts, rede e falhas de inicialização dos executáveis preservam o mapping; verificações de identidade continuam obrigatórias |
| Checkpoint rejeitado podia continuar antigo sem outro comando | Retry do estado mais recente, com três tentativas adicionais e cancelamento no shutdown |
| Restauração de estado usava contexto sem prazo próprio | Leitura limitada a 3 s |
| Shutdown podia esperar uma dependência que ignorasse cancelamento | Prazo final de 25 s para a desmontagem; Compose mantém grace period de 30 s |
| Cards repetiam o nome da aplicação | `Now Playing`, `Queue` e `Help`, preservando metadados, capa e cores |

Reutilizados: um scheduler/player por guild; circuitos Voice de FIX-020; Gateway Voice com heartbeat e sem reconexão paralela; criação de transporte/UDP/DAVE novo; subprocessos em grupos canceláveis; limites de busca; persistência SQLite, migrations, catálogo, VFS e alertas de DB-022. Não houve migration SQL nova.

## 2. Política de watchdog e recuperação

- Health a cada 5 s, com sinal separado de Gateway, SQLite, responsividade, progresso e capacidade de recuperar sessões. `bard healthcheck` rejeita sonda com mais de 15 s, dependências indisponíveis ou recuperação esgotada.
- Notificação perdida: recuperação local do scheduler em 30 s. Não reordena nem consome outra posição.
- Preparação em `Bot.play`: prazo de 45 s. Após metadados verificados, começa uma nova janela de progresso para o pipeline.
- Extrator e primeiro áudio: 20 s por estágio por padrão. Inatividade durante áudio: 45 s. `Streamer` continua permitindo configuração explícita dos prazos. Pausa suspende o relógio de inatividade.
- Pipeline cancela subprocessos localmente primeiro. Se ainda não houver progresso em 55 s, ou um cancelamento não terminar em 10 s, o supervisor pode encerrar o processo. Uma trava da sonda/worker precisa persistir por 45 s observados; a campanha mediu 52 s desde a injeção.
- Gateway desconectado, fila vazia, pausa e circuito suspenso são degradações/esperas distintas; não provocam reinício por si sós.
- Orçamento de **três reinícios do watchdog por janela de 10 minutos**, persistido em `<BARD_DB_PATH>.supervision.json`, com escrita atômica e fsync. Erro/corrupção desse arquivo impede reinício automático. Ao esgotar, fica degradado e requer correção/interação; não gira em loop. Esse arquivo pequeno é metadado de supervisão separado dos arquivos SQLite contabilizados pelo VFS.
- Voice mantém o orçamento existente de cinco tentativas, backoff exponencial com jitter e deadline de 15 s por tentativa. Esgotamento preserva a fila; `/resume` ou uma sessão autorizada permite nova tentativa. O primeiro frame confirma recuperação. Uma notificação de incidente, sem uma mensagem de falha para cada música pendente.
- Rede antes do primeiro frame: tentativas adicionais em aproximadamente 1 s e 2 s, com jitter de até 25%. Indisponibilidade prolongada suspende a fila. `/stop` cancela a espera; `/skip` elimina explicitamente a posição e limpa o backoff anterior.
- Checkpoints: três retries adicionais em 1/2/4 s, cada escrita com prazo de 3 s, sempre serializados com as mutações. Esgotamento mantém erro visível; não promete durabilidade de novas alterações quando o disco/banco recusa gravações.

Eventos incluem `runtime_health_changed`, `playback_stall_detected`, `recovery_started`, `recovery_attempted`, `recovery_completed`, `recovery_failed`, `process_supervision_triggered` e `state_restored`, com componente e identificação do incidente/operação nos caminhos de recuperação. A fila de diagnósticos existente continua limitada.

## 3. Campanha executada e limites definidos

Limites adotados antes das execuções: detectar travamento em até 60 s; relançar/reabrir após crash em até 30 s; manter 100% das posições ainda pendentes; até uma notificação de incidente; zero filhos persistentes após teardown; zero corrupção e zero retries ilimitados. Timeouts reduzidos são indicados explicitamente.

| Cenário | Método / resultado observado | Evidência |
|---|---|---|
| SIGTERM | Processo Go descartável, SQLite real; reabertura/restauração em 1,88 ms, 123/123 | [runtime-faults.txt](evidence/runtime-faults.txt) |
| SIGKILL | Processo filho real morto, reabertura em 0,89 ms, 123/123 | Mesmo arquivo |
| Panic fatal | Processo isolado; reabertura em 5,64 ms, 123/123 | Mesmo arquivo |
| Sonda bloqueada | Supervisor real no container; saída efetiva em **52.002 ms** | [container-faults.txt](evidence/container-faults.txt) |
| Crash em worker do container | Panic não recuperado; Docker relançou em **454 ms**, fila validada novamente | Mesmo arquivo |
| Stop/start do container | Mesmo volume descartável, restauração 123/123, Voice ausente | Mesmo arquivo |
| FFmpeg congelado | Executável FFmpeg real recebe SIGSTOP; timeout de teste de 1 s; morto e coletado em **998 ms** | [ffmpeg-sigstop.txt](evidence/ffmpeg-sigstop.txt) |
| DNS/rede transitória | Dois processos extratores controlados falham com diagnóstico DNS; terceiro entrega PCM ao FFmpeg real; recuperação em **3,64 s**, 123 posições intactas | [player-faults.txt](evidence/player-faults.txt) |
| Rede persistente | Player real, falha de fronteira injetada: três chamadas, zero notificações de skip, 123/123 preservadas; nenhuma nova tentativa após uma hora virtual | Mesmo arquivo |
| SQLITE_BUSY | Dois bancos/conexões reais; transação bloqueia checkpoint. Após rollback, retry salva sem outro comando, em **3,91 s** desde a tentativa original | [sqlite-busy.txt](evidence/sqlite-busy.txt) |
| Crash com WAL e transação aberta | SIGKILL real, commit anterior preservado, transação incompleta revertida, WAL presente | [database-discord.txt](evidence/database-discord.txt) |
| Voice/heartbeat | WebSocket local real: close 4006, heartbeat sem ACK e fechamento explícito; nova conexão/DAVE e cancelamento da recuperação verificados | Mesmo arquivo |
| Panic recuperável, fontes encerradas, pipe herdado, cancelamento e teardown | Regressões existentes e campanha de processos locais aprovadas | [player-faults.txt](evidence/player-faults.txt) |
| Logs indisponíveis | Fixtures com caminho de log apontando para diretório; fallback permite fila persistente e encerramento/recuperação | [runtime-faults.txt](evidence/runtime-faults.txt) |

Os tempos de reabertura do processo filho medem **SQLite/player**, não login no Discord. O container executou fixture Go com SQLite real e supervisor de produção, sem Gateway externo; 128 MiB, 0,5 CPU e rede `none`. Amostra após recuperação: 5,621 MiB, 0,04% CPU e 7 tarefas; não caracteriza consumo do bot em reprodução real.

A injeção DNS usa diagnóstico de extrator e transporte HTTP controlados; não modifica DNS ou firewall do host. Falhas de Spotify, YouTube, UDP/DAVE e permissões permanecem testes de fronteira/local, sem afirmar homologação dos provedores reais. A suíte completa inclui os cenários existentes de falha HTTP, cancelamento e identidade incorreta.

## 4. Integridade e DB-022

As regressões passaram: schema/migrations, CRUD e concorrência, rollback, recuperação de WAL, registro físico de cota danificado, escritores em processos separados, leituras após bloqueio e restauração sem Voice.

Reconstrução offline: **123 persistidas / 123 reconstruídas, zero chamadas Spotify**. Não se repetiu a importação pública opt-in nesta campanha. O teste live permanece marcado como skip; a evidência live anterior pertence ao DB-022.

VFS físico permanece inalterado: máximo de **10 GiB**, incluindo banco, WAL, SHM, journal e controles existentes; testes usam cotas menores. Nenhum arquivo de 10 GiB foi criado. Alertas reais pelo adaptador REST contra servidor HTTP local: warning=1; critical com falha=1 e retry=1 com nonce estável; reinício não repete confirmação; recuperação abaixo de 85% rearma. Não se afirma entrega externa exatamente uma vez.

Logstore continua separado, com teto existente de **524.288.000 bytes (500 MiB)**. Rotação dos logs stdout do Docker é adicional, até três arquivos de 10 MiB, fora dessa cota SQLite. Não se afirma teto global de 500 MiB somando o armazenamento interno do daemon Docker. Nenhum dado musical existente é apagado para abrir espaço.

## 5. Soak limitado e falha encontrada

Executado por 60 s, duas guilds simuladas, lotes de 123, cache warm, subprocessos locais, FFmpeg real e injeções de fonte, Voice e cancelamento. Resultado final: **pass**, 60,010 s.

Cada guild aceitou 246 posições, fez 225 tentativas, incluindo 9 suspensões recuperáveis, e preservou 30 pendentes: `225 - 9 + 30 = 246`. Asserções de ordem e contabilização permaneceram ativas. Não houve filho restante depois da limpeza.

Recursos do processo de QA: RSS máximo 28.803.072 bytes; goroutines 3 → pico 16 → 3; filhos pico 2 → 0. CPU do processo: 5,84 s; filhos coletados: 11,26 s. É um teste curto em máquina local, sem inferência de uptime prolongado, e2-micro ou VPS.

A primeira execução **falhou**, registrada em [soak-initial-failed.json](evidence/soak-initial-failed.json): o modelo antigo removia a posição ao selecionar e esperava que uma falha Voice a consumisse. O player a preservava corretamente. O fixture foi corrigido para contar tentativas suspensas, recolocar a identidade no modelo independente e oferecer transporte substituto controlado, injetando cada interrupção uma única vez. A comparação integral da fila salva foi mantida. Resultado posterior e amostras: [soak-60s.json](evidence/soak-60s.json), [recursos](evidence/soak-60s-resources.jsonl).

## 6. Cards existentes

`The Bard · Now Playing` → `Now Playing`; `The Bard · Queue` → `Queue`; `The Bard · Help` → `Help`. O orçamento UTF-16 da fila foi atualizado junto ao título. Asserts de anúncios e baseline visual foram atualizados.

A regressão serializa os componentes reais e exige título da música Unicode, artista, `02:03` e a mesma URL de capa. Cores e estrutura permanecem intactas. Os testes de anúncios existentes continuam verificando envio único por evento. Não havia cards de `Paused`, `Resumed`, `Playlist Added` ou `Storage Warning/Critical` com prefixo a renomear: suas respostas existentes não foram convertidas em novos embeds. Nome/avatar da aplicação permanecem intactos. Não houve captura visual de um canal Discord real nesta campanha.

## 7. Docker e reprodução

Compose mantém `init: true`, volume persistente, filesystem somente leitura, tmpfs e `restart: unless-stopped`. Adicionados healthcheck funcional, `mem_limit` (`BARD_MEMORY_LIMIT`, padrão 768m), CPU (`BARD_CPU_LIMIT`, padrão 1.0) e rotação stdout.

Um `unhealthy` isoladamente não causa reinício no Compose: o supervisor interno encerra apenas travamentos funcionais elegíveis, e o Docker aplica sua política de reinício. Esgotamento de recuperação ou outage externo permanece degradado. Falhas repetidas de configuração/startup ainda exigem correção operacional; não se promete recuperá-las por reinício.

O primeiro build legado falhou ao executar SQLite sobre overlay, recusado pelo VFS. A solução usa **BuildKit com tmpfs executável na etapa dos testes**, mantendo os mesmos testes e o fail-closed do VFS. Neste host sem plugin instalado, foi usado Buildx oficial v0.28.0 somente em `/tmp`, sem instalação global.

Uma tentativa inicial de testar restart com `docker kill` não relançou o container, pois a operação é tratada como parada manual neste ambiente. Evidência preservada em [container-manual-kill.txt](evidence/container-manual-kill.txt). A campanha final provoca saída pelo watchdog e panic dentro da fixture; SIGKILL continua comprovado nos processos locais. Todos os containers/volumes da campanha usam nomes aleatórios `rel024-qa-*` e são removidos por trap.

Comandos de validação:

```sh
go test ./...
go vet ./...
go test -race ./internal/... ./cmd/bot
docker buildx build --load -t whiterun-rel024:qa .
scripts/rel024-container-qa.sh
go run ./cmd/qa-soak --out /tmp/rel024-soak-novo --duration 60s --sample 5s --guilds 2 --playlist 123 --warm --faults
```

O caminho do soak precisa ser novo e exclusivo. O script de container compila a fixture do checkout atual e a monta somente leitura, sem usar token Discord ou volumes existentes.

## 8. Validação final

- `go test ./...`: aprovado; [saída](evidence/go-test.txt).
- `go vet ./...`: aprovado, sem diagnósticos; [saída](evidence/go-vet.txt).
- `go test -race ./internal/... ./cmd/bot`: aprovado; [saída](evidence/race.txt).
- Build Docker com testes, FFmpeg/libopus e executável final: aprovado; [saída](evidence/docker-build.txt).
- Campanhas de processo, SQLite, pipeline, cards e container: aprovadas nas evidências acima.
- Soak de 60 s: aprovado após corrigir o modelo da fixture; falha inicial preservada.

Os testes foram executados no workspace com alterações locais preexistentes em logstore, resolver, testes de mídia e scripts de auditoria. Essas alterações foram preservadas e excluídas do commit REL-024.

## 9. Limitações e riscos remanescentes

Não executados: Discord real com áudio audível, negociação DAVE/UDP sob falha de rede real, Spotify/YouTube de produção, reboot da VM/daemon Docker, pressão OOM do bot completo ou observabilidade da VPS. O teste de container sob cota de memória não é um ensaio OOM. O boot do Docker via systemd e as políticas do provedor precisam ser verificados na VPS autorizada; nenhum ajuste remoto foi realizado.

Em falha irrecuperável, a fila fica preservada para interação autorizada; não se reconecta automaticamente a um canal após reboot. Uma VM única continua sem alta disponibilidade. Cota cheia ou SQLite indisponível impedem novos checkpoints; retries são limitados e o último estado confirmado permanece a referência. SIGKILL pode repetir a música atual desde o início, sem reproduzir comandos antigos.

A recuperação de Voice comprovada aqui é local/instrumentada. Homologação real continua necessária antes de anunciar recuperação de Voice em produção. A infraestrutura deve oferecer ext4/tmpfs suportado pelo VFS; o filesystem overlay continua recusado.

## 10. Fechamento de escopo

Implementação e validação local de REL-024 entregues. **Escopo da v1.0 congelado**: próximas atividades restritas à homologação real no Discord, deploy autorizado na VPS brasileira, monitoramento inicial e correção de bugs comprovados. Nenhum REL-025 ou novo milestone de funcionalidades foi iniciado.
