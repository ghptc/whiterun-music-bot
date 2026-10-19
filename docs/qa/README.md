# Programa de qualidade v1.0

Auditoria iniciada em 2026-10-19. Evidências correspondem ao checkout local; nenhum banco operacional, token ou serviço externo é necessário.

## Inventário

Baseline: 296 funções Test, distribuídas em cmd/bot, media, player, discord e storage. O inventário máquina-legível contém arquivo, função e linha em evidence/test-inventory.json. Funções, subtestes, operações geradas e cenários não são medidas intercambiáveis. Subtestes pais não devem aumentar a contagem de cenários finais.

Comandos registrados (22): summon, play, now, pause, resume, previous, skip, queue, clear, stop, leave, remove, move, shuffle, jump, replay, loop, seek, autoplay, lyrics, history, help. play.query obrigatório; play.force opcional; skip.count opcional; queue.limit opcional. Seek responde indisponibilidade; autoplay salva preferência sem recomendar; lyrics não possui provedor licenciado.

Player: fila de até 1.000 posições, histórico de navegação, coleções pendentes, tickets de geração, loop off/track/queue, pausa por gate, estados PENDING/RESOLVING/READY/STARTING/PLAYING/FINISHED/FAILED/CANCELED. Uma goroutine serializa streams; mutex protege estado e workers resolvem coleções. Guilds independentes; cache de busca compartilhado deduplica misses com cancelamento por assinante.

Integrações: Discord REST/Gateway/Voice/DAVE, Spotify autenticado/público, YouTube via yt-dlp, FFmpeg/libopus e transporte Opus/Ogg. SQLite modernc real, três migrations embutidas, WAL, transações, settings, mappings de 24h, catálogo, artwork e eventos de reprodução. SearchCache de dez minutos e 256 entradas. UI inclui componentes V2, paginação, artwork e anúncios assíncronos.

Inspeção incluiu fontes Go, testes, migrations, fixtures, scripts, workflows, Dockerfile, Compose e documentação. .env e bases operacionais excluídos. A documentação antiga descreve estados históricos incompatíveis entre si; código e evidências atuais prevalecem. go.mod requer Go 1.27.0. FFmpeg disponível; yt-dlp real não instalado neste ambiente.

## Plano incremental

1. Baseline e matriz: inventariar funções, executar testes/cobertura, race e vet.
2. P0 fila: modelo independente com seeds fixas, limites, snapshot, metadados, mutações inválidas e 20 produtores concorrentes.
3. P0 identidade: matriz de versões, contradições, first confirmed e Force Play separado; cache com relógio explícito.
4. P0 durabilidade: SQLite real, rollback em cada estágio, restart, corrupção isolada e indisponibilidade.
5. Fronteira Discord: contrato de registro, validação de voz e falha de acknowledgement para cada comando.
6. Áudio: truncamento de Ogg em todas as fronteiras, segmentação, falha do consumidor e integração FFmpeg existente.
7. Mutação isolada, stress repetido, benchmarks e relatório de lacunas.
8. Homologação E2E externa e soak operacional antes da release.

Orçamento de cenários significativos (planejamento, não resultado): player/fila 150–220, identidade/cache 150–220, SQLite 60–100, Discord/UI 90–140, áudio/processos 80–140, integração/adversarial 40–80. Reutilizar os existentes e revisar redundâncias; não gerar combinações sem risco distinto.

## Execução

- Diário: go test -short ./... -count=1
- Completo local: go test ./... -count=1
- Race: go test -race ./... -count=1
- Estática: go vet ./...
- Cobertura: go test ./... -count=1 -covermode=atomic -coverprofile=/tmp/bard.cover
- Detalhe: go tool cover -func=/tmp/bard.cover
- Stress P0: go test -race ./internal/player ./internal/media ./internal/storage ./internal/discord -run Quality -count=20 -timeout=5m
- Mutação: python3 scripts/qa_mutations.py
- Performance: go test ./internal/player -run '^$' -bench Quality -benchmem -count=5

A suíte atual mistura testes rápidos e integração local; -short ainda não separa todos os testes FFmpeg existentes. Não alegar separação completa por tags. Testes novos com Quality no nome permitem seleção incremental. Workflows existentes executam teste, race, vet, cobertura e SQLite; metadata externo é manual e não equivale a E2E Discord.
