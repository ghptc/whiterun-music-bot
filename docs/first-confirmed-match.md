# First Confirmed Match Wins

Mudança de 18/10/2026, comparada ao baseline `2924731`. O primeiro candidato que
atinge `MATCH_CONFIRMED` encerra a resolução. A política não espera os demais
uploads, não compara a margem de pontuação e não reabre uma escolha confirmada.

## Fluxo e confirmação

A consulta Music vem primeiro. Cada resumo é avaliado na ordem recebida; quando
não há incompatibilidade explícita, o resolver extrai os metadados completos do
vídeo exato e executa `CompareSpotify`. Rejeitado: continua. Incerto: continua.
Confirmado: retorna imediatamente, persiste o mapeamento e não inicia outra busca
ou verificação. Sem confirmação, amplia as consultas para busca normal, official
audio e Topic. IDs duplicados reutilizam a extração anterior, inclusive falhas.

`MATCH_CONFIRMED` exige todos os critérios de título, artista principal, créditos,
versão, duração, disponibilidade e ausência de contradições conhecidas de ISRC e
explicit. Além desses critérios, agora exige proveniência independente: ISRC válido
igual ao solicitado ou fonte compatível de artista verificado, Topic, VEVO ou
lançamento distribuído. Um título/score alto, descrição com “official” ou nome de
artista copiado em canal desconhecido não confirmam a gravação. Fonte oficial
não anula versão, créditos ou artista incompatíveis. As regras detalhadas estão
na [política de identidade](spotify-fidelity.md).

O score mínimo preexistente continua sendo um filtro auxiliar; nenhum novo limiar
percentual foi introduzido. Não se usa o score para escolher entre candidatos já
confirmados. `MinScoreMargin` é configuração legada sem efeito na seleção.

A comparação é individual, com a identidade Spotify solicitada. Se dois vídeos
oficiais satisfazem esses critérios, o primeiro vence. Um ISRC diferente do
solicitado é rejeitado. Quando o Spotify não fornece ISRC, um identificador diferente
em outro upload ainda não visitado não causa veto retroativo. Isso não afirma
que uploads são equivalentes acusticamente. A checagem de equivalência existente
continua como diagnóstico testado, fora do caminho de seleção.

O My House do corpus passa a selecionar `uo35R9zQsAI`, vídeo oficial de Flo Rida,
196 s contra 192,190 s no Spotify (delta 3,810 s), que atende à política de duração
preexistente e aos demais critérios individuais. O baseline continuava procurando
até o lançamento auto-gerado `z9boCCK7M8c`. A nova seleção é deliberada: primeiro
confirmado, sem preferência posterior por outro upload. Não houve comparação
acústica ou afirmação de identidade exata de edição além dos metadados disponíveis.

## Limites, concorrência e estado

Foram removidos os cortes de quatro verificações por rodada e oito por resolução.
O teste com confirmação na 13ª extração passa; o baseline para antes e falha.
Permanecem os limites operacionais: plano de quatro consultas, até dez resumos por
consulta, deadline global de 90 s, 15 s por extração, limite global de quatro
resoluções, reserva de capacidade para a primeira faixa e prefetch de seis posições.
Uma busca sem confirmação pode examinar até 40 IDs recuperados, se couber no prazo.
A exaustão desse plano não prova inexistência da gravação em todo o catálogo.

As verificações de uma mesma faixa são sequenciais: não há subprocessos especulativos
concorrentes para abandonar depois da confirmação. Cada `cmd.Run` é aguardado
antes do próximo candidato. Cancelamento do contexto usa o mecanismo existente
que mata o grupo de processos; `Wait` recolhe o processo iniciado. O teste novo
cancela uma extração real bloqueada e verifica que o processo foi recolhido e a
goroutine retornou. As regressões de streaming também cobrem auxiliares, EOF,
cancelamento e limpeza da pipeline. Nenhum worker novo foi criado.

Resoluções de outras posições continuam concorrentes e independentes. O coordinator
atualiza slots existentes por collection ID/índice, publicando prefixos ordenados.
`PENDING → RESOLVING → READY` preserva Spotify ID e posição original, mesmo quando
um resultado posterior termina primeiro. Nenhum resultado é anexado ao fim da fila
para substituir sua posição. Skip/cancelamento remove o slot e impede ressurreição.
O singleflight preserva o trabalho enquanto outro solicitante ainda o necessita;
quando todos cancelam, cancela o contexto compartilhado.

O primeiro mapeamento validado é salvo no SQLite antes de retornar o resultado.
Chaves passam à versão 4 para impedir reuso automático de decisões da política
anterior. Retenção de 24 h e reuso de prova fresca por dez minutos continuam.
Revalidação expirada verifica o vídeo salvo; não compara novamente outros uploads.
O teste de integração resolve, verifica o mapeamento no SQLite, torna o executável
indisponível e reutiliza o mesmo ID em outra posição da playlist, sem subprocesso.

## Medições antes/depois

Fixtures reais previamente capturadas, sem rede. Logs brutos em
[metrics/first-confirmed](metrics/first-confirmed).

| Faixa | Buscas/verificações no baseline | Atual |
| --- | ---: | ---: |
| Little Bad Girl | 2/4 | 1/1 |
| Whistle | 1/1 | 1/1 |
| My House | 4/5 | 2/1 |
| Pumpkin Edit | 1/1 | 1/1 |
| Total | 8/11 | 5/4 |

Redução no replay: 37,5% das buscas e 63,6% das verificações. Os quatro casos
positivos continuam aceitos; o ID de My House muda como explicado acima. Não há
falso positivo conhecido nos testes adversariais executados. Isso não estima a
taxa em produção nem comprova identidade acústica.

No teste controlado com duas fontes oficiais e atraso simulado de 10 ms por
operação, o baseline fez duas buscas/duas verificações, levou ~41 ms e selecionou
o segundo upload. A versão nova faz uma busca/uma verificação, leva ~20 ms e
seleciona o primeiro. O teste idêntico falha no baseline e passa agora. Esses tempos
medem metadados simulados, sem áudio ou serviço externo.

Microbenchmark do seletor, mesmo pool oficial nos dois checkouts, três execuções,
Go 1.27/Linux/AMD Ryzen 7 9800X3D:

| Métrica | Baseline | Atual |
| --- | ---: | ---: |
| ns/op, mediana | 424.775 | 98.205 |
| bytes/op, aproximadamente | 718.000 | 167.000 |
| alocações/op | 11.136 | 2.633 |

~77% menos tempo e bytes alocados nesse cenário sem I/O. Cache quente continua
com zero verificações externas de metadados dentro da validade; a extração de
formatos para áudio ainda ocorre. Não foram medidos `/play → primeiro frame` real,
rede, intervalo real entre faixas, CPU/RSS sob tráfego ou E2E no Discord nesta
mudança. Não houve deploy. Disponibilidade externa impede prometer início instantâneo.

## Regressões e reprodução

Novos testes: primeiro confirmado encerra imediatamente, primeiro incerto continua,
primeiro errado/cover/remix com mesma duração é rejeitado, score alto sem proveniência
não confirma, dois vídeos oficiais compatíveis selecionam o primeiro em ambas as
ordens, confirmação na 13ª verificação, cancelamento após verificação impede publicar
o resultado, cancelamento de subprocesso real e persistência/reuso do primeiro ID.

Permanecem as regressões de artistas semelhantes, convidados ausentes, ISRC/explicit
contraditórios, versões adversariais, ordem de resoluções concorrentes, playlist de
123 posições, singleflight com cancelamento independente e callbacks atrasados.
Testes antigos que exigiam ambiguidade entre uploads agora verificam separadamente
que falta de prova de equivalência não veta a confirmação individual.

Para repetir a comparação sem alterar o checkout atual:

```sh
baseline_dir=$(mktemp -d /tmp/bard-first-confirmed.XXXXXX)
git archive 2924731 | tar -x -C "$baseline_dir"
cp internal/media/first_confirmed_test.go "$baseline_dir/internal/media/"
(cd "$baseline_dir" && go test ./internal/media -run 'TestFirstConfirmed|TestTwoOfficial|TestFirstConfirmationAfterThirteenCandidates' -count=1 -v)
# Esperado: falha por continuar após confirmação, ambiguidade e corte de candidatos.
go test ./internal/media -run 'TestFirstConfirmed|TestTwoOfficial|TestFirstConfirmationAfterThirteenCandidates' -count=1 -v
# Esperado: passa.
go test ./internal/media -run '^$' -bench BenchmarkFirstConfirmedOfficialPool -benchmem -count=3
# Mesmo comando no baseline com o arquivo copiado para comparar o microbenchmark.
```

Validação executada e aprovada: `go test ./... -count=1`,
`go test -race ./... -count=1` e `go vet ./...`. Os resultados dos testes estão
registrados junto às métricas; vet terminou com exit 0 e sem diagnósticos.
