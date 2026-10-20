# Fluxo de playback e contrato de eventos (OBS-001)

## Evidência e reconstrução

Consulta realizada em 2026-10-20: 687 registros retidos no SQLite do volume
`whiterun-music-bot_bard-data`. O volume foi montado somente para leitura, sem
rede; uma cópia temporária foi consultada porque SQLite WAL exige diretório
para arquivos auxiliares. Nenhuma alteração no volume ou reinício do bot.
O Compose não tinha serviço ativo. `docker logs` no contêiner inspecionado
retornou `configured logging driver does not support reading`. Assim, não há
corroboração por stdout Docker disponível nesta execução. `bard-audit.jsonl`
é captura anterior (2026-09-28), não evidência deste incidente.

| ID SQLite | UTC | Evidência |
|---|---|---|
| 423 | 13:11:13.665 | Prefixo original até posição 7 confirmado; primeira em playback, seis prontas |
| 439 | 13:12:09.347 | Comando shuffle recebido |
| 465–467 | 13:17:27.549 | EOF normal sequência 1; ready=6, pending=295, next_pending=true, provider_blocked=false, halted=false |
| 477 | 13:18:47.347 | Comando skip recebido |
| 482–486 | 13:18:47.660 | Resoluções reiniciam nas posições originais 144, 295, 14 |
| 544 | 13:18:50.730 | Sequência 2 seleciona posição original 144 |
| 636–639 | 13:22:14.293–306 | EOF sequência 2; sucessora pronta avança em ~13 ms |
| 678–687 | 13:23:11 | Cancelamento por encerramento, 287 posições não terminadas |

Após EOF houve ~79,8 segundos até o comando skip e ~83,2 segundos até seleção.
Não há falha FFmpeg, Opus ou Voice nesse EOF. A evidência combina com o caminho:
`CollectionJob.run` aguarda `j.wake` quando a janela contém somente prontas;
`Shuffle` muda a janela e acorda apenas `p.wake`; scheduler encontra cabeça
pendente e aguarda commit que não será iniciado. Skip descarta uma posição e
`discardEntryLocked` acorda o coordenador. A regressão com Move reproduz a
mesma perda de notificação sem depender da aleatoriedade do shuffle.
A ordem completa pós-shuffle não pode ser recuperada dos logs históricos.

## Correlação

`guild_id` + `playback_attempt_id` identifica a tentativa durante a sessão.
O attempt é sessão local + sequence, sem depender de interaction_id. IDs não são
persistidos como identidade histórica da fila após reinício. `collection_id` +
`position` identifica o slot original durante a sessão; Spotify/YouTube identificam
a gravação. `queue_position` é índice atual, um-based, no instante do evento.
`queue_generation` invalida tickets em attach/clear/stop/leave; não deve mudar
em shuffle. `queue_version` é revisão diagnóstica das mutações/commits, não token
de cancelamento. Não é uma versão transacional persistida nem contador de toda
mudança interna (dequeue/loop/recovery).

O Timing filho propaga attempt, coleção, geração, versão e sequence para
stream → yt-dlp → FFmpeg → primeiro Ogg/Opus → primeira submissão Voice.
Os eventos de fim/terminal/transição recebem os mesmos campos. Uma submissão
UDP bem sucedida não prova áudio audível no cliente Discord.

## Novos eventos e semântica

| Evento | Quando / campos específicos |
|---|---|
| scheduler_wait | Entrada ou mudança de condição; wait_reason, expected_event, elapsed_wait_ms, contagens, active_resolutions, estado da cabeça, provider_blocked, halted |
| scheduler_wakeup | Notificação consumida ou cancelamento; wakeup_reason, elapsed_wait_ms |
| scheduler_notification | DEBUG; envio/coalescência, wakeup_reason |
| scheduler_track_ready | Saída da espera com cabeça pronta; estado consistente sob mutex |
| scheduler_advance | Decisão de selecionar; collection_id, position, queue_position, IDs de gravação |
| scheduler_stall_detected | WARN após 30 s na condição pendente/resolvendo; mesmo snapshot, goroutines; uma vez por condição |
| queue_mutation | Ação, tamanho, versão, IDs/posições apenas quando aplicável; sem snapshots |
| queue_generation_rejected | Ticket recusado, requested_generation e geração atual; reason inclui stale_or_canceled |
| shuffle_started / shuffle_completed | Limites da mutação atômica e tamanho |
| playback_cancel_requested | Motivo explícito de cancelamento |
| playback_pause_changed | paused, expected_event=resume_or_cancel |
| resolver_window_wait / resolver_window_wakeup | Janela pronta sem trabalho elegível, próximo evento esperado / despertar |
| resolution_position_commit | Slot atualizado/removido, playback_eligible, scheduler_notified, posição original/atual e IDs |
| resolution_commit_discarded | Resultado cancelado ou posição invalidada; nunca ressuscita slot removido |
| track_resolution_complete | Agora após validação de identidade/locator, outcome=ready/discarded/categoria; não equivale a commit |

Campos comuns dos novos eventos: guild (quando configurada), sequence,
queue_generation, queue_version e diagnostic_dropped_total. O acumulador de
drops é por player e não persiste após reinício. Wakeups são coalescidos: o
motivo é a última notificação, não prova de causalidade exclusiva. O snapshot e
o expected_event explicam a condição efetiva. Não se fabricam failed_count ou
current_position quando não há contador/track atual confiável.

## Volume, segurança e latência

Uma fila adicional de 256 records por player, com um writer por player, mantém
novos diagnósticos fora do mutex/I/O de playback. Admissão não bloqueante e
contador de drops; sem retries ou backlog ilimitado. Campos são escalares e
não contêm payloads privados, títulos, URLs, headers ou credenciais. Redaction,
fila SQLite 512, retenção física 500 MiB e política de falha permanecem no
logstore. Um destino bloqueado pode ocupar o writer; Close espera no máximo
um segundo e não garante flush completo. O logger legado ainda tem stdout
síncrono; não há garantia retroativa de não bloqueio para todos os eventos.

Volume adicional é proporcional a slots/commits/controles/transições: poucos
eventos por resolução, não por frame nem por segundo. Uma playlist de 302 slots
adiciona da ordem de milhares de eventos pequenos, e não snapshots quadráticos.
A taxa de catálogo existente foi dominante nesta amostra (302/687 registros).
Não há heartbeat novo. Limites de volume reais dependem de atividade e drops;
medir `log_persistence_metrics` antes de reduzir retenção.

## Inventário estático dos emissores

Tabela gerada da AST dos arquivos Go de produção em `internal/`. Inclui chamadas
com nomes literais de slog, Timing e helpers de diagnóstico. `Campos literais`
inclui valores literais além de chaves; atributos herdados por With/Timing e
campos dinâmicos estão descritos acima ou no código. Mensagens de dependências
Discord/DAVE e nomes dinâmicos não são enumeráveis só pelos emissores locais.
O ponto de código determina se há caminho de sucesso, erro ou ambos; ausência
de error não implica sucesso de playback. Auditoria dos riscos em logging-gaps.md.

| Evento / mensagem | Emissor | Literais de campos |
|---|---|---|
| playback_announcement_delivery | `internal/discord/announcements.go:47` | `guild_id`, `sequence`, `first_frame_to_announcement_ms`, `artwork_present`, `spotify_id`, `video_id`, `position`, `failure_stage`, `reason`, `artwork_metadata_invalid`, `success` |
| playback_announcement_failed | `internal/discord/announcements.go:49` | `guild_id`, `channel_id`, `sequence`, `error` |
| playback_announcement_dropped | `internal/discord/announcements.go:79` | `sequence`, `reason`, `announcement_queue_full` |
| The Bard is connected | `internal/discord/bot.go:157` | `application_id`, `command_guild` |
| durable_state_load_failed | `internal/discord/bot.go:171` | `guild_id`, `error` |
| guild_settings_load_failed | `internal/discord/bot.go:177` | `guild_id`, `error` |
| dave_ready_wait_start | `internal/discord/bot.go:337` |  |
| dave_ready_wait_complete | `internal/discord/bot.go:341` | `error` |
| command_sync_failed | `internal/discord/command_sync.go:19` | `guild_id`, `elapsed_ms`, `error` |
| command_sync_complete | `internal/discord/command_sync.go:73` | `guild_id`, `count`, `elapsed_ms` |
| command_received | `internal/discord/commands.go:48` | `interaction_id`, `name`, `command_id`, `guild_id` |
| play_request_start | `internal/discord/commands.go:53` | `query` |
| play_request_total | `internal/discord/commands.go:54` |  |
| command_ack_http_connection | `internal/discord/commands.go:62` | `interaction_id`, `elapsed_us`, `reused` |
| command_ack_http_first_byte | `internal/discord/commands.go:65` | `interaction_id`, `elapsed_us` |
| command_ack_start | `internal/discord/commands.go:68` | `interaction_id`, `elapsed_us` |
| command_total | `internal/discord/commands.go:70` | `interaction_id`, `elapsed_us` |
| command_ack_complete | `internal/discord/commands.go:79` | `interaction_id`, `elapsed_us`, `error` |
| discord_acknowledged | `internal/discord/commands.go:80` | `error` |
| command acknowledgement failed | `internal/discord/commands.go:82` | `interaction_id`, `guild_id`, `elapsed_us`, `error` |
| discord_response_complete | `internal/discord/commands.go:90` | `error` |
| command response failed | `internal/discord/commands.go:92` | `error` |
| queue_snapshot | `internal/discord/commands.go:141` | `interaction_id`, `elapsed_us`, `entries` |
| voice_join_complete | `internal/discord/commands.go:165` | `error` |
| voice join failed | `internal/discord/commands.go:172` | `guild_id`, `error` |
| media resolution failed | `internal/discord/commands.go:243` | `guild_id`, `stage`, `error` |
| queue_inserted | `internal/discord/commands.go:287` | `error`, `pending` |
| help response failed | `internal/discord/expansion.go:60` | `error` |
| empty voice channel; cleared playback and disconnected | `internal/discord/idle.go:59` | `guild_id`, `stage`, `voice_idle` |
| voice_recovered | `internal/discord/idle.go:107` | `guild_id`, `elapsed_ms` |
| voice_unhealthy | `internal/discord/idle.go:119` | `guild_id`, `main_ready`, `voice_ready`, `cache_known`, `same_channel`, `reason` |
| voice_disconnected | `internal/discord/idle.go:127` | `guild_id`, `reason`, `main_ready`, `voice_ready`, `cache_known`, `action`, `cleared playback` |
| now_delivery_failed | `internal/discord/playback_embed.go:62` | `error` |
| queue_plan | `internal/discord/queue.go:319` | `interaction_id`, `elapsed_us`, `components`, `messages`, `shown`, `total`, `artwork_missing` |
| queue_response | `internal/discord/queue.go:340` | `interaction_id`, `elapsed_us`, `error` |
| queue_delivery_failed | `internal/discord/queue.go:346` | `error` |
| queue_delivery_notice_failed | `internal/discord/queue.go:351` | `error` |
| cleaning up bucket | `internal/discord/rest_limiter.go:74` |  |
| cleaned up rate limit buckets | `internal/discord/rest_limiter.go:80` |  |
| locking buckets | `internal/discord/rest_limiter.go:132` |  |
| unlocking buckets | `internal/discord/rest_limiter.go:135` |  |
| locking rest bucket | `internal/discord/rest_limiter.go:172` |  |
| unlocking rest bucket | `internal/discord/rest_limiter.go:208` |  |
| ratelimit response headers | `internal/discord/rest_limiter.go:233` |  |
| global rate limit exceeded | `internal/discord/rest_limiter.go:246` |  |
| cloudflare rate limit exceeded | `internal/discord/rest_limiter.go:251` |  |
| rate limit exceeded | `internal/discord/rest_limiter.go:255` |  |
| guild_settings_write_failed | `internal/discord/settings.go:40` | `guild_id`, `error` |
| log_persistence_metrics | `internal/logstore/store.go:424` | `failure_category`, `bytes_used`, `records`, `written`, `removed`, `failures`, `overflow`, `oversized`, `lost`, `batches`, `flush_ms`, `cleanup_ms`, `writes_per_second` |
| catalog_artwork_read_failed | `internal/media/artwork.go:70` | `error` |
| resolver_coalesced | `internal/media/cache.go:141` |  |
| catalog_write | `internal/media/catalog.go:27` | `success` |
| catalog_write_failed | `internal/media/catalog.go:29` | `error` |
| Spotify track match failed | `internal/media/collection.go:172` | `stage`, `spotify_match`, `title`, `artist`, `error` |
| partial YouTube playlist extraction | `internal/media/collection.go:235` | `error`, `details` |
| discovery_ranking_complete | `internal/media/discovery.go:97` | `candidates`, `eligible`, `uncertain`, `rejected` |
| discovery_complete | `internal/media/discovery.go:99` | `full_verifications` |
| ranking_complete | `internal/media/discovery.go:133` | `selected`, `score`, `needs_verification` |
| candidate_verification_failed | `internal/media/discovery.go:148` | `video_id`, `error` |
| music_filtering_complete | `internal/media/discovery.go:152` | `video_id`, `accepted` |
| spotify_playback_identity_reused | `internal/media/mapping.go:56` | `spotify_track_id`, `video_id` |
| spotify_mapping_invalidated | `internal/media/mapping.go:93` | `success` |
| collection_metadata_complete | `internal/media/progressive.go:65` | `spotify_tracks`, `metadata_skipped` |
| catalog_lookup | `internal/media/progressive.go:75` | `hit`, `success`, `external_metadata_calls_avoided` |
| catalog_read_failed | `internal/media/progressive.go:77` | `error` |
| resolver_start | `internal/media/resolver.go:83` |  |
| resolver_complete | `internal/media/resolver.go:84` |  |
| resolver_cache_hit | `internal/media/resolver.go:105` | `selected` |
| yt_dlp_start | `internal/media/resolver.go:136` | `purpose`, `category`, `metadata`, `flat`, `candidates`, `target`, `started_at` |
| yt_dlp_complete | `internal/media/resolver.go:138` | `purpose`, `flat`, `candidates`, `error`, `cancel_reason`, `context_error` |
| candidate_parsing_complete | `internal/media/resolver.go:163` | `entries`, `purpose` |
| candidate_selected | `internal/media/resolver.go:258` | `selected`, `video_id` |
| spotify_resolution | `internal/media/spotify_discovery.go:27` | `spotify_track_id`, `search_attempts`, `verification_attempts`, `reason`, `selected_video_id` |
| spotify_mapping_lookup | `internal/media/spotify_rank.go:310` | `hit`, `success` |
| mapping_read_failed | `internal/media/spotify_rank.go:312` |  |
| spotify_mapping_cache_hit | `internal/media/spotify_rank.go:316` | `external_searches_avoided`, `external_verifications_avoided` |
| mapping_write_failed | `internal/media/spotify_rank.go:325` |  |
| mapping_invalidation_failed | `internal/media/spotify_rank.go:333` |  |
| mapping_write_failed | `internal/media/spotify_rank.go:366` |  |
| spotify_candidate_discovery | `internal/media/spotify_rank.go:394` | `spotify_track_id`, `video_id`, `reason` |
| spotify_candidate_verification_failed | `internal/media/spotify_rank.go:404` | `spotify_track_id`, `video_id` |
| spotify_candidate_decision | `internal/media/spotify_rank.go:412` | `spotify_track_id`, `video_id`, `status`, `reason`, `ranking_score`, `candidate_title`, `recording_title`, `recording_artists`, `recording_album`, `duration_seconds`, `recording_isrc`, `channel_id` |
| spotify_first_confirmed | `internal/media/spotify_rank.go:415` | `spotify_track_id`, `video_id`, `evidence` |
| first_discord_send | `internal/media/timing.go:42` |  |
| queue_generation_rejected | `internal/player/collection.go:73` | `requested_generation`, `reason`, `stale_or_canceled` |
| reserve | `internal/player/collection.go:101` | `collection_id`, `reserved_count` |
| track_resolution_start | `internal/player/collection.go:173` | `spotify_id`, `position`, `state`, `resolving`, `concurrent_resolutions` |
| track_resolution_discarded | `internal/player/collection.go:187` | `position`, `reason`, `provider_blocked` |
| track_resolution_complete | `internal/player/collection.go:203` | `position`, `spotify_id`, `video_id`, `outcome`, `cancel_reason` |
| resolution_commit_discarded | `internal/player/collection.go:242` | `collection_id`, `reason`, `canceled`, `cancel_reason` |
| resolution_position_commit | `internal/player/collection.go:260` | `collection_id`, `position`, `queue_position`, `spotify_id`, `video_id`, `playback_eligible`, `scheduler_notified` |
| resolution_commit_discarded | `internal/player/collection.go:264` | `collection_id`, `position`, `reason`, `position_invalidated` |
| collection position could not be matched; skipping | `internal/player/collection.go:297` | `stage`, `collection_position_failed`, `collection_id`, `position`, `spotify_id`, `state`, `unavailable`, `reason`, `error` |
| background_collection_resolution_complete | `internal/player/collection.go:328` | `spotify_tracks`, `youtube_resolved`, `youtube_failed`, `skipped_positions`, `unfinished`, `canceled` |
| first_track_resolution_start | `internal/player/collection.go:338` | `position` |
| first_track_resolution_complete | `internal/player/collection.go:340` | `position`, `success` |
| first_track_committed | `internal/player/collection.go:361` | `position` |
| background_collection_resolution_start | `internal/player/collection.go:368` | `remaining`, `workers` |
| resolver_window_wait | `internal/player/collection.go:391` | `reason`, `prefetch_window_ready`, `expected_event`, `queue_window_changed`, `pending_count` |
| resolver_window_wakeup | `internal/player/collection.go:396` | `wakeup_reason`, `queue_window_changed` |
| collection_prefix_committed | `internal/player/collection.go:476` | `through_position` |
| remove | `internal/player/controls.go:23` | `collection_id`, `position`, `queue_position` |
| move | `internal/player/controls.go:43` | `collection_id`, `position`, `from_queue_position`, `to_queue_position` |
| shuffle_started | `internal/player/controls.go:54` | `queue_size` |
| shuffle | `internal/player/controls.go:56` |  |
| shuffle_completed | `internal/player/controls.go:57` | `queue_size` |
| jump | `internal/player/controls.go:68` | `queue_position` |
| durable_state_write_failed | `internal/player/durable.go:87` | `guild_id`, `error` |
| playback_history_write_failed | `internal/player/lifecycle.go:163` | `guild_id`, `error` |
| next_track_start | `internal/player/lifecycle.go:168` | `state`, `playing`, `next_track_delay_ms`, `spotify_id`, `sequence`, `video_id`, `position` |
| queue_mutation | `internal/player/observability.go:68` |  |
| scheduler_track_ready | `internal/player/observability.go:127` |  |
| scheduler_wait | `internal/player/observability.go:134` |  |
| scheduler_wakeup | `internal/player/observability.go:141` | `wakeup_reason`, `canceled`, `elapsed_wait_ms` |
| scheduler_wakeup | `internal/player/observability.go:146` | `wakeup_reason`, `elapsed_wait_ms` |
| scheduler_stall_detected | `internal/player/observability.go:152` |  |
| provider_playback_recovery_requested | `internal/player/pause.go:76` | `provider`, `youtube`, `previous_category`, `action`, `retry same logical position` |
| playback_pause_changed | `internal/player/pause.go:91` | `paused`, `expected_event`, `resume_or_cancel` |
| scheduler_notification | `internal/player/player.go:81` | `wakeup_reason` |
| generation_changed | `internal/player/player.go:98` |  |
| queue_generation_rejected | `internal/player/player.go:113` | `requested_generation`, `reason`, `stale_or_canceled` |
| enqueue | `internal/player/player.go:127` | `added_count` |
| generation_changed | `internal/player/player.go:144` |  |
| stop | `internal/player/player.go:146` |  |
| generation_changed | `internal/player/player.go:163` |  |
| leave | `internal/player/player.go:165` |  |
| player_loop_exit | `internal/player/player.go:196` | `cause` |
| scheduler_advance | `internal/player/player.go:219` | `collection_id`, `position`, `queue_position`, `spotify_id`, `video_id` |
| next_track_selected | `internal/player/player.go:239` | `sequence`, `collection_id`, `position`, `spotify_id`, `title`, `video_id` |
| track_playback_start | `internal/player/player.go:240` | `sequence`, `position`, `video_id` |
| track_playback_end | `internal/player/player.go:253` | `sequence`, `position`, `title`, `video_id`, `elapsed_ms`, `category`, `cancel_reason`, `error` |
| playback_attempt_terminal | `internal/player/player.go:265` | `sequence`, `spotify_id`, `position`, `state`, `started` |
| track_unavailable | `internal/player/player.go:287` | `sequence`, `spotify_id`, `video_id`, `position`, `state`, `unavailable`, `reason`, `consecutive_failures`, `action`, `advance_in_original_order` |
| player_transition | `internal/player/player.go:292` | `sequence`, `ready`, `pending`, `halted`, `provider_blocked`, `state`, `transitioning`, `next_pending` |
| provider_playback_blocked | `internal/player/player.go:314` | `provider`, `youtube`, `category`, `action`, `queue retained; use resume to retry the same position` |
| clear | `internal/player/queue.go:81` | `invalidated_count` |
| skip | `internal/player/queue.go:134` | `skipped_count` |
| playback_cancel_requested | `internal/player/queue.go:145` | `cancel_reason` |
| playback_start | `internal/player/stream.go:28` |  |
| playback_complete | `internal/player/stream.go:29` | `error` |
| playback_context_complete | `internal/player/stream.go:34` | `cause`, `parent_error` |
| yt_dlp_first_byte | `internal/player/stream.go:80` |  |
| ffmpeg_first_input | `internal/player/stream.go:82` |  |
| stream_resolve_start | `internal/player/stream.go:93` |  |
| yt_dlp_start | `internal/player/stream.go:94` | `purpose`, `playback`, `category`, `stream`, `started_at`, `candidates`, `video_id` |
| yt_dlp_complete | `internal/player/stream.go:103` | `purpose`, `playback`, `candidates`, `error`, `cancel_reason` |
| audio_source_exit | `internal/player/stream.go:139` | `video_id`, `spotify_id`, `yt_exit_code`, `decoder_started`, `category` |
| ffmpeg_start | `internal/player/stream.go:160` | `purpose`, `audio_decode`, `category`, `ffmpeg`, `started_at`, `candidates` |
| audio_source_start | `internal/player/stream.go:182` | `video_id`, `live`, `yt_pid`, `ffmpeg_pid` |
| stream_process_started | `internal/player/stream.go:183` | `pid` |
| ffmpeg_first_output | `internal/player/stream.go:208` |  |
| first_opus_packet | `internal/player/stream.go:210` |  |
| first_opus_frame | `internal/player/stream.go:211` |  |
| first_voice_frame_attempt | `internal/player/stream.go:242` |  |
| first_audio_frame | `internal/player/stream.go:249` |  |
| first_ogg_packet | `internal/player/stream.go:257` |  |
| ffmpeg_complete | `internal/player/stream.go:283` | `exit_code`, `error` |
| stream_resolve_incomplete | `internal/player/stream.go:298` | `error` |
| audio_source_exit | `internal/player/stream.go:301` | `video_id`, `yt_exit_code`, `ffmpeg_exit_code`, `category`, `canceled`, `cancel_reason` |
| audio_source_exit | `internal/player/stream.go:329` | `video_id`, `yt_exit_code`, `ffmpeg_exit_code`, `category`, `yt_stderr_tail`, `ffmpeg_stderr_tail` |
| stream_resolve_complete | `internal/player/stream.go:368` |  |
| stream_url_resolved | `internal/player/stream.go:370` |  |
