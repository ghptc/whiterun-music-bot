# Plano E2E externo — pendente de homologação

Nenhuma sessão Discord/Spotify/YouTube real foi executada neste programa. FFmpeg local e doubles REST não homologam este gate.

Ambiente: aplicação/bot e guild exclusivos de QA; dois canais de voz, três contas humanas autorizadas, um usuário sem Connect/Speak e duas guilds; volume SQLite descartável e credenciais fora do repositório. Executar com versão exata de Go/disgo/DAVE/FFmpeg/yt-dlp da imagem de release.

| ID | Ação | Evidência necessária |
|---|---|---|
| E01 | registrar os 22 comandos, inspecionar query/force/count/limit | resposta REST e captura do cliente |
| E02 | play YouTube, receber áudio nos dois clientes | logs primeiro frame + confirmação audível |
| E03 | Spotify original/remix/live, dois artistas e metadados incompletos | decisão de identidade e URL final; gravação pedida |
| E04 | Force Play do vídeo exato, depois Spotify igual | IDs e ausência de contaminação do matching |
| E05 | pause/resume/replay/previous/skip e transição EOF | áudio e uma única mensagem de início válida |
| E06 | usuário em outro canal, sem canal e sem permissão | rejeição sem alterar fila/áudio |
| E07 | perder e recuperar Voice/Gateway/DAVE | logs de negociação; áudio retomado sem stream concorrente |
| E08 | duas guilds reproduzirem mesma música e uma executar stop | independência, cache compartilhado sem cancelar a outra |
| E09 | playlist de 500 músicas, shuffle durante importação, comandos de 20 usuários | ordem, contagem, metadados e erros explicáveis |
| E10 | restart e kill durante persistência | integrity_check e estado consistente; reconexão apenas explícita |
| E11 | artwork indisponível, resposta 429 e interação expirada | áudio continua; retry/erro observável e sem duplicação |
| E12 | stop/leave durante resolução e FFmpeg | sem filhos sobreviventes, áudio encerra, ticket antigo rejeitado |

Guardar versão/commit, timestamps, comandos e IDs anonimizados, logs redigidos, resultado por ID e responsável. Falha P0 bloqueia release. Falta de execução de E02/E03/E06/E07/E10/E12 também bloqueia. Não converter ausência de credenciais em aprovação.
