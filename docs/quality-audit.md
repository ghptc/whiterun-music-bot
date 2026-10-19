# Auditoria de qualidade — 2026-10-18

## Diagnóstico anterior às alterações

Inspeção dos arquivos rastreáveis (incluindo arquivos ocultos, scripts, Dockerfile,
Compose, fixtures e documentação), dos pacotes cmd/bot, cmd/profile, internal/media,
internal/player e internal/discord. Não há AGENTS.md aplicável no repositório.
Credenciais em .env não foram lidas.

Comandos realmente registrados: summon, play, now, pause, resume, skip (count),
queue (formato anterior ao CR-001), clear, stop, leave. Previous não existe. Consultas now/queue são
permitidas sem conexão de voz; mutações consultam o canal do usuário e do bot,
sem restrição ao solicitante original. Play revalida o canal após a busca.

Cada guild possui player, mutex e contexto de busca próprios. O player serializa
áudio em uma goroutine, usa tickets de geração para impedir buscas obsoletas,
reserva posições pendentes e resolve coleções com três workers. Pause retém a
pipeline e bloqueia entrega de frames; não é seek nem recuperação exata após crash.
Skip cancela o atual antes da próxima transição. Falhas individuais avançam;
cinco falhas consecutivas suspendem a fila; bloqueios globais do provedor retêm
posição para retry explícito. Stop/leave cancelam buscas e subprocessos.

A pipeline usa grupos de processos Linux, cancelamento, Wait, deadlines de startup,
inatividade e drenagem. Há testes reais locais com FFmpeg e testes de falha com
executáveis controlados. Voice/DAVE possui espera limitada e recuperação observada
pelo watcher; isso não comprova negociação nem áudio no Discord real.

Spotify usa API autenticada e fallback público, paginação limitada a 500 faixas,
metadados separados da URL de áudio e comparação conservadora. A seleção verifica
até quatro candidatos, rejeita variantes e durações incompatíveis e preserva
artistas/título/álbum. Lacunas: ID só implícito na URL; ISRC/explicit não modelados;
sem estado público CONFIRMED/UNCERTAIN/REJECTED, thresholds fixos, ambiguidade não
tratada. Duration original é substituída pela duração do vídeo no MatchSpotify.
Entradas inacessíveis são contabilizadas, mas posições da API são compactadas.

Não há banco, migrations ou recuperação de fila. SearchCache é somente memória,
expira após dez minutos; JSONL de auditoria não é persistência operacional.
Anúncios ocorrem uma vez no primeiro frame entregue, via worker por guild; faltam
solicitante e resumo final de importação. Canal de anúncios cheio pode bloquear
entrega de áudio: risco concreto sob REST lento e transições rápidas.

## Baseline executado

- `go test ./...`: PASS (player 21.824s).
- `go test -race ./...`: PASS (player 22.895s).
- Primeira tentativa no sandbox falhou por cache Go somente leitura; ambas foram
  repetidas com permissão e concluíram. Nenhum token externo foi necessário.

## Prioridades verificáveis

1. Estados de fidelidade e critérios configuráveis, evidência contraditória,
   identidade original, ambiguidade e fixtures adversariais.
2. Previous com histórico limitado; solicitante; isolamento de áudio/REST.
3. SQLite transacional local, migrations e testes em arquivos reais; recuperação
   sem reconectar voz automaticamente e revalidação dos mapeamentos.
4. Cobertura das novas regras, CI e documentação de limites reais.

Resultados finais e limitações serão registrados após os testes das alterações.

## Alterações implementadas

- Modelo Spotify com SpotifyID, ISRC e explicit opcionais; duração original
  preservada; comparação pública determinística e estados de decisão; política
  configurável, ambiguidade, créditos e artistas com identidade inteira.
- Posições da API conservadas, rejeição de paginação truncada, decisões registradas
  e resumo da última importação em `/queue`.
- `/previous` com histórico de até 100 inícios reais; reinicia predecessor e
  recoloca faixa interrompida imediatamente depois dele. Pause/resume existentes
  mantidos. Consultas now/queue seguem públicas na guild, conforme comportamento
  anterior; mutações exigem o mesmo canal, sem exclusividade ao solicitante.
- Solicitante nas faixas e anúncios. Mensagem REST falhando não interrompe áudio.
  Fila de anúncios saturada descarta/loga o anúncio em vez de bloquear frames.
- SQLite local sem CGO, migrations versionadas, transações para estado/metadados e
  histórico de navegação por guild, settings e mapeamentos. Testes usam arquivos
  reais. Integração inclui rollback forçado após atualização parcial.
- Recuperação sem voz automática; faixa interrompida recomeça, não recupera seek.
  Pendências Spotify são resolvidas novamente; áudio Spotify sempre revalidado
  antes de streaming. Mapeamentos expiram em 24h e são revalidados em cada uso.
- Correção da recuperação da primeira posição bloqueada da playlist e da
  propagação de falhas de verificação (sem converter erro de provedor em rejeição).
- CI de testes, race, vet, cobertura e integração real SQLite; probe externo
  separado e manual, sem alegação de E2E de áudio.

## Operação e riscos residuais

`BARD_DB_PATH` define o arquivo SQLite (default `bard.db`). Docker Compose usa
volume `bard-data` em `/home/bard/data`, compatível com filesystem raiz read-only.
Backups precisam considerar o WAL (usar backup SQLite ou checkpoint com bot parado).
Uma instância de bot por arquivo/guild é o modelo suportado; não há consenso entre
réplicas ou recuperação distribuída. Não se introduziu PostgreSQL porque os dados
são locais, pequenos e transacionais, sem exigência de serviço compartilhado.

Checkpoints ocorrem sob o mutex do player e usam timeout de três segundos. Isso
mantém ordem de gravação; disco lento pode aumentar latência de controles/transições.
Erro de escrita preserva o último snapshot transacional e aparece no log e em
`/queue`; playback em memória continua, portanto mudanças após falha de disco não
possuem garantia de recuperação. O histórico de navegação é limitado aos últimos 100 inícios. A tabela
playback_events preserva separadamente todos os inícios de áudio (append-only),
inclusive replays; a consulta expõe até as 1000 entradas mais recentes, e não há
limpeza automática de eventos nesta versão. Resumos de importação
são de sessão. Configuração persistida atualmente: canal de anúncios por guild.

Após crash, o último snapshot pode repetir a faixa interrompida desde o início;
essa política de recuperação é intencional. Conexões, DAVE, goroutines, FFmpeg,
URLs assinadas e Timing nunca são persistidos. Novas solicitações entram após a
fila recuperada. Stop/leave explícitos limpam a fila durável; shutdown preserva.

A comparação de metadados não reconhece acusticamente uma gravação mal rotulada.
Anúncios têm entrega best-effort: um início produz no máximo um anúncio; falha REST
ou saturação é registrada, sem retry que possa duplicar mensagens. E2E externo
Discord/Spotify/YouTube ainda não foi executado nem comprovado.

## Verificação final local

- 34 arquivos de testes; 195 funções Test, além dos subtestes.
- `go test ./... -count=1 -timeout=3m`: PASS após implementação.
- `go test -race ./... -count=1 -timeout=3m`: PASS, sem data races nos caminhos cobertos.
- Integrações SQLite em arquivo e FFmpeg/libopus reais: PASS, incluindo Bot.play
  rejeitando gravação incompatível antes de produzir frames.
- `go vet ./...`: PASS.
- `CGO_ENABLED=0 go build -o /tmp/whiterun-quality-bard ./cmd/bot`: PASS.
- `docker compose config --quiet`: PASS; workflows também tiveram parsing YAML
  local validado. A execução remota de GitHub Actions ainda depende de push.
- `git diff --check`: PASS.
- `go mod tidy`: primeira tentativa falhou em DNS; repetição autorizada com
  `GODEBUG=netedns0=0` concluiu, usando a correção de DNS já presente no Dockerfile.

Rodada final da árvore entregue: PASS para todos os pacotes com cobertura e com
race detector (player 21.708s / 22.883s). Cobertura total: **83,0%** dos statements.
Por pacote: media **88,6%**, player **89,8%**, discord **73,7%**, storage **78,8%**,
cmd/bot **1,3%**, cmd/profile **0%**. Os entrypoints e o transporte remoto permanecem
lacunas explícitas; cobertura não é evidência de áudio externo. Artefatos locais:
`coverage.out`, `coverage-summary.txt` e `coverage.html` (gerados, não versionados).
E2E remoto, handshake DAVE real e recepção de áudio por usuário Discord não foram
executados. Não houve deploy, reinício de produção ou publicação no GitHub.
