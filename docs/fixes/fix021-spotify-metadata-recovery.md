# FIX-021 — Recuperação de metadados Spotify

## Causa raiz e correção

O pipeline conserva a identidade Spotify e usa o YouTube apenas como localizador de áudio. A descoberta resumida autoriza extração completa; `CompareSpotify` decide a identidade; o mapping persistente guarda a evidência confirmada e o playback reutiliza a prova recente, verificando disponibilidade durante a extração do stream.

Nove recusas eram falsas negativas demonstráveis: Nothing Else Matters, The Unforgiven, Enter Sandman, Sad But True, Wherever I May Roam, Don't Tread on Me, Holier Than Thou, Of Wolf and Man e Through the Never, todas Remastered 2021. A busca retorna o catálogo oficial com “(Remastered)” no título. O matcher exigia também o token `2021`, descartando esses resultados antes de enriquecer. Depois só restavam uploads comuns com o ano no título, sem proveniência verificável, recusados corretamente como `recording_provenance_unknown` e agregados em `insufficient_metadata`.

Exemplo real: `ozXZnwYTMbs`, Nothing Else Matters (Remastered), canal Metallica verificado, créditos Metallica, álbum Metallica (Remastered Deluxe Box Set), duração 389 s e `release_year: 2021`. O ano está nos metadados completos, não no título.

A correção separa o ano da edição dos tokens do título, permitindo a verificação dos candidatos promissores. Na decisão final, exige o ano solicitado no título/álbum ou no campo estruturado `release_year`. Ano ausente continua incerto; ano diferente é incompatível. Preserva todos os gates de artista, colaboradores, duração, live, cover, remix, instrumental e proveniência. Suporta tanto “2011 Remaster” quanto “Remastered 2011”. Não reduz thresholds nem trata título de uploader comum como prova.

As dez recusas remanescentes são Battery, Blackened, Welcome Home (Sanitarium), Harvester of Sorrow, ...And Justice for All, Disposable Heroes, Eye of the Beholder, Orion, The Shortest Straw e Leper Messiah. Não foi demonstrado que sejam dez falsos negativos: entre os resultados examinados há remasters não solicitados, créditos de outros artistas e uploads sem proveniência suficiente. Blackened passou de `no_acceptable_candidate` para `insufficient_metadata` por variação dos resultados de busca; continua recusada. O JSON anexo detalha as razões de verificação por faixa, antes/depois, sem contar repetidamente o mesmo vídeo.

`entries: 0` não comprova falha de extração: vídeos individuais vêm na raiz do JSON. A evidência original contém uma extração válida desse formato. Não houve evidência de que um erro do yt-dlp fosse a causa das nove falsas recusas; campos ausentes em uploads comuns não foram inventados. Os créditos completos também revelam covers que a busca resumida não identifica.

## Recuperação e desempenho

Mantém o fast path existente de SearchCache e mappings SQLite confirmados. O novo campo viaja no JSON de Candidate já persistido; não há nova tabela, migração, serviço ou dependência. Uma prova recente evita busca e verificação redundantes. Quando necessário, enriquece candidatos que passam os filtros preliminares, em ordem de descoberta, parando na primeira confirmação. O cache local de verificação por ID evita extrair o mesmo vídeo em buscas alternativas.

Permanecem quatro buscas de até dez resultados, extrações sequenciais por resolução, timeout de 15 s por operação e 90 s por resolução. O MatchLimiter da aplicação permite quatro resoluções globais, no máximo três em background, reservando capacidade para a primeira faixa. Cancelamento mata o grupo de processos; não há alteração no prefetch ou locks da fila. A recuperação abre um caminho antes descartado e geralmente evita buscas alternativas.

Eventos estruturados: `track_resolution_started`, `candidate_search_completed`, `metadata_recovery_attempted`, `candidate_verification_completed`, `track_resolution_completed`, com identificação Spotify/vídeo, estratégia, duração e resultado/motivo. As resoluções recebem `resolution_id`; decisões registram também o ano. Não adiciona armazenamento ou muda o limite de 524288000 bytes dos logs. Os eventos de recuperação/verificação são emitidos apenas em extrações únicas, não em hits do cache local.

## Validação

Playlist `37i9dQZF1DZ06evO1sJmec`, mesmas 50 posições/IDs e política padrão. Probes isolados com três workers, imagem local `whiterun-music-bot:fix020`, yt-dlp 2026.08.19, mesmas credenciais de ambiente, sem cache de busca ou mappings pré-carregados, sem conexão Discord ou extração de áudio. Nenhum volume SQLite foi montado. O store do probe apenas captura em memória a prova que seria persistida.

O baseline é `.qa-runs/hotfix-playlist/results.json` (também disponível em `docs/hotfix-playlist-evidence.json`). Resultados completos e evidências de seleção estão em [fix021-playlist-evidence.json](fix021-playlist-evidence.json). O probe reproduzível está em `scripts/probe-spotify-recovery/main.go`; compilar com `go build -o /tmp/fix021-probe ./scripts/probe-spotify-recovery` e executar onde yt-dlp/deno e as variáveis Spotify estejam disponíveis, sem montar dados do bot.

Testes direcionados cobrem recuperação pelo ano estruturado, ano ausente, edição conflitante, cover/live, uploader sem proveniência e round-trip JSON da evidência. `go test ./...` e `go vet ./...` passaram, incluindo regressões existentes de identidade, cache, cancelamento, fila e playback. Não houve fuzzing ou campanhas genéricas adicionais.

A auditoria é de identidade por metadados: não encontrou contradições nas seleções. Nas nove recuperadas, verificou separadamente créditos exatos Metallica, canal verificado, remaster 2021 e diferença de duração de até 2 s. Não mede equivalência acústica nem audibilidade no Discord. Não houve deploy.

## Resultado das 50 faixas

| Métrica | Baseline | FIX-021 (passagem final) |
|---|---:|---:|
| Resolvidas | 31 | 40 |
| Recusadas | 19 | 10 |
| insufficient_metadata | 18 | 10 |
| no_acceptable_candidate | 1 | 0 |
| Latência média (ms) | 6979.66 | 9149.36 |
| Mediana (ms) | 6551.5 | 4911.0 |
| p95 (ms, nearest rank) | 15947 | 25165 |

Nove recuperadas; nenhuma das 31 seleções anteriores foi perdida. Não foram encontradas correspondências com metadados contraditórios na auditoria; isso não estabelece uma taxa de erro acústica.

A primeira passagem pós-correção também resolveu 40/50, com média 6257,02 ms e mediana 4119,5 ms. A passagem final apresentou cauda maior: faixas 49/50 levaram 41,15/39,29 s e a 11 levou 23,61 s. Portanto, a mediana melhorou, mas a média e o p95 da passagem final pioraram; não se afirma melhoria uniforme de latência nem se isola causalidade de rede com essas amostras. O número total de processos yt-dlp caiu de 253 no baseline para 232 na primeira passagem e 240 na final (final: 118 buscas e 122 verificações únicas; baseline: 135 e 118). A primeira faixa caiu de 8436 ms para 3960 ms na passagem final.

| # | Faixa | Antes | Depois | Vídeo selecionado |
|---:|---|---|---|---|
| 1 | Nothing Else Matters - Remastered 2021 | incerta | resolvida | ozXZnwYTMbs |
| 2 | The Unforgiven - Remastered 2021 | incerta | resolvida | domjqjQ_WRI |
| 3 | Whiskey In The Jar | resolvida | resolvida | 9PmfpsIh5w0 |
| 4 | Master Of Puppets | resolvida | resolvida | 6xjJ2XIbGRk |
| 5 | Enter Sandman - Remastered 2021 | incerta | resolvida | XZuM4zFg-60 |
| 6 | One | resolvida | resolvida | WM8bTdBs-cw |
| 7 | For Whom The Bell Tolls - Remastered | resolvida | resolvida | B_HSa1dEL9s |
| 8 | Fuel | resolvida | resolvida | ttNSr4Ecdzo |
| 9 | Sad But True - Remastered 2021 | incerta | resolvida | TpohVYomw2o |
| 10 | Seek & Destroy - Remastered | resolvida | resolvida | FLTchCiC0T0 |
| 11 | Turn The Page | resolvida | resolvida | qPOTEs_yTJo |
| 12 | The Unforgiven II | resolvida | resolvida | zjy5sq26hTc |
| 13 | Fade To Black - Remastered | resolvida | resolvida | 9HZ_tx8aWuA |
| 14 | Wherever I May Roam - Remastered 2021 | incerta | resolvida | Z-cEyiM9adE |
| 15 | The Day That Never Comes | resolvida | resolvida | RD83oy7ksUE |
| 16 | Until It Sleeps - Remastered | resolvida | resolvida | U1S1AShYXLg |
| 17 | The Memory Remains | resolvida | resolvida | r-tKBorVGbg |
| 18 | Creeping Death - Remastered | resolvida | resolvida | X8OeBZQn3_w |
| 19 | Battery | incerta | incerta | — |
| 20 | Blackened | recusada | incerta | — |
| 21 | Don't Tread on Me - Remastered 2021 | incerta | resolvida | gGJdWNnC80s |
| 22 | Ride The Lightning - Remastered | resolvida | resolvida | DVOImtJY7po |
| 23 | Welcome Home (Sanitarium) | incerta | incerta | — |
| 24 | Harvester of Sorrow | incerta | incerta | — |
| 25 | Holier Than Thou - Remastered 2021 | incerta | resolvida | Jk8magGDcKE |
| 26 | The Unforgiven III | resolvida | resolvida | VD4PYP7WUCA |
| 27 | Hardwired | resolvida | resolvida | THO9ycDW8tA |
| 28 | ...And Justice for All | incerta | incerta | — |
| 29 | Of Wolf and Man - Remastered 2021 | incerta | resolvida | vh9uz3GS7so |
| 30 | Whiplash - Remastered | resolvida | resolvida | beWvPkYHaic |
| 31 | Die, Die My Darling | resolvida | resolvida | 0o4yv6Cm_ag |
| 32 | Hero of the Day - Remastered | resolvida | resolvida | kfhkKU3X7X0 |
| 33 | I Disappear | resolvida | resolvida | 5AXdCgoyUXs |
| 34 | The Four Horsemen - Remastered | resolvida | resolvida | -zKOhVSERS8 |
| 35 | St. Anger | resolvida | resolvida | 80xgvSCwjAY |
| 36 | Mama Said - Remastered | resolvida | resolvida | KVMTMoLsweI |
| 37 | Through the Never - Remastered 2021 | incerta | resolvida | 6rwQ26VxGyo |
| 38 | Fight Fire With Fire - Remastered | resolvida | resolvida | 8zSODUOoE8w |
| 39 | Lux Æterna | resolvida | resolvida | ZlnfVNrBTUc |
| 40 | King Nothing - Remastered | resolvida | resolvida | mlbjlKkS3lA |
| 41 | Moth Into Flame | resolvida | resolvida | BtRyz1qGI8c |
| 42 | Disposable Heroes | incerta | incerta | — |
| 43 | All Nightmare Long | resolvida | resolvida | kKKxPofBQfU |
| 44 | Eye of the Beholder | incerta | incerta | — |
| 45 | No Leaf Clover - Live | resolvida | resolvida | fg6pNJX83uw |
| 46 | Orion | incerta | incerta | — |
| 47 | Motorbreath - Remastered | resolvida | resolvida | crhU92GKnBk |
| 48 | The Shortest Straw | incerta | incerta | — |
| 49 | Trapped Under Ice - Remastered | resolvida | resolvida | bhH5M_HH2k4 |
| 50 | Leper Messiah | incerta | incerta | — |
