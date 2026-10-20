# UI-015 — Queue density, metadata e artwork

Verificação: 20/10/2026. Dependência: DisGo v0.19.6 (go.mod). Implementada a variante C. Nenhum deploy ou envio de teste ao Discord foi realizado.

## Limites e evidência

A [referência oficial atual](https://docs.discord.com/developers/components/reference), conferida também no [fonte oficial](https://github.com/discord/discord-api-docs/blob/main/developers/components/reference.mdx), estabelece:

| Elemento | Regra documentada |
| --- | --- |
| Mensagem V2 | 40 componentes totais; flag `IS_COMPONENTS_V2` |
| Section | 1–3 Text Displays e um acessório |
| Thumbnail | Apenas acessório de Section; imagem; descrição até 1.024 caracteres |
| Container | Filhos permitidos: Action Row, Text Display, Section, Media Gallery, Separator e File |
| Aninhamento | Container não é filho permitido de Container; Section não aceita outra Section |
| Nível superior | A referência atual não declara teto V2 separado inferior aos 40 totais |
| Filhos de Container | A tabela atual não declara teto separado de 10 filhos |
| Text Display | Markdown em `content`; a tabela atual não explicita limite numérico |

Não se importou o teto legado de cinco Action Rows nem um teto de dez filhos de versões antigas. Todos os descendentes e acessórios entram na contagem recursiva. Há somente um Container no nível superior no payload implementado.

**Lacuna documental:** nas páginas oficiais atuais consultadas, os números 4.000 encontrados na referência dizem respeito a Text Input de modais. Não foi possível confirmar nelas uma declaração explícita de 4.000 caracteres agregados de Text Display. Mantivemos o orçamento conservador já usado pelo projeto: **4.000 unidades UTF-16 somadas de todos os Text Displays**, incluindo Markdown, cabeçalho, espaços, quebras e rodapé. Isso não deve ser apresentado como nova confirmação de limite pelo servidor. Uma unidade suplementar como 🎵 consome duas unidades nessa política. URLs, alt text e sintaxe JSON são medidos separadamente e não substituem o orçamento textual.

O [guia oficial de mensagens](https://docs.discord.com/developers/components/using-message-components) demonstra múltiplos componentes e layouts aninhados; não autoriza ultrapassar o total recursivo.

## DisGo e validação

Inspecionados `discord/component.go`, `discord/message_create.go` e `discord/message_update.go` da versão fixada. `SectionComponent` aceita `[]SectionSubComponent` e `SectionAccessoryComponent`; Container aceita `[]ContainerSubComponent`. As interfaces restringem tipos, mas os construtores, setters e `MarshalJSON` não validam orçamento agregado, cardinalidade de Section ou quantidade de filhos. `NewMessageUpdateV2` aplica a flag apropriada. Serialização bem-sucedida não prova aceitação pelo Discord.

O simulador serializa **MessageUpdate real**, com os mesmos structs DisGo e AllowedMentions do envio. Depois decodifica o JSON e valida independentemente envelope, tipos, posições permitidas, cardinalidade de Section, URL HTTPS, alt text, orçamento textual e contagem recursiva. Compara a contagem observada com o contador do renderer e verifica a ordem visual serializada. Casos negativos exercitam nesting, acessórios e estouro de orçamento. É validação local do subconjunto utilizado, não validação ao vivo da API.

## Variantes e escolha

- **A — atual:** cópia congelada do renderer anterior, apenas em arquivo de teste. Container, cabeçalho, rodapé, até dez próximas faixas ilustradas e cauda textual. Now Playing era somente texto. O código encontrado já usava um Text Display por Section e não tinha Separator individual; não se inventaram separadores para inflar o baseline.
- **B — compacto:** um Text Display por Section com título, artista e duração em linhas separadas; Thumbnail individual; nenhum Separator. Now Playing também ilustrado. Para medir a capacidade do layout exclusivamente por faixa, encerra ao esgotar componentes para artwork.
- **C — otimizado, escolhido:** mesmo layout visual de B; reserva um componente para uma cauda Text Display que agrupa as faixas posteriores, separadas por linha em branco. Continua até o orçamento textual, mantendo a ordem da fila e faixas inteiras.

O custo mínimo de uma capa vinculada à faixa é **3 componentes**. A reserva fixa é Container + cabeçalho + rodapé = **3**, mais **1** para a cauda. Portanto `floor((40 - 4) / 3) = 12` faixas ilustradas, incluindo Now Playing. Sem Now Playing são até 12 próximas faixas; com ele, até 11 próximas mais a atual. Ausência de artwork usa Text Display como fallback visual e pode reduzir esse teto: mantém-se a ordem e a associação inequívoca entre texto e imagem. Não há imagem artificial de placeholder. Remover o Container ou combinar o cabeçalho com uma faixa não libera os três componentes necessários para uma 13ª capa mantendo rodapé e cauda.

No perfil curto de 1.000 próximas faixas, A exibe 136 próximas e dez capas; C exibe 98 próximas e 12 capas. O total textual cai porque cada faixa agora apresenta artista e duração em linhas próprias, com espaçamento entre faixas compactas. A comparação não elimina metadados para inflar capacidade. No perfil misto, A exibe 86 próximas e oito capas; C exibe 67 e 11 capas. Para dez próximas faixas, C inclui todas e adiciona a capa da atual.

## Simulação reproduzível e medições

```sh
go test ./internal/discord -run '^TestQueueDensitySimulation$' -v
```

Duas entradas determinísticas para cada tamanho de fila (10, 100, 300, 1.000), sempre com Now Playing adicional. `short`: título curto, artista e duração disponíveis, todas as capas. `mixed`: título Unicode longo a cada sete posições, artwork ausente a cada cinco, duração indisponível a cada nove. Inclui acentos, japonês, emoji suplementar e marca combinante. A faixa atual tem duração acima de uma hora. Testes adicionais verificam 07:55, 01:01:11, duração desconhecida, artwork inválido, fallback YouTube, títulos maiores que todo o orçamento, ranges e concorrência do snapshot.

Host: AMD Ryzen 7 9800X3D. Tempos de relógio para 200 operações após aquecimento; alocações via `testing.AllocsPerRun(200)`, separando construção e serialização. Números de tempo variam por máquina, GC e carga. Não incluem SQLite, rede, cópia do snapshot pelo player, carregamento de imagens nem renderização pelo cliente Discord. `shown upcoming` exclui a atual; `images` inclui a atual. `selection` significa que todas as posições solicitadas couberam. C pode esgotar componentes para capas e ainda continuar até `text`.

| profile | queued | layout | shown upcoming | images incl current | components | UTF16 text | JSON bytes | render ns/op | marshal ns/op | render allocs/op | marshal allocs/op | stopping budget |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| short | 10 | A-current | 10 | 10 | 34 | 404 | 2219 | 13926 | 20394 | 235 | 139 | selection |
| short | 10 | B-sections | 10 | 11 | 36 | 517 | 2472 | 10952 | 22981 | 217 | 147 | selection |
| short | 10 | C-optimized | 10 | 11 | 36 | 517 | 2472 | 10862 | 23018 | 217 | 147 | selection |
| short | 100 | A-current | 100 | 10 | 35 | 2928 | 5037 | 131319 | 29612 | 1769 | 147 | selection |
| short | 100 | B-sections | 11 | 12 | 39 | 576 | 2690 | 15762 | 24969 | 250 | 159 | components |
| short | 100 | C-optimized | 98 | 12 | 40 | 3966 | 6538 | 92786 | 40811 | 1570 | 166 | text |
| short | 300 | A-current | 136 | 10 | 35 | 3995 | 6213 | 185411 | 34204 | 2585 | 147 | text |
| short | 300 | B-sections | 11 | 12 | 39 | 577 | 2691 | 15707 | 25375 | 276 | 159 | components |
| short | 300 | C-optimized | 98 | 12 | 40 | 3968 | 6540 | 94530 | 38518 | 1716 | 166 | text |
| short | 1000 | A-current | 136 | 10 | 35 | 3996 | 6214 | 191177 | 34432 | 2679 | 147 | text |
| short | 1000 | B-sections | 11 | 12 | 39 | 578 | 2692 | 15974 | 27493 | 276 | 159 | components |
| short | 1000 | C-optimized | 98 | 12 | 40 | 3969 | 6541 | 92738 | 37048 | 1772 | 166 | text |
| mixed | 10 | A-current | 10 | 8 | 30 | 624 | 2341 | 17085 | 20122 | 231 | 123 | selection |
| mixed | 10 | B-sections | 10 | 9 | 32 | 767 | 2624 | 15147 | 21969 | 209 | 131 | selection |
| mixed | 10 | C-optimized | 10 | 9 | 32 | 767 | 2624 | 12458 | 23455 | 209 | 131 | selection |
| mixed | 100 | A-current | 86 | 8 | 31 | 3983 | 6833 | 133357 | 36090 | 1558 | 132 | text |
| mixed | 100 | B-sections | 13 | 11 | 39 | 900 | 3102 | 18823 | 27514 | 277 | 159 | components |
| mixed | 100 | C-optimized | 67 | 11 | 40 | 3974 | 7109 | 75004 | 40573 | 1095 | 167 | text |
| mixed | 300 | A-current | 86 | 8 | 31 | 3984 | 6834 | 137402 | 38407 | 1692 | 132 | text |
| mixed | 300 | B-sections | 13 | 11 | 39 | 901 | 3103 | 18580 | 27167 | 307 | 159 | components |
| mixed | 300 | C-optimized | 67 | 11 | 40 | 3975 | 7110 | 74016 | 42399 | 1210 | 167 | text |
| mixed | 1000 | A-current | 86 | 8 | 31 | 3985 | 6835 | 144001 | 37015 | 1736 | 132 | text |
| mixed | 1000 | B-sections | 13 | 11 | 39 | 902 | 3104 | 21433 | 28085 | 307 | 159 | components |
| mixed | 1000 | C-optimized | 67 | 11 | 40 | 3976 | 7111 | 76705 | 41503 | 1235 | 167 | text |

## Correção do Now Playing e caminho de dados

A causa comprovada estava em `buildQueueRange`: `illustrated := art != "" && row.Position > 0 && preview`. O teste `row.Position > 0` excluía a faixa atual, independentemente dos metadados. Foi removido; a atual entra primeiro na mesma seleção de artwork das demais faixas.

O caminho existente foi auditado e preservado:

1. Spotify API/anonymous/embed preenchem `Track.SpotifyArtworkURL` e `Track.Duration` quando disponíveis.
2. `CollectionResolver.Prepare` reaproveita o catálogo; `restoreCatalogArtwork` restaura capa válida pelo SpotifyID exato, antes da entrada no player, sob contexto de catálogo compartilhado de 200 ms.
3. `storage.SaveCatalog` persiste metadados e preserva artwork Spotify anterior quando uma atualização não tem capa; `LoadCatalog` usa validade de 24 horas. A chave de mapping exclui artwork deliberadamente: isso evita invalidar identidade de áudio por uma troca de capa, não apaga a capa do Track.
4. A resolução de áudio em `progressive.go` conserva o Track Spotify e troca os localizadores YouTube. O player seleciona o Track, recebe metadados resolvidos e copia o resultado para `current`.
5. `GuildPlayer.State` copia a faixa atual e as entradas sob o mesmo lock; renderer trata esse snapshot como somente leitura. A persistência de sessão já transporta os campos do Track.
6. `media.ArtworkURL` prioriza URL Spotify válida (inclusive a restaurada do catálogo), depois thumbnail YouTube explícito, depois thumbnail derivado de VideoID válido. Sem URL, usa fallback textual.
7. Agora a Section da atual recebe essa URL no Thumbnail, sem exclusão por posição.

Não foi criado outro cache. `/queue` faz **zero consultas SQLite e zero buscas externas**; também não tenta reparar sessões antigas consultando o catálogo durante a interação. Se o Track já perdeu todos os metadados de artwork, o renderer não consegue reconstruir uma capa Spotify; usa YouTube quando possível. URL sintaticamente válida não garante que o CDN continuará entregando a imagem.

A duração é exclusivamente `Track.Duration` total, nunca offset/seek ou tempo decorrido. Valores positivos usam MM:SS ou HH:MM:SS; valores não positivos mostram `Duration unavailable`. Artista usa `Artist`, com `Channel` como fallback existente, e é omitido quando ambos faltam.

## Estruturas e custo

Reutilizado o snapshot consistente existente, sem novo modelo de estado mutável. Não foi introduzida uma segunda estrutura de apresentação: cada linha é preparada uma vez e sua URL é validada uma vez pelo renderer. Slices de corpo e de linhas têm pré-alocação limitada; cauda usa `strings.Builder`, evitando copiar repetidamente todo o texto acumulado. A contagem UTF-16 percorre a string diretamente, sem construir slices de runes/UTF-16. Componentes e caracteres são contabilizados incrementalmente, recalculando somente o rodapé candidato para reservar seu tamanho exato.

No perfil curto de 1.000, construção A/C: aproximadamente 191/93 µs e 2.679/1.772 alocações; serialização A/C: 34/37 µs e 147/166 alocações. No misto de 1.000: construção 144/77 µs, 1.736/1.235 alocações; JSON 6.835/7.111 bytes. A construção ficou mais barata nesta amostra, enquanto o JSON maior e as duas capas adicionais aumentam o custo de serialização. A quantidade de linhas processadas também é menor em C, portanto esses números não isolam um ganho por faixa. Não houve mudança em SQLite por já estar fora desse caminho. Evitou-se pool, cache duplicado, apresentação persistente e alteração ampla em `player.State` sem medição específica que justificasse isso.

## Contratos, visual e validação

`/queue` sem flags permanece válido; limit/from/to continuam opcionais, inclusivos e sujeitos à mesma validação. A fila real não é alterada. A resposta continua exclusivamente por PATCH da mensagem original, sem follow-up, arquivo, paginação ou deploy. O rodapé continua exibindo posições mostradas, restantes e próxima posição. Uma faixa que não cabe inteira encerra a seleção; títulos enormes não são substituídos por metadados inventados.

O cliente Discord controla altura de Thumbnail, largura, quebra de linhas, fontes e espaçamento de Section. Uma capa não garante três linhas de altura uniforme. Títulos longos podem ocupar várias linhas em celular. A cauda tem a mesma tipografia com uma linha vazia entre faixas, mas sem imagens adicionais. Preservados o acento dourado e AllowedMentions vazio. Não houve inspeção visual ao vivo, e a validação local não promete equivalência pixel a pixel entre clientes.

Validação solicitada: suíte completa `go test ./...`, race detector `go test -race ./...`, `go vet ./...` e `git diff --check`. Todos passaram (race: player 61,5 s, discord 31,7 s; demais pacotes também aprovados). Não foi iniciada campanha de QA/soak, fuzzing prolongado ou deploy.
