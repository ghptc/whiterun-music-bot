# Matriz de testes

Testes normais não exigem tokens Discord, Spotify ou YouTube. Exigem Go indicado
no go.mod, Linux (grupos de processos), FFmpeg com libopus e shell para executáveis
de falha controlados. SQLite é real, embutido em Go; não requer daemon, Docker ou
Testcontainers. Testcontainers acrescentaria infraestrutura sem testar um motor
diferente do utilizado pelo bot.

| Área | Evidência automatizada |
| --- | --- |
| Comandos reais e canal | discord/commands_test.go, quality_test.go: usuário fora do canal, outro canal, outro membro autorizado, play/playlist, pause/resume, skip, previous, now/queue, clear/stop/leave |
| Revalidação após busca | commands_test.go; ticket de sessão impede buscas antigas ressuscitarem playback |
| Isolamento e concorrência | player/player_test.go, pause_test.go, skip_test.go, collection_test.go, previous_test.go; guilds separadas, controles e reservas simultâneos |
| Ordem, capacidade e duplicatas | player/collection_test.go, media/source_intent_test.go, spotify_test.go, spotify_anonymous_test.go; 123 e até 1000 posições na fila; limite Spotify 500 |
| Previous | player/previous_test.go: sem histórico, predecessor real, faixa interrompida recolocada, pause, stop, cópia e concorrência |
| Transição e falha individual | player/player_test.go, lifecycle_test.go; EOF, skip final, panic, limite de falhas e retomada |
| Pause preserva pipeline | player/pause_test.go, stream_test.go: gate de frames e cancelamento; não testa seek remoto |
| Anúncios | discord/announcements_test.go, quality_test.go: um por primeiro frame, título/artista/solicitante, REST falhando e fila saturada sem bloquear áudio |
| Erros e resumo | discord/commands_test.go, quality_test.go; mensagens sem detalhes sensíveis, matched/uncertain/rejected/unavailable/skipped |
| Fidelidade adversarial | media/source_intent_test.go, fidelity_test.go: cover/live/remix/slowed/sped up/instrumental/karaoke/remaster, duração, Unicode/pontuação, convidados ausentes, artistas parecidos e diferentes, badges enganosos, ambiguidade, ausência, evidência incompleta, explicit e ISRC contraditórios |
| Paginação e rede | media/spotify_test.go, spotify_public_test.go, spotify_anonymous_test.go, spotify_embed_test.go; páginas múltiplas, duplicatas, indisponíveis, truncamento e falha tardia atômica |
| SQLite real | storage/sqlite_test.go: migrations idempotentes, JSON/CHECK/FK, transações, trigger de falha e rollback, settings, concorrência por guild, reabertura, recuperação sem voz, falha durável visível, histórico append-only e rejeição de schema futuro |
| Mapeamento real | storage/mapping_test.go: banco em arquivo + extrator controlado, hit revalidado sem busca, vídeo inconsistente invalidado e playback impedido |
| Processos e áudio local | player/stream_test.go, lifecycle_test.go, reliability_test.go: FFmpeg real, Opus, frames distintos, EOF, deadline, subprocessos falhando, cancelamento, helpers órfãos e 125 transições |
| Guard de identidade em runtime | discord/playback_integrity_test.go cruza Bot.play, extrator local, FFmpeg/libopus real e saída de frames: versão errada produz zero frames; versão compatível entrega áudio local; settings e fila são recuperados por guild |
| Discord Voice / DAVE | discord/readiness_test.go, idle_test.go: readiness simulada, timeout, cancelamento, desconexão e retomada transitória; sem handshake remoto |
| Race detector | suíte completa incluindo SQLite, snapshots, controles concorrentes e workers |

## Execução reproduzível

```sh
go vet ./...
go test ./... -count=1 -timeout=3m
go test -race ./... -count=1 -timeout=3m
go test ./... -covermode=atomic -coverprofile=coverage.out -timeout=3m
go tool cover -func=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

A CI Quality executa esses gates com FFmpeg instalado e valida o encoder Opus.
Um job próprio executa integração SQLite com race detector. Cobertura e HTML são
artefatos da CI; o resultado remoto só existe após executar o workflow no GitHub.
Não há threshold artificial para mascarar caminhos sem cobertura.

## Serviços externos e E2E

O workflow `Controlled external metadata probe` é manual e usa environment
`external-tests` para execução controlada. Ativa os probes públicos Spotify já
existentes com `BARD_SPOTIFY_LIVE_TEST=1`. `BARD_LIVE_YTDLP` habilita o probe de
pesquisa/timing existente. Esses probes fazem rede e não equivalem a reprodução.
Nenhum deles foi usado como prova de áudio nesta entrega.

E2E real Discord + Spotify + YouTube permanece dependente de credenciais de teste,
guild/canal autorizados, permissões de voz, disponibilidade externa e um receptor
que confirme áudio recebido. O repositório não possui um teste remoto que comprove
essa cadeia. Não há alegação de áudio funcional no Discord real. Não habilitar
probes externos nos testes normais nem usar tokens de produção como fixtures.

Resultados locais finais estão em quality-audit.md. Documentos históricos de
setembro preservam resultados e limitações daquela revisão, não substituem esta
matriz nem os resultados de outubro.
