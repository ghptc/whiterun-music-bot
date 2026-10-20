# Segurança — govulncheck e triagem

govulncheck v1.9.0 executado em modo source/symbol, Go
go1.27.1-X:nodwarf5, Linux/amd64. Base reportada: vuln.go.dev, atualização
2026-10-19T21:08:39Z. A primeira tentativa falhou ao carregar cache compartilhado;
a execução concluída usou GOCACHE=/tmp/bard-qa008-govuln-cache. Evidências:
security/version.txt, govulncheck.jsonl, status.json e triage.json.

Resultado de módulos: **11 avisos no nível de módulo; zero call traces vulneráveis**.
Limitação encontrada ao revisar o protocolo: o SBOM do scanner reportou go_version
como "go" e stdlib sem versão, apesar de config.go_version registrar o toolchain
customizado. Portanto, o veredito sobre **stdlib é parcial**. O runner classifica
isso como PARTIAL e retorna código 2; repetir com toolchain oficial na release.
A interseção entre pacotes afetados de cada módulo reportado e go list -deps ./...
foi vazia nesta configuração. Não confundir net/http da stdlib com o pacote
golang.org/x/net/http2: o finding para x/net não afirma que a stdlib atual é afetada.

| Avisos | Módulo / pacote afetado | Triagem |
|---|---|---|
| GO-2026-6603, 6610, 6611, 6612, 6617 | x/net v0.56.0 / http2 | não importado; correção reportada v0.60.0 |
| GO-2026-6629 | x/text v0.40.0 / secure/precis | não importado; correção v0.41.0 |
| GO-2026-5841 | klauspost/compress v1.18.4 / s2 | não importado; correção v1.18.7 |
| GO-2026-5932 | x/crypto v0.54.0 / openpgp | não importado; sem versão de correção reportada |
| GO-2026-6303 | x/crypto v0.54.0 / ssh | não importado; correção v0.55.0 |
| GO-2026-6354, 6355 | x/crypto v0.54.0 / ssh | não importado; correção v0.56.0 |

Proposta: atualizar dependências em mudança separada, com matriz de compatibilidade
DAVE/Voice, e repetir scanner no binário/imagem de release. Não há vulnerabilidade
alcançável demonstrada por este scan; isso não prova ausência de vulnerabilidades.
Não foi feita atualização de go.mod ou go.sum nesta tarefa.

O runner distingue avisos de módulo, call traces e análise parcial. Não atribui
sucesso completo a SBOM sem versão Go. Falha em caso de call traces, scanner
incompleto/parcial ou indisponibilidade. Repetir triagem para outros
GOOS/GOARCH/build tags. FFmpeg, yt-dlp, Deno e bibliotecas nativas de DAVE/Opus exigem
scanner de imagem/SBOM separado; govulncheck não é esse scanner.

Validação adversarial inclui URLs com authority spoofing, userinfo, portas, schemes
ativos, Unicode e escapes; IDs canônicos; limites de fila, timestamp, buffer e
corpus; encerramento de processos próprios e isolamento de guilds. Nenhum endpoint
operacional foi usado para testes destrutivos.

Referências primárias: [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
e [base Go](https://vuln.go.dev/). Cada ID tem URL específica em security/triage.json.
