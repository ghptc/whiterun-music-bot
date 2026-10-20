# Bloqueios de release após QA-010

**v1.0 não está homologada.** A aprovação local descrita nesta etapa não aprova estabilidade de horas nem Discord real.

| Gate | Estado | Próxima evidência necessária |
|---|---|---|
| 1h contínua | PENDENTE | executar CLI/workflow pelo tempo integral, reconciliar ledger e recursos |
| 6h / 12h contínuas | PENDENTE | runner dedicado, segmentos íntegros, relatório de cleanup/repouso e sem extrapolação de tempo |
| Playlist 500 completa em cada guild | PENDENTE | drenar todas as posições, além do aceite/preservação já demonstrados |
| Crescimento sustentado de RSS/heap/cache | INCONCLUSIVO em sessões curtas | séries por horas, ciclos equivalentes, pós-GC/repouso, perfil de retenção; não classificar RSS crescente isoladamente como leak |
| Discord Voice/DAVE audível, reconnect e permissões | PENDENTE | executar checklist autorizado com clientes reais e imagem de release |
| Falhas externas prolongadas | PENDENTE | provedor/rede real controlada e recuperação durante sessão prolongada |
| Crash físico em commit/fsync e ENOSPC | PENDENTE | filesystem/volume exclusivo e reprodução controlada; SIGKILL mid-transaction não prova resistência a perda de energia |
| Metas de cobertura | PENDENTE herdado | QA-009 reportou 87,5%, ainda abaixo de 90%; 95% crítico/branches não demonstrado. Não recalculado como parte da QA-010; novo código de QA muda o denominador global |
| Security gate de stdlib | PENDENTE herdado | repetir reachability conforme relatório QA-008 com toolchain oficial; não foi executado govulncheck nesta etapa |
| CI remoto | NÃO EXECUTADO nesta chamada | workflow GitHub rodado e artefatos; YAML foi parseado localmente e comandos executados localmente |

## Evidências locais aprovadas

- Segmentos reais de 12,17s, 60,17s, 120,63s, retomada de 20,25s e medição de entrega de 20,06s. Maior segmento 120,63s, nunca somados para declarar horas contínuas.
- Ledger/ordem/identidade/persistência consistentes nas sessões; falhas individuais não impediram as seleções seguintes.
- 20 usuários, duas seeds, 12.000 operações com race; modelo/linearização existentes preservados na suíte completa.
- SIGKILL em quatro pontos de transação SQLite, três repetições com race, recovery/integrity_check e isolamento aprovados.
- FD/goroutines ao baseline e zero filhos diretos após cleanup nos segmentos; sem conclusão de estabilidade de memória prolongada.
- Suíte completa, race, vet e Docker: consultar validation.json para os códigos finais, inclusive logs anteriores de falha de fixture preservados.

Nenhum defeito novo de produção confirmado exige correção nesta etapa. Se uma campanha futura falhar, preservar diretório/checkpoint/seed, reproduzir deterministicamente, registrar causa/impacto/severidade e regressão antes de alterar produção. UI e arquitetura continuam congeladas.
