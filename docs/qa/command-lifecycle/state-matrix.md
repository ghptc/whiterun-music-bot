# QA-013 — matriz de estados e testes

O player expõe AttemptState em maiúsculas. Transição sem current é condição do
scheduler, não enum persistente. Stopped, Recovering e Provider blocked não são
novos estados artificiais de produção.

| Condição solicitada | Condição real / entrada | Comandos/cobertura | Oráculo e efeito posterior |
|---|---|---|---|
| Idle | Current=nil, fila vazia, voz anexada | Todos 22 × todos 22, fase idle | ACK, resposta, fila, histórico, geração, durable state; play posterior + EOF + stop |
| Playing | PLAYING após primeiro WriteFrame aceito | Todos 22 × todos 22, fase playing | Ordem/identidade/histórico/loop; EOF e nova requisição |
| Paused | Gate do atual pausado | Todos 22 × todos 22, fase paused | Replay/skip substituem gate; resume antes de EOF natural; pausa não persistida |
| Starting | STARTING, CurrentStarted=false, stream bloqueado antes do frame | TestQA013StartingControlNeverAnnouncesCanceledAttempt: skip/stop/leave/replay | Nenhum anúncio/histórico da tentativa cancelada; sucessor/replay tem primeiro frame |
| Resolving | Job ativo; searches não nil; matcher bloqueado | TestQA013ProgressiveMutationSequences, TestShuffleWhileResolutionActiveKeepsSlotIdentity | Shuffle/move/remove/skip/jump/stop/clear/leave; slot original não muda, late result não ressuscita |
| Pending | Cabeça reservada, Pending=true; nenhuma elegibilidade antes de commit | Regressões 302 e sequências progressivas; EOF pendente/pronto em OBS-001 | Espera correta; resolução/commit acordam reprodução; sem skip automático |
| Transitioning | EOF retornou, Current=nil, cabeça pendente | TestQA013Production302ShuffleEOF, TestQA013Production302LostNotificationManualSkip | ready=6/pending=295; liberação correta e replay histórico do skip manual |
| Failed | Terminal FAILED; não há latch FAILED permanente | Modelo advanced op10, TestProgressiveWorkerPanicFailsPositionAndAdvances, testes stream/lifecycle | Falha não repete em loop; próxima elegível avança; histórico só após frame |
| Provider blocked | Latch legado explícito; falhas atuais drenam sem ativá-lo automaticamente | TestPauseAndProviderDoNotReportStall; testes provider/force/reliability existentes | Não classificar espera legítima como stall; resume ou stop libera latch; não inventar disponibilidade |
| Stopped | Stop limpou atual/fila/loop e incrementou geração, voz mantida | Todos pares que passam por stop; sequências stop→play→EOF | Ticket velho rejeitado, resolução cancelada, nova busca funciona |
| Recovering | ConfigureStore recuperou metadata, voz ainda ausente | Todos 22 com SQLite/reopen/summon e TestRestartDuring123TrackResolution | Não autoplay antes de attach; current recupera no começo, artwork e ledger preservados |

## Rejeições e falhas

TestQA013PermissionRejectionPreservesSubsequentPlayback cobre os 17 comandos
que exigem voz, em outra sala, e verifica EOF/skip subsequente. A suíte existente
TestQualityVoiceBoundary cobre ausência de voz e sala distinta para os mesmos
mutadores. Os cinco read-only (now/queue/lyrics/history/help) são permitidos sem voz.

TestQA013InvalidArgumentsContinuePlayback cobre skip=0, remove negativo, move
fora da fila, jump fora da fila, loop inválido, seek inválido, history=21 e force
sem URL suportada. Os testes existentes de queue/force/controls e fuzzing ampliam
ranges e parsing. Rejeições preservam estado observável, geração e durable state.

Não se conta o produto 22×11 como executado: alguns cruzamentos não são aplicáveis
ou estão apenas em testes de domínio. Ex.: histórico não depende de voice recovery;
FAILED é instantâneo, e não uma sessão em que todos comandos poderiam executar.
Read-only em todos esses instantes não tem cobertura cartesiana nova.
