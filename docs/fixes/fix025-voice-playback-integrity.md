# FIX-025 — Voice, retomada e integridade da playlist

Data: 23/10/2026. **Aceite de homologação ainda pendente. Sem deploy.**

A investigação histórica foi concluída antes das alterações. As correções e os testes locais não constituem prova de estabilidade contínua por três horas. O teste real foi iniciado em cliente temporário, mas abortou no preflight: o canal autorizado estava sem ouvintes humanos. O usuário posteriormente orientou deixar a homologação real para depois e ampliar a cobertura automatizada. Não entrou em Voice, não transmitiu áudio e não enviou mensagens.

## Fontes e preservação

- `docker compose exec -T bard bard logs --last 1000` e depois `--last 10000`, ambos somente leitura.
- Cópia do SQLite `bard-logs.db`: **11.732 registros**, de `2026-10-20T13:10:46.081421945Z` até `2026-10-23T16:29:27.421703374Z`. A consulta direta incluiu os registros anteriores aos 10.000 retornados pelo CLI.
- Histórico completo disponível de `docker compose logs --no-color`, estado Docker, memória/PIDs atuais e horário de boot da VM.
- Cópia do estado operacional e consulta SQLite `mode=ro` ao catálogo vivo, **incluindo WAL**. Copiar apenas `bard.db` após as gravações não mostra transações recentes ainda no WAL; essa comparação foi substituída pela leitura correta.
- Código do monitor Voice, circuito do player, supervisão REL-024, pipeline yt-dlp/FFmpeg e implementação local de disgo v0.19.6.

O recorte com IDs SQLite, horários e erros originais está em [historical-evidence.json](fix025/historical-evidence.json), acompanhado do SHA-256 da cópia investigada. Os bancos históricos não foram apagados, migrados manualmente ou reescritos durante a investigação. Alterações locais anteriores foram preservadas e ficam fora deste commit; a validação da árvore de trabalho também inclui essas alterações.

## Timeline correlacionada

Os incidentes informados correspondem a **21/10 às 22:08 e 23:08 em São Paulo (UTC−03)**, respectivamente 22/10 às 01:08 e 02:08 UTC. Guild `698256401922785340`, sessão do player `1792629263190533464`.

| UTC | Posição / tentativa | Evento, componente e resultado |
|---|---|---|
| 22/10 00:48 | 11, Pursuit Of Happiness | Candidatos do Extended Steve Aoki Remix rejeitados como `credited_artist_mismatch`. |
| 22/10 01:06 | 17, Heads Will Roll | Candidatos do A-Trak Remix rejeitados pelo mesmo critério de créditos. |
| 22/10 01:08:55.692 | 10 / 10, We Want Some P--sy | Monitor registra `main_ready=false`, `voice_ready=false`, cache conhecido e mesmo canal; abre circuito imediatamente. |
| 22/10 01:08:55.694 | 10 / 10 | FFmpeg é morto após cancelamento `voice transport unavailable`; yt-dlp havia terminado sem erro. Faixa é reinserida na cabeça. |
| 22/10 01:08:56.321 | incidente `/voice/10` | Uma notificação entregue. Monitor registra recuperação às 01:08:56.693. |
| 22/10 01:08:57.811 | recuperação 1 | `error opening voice gateway`: **voice session already opened or closed**. Tentativa termina às 01:09:11.808 por timeout de 15 s. |
| 22/10 01:09:14.899 | recuperação 2 | Nova negociação concluída. |
| 22/10 01:09:17.853 | 10 / 11 | Primeiro áudio da mesma posição: **22,160 s** depois da detecção. A implementação antiga reiniciava a faixa, sem seek. |
| 22/10 01:37:28.406 | 20 / 19, Fuck Her Gently | `youtube_auth_challenge`: YouTube exige autenticação para confirmar idade; zero áudio. Não é falha Voice. |
| 22/10 01:42:48.418 | 23 / 22, Beelzeboss | `source_http_403`: extração identifica o vídeo, mas download recebe HTTP 403. Não comprova remoção nem bloqueio regional. |
| 22/10 02:08:38.692 | 31 / 30, Pump It | Repete a combinação `main_ready=false`, mesmo canal e cancelamento pelo monitor. FFmpeg e yt-dlp mortos como consequência. |
| 22/10 02:08:39.047 | incidente `/voice/30` | Uma notificação entregue; monitor volta a registrar recuperação em aproximadamente 1 s. |
| 22/10 02:08:40.701 | recuperação 1 | Repete `voice session already opened or closed`; timeout às 02:08:54.926. |
| 22/10 02:08:57.574 | recuperação 2 | Nova sessão pronta. |
| 22/10 02:09:00.695 | 31 / 31 | Primeiro áudio da mesma posição: **22,002 s** depois da detecção. |
| 22/10 02:09:06 | 31 / 31 | Cancelamento explícito `leave`; aplicação continua funcionando. |

Intervalo entre as duas detecções: **59 min 43 s**. Dois pontos sugerem periodicidade próxima de uma hora, mas não demonstram a causa da reconexão do Gateway principal.

Há incidentes anteriores distintos: em 20/10 às 18:10:27 UTC, o Gateway Voice registrou EOF/1006 e a tentativa de reabertura recebeu **4006 — Session is no longer valid**. Às 19:59:12 houve outro EOF/1006, com recuperação em aproximadamente dois segundos. Uma sessão inválida por 4006 exige negociação nova; não deve ser preservada artificialmente.

## Causas confirmadas, hipóteses e desconhecidos

### Confirmadas por logs e código

1. **O monitor podia provocar a interrupção apenas porque o Gateway principal estava reconectando.** O booleano `voiceReady` era calculado como conjunção de Gateway principal, cache, canal e Gateway Voice. `checkVoiceHealth` chamava `SuspendVoice()` na primeira observação. Portanto, os logs antigos com `voice_ready=false` não provam que o websocket Voice estivesse inválido.
2. **A recuperação sempre fechava a conexão existente.** Não tentava confirmar sua validade antes de `Close`, que também envia saída do canal.
3. **A primeira tentativa de recuperação dos dois incidentes falhou localmente antes do timeout**, com `voice session already opened or closed`; a segunda conseguiu transmitir áudio. A perda de áudio não foi um crash do processo.
4. **Não havia progresso de áudio persistido nem seek.** A posição da fila era retida, mas a música recomeçava inteira. O percentual histórico exato é desconhecido: os logs não contêm contagem de frames, e duração de parede não foi tratada como progresso ouvido/enviado.
5. **Os remixes falhavam no matching de créditos**, embora as gravações candidatas trouxessem o remixer no título estruturado exato. O Spotify inclui Steve Aoki e A-Trak na lista de artistas; os uploads não os repetem em `Artists`.
6. A restrição etária e o HTTP 403 das faixas Tenacious D são falhas da fonte, com diagnóstico anterior ao Discord Voice.

### Hipóteses não comprovadas historicamente

- Uma resposta atrasada de saída pode atingir a conexão recém-criada: disgo encaminha `VoiceStateUpdate` com canal nulo ao objeto atualmente registrado, fechando seu Gateway. O cenário é **reproduzido no teste direcionado** e protegido pela correção, mas os payloads históricos de VoiceStateUpdate não foram registrados; não é possível atribuir cada erro histórico a essa corrida com certeza.
- O padrão próximo de uma hora pode decorrer de renovação/conectividade do Gateway principal. Não há código de fechamento, heartbeat ACK ou evento de renovação correspondente nos dois incidentes para concluir a origem.

### Desconhecidos / sem evidência

Não há evidência nos incidentes de heartbeat Voice perdido, erro DAVE, timeout UDP, OOM, SQLite bloqueado, DNS ou EOF de áudio como causa inicial. Também não há amostras históricas de goroutines, memória ou subprocessos que permitam excluir vazamento retrospectivamente.

O SQLite não contém eventos de acionamento do supervisor de processo REL-024 nesses incidentes. O monitor de Voice foi o cancelador observado; a morte dos subprocessos veio depois.

## Sessões longas e saúde do processo

Em 20/10, a guild `404344088691867649` teve entrada Voice às 18:49:13 UTC e início de faixa ainda às 21:55:23: mais de **3h06**, com interrupções no meio. O processo iniciou às 18:48:18 e registrou desligamento limpo às 22:36:11. Isso comprova longevidade, não três horas de áudio estável.

O processo iniciado em 22/10 às 00:08 continuou emitindo métricas após os dois incidentes e só registrou `shutting down` em 23/10 às 02:54:07 UTC. A VM atual iniciou às 10:33:27 e o container às 10:33:42 de 23/10. O responsável pelo desligamento anterior não é identificável no log antigo.

Inspeção atual: container saudável, `OOMKilled=false`, `RestartCount=0`, aproximadamente **36,82 MiB / 768 MiB e 9 PIDs**. Esses campos descrevem o estado observado, não todo o histórico de reinícios ou de OOM da VM.

## Correções

- Separação do estado do Gateway principal e do Gateway Voice. Reconexão do principal com Voice pronto não cancela áudio. Uma indisponibilidade Voice observada pelo monitor recebe tolerância de cinco segundos; remoção/movimentação confirmada suspende imediatamente.
- Recuperação confirma Gateway, DAVE e Speaking na sessão existente antes de recriá-la. Erro real UDP marca o transporte como comprometido; a API atual não oferece renegociação independente segura desse transporte, então uma sessão nova é necessária. Gateway fechado/inválido não é reutilizado.
- O callback de perda do Gateway deixa de disparar `Conn.Close`/saída implicitamente: fecha UDP e DAVE locais, interrompendo seus watchdogs. O scheduler decide a nova negociação; o cleanup de canal vazio continua ativo com cache confiável, mesmo sem áudio pendente.
- A associação ao canal é verificada antes e depois da confirmação local. Remoção, movimentação ou cache desconhecido impedem reentrada automática.
- Um evento de saída anterior à confirmação da nova entrada não fecha o novo Gateway. Após a entrada confirmada, saída real continua sendo encaminhada e invalida a sessão. Eventos de outros usuários não alteram essa proteção.
- Progresso: **960 amostras a 48 kHz / 20 ms por pacote Opus enviado com sucesso**, excluindo os cinco pacotes finais de silêncio. Envio não comprova recepção nem áudio ouvido.
- Checkpoints a cada cinco segundos de áudio confirmado, além das transações existentes de início, término e comandos. A gravação periódica tem orçamento de 100 ms; se o disco estiver ocupado, registra aviso e tenta no próximo intervalo, sem aplicar o timeout de três segundos dos comandos ao fluxo de áudio. O offset pertence ao estado da guild; não altera a chave do catálogo/mapping nem cria uma identidade de catálogo por checkpoint.
- **Abaixo de 50%:** retém a ocorrência e o offset. FFmpeg usa seek de saída com decodificação/descarte a partir da fonte novamente extraída; não depende de byte-range nem persiste URLs assinadas.
- **A partir de 50%:** o scheduler único finaliza a ocorrência e expõe exatamente a próxima posição, sem aplicar repetição de loop à interrupção. Mesma política ao restaurar um `Current` com checkpoint conhecido.
- Sem duração conhecida, o percentual não é inventado: a ocorrência é retida para retomada. Live não aceita seek de offset finito.
- **Fallback de seek:** falha de decodificação antes do primeiro frame retomado, fonte sem seek suportado ou orçamento de recuperação esgotado mantém a posição salva e suspende a reprodução. `/resume` pode tentar novamente; `/skip` permite avançar explicitamente. Nunca há fallback silencioso ao início.
- Falhas reconhecidas de rede, stall, EOF prematuro e interrupções de subprocessos após início recuperam mídia localmente, sem destruir Voice. EOF mais de dois segundos antes da duração esperada deixa de ser tratado como conclusão normal.
- Retries limitados: cinco tentativas Voice com backoff exponencial/jitter e timeout por tentativa; mídia mantém até três tentativas. Um frame isolado não zera esses orçamentos: exige cinco segundos de áudio bem-sucedido ou término normal.
- `/skip` e `/stop` invalidam a tentativa anterior; `/pause` aplicado durante recuperação é preservado na próxima tentativa. Retomadas não duplicam card, histórico em memória ou ledger de início de reprodução.
- Créditos do remixer podem ser comprovados pelo título estruturado **exato** nas duas fontes; demais colaboradores, duração, versão e proveniência continuam obrigatórios.

Checkpoint periódico não é exactly-once de áudio: um crash pode repetir até aproximadamente cinco segundos desde o último checkpoint confirmado, mais o período em que a persistência esteve indisponível. Transações continuam atômicas para as posições lógicas; não se promete eliminar toda repetição audível após falha de energia.

## Resolução real isolada e catálogo

Execução em container descartável com o yt-dlp/FFmpeg do runtime, sem cliente Discord, usando a playlist original e seus **123 lugares**. Cada sucesso exigiu metadata confirmada e **50 frames de áudio decodificados (aproximadamente um segundo)** em sink local. Isso não testa a faixa inteira nem audibilidade no Discord.

| Posição | Faixa | Vídeo | Resultado observado |
|---|---|---|---|
| 11 | Pursuit Of Happiness — Extended Steve Aoki Remix | `9Go1GQZcK1U` | MATCH_CONFIRMED, 50 frames, mapping salvo. |
| 17 | Heads Will Roll — A-Trak Remix | `TDyG4YNUXcI` | MATCH_CONFIRMED, 50 frames, mapping salvo. |
| 20 | Fuck Her Gently | `KLBxvNPBDxE` | Identidade confirmada; restrição etária persiste, zero frames, mapping novo não salvo. |
| 23 | Beelzeboss | `7qtIwDi90N4` | Mesmo vídeo histórico agora produziu 50 frames; mapping salvo. O 403 anterior não comprova remoção ou restrição regional permanente. |

Uma busca adicional limitada por `Tenacious D Fuck Her Gently official audio` retornou uma playlist de busca sem candidatos. Nenhuma alternativa válida foi identificada nessa consulta; isso não prova inexistência de uma fonte mundialmente disponível. Não foram adicionados cookies nem enfraquecidos os critérios de gravação.

Evidências: [resolução](fix025/isolated-resolution.json), [nova tentativa da faixa bloqueada](fix025/gently-recheck.json), [verificação do catálogo incluindo WAL](fix025/catalog-verification.json).

Mappings passaram de **215 para 218**, com os três IDs verificados. Catálogo: 549 registros; tracks: 1.018; ledger: 159; histórico: 129, todos preservados. Os estados persistidos das duas guilds mantiveram o mesmo hash. Foram feitas apenas as gravações de correspondência/catálogo autorizadas, sem deploy.

## Observabilidade

Instrumentação direcionada: `voice_session_state_changed`, `voice_gateway_closed`, `voice_udp_error`, `dave_session_error`, `audio_stream_interrupted`, `playback_resume_started`, `playback_resume_completed`, `playback_resume_failed`, `watchdog_recovery_triggered`, `playback_checkpoint_delayed` e `container_shutdown_requested`. Player/stream usam os identificadores de tentativa, guild, posição e incidentes, offset confirmado e duração disponíveis no contexto. A notificação Voice também leva o ID do incidente internamente.

Faltavam, historicamente, estado Voice independente do principal, contagem confirmada, razão do fechamento local e distinção entre saída tardia e invalidação real. Os novos eventos permitem investigar esses caminhos; não recompõem a evidência perdida. Nem toda falha tem código remoto: um erro UDP, por exemplo, é classificado pela operação que falhou.

Nenhum payload de token, segredo, chave DAVE ou URL assinada foi adicionado. Limites continuam em **500 MiB para SQLite de logs**, rotação Docker de 3 × 10 MB e **10 GiB de cota física musical**. Observação final do catálogo: main DB 2.023.424 bytes e WAL 86.552 bytes; logs aproximadamente 8,8 MB. Não houve alteração de quota ou identidade visual.

## Testes e limitações de aceite

Testes FIX-025 direcionados cobrem 10%, 25%, 49%, 50%, 75% e 99%; 123 ocorrências com duplicações legítimas; recuperação local; UDP comprometido; sessão inválida 4006/heartbeat; transição de faixa; seek real com FFmpeg; seek falho; skip/stop durante recuperação; pausa; restauração de checkpoint sem conexão automática; crash real de subprocesso sem Close, recuperando WAL aos 10 s e 60 s de progresso salvo; guilds isoladas; checkpoint com armazenamento ocupado sem bloquear áudio pelo timeout de comando; orçamento após um único frame e ausência de avanço/card/histórico duplicados.

Comandos de entrega:

```sh
go test ./... -count=1 -timeout 180s
go vet ./...
go test -race ./internal/player ./internal/discord ./internal/media ./internal/storage -count=1 -timeout 300s
```

Resultados e tempos finais estão em [validation.txt](fix025/validation.txt). Testes antigos que esperavam replay integral/ledger duplicado foram ajustados para o contrato novo. O teste de integridade com fonte sintética de 100 ms e metadata de 210 s agora exige diagnóstico de EOF prematuro.

### Soak real

Destino autorizado: guild `404344088691867649`, Voice `921489232374140978` (confirmado por REST, tipo 2). `TestFIX025RealVoiceSoak` é opt-in, exige esse destino, duração mínima de três horas e aborta se já existir uma sessão do bot na guild. Usa SQLite temporário; ignora interações recebidas; não sincroniza comandos, não envia cards e não executa deploy. A rotina real de saída por canal vazio permanece ativa.

Tentativa às 20:28:39 UTC: **preflight bloqueado por ausência de ouvintes humanos**. Tempo Voice conectado: 0; áudio transmitido: 0; mensagens: 0. Desconexões durante reprodução, MTTR, músicas retomadas/avançadas e estabilidade de três horas: **não medidos**. [Evidência](fix025/soak-preflight.json).

Para repetir, compilar o teste de integração e executá-lo no runtime com `BARD_FIX025_SOAK=1`, `BARD_LIVE_GUILD=404344088691867649`, `BARD_LIVE_CHANNEL=921489232374140978` e `-test.run '^TestFIX025RealVoiceSoak$' -test.v -test.timeout 3h10m`, mantendo um ouvinte humano autorizado no canal. Duração padrão: 3h05. As amostras registram saúde, goroutines, tempo conectado, posições restantes e eventos reais de recuperação. A execução ficou adiada por orientação explícita do usuário. O resultado deverá ser anexado antes do aceite P0.

**Riscos remanescentes:** soak real pendente; faixa com restrição etária ainda sem fonte validada; falhas de rede/Discord não podem ser eliminadas; seek por decodificação pode atingir timeout em fontes lentas; checkpoints podem atrasar quando SQLite falha. Os limites de duração e tolerância de EOF exigem observação real em reprodução longa. Nenhuma afirmação de 100% de disponibilidade é sustentada por esta entrega.
