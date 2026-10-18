# Investigação de falsas rejeições Spotify → YouTube

Investigação em 2026-10-18. Resultado parcial: corrigida a ambiguidade entre
uploads com evidência suficiente de equivalência; o incidente histórico de
Whistle não foi reproduzido e My House continua incerta. Não há validação no
Discord, nem prova acústica. Os critérios de aceite relativos à solução dos dois
casos reais **não estão integralmente atendidos**.

## Fontes e reprodução

Os dois IDs foram consultados no endpoint autenticado Spotify `/v1/tracks/{id}`
com as credenciais já configuradas, sem exportar tokens. Também foram consultadas
as páginas públicas e embeds. API e embed concordaram em título, artista e duração.
As fixtures Spotify conservam os campos de metadados da resposta da API, incluindo
`external_ids.isrc`; ausência de ISRC nos vídeos não foi preenchida por inferência.

- [Whistle no Spotify](https://open.spotify.com/track/3bC1ahPIYt1btJzSSEyyrF)
- [My House no Spotify](https://open.spotify.com/track/6Knv6wdA0luoMUuuoYi2i1)

YouTube e Music foram consultados via imagem local `whiterun-music-bot-bard:latest`,
yt-dlp **2026.08.19**, sem iniciar o bot, gateway ou áudio. Cada subprocesso de
captura teve timeout de 40 s, socket timeout de 8 s e zero retries configurados.
Foram feitas oito consultas investigativas: Music e YouTube para cada faixa,
alternativa `Artista - Título official audio` para cada faixa, consulta de álbum
para My House e consulta `"My House" "Flo Rida" "Provided to YouTube"`. As
extrações completas se limitaram a 17 IDs distintos. As capturas Music retornaram
centenas de resumos; somente os primeiros dez foram preservados/usados no replay,
conforme o limite de produção `--playlist-end 10`. Consultas investigativas e
orçamento de produção são distintos. Os resultados mudam com data, região e IP.

As fixtures sanitizadas estão em `internal/media/testdata/fidelity/`: dois
metadados Spotify, pools de descoberta e metadados completos por VideoID. As
respostas não contêm tokens, headers ou URLs de áudio. Descrições arbitrárias
foram removidas; atribuições de lançamentos auto-gerados foram preservadas.
O parser de produção interpreta as fixtures Spotify. Não se inventaram campos
ausentes dos vídeos. `go test ./internal/media -run TestRealSpotify -v` reproduz
as decisões e mostra razões e scores de descoberta e verificação por candidato.
O replay executa o seletor real, mas não chama a rede nem o Discord.

Não havia neste checkout um registro histórico dos pools, scores, versão do
binário ou política que produziram as duas mensagens originais. Portanto a causa
exata do evento antigo não pode ser reconstruída a partir de IDs e mensagem.

## Metadados Spotify

| Posição informada | Spotify Track ID | Título | Artista/créditos | Álbum | Duração | ISRC | Versão |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 5 | `3bC1ahPIYt1btJzSSEyyrF` | Whistle | Flo Rida | Wild Ones | 224653 ms | USAT21201745 | Sem qualificador de variante no título/álbum |
| 6 | `6Knv6wdA0luoMUuuoYi2i1` | My House | Flo Rida | My House | 192190 ms | USAT21500384 | Sem qualificador de variante no título/álbum |

Os artistas estruturados têm somente Flo Rida. Não há papéis de crédito
inventados. A versão é uma interpretação dos qualificadores, não um campo de
identidade acústica fornecido pelo Spotify.

## Whistle: gravação confiável pelos critérios de metadados

Selecionado: [MOIwxOYdhrk](https://www.youtube.com/watch?v=MOIwxOYdhrk).
O vídeo é um lançamento auto-gerado atribuído a Poe Boy/Atlantic; canal Flo Rida
verificado, `track=Whistle`, `artists=[Flo Rida]`, `album=Wild Ones`, 225 s,
sem marcadores de variante conflitantes. Delta Spotify: aproximadamente 347 ms.
ISRC e explicit do vídeo não foram fornecidos e permanecem desconhecidos.

| Candidato verificado | Descoberta | Verificação | Resultado |
| --- | --- | --- | --- |
| MOIwxOYdhrk | 650, resumo Music sem duração/créditos | 839 | Aceito, lançamento correspondente |
| t8fTSl2wItI | 650, resumo Music com mesmo título | Rejeitado | Artista real MI AMI; `primary_artist_mismatch` |
| cSnkWzZ7ZAA | 799, 235 s | 803, 234 s | Aceito individualmente; vídeo oficial com duração diferente |
| 0qZwFWyPKzQ | 771, 227 s | 775, 226 s | Aceito individualmente; upload sem autoridade oficial |

A margem entre 839 e 803 é 36, acima de 20. **O seletor anterior também aceita
esse pool atual**: não atribuímos essa aceitação à correção nova. O erro histórico
`ambiguous_candidates` de Whistle não se reproduziu nesta captura. A pesquisa
Music incluiu ainda títulos diferentes, karaokês e versões que foram eliminados
antes de verificar. O caso MI AMI demonstra por que aceitar um resumo de busca
seria incorreto.

## My House: lançamento correspondente encontrado, seleção ainda incerta

Encontrado: [z9boCCK7M8c](https://www.youtube.com/watch?v=z9boCCK7M8c), lançamento
auto-gerado, canal Flo Rida verificado, `track=My House`, `artists=[Flo Rida]`,
`album=My House`, 192 s. Delta aproximado de 190 ms. É uma evidência forte de
lançamento compatível, sem ISRC exposto pelo YouTube. **Nenhum vídeo foi autorizado
pelo seletor**: a ambiguidade permanece e não foi escondida pela fixture.

| Candidato | Descoberta | Verificação | Evidência/limitação |
| --- | --- | --- | --- |
| taXoKRkQlg8 | 837, 193 s | 840, 192 s | Canal Flo Rida verificado; sem track, álbum e artistas estruturados |
| uo35R9zQsAI | 821, 197 s | 825, 196 s | Vídeo oficial, canal verificado; sem identidade estruturada da gravação |
| TuxMwALL_S4 | 773, 194 s | 777, 193 s | Canal Charted; título diz Official Audio, sem créditos estruturados |
| RMmEtcIW_WA | 767, 193 s | 770, 192 s | Lyric video, sem créditos estruturados |
| z9boCCK7M8c | Encontrado na busca por lançamento | 840, 192 s | Metadados completos do lançamento compatível |
| Bfldb-u4Vb4 | Mesmo título na busca por lançamento | Rejeitado | Artista Mr. Miami, álbum Basketball Finals 2016 Playoffs |

Os quatro primeiros integram a shortlist inicial real. A diferença 840−825=15
reproduz `ambiguous_candidates`: os dois melhores passaram individualmente,
mas IDs distintos foram tratados como alternativas de gravação. A quarta busca
progressiva acrescenta z9boCCK7M8c, empatado em 840. Os artistas e álbuns ausentes
nos outros vídeos não foram preenchidos usando esse lançamento. O vídeo oficial
de 196 s também não foi declarado equivalente ao áudio de 192 s com base apenas
na duração tolerada pela comparação individual.

A consulta Music de My House retornou nos dez primeiros itens músicas diferentes,
inclusive de 2PM. A consulta alternativa por official audio não resolveu; a
consulta por álbum também não encontrou um lançamento novo decisivo. A consulta
por atribuição de distribuição encontrou o lançamento acima. Isto confirma uma
limitação de descoberta além do problema de decisão.

## Causa e hipótese

O código anterior compara o primeiro e segundo **VideoID** aceitos. Quando a
margem é menor que 20, rejeita inclusive dois uploads da mesma gravação com
metadados suficientes. Isso é um defeito demonstrado por regressões controladas:
os mesmos candidatos equivalentes antes geravam ambiguidade, agora formam um
grupo e são aceitos. Nenhum threshold foi reduzido.

Para My House foi reproduzida a mecânica da ambiguidade, mas não comprovada a
identidade acústica entre os vídeos concorrentes. Para Whistle não foi reproduzido
o incidente. Assim, a hipótese prioritária está **validada como falha de modelagem**
e **não comprovada como explicação acústica dos dois eventos históricos**.
A presença de vídeos oficiais, letras ou territórios diferentes não é prova
isolada de equivalência. Os ISRCs Spotify foram recuperados; nenhum dos vídeos
capturados forneceu ISRC verificável para demonstrar igualdade entre uploads.

## Modelo implementado

1. Cada vídeo ainda precisa passar por todas as verificações completas existentes:
   título, precisão, artista, participações, duração, variantes, disponibilidade,
   ISRC e explicit quando fornecidos. A versão pode excluir o vídeo antes de agrupar.
2. A equivalência exige duração conhecida com diferença de até **2 s** e ausência
   de ISRCs contraditórios. Esse limite é mais estrito que a tolerância individual;
   absorve arredondamento e pequenos limites de contêiner, sem autorizar introduções
   ou edições maiores como equivalentes.
3. Evidência suficiente: ISRC válido compartilhado (12 caracteres), ou **ambos**
   os vídeos de fonte compatível oficial (artista verificado, Topic, VEVO ou
   lançamento auto-gerado), ambos com track exata, álbum igual ao Spotify e mesmos
   artistas estruturados. Um remaster sem ISRC compartilhado não usa o segundo caminho.
4. O agrupamento é de ligação completa: cada membro precisa ser compatível com
   todos os outros. A~B e B~C nunca apagam uma contradição A/C.
5. A margem existente é aplicada entre os melhores representantes dos grupos.
   Dentro do grupo, seleciona-se o maior score existente (inclui autoridade e
   duração); empate usa VideoID lexical para tornar a decisão reproduzível.
   Visualizações e ordem dos resultados não desempatarão os membros verificados.
6. Metadados copiados continuam sendo uma limitação. Esse agrupamento é uma
   decisão corroborada de metadados; não certifica identidade acústica.

A versão da chave de mapeamento passou a 2; a revalidação por vídeo e antes de
reprodução continua obrigatória. Não foram alterados índices, reservas, commits
de fila ou ordem da playlist.

## Busca progressiva e limites

A descoberta começa com Music e YouTube. Em caso de rejeição/incerteza, tenta
`Artista - Título official audio` e depois `"Título" "Artista" "Provided to YouTube"`.
Álbum e créditos são usados na verificação e equivalência; não se confia no texto
de atribuição da pesquisa como identidade. Cada pool é limitado a dez resultados,
deduplicados por VideoID. Até quatro candidatos são considerados por rodada,
reservando espaço para Music; metadados completos são reutilizados somente dentro
da resolução atual. Máximo: quatro consultas de descoberta, oito verificações
únicas, 15 s por extração e deadline global existente de 90 s. Ao estourar orçamento
de verificação, não se aceita candidato por falta de comparação.

Falhas de infraestrutura na verificação interrompem a progressão e preservam a
causa original. Falhas de busca não são transformadas em prova de ausência. O
resolver não adquire locks do player e mantém cancelamento do grupo de processos.
O player continua utilizando a orquestração assíncrona existente e os resultados
permanecem associados às posições originais. O limite de 90 s é de resolução,
não uma promessa de latência de importação de uma coleção inteira.

## Observabilidade e testes

Eventos novos/estendidos incluem Spotify Track ID, IDs dos candidatos, estágio,
razões de aceitação/rejeição, score, duração, classe da fonte, grupo, evidência de
equivalência, motivo final, VideoID selecionado, número de buscas/verificações e
tempo total. Falhas de verificação usam motivo fixo, sem imprimir erro bruto no
evento novo. São emitidos pelo Timing da requisição; chamadas sem Timing não
produzem esses eventos. Não se registram credenciais.

Regressões controladas cobrem: uploads oficiais/territórios diferentes; áudio e
lyric equivalentes; duração equivalente; mesmo título/artista com ISRC diferente;
metadados incompletos; autoridade ausente; remaster incerto; contradições
transitivas; live/remix/cover; busca inicial vazia seguida de sucesso; cancelamento;
falha de infraestrutura e limite de oito verificações. Os testes adversariais
existentes de versões, artistas semelhantes, convidados, duração e explicit
continuam aplicados. As duas fixtures reais cobrem tanto aceitação quanto
preservação da incerteza; não são fixtures sintéticas apresentadas como reais.

Validação local final: `go test ./...`, `go test -race ./...`, `go vet ./...`.
Os resultados concretos são registrados na entrega. Isso não equivale a testar
reprodução audível no Discord. Não houve publicação, restart do serviço ou teste
real de voz nesta investigação.

## Próximo passo para My House: verificação acústica opcional

Não implementada nesta alteração. Antes de autorizar a união dos grupos, comparar
o áudio dos uploads de 192 s e do vídeo de 196 s com alinhamento temporal,
permitindo somente silêncio/introdução fora do segmento musical. Uma impressão
Chromaprint pode auxiliar, mas comparação local de fingerprints exige alinhamento,
calibração e exemplos negativos (covers, versões ao vivo, clean/explicit,
remasters). Score alto isolado não comprova identidade. AcoustID tampouco garante
identificação para todo o catálogo ou resolve automaticamente remasters.

Custo: downloads adicionais e decodificação por FFmpeg, CPU, armazenamento
temporário e latência. Não há benchmark local que justifique valores estimados de
custo/confiabilidade. É viável como diagnóstico offline primeiro, com corpus
rotulado e resultados mensuráveis, sem bloquear o player. Uma implementação futura
precisaria orçamento próprio, cache de evidência por vídeo, cancelamento, critérios
de rejeição e tratamento dos limites do provedor. Comparar dois vídeos entre si
prova no máximo semelhança entre eles; ligar ao Spotify exige referência confiável.
A prévia Spotify é parcial, pode estar ausente e não certifica a faixa inteira.

A redução demonstrada nesta entrega é a classe de falsos negativos de uploads
com evidência suficiente de equivalência nas regressões. **Não foi demonstrada
uma mudança de rejeitado para confirmado em nenhum dos dois pools reais atuais.**
Whistle já seria aceita; My House continua incerta. Resolver My House sem novas
evidências exigiria relaxar a decisão, o que esta alteração não faz.
