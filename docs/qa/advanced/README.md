# QA-008 — Advanced Reliability Testing

Extensão dos relatórios existentes de QA. Não houve nova auditoria geral nem
alteração de produção, dependências, layout ou identidade visual congelada.

## Resultado verificável

- 2.048 sequências determinísticas de player/fila: 266.240 operações comparadas
  com modelo independente; 128 seeds de concorrência comparados com seis
  linearizações possíveis.
- Sete targets nativos de fuzzing: 1.761.527 execuções em campanhas limitadas;
  dez inputs interessantes preservados, nenhum crash de fuzz observado.
- 178 mutantes detectados, zero sobreviventes executados; 43 não cobertos.
  Efficacy 100% entre executados; cobertura dos mutantes do escopo 80,5%.
- Cobertura go test: 85,9% → 86,2%. Cobertura de bootstrap do binário real separada.
- Três defeitos determinísticos registrados com reproduções intencionalmente
  vermelhas, sob tag própria. Nenhum foi corrigido nem aceito como comportamento
  correto.
- govulncheck executado: 11 avisos de módulo triados, nenhum pacote afetado
  importado nos módulos reportados e nenhum call trace. SBOM sem versão da stdlib
  torna essa parte da análise parcial; repetir com toolchain oficial.

## Contagem sem inflação

A execução padrão passou por 3.165 folhas de teste, contra 916 anteriores:
2.048 seeds do modelo + 128 seeds de linearizabilidade + 47 seeds/corpus de fuzz
+ 26 cenários dirigidos novos. Esses 2.249 casos adicionais não representam
2.249 requisitos independentes. Os milhões de fuzz executions e os milhares de
passos internos não são adicionados à contagem da suíte. Nove cenários offline do
executável e três reproduções vermelhas ficam separados.

## Comandos

- Diário: go test -short ./... -count=1
- Completo: go test ./... -count=1
- Race: go test -race ./... -count=1
- Estática: go vet ./...
- Modelo completo: go test ./internal/player -run '^TestAdvancedModelSequences$' -count=1
- Stress: BARD_QA_MODEL_SEED=20261019 go test -race ./... -run '^TestAdvanced' -count=20 -timeout=5m
- Fuzz longo separado: python3 -B scripts/qa008_fuzz.py --seconds 60
- Mutação: python3 -B scripts/qa008_mutation.py --tool /CAMINHO/gremlins
- Binário offline: python3 -B scripts/qa008_binary_coverage.py
- Segurança: python3 -B scripts/qa008_security.py --tool /CAMINHO/govulncheck
- Defeitos conhecidos (esperado FAIL): go test -tags qa008_knownissues ./internal/media ./internal/player ./internal/discord -run '^TestKnownIssues' -count=1

Gremlins usado: github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0.
govulncheck usado: golang.org/x/vuln/cmd/govulncheck@v1.9.0.
Instalar ferramentas fora do go.mod do projeto. Os runners encerram seu grupo de
processos em timeout; FFmpeg/yt-dlp nos testes têm contextos e teardown próprios.

## Validação e custo

Suíte completa, race e vet passaram. Stress Advanced passou em 20 repetições;
SQLite interrompido 10, FFmpeg morto 5, relógios virtuais/cancelamento 30, disputa
de fila 20. Evidências brutas ficam em evidence/, incluindo resultados vermelhos.

O modelo completo levou 1,75s sem race na primeira execução e 13,05s sob race em
medição isolada posterior (13,26s com comando/setup). Pacote player completo:
24,86s normal e 37,13s race; baseline anterior 21,54s/23,08s. As execuções completas
coexistiram com outras campanhas: os deltas não são SLA nem benchmark controlado
do GitHub runner.

No CI existente, testes novos rápidos e seeds/corpus executam normalmente; -short
pula o modelo de 2.048 seeds e o novo fault test FFmpeg, mas não separa integrações
legadas. O workflow manual advanced-reliability separa campanhas longas,
mutação, binário offline e scan de segurança. Default manual: 60s por target,
cerca de sete minutos de fuzz mais setup, mutação e validação. Orçamento máximo
do job: 30 minutos; duração local pode ser maior. Nenhuma campanha longa foi
adicionada automaticamente a cada push.

## Relatórios e release

- model-and-concurrency.md: oracle, schedules e replay de seeds.
- fuzzing.md: execuções, corpus, limites e oracles.
- mutation.md: escolha da ferramenta, escopo e classificação.
- fault-injection.md: mecanismos reais e simulados.
- coverage.md: métricas comparáveis e GOCOVERDIR.
- security.md: triagem e limites do scanner.
- findings.md: reproduções e propostas separadas.

Release permanece não homologada: resolver ou deliberar explicitamente sobre os
três defeitos conhecidos (incluindo lifecycle de dependência), cobrir os mutantes
pendentes, concluir análise da stdlib com toolchain oficial, atingir/revisar metas
90%/95%, executar soak prolongado e E2E essencial de Discord/DAVE em ambiente
dedicado. Não há alegação de E2E real validado nem ausência universal de vazamentos.
