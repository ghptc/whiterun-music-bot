# Defeitos e lacunas investigados

## QA-001 — data race no recorder REST de testes

Reprodução: go test -race ./internal/discord -run '^TestQueueDuringSpotifyImportAndSkip$' -count=100.

Na execução completa, duas goroutines de consulta de guilds distintas escreveram recordingRest.response e recordingRest.update sem mutex, em commands_test.go:33–34. Evidência original em evidence/race-discovered.txt. O teste aguarda ambos os canais done antes de ler a resposta; a disputa observada é entre escritores.

Impacto: teste crítico pode falhar de maneira intermitente, invalidando o sinal de concorrência. O stack trace não demonstra race no caminho de produção.

Proposta antes da alteração: sincronizar os métodos gravadores do helper com mutex e proteger queueText; manter as leituras diretas apenas após comandos síncronos ou join dos workers, como já fazem os testes. Não alterar produção, nem serializar artificialmente os comandos concorrentes.

## QA-002 — mutante de validação de versão inicialmente sobrevivente

O mutante removia a comparação de versionMarkers. A matriz com título curto Song (Live) continuava sendo rejeitada por precisão de título, mascarando a ausência da proteção semântica.

Ampliação: títulos longos cujo conteúdo adicional ainda passa o limiar de precisão, e variantes presentes somente no álbum. Após ampliação, os nove mutantes foram detectados por assertions. Isso é lacuna de teste demonstrada, não defeito confirmado no matcher de produção.

## Limites ainda materiais

Cobertura abaixo das metas e sem ferramenta de branch coverage; cobertura de statements não mede MC/DC. Homologação externa e soak prolongado pendentes. Ausência de falhas nas execuções observadas não prova ausência universal de vazamentos ou races.
