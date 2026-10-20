# PERF-017 — investigação do /queue

Data: 2026-10-20. Base: `498e5e4`, working tree inicialmente limpo; fetch confirmou HEAD igual a origin/main. Sem deploy ou alteração de bancos originais.

## Evidência e causa das capas

Inspeção de cópias dos dois SQLite do volume local, montado somente para leitura, com serviço parado. Catálogo: 476 identidades Spotify, 143 sem artwork Spotify. As quatro ocorrências relatadas estão sem artwork tanto no catálogo como nos registros pendentes de `tracks`:

| Faixa | Spotify ID | Posição original |
|---|---|---:|
| Flo Rida — GDFR | 26rdOwwjC2UnweK3xeS58u | 8 |
| Public Enemy — Bring The Noise Remix - Pumpkin Edit | 3SMVk8c3YOVv6DcTn8ABXa | 9 |
| 2 LIVE CREW — We Want Some P--sy | 5DGKZEzZho3ZZWkYPIiCyC | 10 |
| Wale — Pretty Girls | 2UnhwzQ50JjvUt1a5EKcou | 12 |

A playlist pública afetada é `35m4wmHSlcS2RNsafX4dEg`. Verificação de seu HTML e Embed em 20/10:

* O HTML público contém 30 linhas e fontes de capa por identidade em `initialState.entities.items[playlist].content.items[].itemV2.data.albumOfTrack.coverArt.sources`.
* O Embed contém 100 faixas, mas as entradas `trackList` consultadas não têm `imageUri`. A capa da playlist não representa cada faixa.
* O fallback anterior usava initialState somente para validar contagem e prefixo, descartando as capas. O catálogo preservou essa ausência. Enquanto a faixa está pendente e não tem VideoID, o fallback YouTube também não é possível.
* A página individual de GDFR fornece imagens em `visualIdentity.image`, não em `coverArt.sources`. O parser individual anterior ignorava esse formato, afetando também a hidratação do restante da playlist.

Correção: aproveitar as capas do prefixo público já baixado, somente pelo Spotify ID exato, após validar a coleção completa. Aceitar `visualIdentity.image` no parser individual quando `coverArt.sources` não fornecer uma URL válida. `SafeArtworkURL` e `ArtworkURL` continuam centralizando validação/prioridade. Nenhuma chamada externa adicional, worker ou consulta SQLite foi acrescentada ao /queue.

A propagação permanece Spotify → Track da coleção → catálogo → reserva pendente → matching que preserva identidade/artwork → snapshot detached → `ArtworkURL` → Thumbnail Discord. Registros já persistidos sem capa não são reparados retroativamente; uma nova importação com o parser corrigido recupera as capas disponíveis. Faixas do meio do Embed sem imagem e fora do prefixo público ainda podem depender do catálogo ou do fallback YouTube após matching. Não inventamos capas para essas faixas.

O log do /queue afetado mostrou 71 posições, 55 sem fonte de artwork; isso distingue ausência de metadados de esgotamento de componentes. Não há captura do payload histórico nem prova de rejeição de URL pelo Discord. Após o orçamento de seções, capas são intencionalmente omitidas mesmo quando a URL existe.

## Posição 71 e caracteres quadrados

A coleção de 123 tem `희망` (título), `박세아` (artista), Spotify `78RBvzlMOEP3llq5a47sop`, duração 258234 ms = **04:18**, na posição original 72. Com a primeira faixa tocando, ela ocupa a posição 71 da fila. O banco contém também sua versão resolvida para YouTube `ZbBJmA5lCaw`, com o mesmo título/artista e canal `박세아 - Topic`.

São caracteres Hangul legítimos, preservados no catálogo e nos registros pré/pós-matching. Não há evidência de UTF-8 inválido, U+FFFD ou corte de surrogate nessa faixa. O renderer percorre runes para contar UTF-16, e o JSON de uma reprodução da posição 71 conserva título, artista e duração. Não existe payload histórico armazenado para comparar byte a byte, nem acesso ao cliente Discord que mostrou quadrados. A evidência é compatível com falta de glifos/fallback de fonte no cliente; não permite afirmar qual fonte falhou. Nenhuma conversão para ASCII ou substituição de caracteres foi aplicada.

Regressão nova exercita posição 71, JSON UTF-8, título Hangul, artista e 04:18. Os dados originais permanecem intactos.

## Renderer, orçamento e medições

Mantidos: uma mensagem, PATCH da resposta original, sem follow-ups, argumentos opcionais limit/from/to, Now Playing com artwork quando disponível, título/artista/duração em linhas separadas, capas dentro de 40 componentes e restante compacto em até 4000 unidades UTF-16. Contagem de posições restantes inclui tudo que ficou fora do intervalo exibido. A suíte existente cobre limites e entrega.

Três execuções por tamanho, Ryzen 7 9800X3D, Go local, fixture existente. Tempo é média por operação do benchmark, não percentil. Serialização medida separadamente para obter bytes; tempo/alocações abaixo são do renderer.

| Entradas | Tempo por render | Bytes alocados/op | Alocações/op | Payload JSON |
|---:|---:|---:|---:|---:|
| 1 | 1,29–1,31 µs | 1800 | 27 | 513 B |
| 10 | 9,19–9,20 µs | 12862 | 189 | 2390 B |
| 20 | 17,7–17,9 µs | ~24700 | 342 | 3303 B |
| 123 | 60,4–60,8 µs | ~102390 | 1093 | 6394 B |
| 500 | 61,9–62,4 µs | ~103629 | 1241 | 6395 B |
| 1000 | 62,9–63,4 µs | ~103762 | 1241 | 6396 B |

Antes: 123 entradas, 61,4–63,9 µs, ~102386 B/op, 1093 alocações. Não houve alteração do algoritmo do renderer; variação de tempo é ruído, não ganho reivindicado. O tamanho serializado inclui estrutura de componentes e URLs, por isso pode ultrapassar 4000 bytes sem exceder o orçamento textual. Evidência: `../performance/evidence/perf017-queue.txt`.

Não foi necessário gastar legibilidade para obter mais imagens nem introduzir consultas por linha. As correções ficam na origem dos metadados.

## Validação e limites

Regressões adicionadas: capa pública por ID, fallback completo sem chamadas adicionais (duas requisições da fixture), novo formato individual e preservação Hangul no payload. A suíte geral também inclui QA-013, QA-014 e FIX-014 existentes, sem campanhas de endurance adicionais. Resultado global e limitações de medição real em `../performance/playback-startup.md`.
