# Auditoria de qualidade — 2026-10-18

## Diagnóstico anterior às alterações

Inspeção dos arquivos rastreáveis (incluindo arquivos ocultos, scripts, Dockerfile,
Compose, fixtures e documentação), dos pacotes cmd/bot, cmd/profile, internal/media,
internal/player e internal/discord. Não há AGENTS.md aplicável no repositório.
Credenciais em .env não foram lidas.

Comandos realmente registrados: summon, play, now, pause, resume, skip (count),
queue (page), clear, stop, leave. Previous não existe. Consultas now/queue são
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
