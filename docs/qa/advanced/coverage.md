# Cobertura antes/depois e integração do binário

Baseline existente: **85,9%** com -coverpkg=./... (docs/qa/coverage-report.md).
Após QA-008: **86,2%** pelo mesmo denominador, sem excluir cmd ou produção.
Meta global de 90% ainda não alcançada; meta crítica 95% com análise de branches
continua não demonstrada. Go cover é statements, não MC/DC.

| Pacote | Statements atingidos/total | Cobertura |
|---|---|---|
| whiterun/cmd/bot | 1/83 | 1.2% |
| whiterun/cmd/profile | 0/51 | 0.0% |
| whiterun/internal/discord | 750/925 | 81.1% |
| whiterun/internal/media | 1815/1958 | 92.7% |
| whiterun/internal/player | 1102/1238 | 89.0% |
| whiterun/internal/storage | 129/151 | 85.4% |

evidence/test.cover é a cobertura da suíte go test (unitários e integrações locais
que já convivem no projeto). Não fingir que todos esses testes são unitários
isolados. evidence/test-functions.txt permite inspecionar funções descobertas.

## go build -cover / GOCOVERDIR

qa008_binary_coverage.py compila cmd/bot com -cover -coverpkg=./... em diretório
temporário; executa **nove cenários offline do binário real**, ambiente com whitelist
de variáveis, credenciais sintéticas, banco temporário e stubs de runtime. Nenhum
caso chama Start com configuração válida nem autentica no Discord.
O construtor disgo consulta /gateway: uma fixture HTTPS/CONNECT local, com CA
temporária confiada apenas pelo subprocesso, responde somente esse GET. Não há
código de encaminhamento externo; outras autoridades/endpoints falham. O log de
CONNECT/GET em evidence/binary/network-fixture.json refere-se ao servidor local.
Um piloto inicial sem esse isolamento chegou a consultar metadados públicos de
gateway; foi excluído como evidência offline e os nove cenários foram regenerados
com a fixture. Nenhuma sessão de voz ou autenticação foi executada.

Casos: token ausente; application ID inválido; guild ID inválido; runtime ausente;
JSON de policy inválido; policy inválida; SQLite corrompido; diagnose sem argumentos;
diagnose com Spotify URL rejeitada. Cada caso deve sair com código 1 e mensagem
esperada. Isto verifica o executável e as falhas de bootstrap, não uma sessão de voz.

GOCOVERDIR produz metadados/contadores; go tool covdata percent/func/textfmt gera
evidence/binary/percent.txt, functions.txt e integration.cover, mantidos separados
do perfil de testes. Coverage do cmd/bot no binário: **68,7%**; agregado desse
bootstrap: **2,3%**. Os denominadores são diferentes; não somar percentuais ou
atribuir 68,7% ao programa completo.

Não foram persistidos os contadores binários dependentes de paths temporários;
o runner reproduz a geração e os perfis textuais estão versionados. Os perfis de
go test e do executável não foram mesclados para declarar aprovação de cobertura.

Caminhos críticos pendentes: negociação/reconexão DAVE/Voice real, E2E de áudio
recebido, crash no commit/WAL, mutantes não cobertos, defeitos QA008-D01/D02/D03
e caminhos de infraestrutura ainda descobertos no relatório por função.

Referência primária: [cobertura de integração Go](https://go.dev/doc/build-cover).
