# QA-013 — regressão P0 de produção

Origem: 687 registros SQLite analisados em OBS-001. Em 2026-10-20 UTC,
shuffle às 13:12:09 (ID 439), EOF natural às 13:17:27 (465–467), ready=6,
pending=295, next_pending=true, provider_blocked=false, halted=false. Nenhuma
resolução seguinte até skip às 13:18:47 (477); seleção da sequência 2 às
13:18:50 (544). Ver [cronologia e limitações Docker](../../observability/playback-flow.md).

`TestQA013Production302ShuffleEOF` usa 302 identidades originais, seis slots
prontos e 295 pendentes. Barreiras controlam EOF e a segunda janela de resolução.
A permutação do caminho real de Shuffle coloca a posição 302 na cabeça. Após
EOF, a assinatura exata acima é verificada antes de liberar o matcher. A próxima
faixa deve ser 302, sem skip. Stop cancela workers e não permite ressurreição.
Relógio virtual e canais substituem sleeps e dependências externas.

`TestQA013Production302LostNotificationManualSkip` injeta o caminho histórico:
mesma permutação, mas notificação somente ao scheduler. O resolver fica dormindo;
a assinatura é verificada e skip **manual no teste** descarta a cabeça e inicia
posição original 3. É reprodução diagnóstica do incidente, não solução automática.

O mutante `lost-resolver-window-wakeup` remove exclusivamente o broadcast
introduzido pelo hotfix, em cópia descartável do código. A regressão de 302
posições deve falhar com `shuffle + EOF did not advance after pending resolution`.
O código do checkout, volume e contêiner de produção não são modificados.

```sh
go test ./internal/player -run '^TestQA013Production302' -count=1
python3 scripts/qa013_mutations.py
```

A causa já foi corrigida no commit `941a876`: a mudança de janela precisa acordar
`CollectionJob.wake` e `GuildPlayer.wake`. A campanha QA-013 acrescenta prova
regressiva; não adiciona outra correção funcional nem altera Fidelity.
