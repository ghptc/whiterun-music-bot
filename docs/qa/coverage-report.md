# Relatório de cobertura

Execução completa com -count=1 -coverpkg=./... -covermode=atomic. Total agregado: **85.9%**. Todas as fontes cmd e internal estão no denominador.

| Pacote | Statements atingidos/total | Cobertura |
|---|---|---|
| whiterun/cmd/bot | 1/83 | 1.2% |
| whiterun/cmd/profile | 0/51 | 0.0% |
| whiterun/internal/discord | 749/925 | 81.0% |
| whiterun/internal/media | 1810/1958 | 92.4% |
| whiterun/internal/player | 1096/1238 | 88.5% |
| whiterun/internal/storage | 129/151 | 85.4% |

Baseline anterior: 83,3% com instrumentação padrão por pacote (baseline.cover). O perfil final instrumenta também chamadas entre pacotes. Portanto, não atribuir toda diferença de percentual aos testes novos: a metodologia foi ampliada. Os testes de integração SQLite exercitam player e media entre pacotes, por exemplo.

Foram aprovados 916 casos finais na execução JSON; 289 pertencem aos testes Quality novos. Pais de subtestes não entram na contagem. Seeds e ciclos internos não aumentam esse total. O arquivo build-tag soak e benchmarks ficam fora da contagem. Esta contagem mede casos executados, não requisitos independentes revisados por humano.

Meta global >=90% ainda não atingida. Meta crítica >=95% ainda não demonstrada por um conjunto de caminhos formalmente delimitado. Go cover mede statements: não fornece branch/condition coverage nem MC/DC. Não excluir bootstrap e rede para declarar sucesso.

Evidências: final.cover, coverage-functions.txt, test.jsonl. Lacunas incluem bootstrap real de Discord, Voice/DAVE, rotas de falha de migrations/leitura/catálogo e infraestrutura cmd. Há funções legadas não exercitadas. Revisar relatório por função e matriz de requisitos antes de expandir testes; não testar somente para preencher linhas.

Condições críticas revisadas: canal ausente/canal divergente; ack sucesso/falha; fila vazia/cheia/overflow; posições válidas/inválidas; cancelamento antes de sucessor; incompatibilidade de versão/ISRC/explicit; expiração antes/exata/depois; rollback em estágios de escrita. Esta revisão não equivale a cobertura automatizada de branches.
