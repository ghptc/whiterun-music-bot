# Homologação Discord real — não executada

**Todos os itens abaixo estão PENDENTES.** QA-011 executou uma hora local, sem Discord; isso não aprova V13 nem qualquer outro item externo. Reprodução FFmpeg local, SDK contra localhost, mocks e readiness unitário não aprovam Discord Voice/DAVE.

## Preparação e autorização

- [ ] Registrar autorização explícita do responsável pelo bot, guild, canais de texto/voz e participantes; delimitar janela e quem pode publicar mensagens.
- [ ] Usar aplicação/guild/volume SQLite de QA separados, imagem/commit exatos da release e contas humanas autorizadas. Nunca reaproveitar o volume de produção.
- [ ] Carregar tokens por secret manager/ambiente protegido. Desligar dumps de Authorization, tokens de interação, URLs assinadas e credenciais. Redigir logs antes de exportar.
- [ ] Registrar versões de Go, DisGo, DAVE, yt-dlp, FFmpeg/libopus, SO, imagem, cenário/seed e horários UTC. Guardar tabela de IDs anonimizados fora de logs públicos.
- [ ] Confirmar coleta de recursos, ownership dos subprocessos e procedimento de encerramento. Registrar a duração real ininterrupta de cada sessão.

## Fluxo e evidência necessária

| ID | Verificação | Evidência mínima para aprovação |
|---|---|---|
| V01 | registro de slash commands e opções, Force Play opcional | contrato observado na guild e comandos aceitos pelo cliente |
| V02 | summon/entrada no canal e áudio audível | dois clientes humanos confirmam áudio; logs de primeiro envio e IDs da tentativa |
| V03 | negociação Voice/DAVE, readiness, desconexão e reconexão | handshake real e áudio recebido após recuperação; nenhum stream concorrente |
| V04 | pause/resume, skip count, previous, replay, loop e EOF | áudio e ledger de resultados; ordem/histórico esperados; seek retorna indisponibilidade sem mudar reprodução |
| V05 | 20 usuários, duas guilds e mesma música | sequência/IDs, fila isolada, latências, rejeições explicadas e ausência de duplicações indevidas |
| V06 | usuário sem voz, outro canal, Connect/Speak negados | rejeição sem alterar fila/áudio; permissões reais no servidor |
| V07 | playlists 123 e 500, importação pendente, shuffle/removal/jump | todas as posições explicadas: concluída, skip, indisponível, erro ou pendente; metadados e ordem rastreáveis |
| V08 | Force Play com vídeo específico seguido por Spotify | Video ID exato e ausência de contaminação da fidelidade; original/remix/live corretos |
| V09 | anúncios, Now Playing, /now, Queue, /help e artwork | títulos The Bard congelados, anúncio somente após áudio efetivo, limites de componentes/followups respeitados |
| V10 | interações expiradas, indisponibilidade/429 e rede controlada | ack/erro dentro do contrato, sem efeito duplicado e sem mensagens fora do canal autorizado |
| V11 | restart durante play/resolução/persistência, reconexão explícita | integrity_check, fila/histórico/artwork/settings/mapping restaurados; não prometer mesmo timestamp |
| V12 | stop/leave durante fonte/decoder e falhas repetidas | áudio termina, filhos próprios encerrados, ticket antigo não ressuscita faixa |
| V13 | 1h contínua e playlist 500 integral | duração real, ledger completo, CPU/memória/FD/goroutines/filhos antes/durante/depois e confirmação audível periódica |
| V14 | 6h e 12h contínuas em runner dedicado | sessões integrais separadas, ausência de stalls/perdas/vazamentos sustentados, recursos após repouso e cleanup |

Cada item exige PASS/FAIL, responsável, timestamps e artefatos redigidos. Item não executado continua PENDENTE. Falhas P0, violação de identidade/permissão, corrupção de fila ou vazamento confirmado bloqueiam a release. Não há autorização implícita para enviar mensagens a canais externos a este roteiro.
