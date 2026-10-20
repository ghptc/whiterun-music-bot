# Bloqueios de release após QA-010

**v1.0 não está homologada.** A hora local foi executada, mas não aprova 6h/12h ou Discord real.

| Gate | Estado | Próxima evidência necessária |
|---|---|---|
| 1h contínua | APROVADA LOCALMENTE SEM RACE NA QA-012 | 3.600,018s, integridade e RSS estável; hora QA-011 com race continua com restrição de recursos |
| 6h / 12h contínuas | EXECUÇÃO LOCAL SEM RACE LIBERADA; RESULTADOS PENDENTES | runner protegido, limites e coleta; segmentos integrais e cleanup/repouso; não iniciadas |
| Playlist 500 completa em cada guild | APROVADA LOCALMENTE NA QA-011 | três lotes de 500 posições drenados por guild, com falhas classificadas; E2E real segue pendente |
| Crescimento sustentado de RSS/heap/cache | TRIAGEM LOCAL SUFICIENTE PARA EXECUÇÃO NATIVA; RACE PROLONGADO RESTRITO | QA-012 isolou efeito da instrumentação/churn; nativo estável por uma hora; detalhes internos de anonymous allocations e DAVE real não demonstrados |
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

## QA-011

A campanha de uma hora terminou no tempo integral, exit=0, fila reconciliada, FD/goroutines ao baseline e zero filhos diretos. Há crescimento sustentado de RSS que impede a aprovação operacional global. As campanhas 6h/12h permanecem não iniciadas pela condição explícita de aprovação da primeira campanha; não por um resultado verde presumido. Controle diagnóstico sem race concluído em 600,029s, com RSS de cleanup 17,73MiB; não substitui uma sessão prolongada. Nenhum Discord real acessado, nenhum serviço de produção interrompido, checkpoints exclusivos de QA preservados.


## QA-012 — decisão atual

[Investigação](rss-investigation.md): 21 controles e uma hora nativa concluídos. Carga fixa reproduz retenção sob race associada a goroutines curtas de cancelamento SQLite; não se confirmou vazamento de produção. Sem mudança em player/media/storage ou dependências. Execução protegida de 6h/12h sem race liberada, aprovação dessas durações pendente. Prolongadas com race no envelope atual não liberadas. Atribuição fina de memória nativa do sanitizer e memória DAVE/Discord real permanecem limitações; a release continua bloqueada pelos gates restantes.
