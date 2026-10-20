# Mutação sistemática

> Atualização QA-009: este documento registra o estado histórico da QA-008.
> O score sistemático anterior foi invalidado: Gremlins 0.6.0 gerava `-cpu 2`
> como um único argumento e falhas de execução eram contadas como KILLED.
> As reproduções D01/D02 foram promovidas a regressões padrão; D03 foi
> investigado além do prazo de Close e mitigado por limiter com ciclo de vida próprio.
> Evidências corrigidas, classificação e limitações: [QA-009](../defect-elimination/README.md).

Ferramenta escolhida neste ambiente: **Gremlins v0.6.0** (versão/sum no binário).
Piloto reproduzível: cópia temporária do módulo, dependências locais com versões
exatas e enumeração dry-run. A primeira tentativa falhou por metadados do proxy
indisponíveis; a segunda funcionou offline. O dry-run enumerou o player inteiro
(438 mutantes: 408 runnable e 30 sem cobertura), não uma execução de 438 mutantes.

Gomutants foi avaliado pelas fontes primárias e por tentativas de instalação da
versão v0.5.0. Download/checksum falharam por DNS mesmo em execução escalada;
a verificação de checksum não foi desativada. Não houve execução de gomutants,
comparação de velocidade nem experimento head-to-head concluído. A escolha de
Gremlins é operacional, por piloto executável e relatórios obtidos; reavaliar
gomutants quando instalação e checksum estiverem disponíveis.

## Campanha final

| Área | Detectados | Sobreviventes | Não cobertos | Wall time |
|---|---|---|---|---|
| player | 26 | 0 | 3 | 8.2s |
| fidelity | 81 | 0 | 26 | 26.2s |
| storage | 34 | 0 | 3 | 5.6s |
| permissions | 37 | 0 | 11 | 17.6s |

Total: **178 detectados, zero sobreviventes entre executados, 43 não cobertos**.
Nenhum equivalente declarado, inválido/NOT VIABLE, timeout ou inconclusivo final.
Falhas prévias de infraestrutura não entram no score.

- Efficacy entre executados: 178/(178+0) = **100%**.
- Cobertura dos mutantes enumerados no escopo: 178/221 = **80,5%**.
- Não é um score de 100% sobre todo o repositório.

Escopo: boundary nos arquivos controls/queue/previous/durable do player; negation
em spotify_rank/fidelity, sqlite e commands. A campanha enumera sistematicamente
esses operadores e arquivos, não todos os operadores possíveis. Os nove mutantes
guiados anteriores são complementares e não foram somados a este denominador.

Cada campanha usa cópia temporária, prova baseline pelo Gremlins, mantém fontes
originais e usa testes Advanced/Quality/Force/CommandsRequireGuildAndVoice. O
modelo usa seed 41 nas mutações para limitar custo; a campanha completa de 2.048
seeds é executada separadamente. Wrapper e comando exatos em qa008_mutation.py
e evidence/mutation/*-run.json. Não alterar go.mod do checkout: replacements de
módulos em cache existem só nas cópias.

Não houve sobrevivente executado a fechar nesta rodada. Os 43 NOT COVERED
continuam riscos: executar demais testes/integrações para essas linhas antes de
classificá-los como equivalentes. Mutantes de aritmética, retorno, chamadas,
anúncio e remoção de cancelamento não foram enumerados nesta campanha de dois
operadores. Continuar os experimentos guiados para esses riscos.

Referências primárias: [Gremlins](https://github.com/go-gremlins/gremlins) e
[gomutants](https://github.com/gomutants/gomutants). Recursos documentados como
overlays/cache de gomutants não são prova de compatibilidade neste repositório.
