# Inventário auditável

O inventário completo de fontes Go e testes com SHA-256 está em evidence/source-inventory.json; cada função Test com arquivo e linha está em evidence/test-inventory.json. São 114 arquivos Go e 319 funções Test, incluindo um soak sob build tag. A execução padrão registra 916 casos finais aprovados; não somar pais e filhos.

| Área | Fontes / responsabilidade | Famílias de testes |
|---|---|---|
| cmd/bot | startup, configuração de provedores, diagnóstico | spotify_diagnose; bootstrap externo ainda descoberto |
| cmd/profile | medição de reprodução | sem teste direto; benchmark separado |
| player | lifecycle, pause, queue, controls, previous, collection, durable, stream, ogg | 20 arquivos, modelo, race, teardown, recuperação, FFmpeg e processos |
| media | resolver, ranking, fidelidade, identidade, progressivo, Spotify API/anonymous/embed/public, cache, mapping, catalog, artwork | fixtures de JSON e shell, first confirmed, Force Play, versões, cache/cancelamento |
| storage | sqlite, catalog e migrations 001/002/003 | SQLite real, mappings, controles, catálogo, schema existente, rollback por estágio, restart e indisponibilidade |
| discord | bot, commands, command_sync, expansion, settings, queue, playback_embed, announcements, idle | registro, permissões, ack, UI V2, limites, artwork, imports, guilds, readiness e contratos HTTP já existentes |
| scripts | capture-bard-audit, measure-matcher, check_queue_mutations, check_ui_mutations, qa_mutations | mutações novas isoladas com baseline e classificação de resultados |
| deploy | Dockerfile, compose.yaml e workflows quality/external-metadata | FFmpeg/libopus validado no CI; execução externa manual |
| docs | auditorias anteriores, requisitos, métricas de latência e regressões | fontes históricas, não evidência atual automática |

## Riscos por mecanismo

- Estado compartilhado: mutex por player/guild, workers de coleção, goroutine de áudio, cache com singleflight; gravação SQLite serializada por conexão.
- Persistência: WAL, migrations transacionais, estado por guild em JSON, histórico de navegação e eventos append-only, settings, catálogo e mappings; sem consenso distribuído.
- Entrada externa: slash options, URLs de provedores, metadata incompleta/contraditória, bytes Ogg/Opus, stdout/stderr e exit de processos.
- UI: registro de comandos, Now Playing, fila completa/paginada, componentes V2, Help, artwork e follow-ups.
- Limites: 1.000 posições futuras, skip inclui atual; 500 faixas por coleção de provedor; cache limitado a 256 entradas, TTL de dez minutos, mapping de 24 horas.
- Histórico de incidentes: stalls, falso match/falsa rejeição, cache de falha, Force Play, primeiro frame, anúncio e fila/artwork; rastreabilidade em risk-traceability.md.

Credenciais, .env e bancos operacionais não foram lidos. Nenhuma homologação de voz real foi inferida de doubles ou metadados externos.
