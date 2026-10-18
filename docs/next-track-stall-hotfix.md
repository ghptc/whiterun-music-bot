# Hotfix: transição de faixas em playlists extensas

## Diagnóstico confirmado no código e em fixtures

O estado `ready=37 pending=50 halted=false next_pending=false` não demonstra
um deadlock. O loop também aguardava `providerBlocked`, campo ausente daquele
log. Uma falha de autenticação reinseria a faixa na cabeça e ativava esse bloqueio
sem recuperação automática; a liberação dependia de `/resume`. Esse caminho é
reproduzível com yt-dlp simulado e FFmpeg real, após o EOF da primeira música.
Também havia um halt após cinco falhas consecutivas de reprodução.

FFmpeg era iniciado antes de qualquer byte do produtor. Quando yt-dlp rejeitava
a extração, o decoder recebia EOF vazio e relatava entrada inválida/ausência de
Opus. Esses erros eram consequências da extração rejeitada, não evidência de
incompatibilidade musical. O cancelamento pelo parser podia ainda competir com
a coleta do erro do produtor.

A resolução de coleções tinha três workers, mas enfileirava toda a playlist.
Não havia janela de distância. O prazo global de dez minutos podia remover
posições ainda pendentes, mesmo durante reprodução saudável. Resultados já
prontos eram publicados por prefixo; não dependiam da conclusão da playlist.

`signal: killed` continua sem causa histórica comprovada: o código usa SIGKILL
para cancelar grupos de processos, e isso também pode resultar de encerramento
externo. `request_elapsed_ms=223449` mede a idade da requisição, não a duração
isolada daquela verificação. Não há evidência suficiente para atribuir esse
registro a OOM, deadlock ou timeout específico. Novos logs incluem causa e
estado do contexto, além da duração local da resolução.

## Comportamento corrigido

- Prefetch das primeiras seis posições restantes de cada coleção, incluindo
  posições prontas; até três workers por coleção. O limitador compartilhado de
  MatchSpotify mantém três resoluções de background e quatro totais.
- Cancelamento por skip/stop/leave/close e prazo de 90 segundos por posição.
  Uma playlist longa não expira por sua duração total. Workers respeitam contexto.
- Apenas o loop de playback faz transições; desmontagem termina antes da próxima
  faixa. Anúncios continuam condicionados ao primeiro frame enviado com sucesso.
- yt-dlp inicia primeiro. Sem bytes e com rejeição, retorna erro classificado e
  não inicia FFmpeg. Dados presentes mas inválidos continuam sendo rejeitados
  pelo decoder/parser, com limpeza dos dois processos.
- Falhas de posições são registradas como indisponíveis e a fila avança na ordem
  original, preservando números/identidades. Não existe retry automático de
  autenticação, nem halt global após cinco falhas. Cada posição tem uma tentativa
  de playback; uma indisponibilidade geral pode esgotar a fila, sem loop infinito.
  O operador pode reenfileirar após restaurar o acesso ao provedor.
- Erros de verificação de infraestrutura não são MatchError musical. Mapeamentos
  não são apagados por erro de infraestrutura na revalidação. Os critérios de
  confirmação musical e a verificação Spotify antes de streaming permanecem.
- Logs de falha/transição incluem IDs Spotify/YouTube, posição, estado, motivo,
  duração, atraso até o primeiro frame sucessor e contagem de resoluções ativas.
  Não são adicionados cookies, tokens ou credenciais; URLs de diagnósticos são
  redigidas.

Esta política substitui a retenção até `/resume` descrita nos documentos de
auditoria anteriores e os testes que codificavam essa retenção.

## Regressões locais

- 123 posições e startup com 122 pendentes: `TestProgressiveFirstStartsWith122Unresolved`.
- Primeira válida, segunda com autenticação, sucessoras com áudio real distinto:
  `TestProviderChallengeAdvancesWithoutRetryLoop` (yt-dlp fixture, FFmpeg real).
  Confirma seis decoders para sete extrações e ausência de áudio na rejeitada.
- Próxima pronta com verificações distantes bloqueadas:
  `TestNextReadyDoesNotWaitForDistantVerification`.
- Entrada inválida, EOF, stall, panic e grupos de processos:
  testes de stream/lifecycle, incluindo soak de 125 transições, PID e descritores.
- Timeout determinístico por posição: `TestCollectionPerPositionTimeoutAndCancellation`
  usa relógio virtual de testing/synctest.
- Cancelamento durante verificação, skip de 50 posições durante prefetch e
  concorrência com snapshots/controles: testes de collection.
- Todas indisponíveis: `Test123UnavailablePlaybackAttemptsTerminate` e
  `TestProgressiveFailureSemantics`; disponibilidade parcial: testes de collection
  e replay com FFmpeg real.
- Reinício durante resolução: `TestRestartDuring123TrackResolution`, com SQLite,
  123 identidades/posições e nenhum locator inventado para posições pendentes.
- Integridade: `TestBotPlaybackRejectsWrongRecordingBeforeStreaming` e testes de
  seleção, ambiguidade, identidade e falhas de verificação em media.

Validação: `go test ./...`, `go test -race ./...`, `go vet ./...`.
Fixtures não fazem chamadas ao YouTube, Spotify ou Discord. Nenhum teste real no
Discord foi realizado; E2E de produção e disponibilidade externa não estão validados.
