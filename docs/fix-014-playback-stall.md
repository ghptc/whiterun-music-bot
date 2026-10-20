# FIX-014 — progresso após EOF com cabeça pendente

## Causa confirmada e evidência

A captura SQLite de OBS-001 foi lida novamente: shuffle no registro 439,
EOF normal no 465, transição no 467 com ready=6, pending=295,
next_pending=true, provider_blocked=false e halted=false. Não começaram novas
resoluções até o skip do registro 477; resoluções reiniciaram no 482 e a próxima
seleção ocorreu no 544. Esses dados não indicam falha de FFmpeg ou Voice no EOF.

No caminho histórico, o coordenador havia preenchido a janela de prefetch e
aguardava `CollectionJob.wake`. Shuffle expunha uma cabeça pendente, mas
notificava somente o scheduler. Este esperava um commit que não seria iniciado.
Skip acordava o coordenador ao descartar a posição. Código e regressões
controladas confirmam esse mecanismo; não se inferiu lost wakeup só pelas
contagens de prontas e pendentes.

O commit anterior `941a876` já corrigiu o broadcast nas mutações. Restava uma
lacuna independente: o fim da reprodução reavaliava o scheduler, mas não
acordava explicitamente os coordenadores. A fixture de notificação perdida,
agora exigindo avanço sem skip, falhou antes de FIX-014 com `current=nil`
mesmo depois de liberar a resolução.

## Correção mínima

Em `internal/player/player.go`, após limpar a tentativa e registrar
`player_transition`, uma cabeça pendente provoca `p.signal("playback_end")`.
O mecanismo existente notifica scheduler e coordenadores sob o mesmo mutex,
com canais limitados e envio não bloqueante. EOF passa a ser um ponto de
reavaliação independente da notificação anterior de shuffle.

A cabeça continua sendo a próxima posição obrigatória: faixas prontas mais
adiante não a ultrapassam. O commit de resolução mantém seu sinal
`resolution_commit`. Não há retry de tentativa, skip automático, mudança de
prefetch, Fidelity ou Force Play. Cabeças prontas e filas vazias não recebem
essa notificação adicional.

A instrumentação existente registra transição, wait_reason, próxima posição,
resolução concluída/commit, wakeup e seleção. O motivo adicional usa o campo
já existente `wakeup_reason=playback_end`, sujeito à coalescência de notificações;
nenhum framework ou heartbeat foi criado.

## Regressões e validação

- `TestEOFReadyAndPendingDiagnostics`: EOF com sucessora pronta ou pendente.
- `TestShuffleWhileResolutionActiveKeepsSlotIdentity`: shuffle durante resolução.
- `TestQA013Production302ShuffleEOF`: 302 músicas, assinatura de produção,
  resolução liberada depois do EOF e reprodução automática da posição 302.
- `TestQA013Production302LostNotificationAdvancesAfterEOF`: substitui o teste
  histórico de skip manual; recupera a notificação perdida usando apenas EOF.
- `TestFIX014StopDuringEOFWaitDoesNotResurrect`: stop durante espera; liberar
  matcher não recria fila nem reprodução, verificado antes de outro stop.

Em 2026-10-20 passaram `go test ./... -count=1`,
`go test -race ./... -count=1`, `go vet ./...` e `docker compose build`.
Imagem local construída: `119560993d11`. Não foram executadas campanhas extras
de fuzzing, mutação ou soak. Não houve deploy nem E2E Discord real.

Resoluções genuinamente pendentes ainda aguardam o provedor ou seu timeout
existente; o log explica essa condição. A validação garante progresso local
após o commit elegível, não disponibilidade de provedores nem audibilidade remota.
