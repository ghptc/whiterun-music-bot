# Bardo: intenção de busca e identidade musical

A busca textual pede uma faixa. `Black Sabbath Paranoid` não autoriza importar
o álbum Paranoid. O título de um álbum também pode ser o título de uma faixa;
essa coincidência não muda o tipo do pedido. Resultados explicitamente marcados
como `Full Album` ou `Complete Album` são rejeitados na descoberta textual,
inclusive quando são o único resultado. URLs diretas de vídeos mantêm a intenção
explícita do usuário.

| Entrada | Comportamento |
|---|---|
| Texto livre | Resolver uma faixa individual; nunca expandir silenciosamente para álbum |
| Spotify `/track/ID` | Resolver a identidade da faixa solicitada, usando YouTube como localizador de áudio |
| Spotify `/album/ID` | Obter todas as páginas de metadados e importar as faixas na ordem original |
| Spotify `/playlist/ID` | Importar a playlist, preservando ordem, duplicatas e identidades |
| YouTube Music `/playlist?list=…` | Importar a coleção, inclusive álbuns apresentados nesse formato |
| YouTube `/watch?v=…&list=…` | Reproduzir o vídeo explícito; o contexto `list` não solicita a coleção |
| Link inválido/não suportado | Retornar erro; links Spotify malformados não viram texto de busca |

## Responsabilidades existentes

Não foram criadas novas camadas. `CollectionURL` e `YouTubeURL` fazem o papel de
InputParser; provedor e tipo retornados, junto com `IsCollection` e o caminho de
texto livre, representam SearchIntent. `Resolver` e `MatchSpotify` resolvem a
identidade musical. `CollectionResolver.Prepare`, os provedores Spotify e
`player.CollectionJob` fazem o papel de CollectionImporter.

A importação Spotify prepara metadados antes de resolver as gravações. Posições
indisponíveis nos metadados são contadas em `Skipped`; sobreviventes conservam
`Position` e `Total`. Falhas posteriores de resolução são contabilizadas e geram
um evento de aviso com a identidade e posição originais. Não se inventa uma
faixa para preencher lacunas. A fila de faixas reproduzíveis mantém a ordem dos
sobreviventes, sem renumerar a identidade de origem.

`FIRST CONFIRMED MATCH WINS` permanece no resolvedor de identidade: uma gravação
confirmada encerra a busca. A mudança não altera a seleção Spotify nem relaxa
suas exigências de artista, título, duração, versão e evidência de gravação.
Remasterizações não são equivalentes ao original apenas por compartilharem o
título. Texto livre possui menos evidência que uma faixa Spotify: ranking textual
não garante a identidade de uma edição sem metadados suficientes.

## Cobertura determinística

Fixtures locais usam JSON de metadados, executável falso de yt-dlp, provedor
Spotify falso e transporte HTTP em memória. Não dependem de catálogo ao vivo.

| ID | Teste em `internal/media/search_intent_test.go` |
|---|---|
| BARD-SEARCH-001 | `TestBardTextSearchIntent`: Black Sabbath Paranoid retorna uma faixa |
| BARD-SEARCH-002 | `TestBardSpotifyResourceIntent`: track preserva ID e identidade, sem expandir álbum |
| BARD-SEARCH-003 | Mesmo teste: álbum Paranoid contém oito faixas ordenadas |
| BARD-SEARCH-004 | Mesmo teste: playlist completa preserva inclusive duplicata |
| BARD-SEARCH-005 | `TestBardTextSearchIntent` e `TestBardRecordingDisambiguation`: 2009 Remaster |
| BARD-SEARCH-006 | `TestBardTextSearchIntent` e `TestBardTextNeverFallsBackToAlbum`: título compartilhado e álbum como único candidato |
| BARD-SEARCH-007 | `TestBardInvalidLink`: erro compreensível antes de subprocessos |
| BARD-SEARCH-008 | `TestBardAlbumUnavailableMetadataPositions`: decoder real conserva posições 1 e 3, total 3, uma falha |

`TestBardAlbumUnavailableRecordingReportsPosition`, em
`internal/player/collection_test.go`, completa BARD-SEARCH-008: lacuna de metadados,
falha de resolução, aviso da posição original, sucessor e contagens preservados.
`TestBardRecordingDisambiguation` cobre artista diferente, título semelhante,
live contra estúdio, original contra remaster e anos de remaster diferentes.
`TestBardYouTubeMusicAlbumIntent` protege o formato de álbum YouTube Music suportado.
Os testes existentes de primeira confirmação, identidade e playlists continuam
fazendo parte da validação completa.

Foi executada uma mutação local: retirar os marcadores de álbum de `musicShape`
faz `TestBardTextNeverFallsBackToAlbum` falhar ao aceitar o vídeo de álbum como
faixa. A proteção foi restaurada antes das validações finais.

## Limites

Importações têm limite de 500 faixas e dependem do acesso do provedor aos
metadados. Faixas indisponíveis não podem ser reproduzidas; contagens e posições
registram as lacunas conhecidas. Outros formatos de links de álbum YouTube Music
não são inferidos como coleções. Vídeos de álbuns sem marcadores explícitos podem
não ser identificáveis pelos metadados disponíveis; não há inferência universal
de álbum pelo nome. Importações YouTube usam metadados planos e verificação antes
de tocar, sem garantir identidade de edição equivalente à evidência Spotify.

Validação local: `go test ./...`, `go test -race ./...`, `go vet ./...`.
Esses testes não validam áudio, disponibilidade externa nem interação no Discord
real. E2E no Discord não foi executado nesta alteração.
