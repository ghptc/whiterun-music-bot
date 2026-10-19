# Integração local e contratos

## Fronteiras exercitadas

| Fronteira | Implementação real | Falha controlada / oráculo |
|---|---|---|
| SQLite | modernc, arquivos em t.TempDir, WAL e migrations | triggers ABORT por operação/tabela; comparar estado anterior e outra guild |
| Recuperação | fechar e reabrir arquivo real | comparar metadados, ordem, explicit, histórico e integrity_check |
| Indisponibilidade | conexão fechada, contexto cancelado, query_only | escrita deve retornar erro; leitura indisponível não vira miss |
| Corrupção | arquivo inválido criado exclusivamente em t.TempDir | Open deve falhar |
| Áudio | FFmpeg/libopus real e WAV sintético existente | quantidade de frames, speaking, cancelamento e drenagem |
| yt-dlp | executáveis shell controlados | stdout/stderr, status, marcadores, timeout, cancelamento |
| Ogg | parser real, bytes locais | truncamento em cada fronteira, continuação, cópia exata de pacotes, propagação do erro do consumidor |
| REST Discord | doubles e serialização reais disgo | contrato do comando, opção, contexto guild, ack e permissões |
| Concorrência | player real e mutex/goroutines reais | modelo independente, 20 produtores, barreiras de teardown e race detector |

Os doubles REST não validam HTTP 429, backoff, assinatura Gateway nem negociação de voz. Os processos controlados não provam comportamento do yt-dlp real sob mudanças de YouTube.

## Lacunas priorizadas

P0: contrato HTTP de transporte para 429/Retry-After, 5xx, conexão abortada e expiração; reconexão DAVE real; árvore de subprocessos com netem e inspeção de PIDs em runner Linux isolado; falha/crash do processo durante commit com recuperação WAL. Não simular crash apenas fechando conexão: são riscos distintos.

P1: indisponibilidade parcial de artwork, DNS lento, paginação Spotify com campos ausentes; ampliar fixtures reais anonimizadas; SQLite com ENOSPC em filesystem temporário dedicado. query_only verifica propagação de falha de escrita, não equivale a disco cheio.

Nunca apontar testes ao BARD_DB_PATH operacional. Não usar credenciais reais nas suítes unitária/local/race/mutação. Homologação externa só em bot, guild e volume dedicados.
