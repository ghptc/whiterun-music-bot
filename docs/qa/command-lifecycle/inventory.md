# QA-013 — inventário real de comandos

Fonte primária: `internal/discord/commands.go:commands`,
`internal/discord/expansion.go:expansionCommands` e dispatch `commandAt`.
Snapshot serializado pelo teste de registro em [registered-commands.json](evidence/registered-commands.json).
São **22 slash commands**, com contexto guild. README não foi usado como autoridade.

Todos recebem ACK antes de validação de guild/voz/argumentos. Se ACK falha, a
operação não executa. Play usa resposta inicial pública; queue defer público;
os demais defer ephemeral. Após ACK, falha na edição **não desfaz** operação já
aplicada. Não há permissão DJ/admin: os mutadores exigem que solicitante esteja
na sala do Bardo; read-only não exige presença. Permissões Discord para entrar
são avaliadas pelo Open da conexão.

`I/P/A` na matriz de estados significam idle/playing/paused, não enums novos.
Erros e respostas podem ser editados após o comando; nenhum teste presume que
sucesso de resposta implica sucesso de áudio. Geração invalida tickets;
queue_version é contador diagnóstico, não versão durável de todas as operações.

| Comando | Opções efetivamente registradas | Pré-condições | Efeito sobre fila/player | Persistência | Discord / falha |
|---|---|---|---|---|---|
| `summon` | — | Voz do solicitante; mesma sala se conectado | Conecta se ausente; attach incrementa geração; fila preservada | Sem escrita de fila no attach vazio | Confirma summons; erro explica View/Connect/Speak |
| `play` | query obrigatório tipo=3 max_length=500; force opcional tipo=5 | Voz e slot de busca; query; force só vídeo YouTube explícito | Resolve, revalida sala/ticket, reserva ou enfileira; pode iniciar nova faixa | Fila, catálogo/cache quando aplicável e destino de anúncios | Mensagem inicial pública; edição com queued/progresso/erro |
| `now` | — | Guild; não exige voz | Snapshot, sem mutação | Nenhuma | UI Components V2 da faixa ou idle |
| `pause` | — | Mesma voz; tentativa ativa | Pausa gate atual; próxima faixa começa sem pausa | Pausa não persiste | Paused/already paused ou not playing |
| `resume` | — | Mesma voz; tentativa ativa ou latch legado | Libera pause/halted/provider; sinaliza scheduler quando necessário | Não persiste pause/latch | Resumed/already playing/recovery ou not playing |
| `previous` | — | Mesma voz; histórico anterior iniciado; capacidade | Reenfileira anterior e atual; cancela tentativa; histórico de navegação recua | Estado de fila/histórico de navegação; ledger append-only preservado | Previous song queued ou erro |
| `skip` | count opcional tipo=4 min_value=1 max_value=1001 | Mesma voz; count 1..1001; ausência é no-op | Cancela atual e descarta N posições lógicas, incluindo pendentes | Fila e estado atual | Quantidade/next/pending ou fila vazia |
| `queue` | limit opcional tipo=4 min_value=1; from opcional tipo=4 min_value=1; to opcional tipo=4 min_value=1 | Guild; limite/range válidos; não exige voz | Snapshot consistente; nenhuma mutação | Nenhuma | Um PATCH original Components V2; aviso de falha via segundo PATCH limitado |
| `clear` | — | Mesma voz; vazio permitido | Cancela buscas/pendentes; generation++; limpa próximas e loop; mantém atual | Fila e loop | Quantidade removida ou already empty |
| `stop` | — | Mesma voz; idle permitido | Cancela buscas/stream; generation++; esvazia fila e loop; mantém conexão | Estado parado | Stopped/queue clear |
| `leave` | — | Mesma voz; desconectado permitido | Cancela buscas/stream; generation++; limpa fila/loop; fecha conexão | Estado vazio; preferências/artwork/histórico preservados | Takes leave |
| `remove` | position obrigatório tipo=4 min_value=1 max_value=1000 | Mesma voz; posição existente em upcoming | Remove slot; invalida/cancela resolução desse slot | Fila | Queue updated ou erro; atual não interrompida |
| `move` | from obrigatório tipo=4 min_value=1 max_value=1000; to obrigatório tipo=4 min_value=1 max_value=1000 | Mesma voz; from/to existentes em upcoming | Move entrada inteira, preserva identidade e slot original | Fila | Queue updated ou erro atômico |
| `shuffle` | — | Mesma voz; upcoming não vazia | Permuta entradas, preserva multiset/IDs; acorda scheduler e resolver | Fila | Queue updated ou empty |
| `jump` | position obrigatório tipo=4 min_value=1 max_value=1000 | Mesma voz; upcoming position existente | Cancela atual e descarta anteriores; alvo pronto/pending mantém identidade | Fila e atual | Jump requested ou erro atômico |
| `replay` | — | Mesma voz; atual; capacidade | Cria nova tentativa do atual na cabeça; cancela anterior; pausa não se transfere | Fila e atual | Restart requested ou not playing/full |
| `loop` | mode obrigatório tipo=3 choices=off,track,queue | Mesma voz; mode off/track/queue | Muda política; EOF repete conforme modo, skip/falha não repete | Loop | Loop mode ou erro; clear/stop/leave desligam |
| `seek` | position obrigatório tipo=3 max_length=16 | Mesma voz; timestamp válido e dentro da duração | Sempre rejeita: pipeline não suporta seek; estado preservado | Nenhuma | Formato/duração/ausência ou indisponibilidade |
| `autoplay` | enabled obrigatório tipo=5 | Mesma voz; enabled boolean | Salva intenção; não gera recomendações ao fim da fila | Autoplay | Preferência salva, provider indisponível, ou disabled |
| `lyrics` | — | Guild; não exige voz | Sem mutação; atual apenas determina mensagem | Nenhuma | Not playing ou provider licenciado indisponível |
| `history` | limit opcional tipo=4 min_value=1 max_value=20 | Guild; limit 1..20, default 10; não exige voz | Lê ledger SQLite append-only; não usa histórico mutável de previous | Leitura; sem mutação | Recentes, vazio, DB ausente/indisponível ou limite inválido |
| `help` | — | Guild; não exige voz/DB/player | Conteúdo estático | Nenhuma | Embed ephemeral com os 22 comandos |

Tipos Discord: 3=string, 4=integer, 5=boolean. A plataforma aplica os bounds do
registro; testes entregam adicionalmente valores inválidos ao handler para
validar defesa local. Não se declara validação local de todo payload impossível
que o SDK possa aceitar (ex.: campo required ausente com tipo arbitrário).

## Logging e rastreabilidade

Todos: command_received → command_ack_start/complete → command_total,
correlacionados por interaction_id. Play acrescenta Timing, resolução e
queue_inserted; queue acrescenta snapshot/plan/response. Mutações relevantes
emitem queue_mutation; resolução commit/discard e scheduler explicam elegibilidade,
wait/wakeup e transição. Tentativas propagam playback_attempt_id, sequence,
coleção e geração. Pause e cancelamento têm eventos próprios. Erros HTTP geram
avisos específicos; não há evento de sucesso uniforme para todas as respostas.

O logging não fornece snapshot before/after para cada comando nem interaction_id
em toda mutação. Os testes verificam esses estados diretamente e validam a
correlação efetivamente disponível, sem fabricar causalidade ausente. Lacunas
remanescentes em [logging-gaps](../../observability/logging-gaps.md).

Cada linha acima participa de TestQA013OrderedCommandPairs em I/P/A,
TestQA013CommandAcknowledgementAndReplyFaults e TestQA013EveryCommandSQLiteRestart.
A cobertura semântica e os gates reais estão nas outras quatro páginas desta campanha.
