# Spotify → YouTube: identidade antes de disponibilidade

O Spotify determina título, artistas, álbum, duração, ID, ISRC e explicit quando
fornecidos. YouTube fornece somente VideoID, URL canônica e canal. A duração do
vídeo não substitui a duração original. SourceURL conserva a origem Spotify.
Não há inferência de ISRC a partir de texto de descrição, selo ou popularidade.
A API fornece uma lista de artistas; papéis de principal/convidado não são
inventados quando a fonte não os distingue. Artista principal é a primeira
entrada, os demais créditos são preservados e comparados. Variantes são extraídas
do título/álbum sem alterar os textos originais. Explicit ausente é desconhecido,
representado por nil, não false.

## Decisões

- `MATCH_CONFIRMED`: comparação completa atende às regras abaixo. É uma decisão
  baseada em metadados, não prova de identidade acústica.
- `MATCH_UNCERTAIN`: faltam evidências verificáveis ou os melhores candidatos
  verificados têm pontuações próximas. Não pode ser reproduzido.
- `MATCH_REJECTED`: incompatibilidade relevante, indisponibilidade, ausência de
  candidato aceitável ou configuração inválida. Não pode ser reproduzido.

`CompareSpotify` é determinístico e independente da posição do resultado.
`MatchError` transporta estado e razão e mantém `errors.Is(ErrSpotifyMatch)`.
O Discord apresenta uma mensagem de ausência de correspondência confiável, sem
expor detalhes internos. Logs operacionais registram candidatos, estágio,
pontuação, duração e razões. `/queue` mostra o resultado da última importação.

## Regras e configuração

`DefaultMatchPolicy()` é usado na ausência de configuração. `SPOTIFY_MATCH_POLICY`
aceita um objeto JSON parcial sobre esses defaults; valores inválidos impedem o
startup. Exemplo de política mais estrita:

```sh
SPOTIFY_MATCH_POLICY='{"MinTitlePrecision":0.85,"MaxDurationDelta":10000000000,"MaxDurationFraction":0.04,"MinScore":700,"MinScoreMargin":30}'
```

`MaxDurationDelta` é `time.Duration` serializado como nanossegundos (10s no
exemplo). Não é uma probabilidade. Os defaults e regras são:

| Critério | Regra |
| --- | --- |
| Normalização | Unicode NFKD, remoção de diacríticos, case e pontuação; aliases feat/ft/featuring |
| Cobertura do título | Todos os tokens semânticos solicitados presentes |
| Precisão do título | Pelo menos 0,65 dos tokens do candidato explicados pelo título, após remover artistas e rótulos de apresentação |
| Artista | Identidade normalizada exata nos artistas estruturados; fallback a identidade inteira do canal/uploader ou prefixo `Artista - Título` |
| Participações | Todos os créditos solicitados precisam de evidência; múltiplos créditos sem lista estruturada completa são incertos; artistas estruturados não podem contradizer os créditos |
| Versões | Compatibilidade bilateral para live/concert, acoustic/unplugged, remix, remaster, instrumental, karaoke, slowed, reverb, sped up, nightcore, cover, mashup, 8d, bass boosted |
| Outros falsos positivos | Rejeitar reaction, tutorial, interview, full album, compilation, documentary, medley, extended e edit não pedidos |
| Duração conhecida | Delta máximo `max(15s, duraçãoSpotify × 0,08)`; duração do candidato ausente torna a decisão incerta |
| Duração Spotify ausente | Título/artistas/versão ainda obrigatórios; candidato acima de 20 minutos é rejeitado; não se inventa uma duração Spotify |
| ISRC / explicit | Se ambos os lados fornecem, divergência rejeita; ausência não é evidência positiva |
| Pontuação | Título ×400, artista ×200, créditos ×100, penalidade de imprecisão, duração e bônus de fonte; mínimo 650 |
| Ambiguidade | Diferença entre primeiro e segundo candidatos verificados inferior a 20 impede a seleção |

Bônus de canal oficial/topic/VEVO apenas ordena candidatos que já passaram pelos
critérios de identidade. Visualizações não são utilizadas. Remaster não é um
substituto automático do original: o rótulo é bilateral; anos e nomes de remix
continuam sendo tokens obrigatórios. Letras de nomes artísticos, como `2 Live
Crew`, são removidas do texto de versão antes de classificar a variante.

As pesquisas Music e YouTube normal são combinadas e deduplicadas por VideoID.
A verificação completa contempla no máximo quatro candidatos, reservando espaço
para os dois pools. Não é garantia de encontrar todos os candidatos existentes.
Candidatos não verificados não são autorizados. Falha na verificação de um
candidato plausível gera incerteza com a causa original preservada, inclusive
para suspender a fila em bloqueios globais do provedor. A identidade também é aplicada
quando a resolução recebe diretamente uma URL de vídeo.

## Integridade de coleções

API autenticada percorre páginas de até 50 itens, até o limite operacional de 500
posições. Paginação usa endpoints construídos localmente e não encaminha tokens
para `next`. Falha de rede ou contagem inconsistente impede publicar uma coleção
parcial como completa. O fallback público tem verificações próprias de
completude; não aceita truncamento silencioso.

Ordem e ocorrências repetidas são preservadas. Na API, posição e total mantêm a
numeração original inclusive quando há itens nulos, locais ou indisponíveis.
Esses itens são contados como indisponíveis; não se inventa um ID para um item
nulo. Reservas internas usam índices separados da numeração original.
Resoluções concorrentes são associadas ao índice que as originou; commit não
adiciona uma resolução atrasada ao fim da fila. Entradas rejeitadas/incertas
são removidas, sem transferir metadados para outra música. O número de posições
não reproduzíveis não é escondido pelo resumo.

`/play` confirma a primeira faixa aceita e informa resolução em andamento.
`/queue` mostra progresso e, após conclusão, matched, uncertain, rejected,
unavailable e skipped da última importação. O resumo é limitado à sessão;
os metadados/fila/histórico são persistidos. Cancelamentos podem deixar posições
não concluídas, identificadas nos logs. Não há spam de mensagens por candidato.

## Mapeamentos duráveis e reprodução

SQLite guarda o candidato validado por hash da identidade completa, política e
versão do algoritmo. Solicitante e posição não fazem parte da chave. A validade
é de 24 horas. Um hit sempre consulta novamente o mesmo VideoID e compara
metadados atuais. Ausência, erro ou inconsistência invalida o mapeamento; uma nova
busca precisa satisfazer os mesmos critérios. Trocar a política muda a chave.
O cache de busca em memória expira em dez minutos e copia campos mutáveis.

Antes de iniciar áudio Spotify, `Bot.play` revalida o VideoID exato com deadline
de 45 segundos, inclusive após recuperação e cache em memória. Falha impede
streaming e invalida o mapeamento; não se troca o vídeo silenciosamente. Faixas
pendentes recuperadas são resolvidas antes da reprodução. URLs assinadas de áudio
não são persistidas.

## Limitações verificáveis

Metadados podem ser incompletos ou falsos. Um upload sem rótulo de cover e com
metadados copiados pode enganar comparação textual. Não existe fingerprinting
acústico ou prova criptográfica da gravação. A ausência de ISRC/explicit na fonte
de áudio não comprova igualdade desses atributos. Nenhum teste local comprova
copyright, disponibilidade futura ou áudio audível no Discord real.
