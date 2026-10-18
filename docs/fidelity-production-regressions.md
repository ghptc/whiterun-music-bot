# Regressões de produção: Spotify → YouTube

Investigação em 18/10/2026. Hotfix baseado em metadados capturados de APIs reais;
sem teste de voz no Discord, comparação acústica ou garantia de fidelidade absoluta.

## Estado do runtime

`docker compose ps` e `docker ps -a` não mostraram Bardo em execução neste daemon.
Não foi possível comprovar a versão ativa de um serviço externo de produção.
A imagem local `whiterun-music-bot-bard:latest`, criada em 18/10/2026,
foi inspecionada sem iniciar o bot:

- Imagem: `sha256:21576e25acde3769612fe60f59b1603c13247f73806495641877b9b6b1fa8dd2`.
- Binário: SHA-256 `ddef0ff4d558c922cf34fc4d55b29af79d9bc314a52392fd5a7a35d616809eed`.
- Contém `spotify_recording_group` e `authoritative_release_credits_duration_version`.
  Portanto há evidência do agrupamento do hotfix anterior na imagem local, mas isso
  não comprova o commit exato nem seu uso em produção.
- Não contém `spotify_release_selected`, adicionado nesta alteração.
- As capturas usaram essa imagem com yt-dlp 2026.08.19. Nenhum restart/deploy foi feito.

## Evidência e reprodutibilidade

`internal/media/testdata/production/` contém metadados Spotify autenticados,
resumos reais dos dez primeiros candidatos por busca e extrações completas
sanitizadas. Foram preservados campos utilizados pelo matcher e descrições de
lançamentos auto-gerados; não foram preenchidos ISRCs/créditos ausentes, nem
salvos tokens ou URLs de áudio. As fixtures anteriores em `testdata/fidelity/`
continuam como uma captura independente: resultados de busca mudam.

Cada comando investigativo teve timeout de 40 s, socket timeout de 8 s, zero
retries e `--skip-download`. A investigação verificou os quatro primeiros
resultados de cada pool, deduplicando IDs, e depois os IDs adicionais efetivamente
requisitados pelo seletor. Isto é um corpus diagnóstico, não o custo de uma
resolução em produção. Nenhum áudio foi baixado.

Nomes de pools: `<SpotifyID>_music`, `_video`, `_alternative` e `_release`
representam as consultas iniciais e alternativas do hotfix anterior.
`_release_by` registra uma tentativa com a expressão completa de distribuição;
`_topic` registra a consulta final nova. Pumpkin tem `_production_music` e
`_production_video`, com Benny Benassi incluído por `spotifyQuery`; os pools
sem esse prefixo documentam a investigação inicial apenas com o artista principal.
`My_House_*` registra sondagens adicionais, incluindo a consulta por Atlantic,
usada exclusivamente para diagnóstico. Nenhum ID/gravadora foi fixado no código.

Replay do caminho de produção, com orçamento e IDs selecionados verificados:

```sh
go test ./internal/media -run TestProductionRegressions -v
```

## Resultados individuais

| Spotify Track ID | Identidade retornada pelo Spotify | Resultado anterior no mesmo replay¹ | Resultado novo | Buscas / verificações |
| --- | --- | --- | --- | --- |
| `55VzNhbcf0Gxf6qRCUAV01` | David Guetta — Little Bad Girl (feat. Taio Cruz & Ludacris) | Aceito | `SzVSTyxQ0rk` | 2 / 4 |
| `3bC1ahPIYt1btJzSSEyyrF` | Flo Rida — Whistle | `ambiguous_candidates` | `MOIwxOYdhrk` | 2 / 4 |
| `6Knv6wdA0luoMUuuoYi2i1` | Flo Rida — My House | `ambiguous_candidates` | `z9boCCK7M8c` | 4 / 5 |
| `3SMVk8c3YOVv6DcTn8ABXa` | Public Enemy, Benny Benassi — Bring The Noise Remix - Pumpkin Edit | Aceito | `55hB6dwU3yQ` | 2 / 1 |

¹ Baseline: exportação isolada de `81e10a5`, adicionando apenas o teste e as
fixtures novas. O stub de busca entrega os mesmos pools por etapa, inclusive o
pool Topic da quarta etapa; assim essa comparação isola a decisão do seletor.
Não mede a recuperação de rede da consulta anterior. Duas rejeições tornam-se
aceitações nesse corpus; nenhuma aceitação anterior torna-se rejeição. A captura
antiga de My House também passa de incerta para lançamento selecionado.

### Como repetir a comparação anterior/atual

A comparação foi repetida após a retomada, com o arquivo de teste final idêntico
nos dois diretórios e `-count=1` para impedir reuso do cache de resultados.
`TestProductionRegressions/3bC1ahPIYt1btJzSSEyyrF` e
`TestProductionRegressions/6Knv6wdA0luoMUuuoYi2i1` falharam no baseline com
`MATCH_UNCERTAIN: ambiguous_candidates` (quatro buscas/cinco verificações cada).
Os outros dois subtestes passaram. No workspace atual, os quatro passaram com
os IDs e contadores da tabela acima.

Para repetir sem modificar o checkout em uso, a partir da raiz do repositório:

```sh
baseline_dir=$(mktemp -d /tmp/bard-fidelity-baseline.XXXXXX)
git archive 81e10a5 | tar -x -C "$baseline_dir"
cp internal/media/production_regressions_test.go "$baseline_dir/internal/media/"
cp -R internal/media/testdata/production "$baseline_dir/internal/media/testdata/"
(cd "$baseline_dir" && go test ./internal/media -run TestProductionRegressions -count=1 -v)
# Esperado: exit 1; somente Whistle e My House falham por ambiguidade.
go test ./internal/media -run TestProductionRegressions -count=1 -v
# Esperado no hotfix: exit 0; os quatro IDs passam.
```

### ID reportado como Sexy Bitch

Há uma inconsistência no incidente: a API autenticada respondeu **Little Bad Girl**
para o ID informado. Não renomeamos a fixture para Sexy Bitch nem buscamos uma
substituição silenciosa. O caso Sexy Bitch por nome não está validado; é necessário
identificar seu ID/metadados originais para reproduzir essa identidade.

`SzVSTyxQ0rk`: lançamento auto-gerado de Universal Music Group, canal David Guetta
verificado, créditos David Guetta/Taio Cruz/Ludacris, 193 s (delta 724 ms).
Álbum YouTube TY.O difere do álbum solicitado; não recebe identidade exata de
lançamento por álbum. O seletor o aceita após verificação individual e rejeita
`kCsPOHn-bs0` por artista e `_ynF5yvMr58`/`-Gv6DMMHnME` por créditos desconhecidos.
Não há reprodução da ambiguidade reportada para esse ID nesta captura.

### Whistle

`MOIwxOYdhrk`: lançamento Poe Boy/Atlantic, track Whistle, artista Flo Rida,
álbum Wild Ones, 225 s, delta 347 ms, score 839. `tUEqnzSvCr4` tem mesmo score,
canal verificado e duração, mas não fornece identidade estruturada suficiente
para equivalência. `cSnkWzZ7ZAA`, vídeo oficial, tem 234 s, score 803.
`t8fTSl2wItI` é rejeitado por artista incompatível.

O empate entre os dois primeiros reproduz a ambiguidade com o código anterior.
Agora o lançamento identificado diretamente resolve a seleção, sem unir esses
vídeos em um grupo ou atribuir identidade acústica a eles. A captura anterior de
Whistle não continha esse concorrente empatado; por isso não reproduzia a falha.

### My House

`taXoKRkQlg8` (192 s, score 840), `uo35R9zQsAI` (196 s, 825), `TuxMwALL_S4`
(193 s, 777) e `RMmEtcIW_WA` (192 s, 770) passam individualmente. Faltam créditos
estruturados para unir os uploads e há diferença de duração no vídeo oficial.
A margem 840−825 continua insuficiente.

`z9boCCK7M8c`: lançamento auto-gerado Poe Boy/Atlantic, artista/track/álbum exatos,
192 s, delta 190 ms, score 840. A consulta Topic o encontrou nos dez primeiros.
A seleção utiliza a identidade desse lançamento, sem preencher metadados dos
outros vídeos. Buscas por atribuição, incluindo `Provided to YouTube by`, variaram
entre capturas e nem sempre o retornaram. A consulta Topic melhora descoberta
neste corpus, mas não promete resultado estável por região/data/IP.

### Bring The Noise Remix - Pumpkin Edit

`55hB6dwU3yQ`: lançamento auto-gerado IIP-DDS, canal **Channel ZERO** verificado,
track Bring The Noise Remix (Pumpkin Edit), créditos Public Enemy e Benny Benassi,
álbum Bring The Noise Remix, 217 s, delta 141 ms, score 835.

O pool investigativo inicial reproduziu `no_acceptable_candidate` sem sequer
verificar: o merge de resumos adicionava o canal distribuidor e a comparação
provisória o tratava como evidência de artista incompatível. A consulta exata de
produção com Benny Benassi já passou no baseline desta captura; não atribuímos
essa aceitação a uma correção que ela não precisava. O incidente histórico não
foi reconstruído integralmente sem seus pools completos.

A correção permite verificar resumos Music sem artistas estruturados mesmo com
canal distribuidor; o resultado completo continua obrigado a provar os créditos.
Fontes auto-geradas verificadas com track, álbum e artista estruturado compatível
são classificadas como lançamento mesmo que o canal seja do distribuidor.

`LcKwDrSsQc4` e `Q5Vl3mtjRLU` são versões Pumpkin Remix longas, não Pumpkin Edit.
`unq2DoBc9J0` tem título Benny Benassi Pump-kin Edit e duração compatível, mas não
foi declarado equivalente apenas por essa semelhança. As formas sem hífen,
com hífen separador e parênteses de **Pumpkin Edit** normalizam igualmente.
Remix/Edit solicitados não são proibições automáticas. Versões Sfaction,
instrumentais, lives e covers continuam incompatíveis.

## Causa raiz e processo progressivo

1. Comparação rigorosa de título, artista principal, colaboradores, versão,
   disponibilidade, duração e explicit/ISRC quando disponíveis permanece obrigatória.
   Resumos escassos autorizam somente extração completa, nunca reprodução.
2. O agrupamento completo ainda exige evidência entre todos os membros: ISRC
   compartilhado ou lançamento com créditos/álbum/track e duração corroborados.
   Títulos com `feat.` podem variar de apresentação quando os créditos estruturados
   permanecem compatíveis; artistas não são apagados da verificação de créditos.
3. **Identidade do lançamento solicitado é uma decisão distinta da equivalência
   entre uploads.** Um grupo contendo um lançamento exato pode vencer outros uploads
   genericamente compatíveis. Exige fonte oficial/verificada, delta até 2 s, ISRC
   correspondente ou track/álbum/créditos exatos e proveniência auto-gerada.
   Dois grupos com identidades de lançamento concorrentes continuam incertos,
   independentemente dos scores. Remasters sem ISRC não usam o caminho por álbum.
4. Persistindo ausência/ambiguidade, a busca progride para official audio e depois
   `"Título" "Artista" "Topic"`. O texto Topic serve somente à descoberta.
   Mantidos: quatro buscas, oito verificações únicas, quatro candidatos por rodada,
   dez resumos por pool, 15 s por extração e deadline global de 90 s. Sem novos
   processos de áudio. Cache dentro da resolução evita extrações repetidas.
5. Mapeamentos passam à versão 3 da chave; continuam sujeitos a revalidação no
   reuso e antes da reprodução. Nenhum threshold de score/margem foi reduzido.

Os grupos separados eram frequentemente uma consequência correta da falta de
prova de equivalência. O defeito era permitir que essa ausência vetasse um
lançamento independentemente identificado. Scores próximos não provam identidade.
Os logs de candidato agora incluem título, track, créditos, álbum, duração,
ISRC, channel ID e verificação; `spotify_release_selected` registra a seleção
por lançamento. Os logs de grupos e decisão final continuam disponíveis.

## Comparação acústica opcional: avaliação e decisão

Não habilitada. [Chromaprint](https://github.com/acoustid/chromaprint/blob/master/README.md)
visa áudio quase idêntico e sacrifica precisão/robustez por eficiência de busca.
`fpcalc` acrescentaria libchromaprint e bibliotecas FFmpeg de decodificação/FFT;
bindings Go acrescentariam integração nativa, enquanto subprocesso manteria
isolamento/cancelamento já existentes. FFmpeg está instalado; fpcalc não está
presente neste host. Não foi instalada dependência adicional.

[AcoustID](https://acoustid.org/webservice) requer chave de aplicação, limita
requisições a três por segundo e oferece identificação por catálogo. Isso não
certifica diferenças clean/explicit, remaster ou edição. Comparação local de
fingerprints exigiria alinhamento e corpus rotulado de versões incompatíveis;
não há precisão medida aqui que autorize um limiar automático.

A [API de faixas Spotify](https://developer.spotify.com/documentation/web-api/reference/get-track)
fornece metadados; preview parcial pode faltar e não valida toda a gravação.
Comparar dois uploads YouTube entre si não os liga automaticamente à identidade
Spotify. Acesso a áudio também pode falhar por autenticação, região e rate limit.

CPU cresce com áudio decodificado e pares comparados; não há benchmark local de
fingerprints, portanto não se atribui custo numérico fictício. Downloads completos
por faixa ampliariam tráfego/latência e não foram introduzidos. Uma etapa futura
somente para ambiguidades persistentes precisaria referência confiável, amostras
limitadas, orçamento próprio dentro do deadline, cache de evidência, cancelamento,
limpeza de arquivos/processos e teste de precisão com negativos. Neste hotfix a
ambiguidade sem prova continua sendo rejeitada, em vez de usar áudio não calibrado.

## Discord, integridade e validação

Falhas de resolução de coleção e de reprodução geram eventos com nome/artista,
posição original e motivo compreensível: busca sem gravação compatível, ambiguidade,
busca falha/timeout, bloqueio externo ou áudio indisponível. Cancelamentos por
controle do usuário não geram avisos espúrios. O worker REST existente é assíncrono
e limitado; saturação/erro de Discord é registrado e pode impedir entrega, sem
bloquear o player. Não há garantia de entrega da API Discord.

Now Playing continua condicionado ao primeiro frame enviado com sucesso.
`/now` e `/queue` agora também usam esse estado e exibem Starting durante preparação.
Paused continua descritivo do controle, inclusive antes do primeiro frame.

Regressões novas verificam os quatro IDs/seleções exatas, distribuidor sem créditos
no resumo, identidade de lançamento concorrente, metadados copiados não verificados,
álbum/créditos divergentes, duração e versões incompatíveis, aviso de skip único
com posição original e ausência de anúncio prematuro. As suítes existentes cobrem
ordem contígua fora de ordem, avanços após falhas, deadline por posição, cancelamento,
limpeza real de subprocessos/FFmpeg, cache, invalidação e SQLite reaberto.

Validação final: `go test ./... -count=1`, `go test -race ./... -count=1` e
`go vet ./...` passaram. As duas execuções de testes desabilitam o cache de
resultados e abrangem todos os pacotes.
O replay dos quatro casos leva aproximadamente 40 ms **sem rede**; mede decisão,
não latência real de resolução nem importação completa. Os contadores máximos
observados foram quatro buscas e cinco verificações. Não foi medido playback real,
latência de voz nem tempo de resolução online completo sob o deadline de 90 s.
Não há falsos positivos nos cenários adversariais testados; isso não demonstra
inexistência de falsos positivos fora do corpus.
