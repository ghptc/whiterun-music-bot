# QA-008 — defeitos reproduzidos e correções propostas

Nenhum código de produção, dependência ou UI foi alterado. Os testes abaixo são
**reproduções intencionalmente vermelhas**, sob a tag qa008_knownissues. A aprovação
da suíte padrão não encerra estes defeitos. Não são testes flaky em quarentena.

## QA008-D01 — duração negativa do extrator chega ao Track

Reprodução: go test -tags qa008_knownissues ./internal/media -run '^TestKnownIssuesNegativeExtractorDuration$' -count=1

Um executável local de yt-dlp retorna ID válido, título e duration=-1. Resolver.Resolve
retorna sucesso e Track.Duration=-1s. Esperado: metadado malformado rejeitado antes
de publicar o Track. Impacto demonstrado: invariante de duração violada; consumidores
que usam duração para apresentação/seek recebem valor impossível. Não foi demonstrada
substituição de identidade nem exploração remota.

Proposta separada: validar duração finita e não negativa no limite de extração;
definir explicitamente como representar duração desconhecida e live, mantendo os
casos legítimos com duração ausente. Ampliar testes para overflow e valores extremos
antes de alterar produção. Prioridade proposta P1; confirmar com responsáveis.

## QA008-D02 — pacote de áudio vazio aceito pelo parser Ogg

Reprodução: go test -tags qa008_knownissues ./internal/player -run '^TestKnownIssuesEmptyOpusPacket$' -count=1

Uma página OpusHead seguida de lacing zero entrega um pacote vazio ao callback e
retorna nil. Esperado: erro, sem entregar áudio vazio como frame Opus.
Impacto demonstrado: consumidor recebe áudio inválido. A callback do Streamer
considera seu primeiro pacote como início de áudio, sem validação adicional.
Não foi demonstrado que FFmpeg íntegro produza este fluxo nem validado o efeito no
Discord real; não classificar como corrupção P0 comprovada de produção.

Proposta separada: rejeitar pacote de áudio vazio antes de firstAudio/WriteFrame e
definir o contrato de validação de Opus; distinguir validação do container e do codec.
Adicionar integração de primeiro frame antes da correção. Prioridade proposta P1.

## QA008-D03 — goroutine do rate limiter disgo sobrevive a Close

Reprodução: go test -tags qa008_knownissues ./internal/discord -run '^TestKnownIssuesDisgoLimiterClose$' -count=1

Com disgo v0.19.6, rest.NewRateLimiter inicia cleanup com ticker. Close retorna sem
encerrar nem juntar essa goroutine. synctest detecta deterministicamente goroutine
bloqueada quando a bolha termina. Evidências: disgo-leak-discovered.txt e
disgo-leak-reproduction.txt. O stack aponta para rest_rate_limiter.go:73.

Impacto: recurso associado ao cliente permanece após encerramento; criação/fechamento
repetidos de clientes acumula goroutines. No processo comum que vive com um único
cliente, não foi demonstrado crescimento durante reprodução contínua. Close também
mantém bucketsMu bloqueado; não chamar métodos do limiter após Close.

Proposta separada: investigar versão corrigida ou correção upstream com sinal de
encerramento, Stop do ticker, join e teste de lifecycle. Não substituir o rate limiter
de produção nesta etapa. Os novos testes de orçamento virtual injetam um limiter de
teste sem manutenção; os contratos existentes do limiter real permanecem.

## Segurança e flakiness

govulncheck concluiu; triagem separada em security.md. Nenhum call trace vulnerável
foi reportado na configuração Linux/amd64 examinada.

Não houve falha intermitente nova observada nas execuções completas e nas repetições
registradas. As três falhas acima são determinísticas. Problemas de infraestrutura
(DNS e cache Go) não foram classificados como falhas de assertions ou mutantes mortos.
