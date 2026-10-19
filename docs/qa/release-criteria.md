# Critérios de release v1.0

Release ainda NÃO homologada.

Bloqueiam: P0 reproduzível, identidade musical incompatível conhecida, corrupção/duplicação indevida da fila, subprocessos vazados, perda de estado, bypass de canal/permissões, race crítica, teste crítico flaky sem investigação, recuperação grave falhando ou E2E essencial não homologado.

Metas: statements total >=90%; caminhos críticos >=95% com revisão de branches/condições; cenários significativos aproximadamente 500–1.000+. Contagem e cobertura não anulam bloqueios.

Checklist de evidências:
- testes completos, race e vet aprovados no commit candidato;
- repetição dos adversariais e triagem de qualquer falha;
- matriz histórica validada por responsáveis de produção;
- nenhum mutante crítico sobrevivente sem justificativa;
- cobertura com denominador incluindo cmd, sem excluir arquivos para melhorar a métrica;
- homologação dos E2E essenciais por responsável e logs;
- soak operacional e recuperação após crash;
- plano de backup/restauração SQLite validado no volume de QA.

Os relatórios atuais registram testes locais aprovados e metas ainda não atingidas. Não há declaração de ausência universal de races, vazamentos ou incidentes.

## Congelamento de UI

UI congelada para a v1.0 após o ajuste final de nomenclatura: The Bard · Queue,
The Bard · Now Playing (anúncios e /now) e The Bard · Help (/help).
Layout, artwork e funcionalidades permanecem como homologáveis nesta versão.
A próxima etapa é a validação pelos testes; o congelamento não representa
aprovação dos gates de qualidade ou do E2E pendente.

Validação após a nomenclatura: go test ./... -count=1,
go test -race ./... -count=1 e go vet ./... aprovados.
