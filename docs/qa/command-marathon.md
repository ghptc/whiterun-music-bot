# QA-014 — Command Marathon & Stateful User Journeys

Última expansão funcional de QA antes da homologação cloud e release v1.0.
Oito jornadas, em `TestQA014CommandMarathon`, usam os 22 comandos do registro
real e do [inventário QA-013](command-lifecycle/inventory.md). Um gate compara
os comandos usados nas jornadas com `commands()`; um comando novo sem jornada
faz o teste falhar. Não foram criados comandos ou opções.

## Escopo e fixtures

Somente testes e este documento foram alterados. Reutilizados:
`lifecycleFixture`, dispatcher `commandAt`, `recordingRest`, conexão/UDP
controlados, gates de frame/EOF/cancelamento, `hardeningSpotify`, SQLite real e
regras de recuperação da QA-013. Os helpers da maratona acrescentam parâmetros,
expectativas de jornada e captura concorrente de JSON; não substituem o player
ou criam servidor/framework de integração.

Cada jornada tem seu próprio banco SQLite em `t.TempDir()`. Discord Voice,
resolução Spotify/YouTube e áudio são fronteiras controladas. H executa também
um subprocesso de metadata local, sem rede. Nenhum banco, token ou guild de
produção é usado. Não houve campanha adicional de fuzzing, mutação ou endurance.

## Jornadas executadas

| Jornada | Sequência e preparação | Transições e invariantes protegidas |
|---|---|---|
| A — normal | Summon → Play 1 → Play 2 → Queue → Pause → Resume → Now → Skip → Previous → Replay → Lyrics → Help → Stop | As duas músicas são solicitadas pelo comando. Skip inicia 2; Previous volta a 1 e devolve 2 à fila; Replay cria outra tentativa de 1. Histórico de navegação distingue essas tentativas intencionais. Stop termina sem fila/stream ativo. |
| B — progressiva/FIX-014 | Play playlist 302 → Shuffle com workers ativos → Move posição original 7 à cabeça → liberar primeiro lote → Move original 302 à cabeça → Queue → EOF → liberar resolução → Queue → Stop | Seis prontas, 295 pendentes e cabeça 302 pendente após EOF. Exige notificação `playback_end`, wait com `next_expected_position=302`, commit, wakeup e seleção da posição 302. Nenhum comando Skip é permitido na jornada. |
| C — manipulação | Play 1 → Play playlist 4 → Move 3→1 → Remove 2 → Shuffle → Jump 2 → Queue → Clear → Play 2 → EOF → Stop | Remove/Jump descartam somente as posições previstas. Shuffle preserva multiset; posições originais e IDs continuam associados. Clear preserva a faixa atual e a nova solicitação reproduz após EOF. SQLite acompanha cada mutação. |
| D — avançados | Play 1 → Play 2 → Seek 0:15 → Loop track → EOF → Loop queue → Skip → Loop off → Autoplay true → EOF → History → Stop | Seek é explicitamente indisponível e não muda estado. EOF em track repete 1; Skip em queue não reinsere a faixa cancelada. Off permite esgotar a fila. Autoplay persiste intenção e não inventa recomendações quando o provider não existe. |
| E — múltiplos usuários | Play playlist 10; Queue do usuário 4 simultâneo a Stop proibido do usuário 5; Pause 4 → Resume 3 → Play 99 por 4 → Move original 10 à cabeça por 4 → Skip 3 → liberar resolução → Queue 4 → Stop 4 | Reprodução ativa e resolução pendente coexistem. Os dois handlers são liberados por barreira; consultas e rejeição de outra sala não mutam a sessão. Comandos autorizados de dois ouvintes são intercalados. Requester de 99 permanece 4; a cabeça pendente 10 inicia após resolução, sem sobreposição de streams. |
| F — recuperação | Play 1 → Play 2 → Queue → fechar player/DB → reabrir DB/recriar player → Summon → Resume → Play 3 → History → EOF → Stop → Leave | Recuperação não reproduz sem attach. Summon restaura 1 no início; Resume já em reprodução não altera estado. Metadata/artwork/fila sobrevivem. Ledger append-only contém as duas execuções de 1; histórico de navegação não duplica indevidamente a recuperação. Leave fecha Voice. |
| G — falhas | Play 1 → Play 2 → falha de stream/provedor → sucessora aceita 2 → Play 4 com resolução falha → Skip → Play 3 → Stop | O único fallback usado é a próxima entrada já aceita na fila, conforme a política existente. Não há substituição musical, bypass de Fidelity ou retry de autenticação. Falha de resolução não insere faixa. Novo Play funciona após as falhas. |
| H — Force Play | Play Spotify 1 → Play force:true URL YouTube → Queue → Skip → Previous → Play Spotify 2 → dois EOFs → Stop | Vídeo forçado permanece `ZYX98765432`, com URL canônica exata e SpotifyID vazio. Previous devolve a faixa normal e mantém o vídeo manual na fila. Mapping Spotify sem alterações; uma nova faixa Spotify continua MATCH_CONFIRMED e não herda ForcedYouTube. Todos os PIDs do extrator de teste são verificados como encerrados/reapados. |

A preparação extra em A cria um sucessor e histórico válido. C usa uma pequena
playlist no segundo Play para que Move/Remove/Jump sejam operações reais.
F é uma recriação limpa de sessão e conexão SQLite no mesmo processo de teste,
não um teste de queda abrupta do sistema operacional.

## Cobertura dos 22 comandos

| Comando | Jornadas | Estado anterior / resultado | Efeito persistido e próxima transição |
|---|---|---|---|
| summon | A, F | Anexado em A; recuperado sem voz em F | F inicia a faixa recuperada e salva current/started/history; attach só incrementa geração quando cria sessão. |
| play | A–H | Idle, playing ou resolução progressiva; normal, playlist e force | Fila/current, identidade, origem, artwork e requester; próxima seleção ou espera por commit. Falha em G não insere. |
| queue | A, B, C, E, F, H | Ready/pending/force; resposta Components V2 | Nenhuma mutação de estado ou geração; fila persistida permanece igual. |
| pause | A, E | Playing → paused | Gate atual; não persiste pause; Resume libera a mesma tentativa. |
| resume | A, E, F | Paused → playing ou já playing | Sem alteração da fila; F não tenta restaurar Voice usando Resume sozinho. |
| now | A | Playing | Consulta sem mutação; Skip seguinte continua correto. |
| skip | A, D, E, G, H | Playing, ou deixa próxima pendente em E | Atual cancelado; próxima faixa ou wait; loop não repete tentativa cancelada. |
| previous | A, H | Histórico anterior iniciado | Reordena navegação, devolve atual à fila; próximo frame pertence à gravação anterior. |
| replay | A | Playing com sucessor | Nova tentativa da mesma gravação e histórico esperado; Stop cancela recursos. |
| stop | A–H | Playing, idle ou pendente | Current/fila vazios, loop off, geração incrementada, jobs/streams cancelados. |
| leave | F | Stopped com Voice anexado | Estado vazio mantido; conexão fechada e nenhum worker da sessão retido. |
| move | B, C, E | Upcoming pronta/pendente | Ordem atual muda; slot original e geração não mudam; resolução faz commit no slot correto. |
| remove | C | Upcoming existente | Remove somente posição 2 após Move; não interrompe atual. |
| shuffle | B, C | Playing; resolução ativa em B | Permuta sem perda/duplicação, preserva IDs/posições originais e acorda resolução. |
| jump | C | Playing com fila não vazia | Descarta anteriores e inicia alvo original da posição atual 2; Clear preserva esse alvo. |
| clear | C | Playing com próximas faixas | Esvazia upcoming e desliga loop sem parar atual; novo Play inicia no próximo EOF. |
| seek | D | Playing, timestamp dentro da duração | Rejeição por pipeline indisponível; nenhuma mutação ou persistência indevida. |
| loop | D | Modos track → queue → off | Cada modo é persistido; EOF e Skip têm efeitos distintos e verificados. |
| autoplay | D | Playing, fila quase esgotada | Preferência true persiste; EOF não gera uma faixa fictícia. |
| lyrics | A | Playing | Resposta informa indisponibilidade; nenhuma mutação. |
| help | A | Playing | Embed estático; nenhuma mutação. |
| history | D, F | Fila esgotada ou recuperada | Leitura do ledger SQLite; nenhuma mutação de navegação, fila ou geração. |

## Validação contínua e diagnóstico

Depois de cada comando serial são comparados current/started, ordem completa
(incluindo pending), histórico esperado, pausa, loop, autoplay, geração e
recursos ativos. Identidade Spotify, flag/origem/URL do vídeo forçado, posição
original, total e artwork são preservados. O estado durável SQLite deve ser
igual à sessão aceita, excluindo Timing, que não é persistido.

Todos os comandos têm exatamente um ACK antes de mutação e uma resposta
Discord. Eventos receive/ACK/total são correlacionados por interaction_id;
mutações e mudanças de pause devem emitir seus eventos no trecho da própria
operação. B verifica também a cadeia causal do scheduler; G verifica a categoria
da falha. Nas consultas e em Seek rejeitado o snapshot e a geração permanecem
iguais. Em E, os dois comandos simultâneos são verificados como um lote
imutável, com ACKs e término individuais; não se inventa uma ordem entre eles.

No final, Stop/Leave já esvaziaram a sessão antes do cleanup. Player.Close e
cancelamento/Wait dos workers Discord aguardam os recursos pertencentes à
fixture; active=0, ausência de sobreposição e jobs vazios são obrigatórios.
`PRAGMA integrity_check` deve retornar `ok`, e as conexões SQLite são fechadas.
H verifica por PID os subprocessos realmente criados. Os demais streams são
controlados e não criam FFmpeg, portanto não se declara teste de descritores
ou audibilidade remota a partir dessas fixtures.

## Determinismo e falhas encontradas

Não há sleeps. `testing/synctest`, canais e barreiras controlam primeiro frame,
EOF, conclusão/cancelamento da resolução e comandos simultâneos. As jornadas têm
entradas fixas, sem seed de geração aleatória de comandos. O Shuffle público
continua aleatório: qualquer permutação válida é aceita somente após validar
multiset, e a permutação é registrada no log de teste. As transições críticas
não dependem dela: Move escolhe a posição original conhecida. Não se afirma que
um seed de teste controle o RNG global do Shuffle.

A primeira versão da fixture B presumiu que a janela inicial sempre conteria
seis sucessoras. Race mostrou cinco prontas quando a janela ainda incluía a
faixa atual antes do dequeue. Isso é uma política de janela legítima, não um
novo stall. A fixture agora expõe a posição 7 antes da posição 302, garantindo
seis prontas e 295 pendentes nos dois escalonamentos. Nenhum defeito novo de
produção foi confirmado e nenhuma correção de produção foi necessária.

Cada passo registra comando, opções, usuário e estado anterior. Falhas mostram
estado real e esperado; o nome da jornada permite reproduzir a sequência:

```sh
go test ./internal/discord -run '^TestQA014CommandMarathon$' -count=1 -v
go test -race ./internal/discord -run '^TestQA014CommandMarathon$/B_progressive302_FIX014$' -count=1 -v
```

## Resultado final e encaminhamento

Em 2026-10-20, após os ajustes finais das fixtures:

| Check | Resultado |
|---|---|
| Oito jornadas dirigidas, com race detector | PASS |
| `go test ./... -count=1` | PASS |
| `go test -race ./... -count=1` | PASS, sem races reportadas |
| `go vet ./...` | PASS, sem diagnósticos |
| `docker compose build` | PASS |

Imagem local construída: `119560993d11` (produção permanece sem alterações).
Logs completos e os passos das jornadas estão no workspace ignorado
`.qa-runs/qa014/`. O build inclui os testes do Dockerfile. PASS refere-se à
suíte padrão offline; os opt-ins live e demais limitações da QA-013 não são
convertidos em aprovação de E2E por esta campanha.

Não foram executadas campanhas extras de fuzzing, mutação ou endurance. A
expansão genérica de QA termina aqui. Próxima etapa: homologação cloud em guild
controlada, com credenciais de homologação, provedores reais, Voice/DAVE,
reconexão, anúncios e audibilidade, seguida da decisão de release v1.0.

Gates pendentes: essa homologação cloud/E2E Discord real e a decisão de release.
Os testes locais não autorizam deploy automático nem equivalem a E2E aprovado
em Discord. Nenhum deploy foi realizado nesta campanha.
