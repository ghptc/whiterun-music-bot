# Auditoria de lacunas (OBS-001)

Amostra: SQLite real, 687 linhas em 2026-10-20. Inventário de emissores e
cronologia em [playback-flow.md](playback-flow.md). Esta auditoria separa
cobertura existente de cobertura nova; não declara instrumentados os gaps abaixo.

| Fluxo / origem | Cobertura existente e momento | Lacuna / risco remanescente |
|---|---|---|
| Discord commands.go | command_received, ack start/HTTP connection/first byte/complete, resposta, duração total; interaction e guild | Nomes de comandos conhecidos, mas vínculo explícito command → mutação depende de ordem/guild; mutações não recebem interaction_id |
| Discord bot.go / announcements.go | Join, DAVE ready wait, anúncio/delivery e falhas; primeiro envio Voice em Timing | Reconexões, épocas DAVE, callbacks de dependências não têm correlação uniforme com tentativa |
| Scheduler player.go | selected/start/end/terminal/transition | Corrigidos: motivo/condição de espera, evento esperado, despertar, snapshot de stall; wakeup coalescido não determina uma única causa |
| Resolução collection.go | Reserva agora explícita; start/complete, prefixo, falhas, cancelamento global | Resultado pronto antes do commit não significa elegibilidade. Agora commit e descarte são explícitos. Não há ID durável de cada ocorrência de track fora de coleção |
| Media resolver.go, discovery.go, spotify*.go | lookup cache, discovery, candidate decision, rejection, verification, first_confirmed, seleção, falha de processo e duração | Cobertura varia por caminho; strings de candidatos são volumosas. Não ampliar para payload HTTP. Processos metadata não herdam attempt antes de haver playback |
| Stream stream.go / lifecycle.go | yt-dlp/FFmpeg start/end, PIDs, exit codes, cancel cause, primeiros bytes, Ogg/Opus/Voice, cleanup e stall de áudio | Ausência de primeiro frame não localiza sozinha a falha. Falha produtor pode gerar erro secundário decoder; priorizar categoria/causa do produtor. Sem confirmação de audibilidade |
| SQLite storage/sqlite.go, catalog.go | catalog_write, falhas de leitura/escrita, persistência de fila e histórico | SaveGuild é síncrono sob mutex por até 3 s; transações/recovery/cache operacional não têm span uniforme nem evento de sucesso para cada operação. Não alterar política de persistência neste patch |
| SQLite logstore | Async bounded admission, métricas de drops/falhas, retenção, fallback console | Não é log de auditoria sem perdas. Falha DB, overflow, crash e checkpoint bloqueado podem perder cópias. Console ainda síncrono em emissores legados |
| Recursos | Start/exit/cancel de subprocessos, testes de teardown e descritores | Stall agora inclui goroutines; contagem de FDs por processo e último progresso de cada worker não instrumentados. Não inferir OOM de signal:killed |

## Logs que induziam diagnóstico incorreto

- `player_transition` com ready>0 não garante cabeça elegível, especialmente após
  shuffle. next_pending=true explica a ordem, mas não explica resolver adormecido.
- `track_resolution_complete` era emitido antes da validação de identidade/URL.
  Agora outcome reflete validação; somente resolution_position_commit com
  playback_eligible=true confirma elegibilidade.
- `playback_attempt_terminal.state` era um tipo Go nomeado rejeitado pelo
  sanitizador (`[omitted: unsupported value]` na amostra). Emissor agora converte
  para string; não ampliar serialização arbitrária de structs no sanitizador.
- `request_elapsed_ms` mede idade da interação, não tempo isolado de verificação.
- `collection_prefix_committed.through_position` após shuffle representa último
  slot do lote, não um prefixo numérico da playlist original. Usar commits por slot.
- `first_opus_packet` e `first_opus_frame`, `stream_resolve_complete` e
  `stream_url_resolved`, `next_track_selected` e `track_playback_start` se
  sobrepõem. Mantidos para compatibilidade; não somar como tentativas distintas.
- Sem stdout Docker consultável, ausência de evento SQLite não prova ausência
  de execução. Métricas de persistência e diagnostic_dropped_total são essenciais.

## Prioridades seguintes

Propagar identidade durável da ocorrência em recovery/loop/replay e versionar
mutações de ponta a ponta; spans de persistência e conexão Voice; mover stdout
legado para política explicitamente assíncrona. Estas mudanças têm escopo e
semântica próprios. Não registrar centenas de faixas por shuffle para suprir a
falta de identidade; registrar apenas slots afetados e deltas necessários.
