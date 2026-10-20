# QA-013 — interações e estratégia quantitativa

## Campanha nova

TestQA013OrderedCommandPairs executa o produto exaustivo dos fatores
primeiro comando (22), segundo comando (22) e estado inicial (idle/playing/paused):
**1.452 sequências**, incluindo auto-pares. Isso cobre todos os pares de comandos
nesses três estados, além de uma covering array reduzida. Não é cobertura
exaustiva de todas opções, usuários ou interleavings.

Cada sequência compara um oráculo independente após cada comando: current,
CurrentStarted, ordem, multiset, histórico de navegação, loop, pause, autoplay,
geração e estado durável. Shuffle pode escolher qualquer permutação válida;
só sua ordem é adotada após validar multiset, nunca current/histórico. Há ACK
antes de mutação, resposta final e teste de EOF, novo play, novo EOF e stop.
Cleanup verifica ausência de recursos de stream ativos e de sobreposição.

O modelo existing advanced (2.048 seeds × 130 operações = 266.240 operações)
agora verifica também geração. TestQA013DurableModelSequences acrescenta
256 seeds × 258 operações = **66.048 operações** com snapshot durável após cada
passo. O store de modelo usa serialização independente, não SQLite; os 22 ciclos
de restart e as suítes storage exercitam o SQLite real. Falhas de seeds QA-013
são salvas em BARD_QA_FAILURE_DIR ou /tmp/bard-qa013-failures, com replay por -run.

## Combinações críticas e classificação

| Interação | Testes / observação |
|---|---|
| Shuffle + EOF / pending | Production302ShuffleEOF; ProgressiveMutationSequences; permutação controlada |
| Shuffle + skip/move/remove/jump | Pares e triples; progressivo verifica slots durante matcher bloqueado |
| Skip + EOF | 64 interleavings EOF/DiscordSkip; aceita apenas current b ou c e histórias legais |
| Skip + pending | Production302LostNotificationManualSkip e sequência progressiva |
| Previous + shuffle | CriticalCommandSequences começa com skip para criar histórico válido |
| Replay + pause / skip | Pares e triples; gate novo, histórico e primeiro frame validados |
| Seek + EOF | Seek rejeitado; EOF continua correto |
| Loop + skip / clear | Pares/triples + modelo; skip não repete; clear desliga loop |
| Stop + resolution | ProgressiveMutationSequences; worker cancelado não ressuscita fila |
| Leave + playback | Pares/triples e StartingControl; conexão fechada e próxima sessão aceita |
| Play + play concorrente | ConcurrentPlayRequestsAndEOF; dois solicitantes preservados, sequência drenada por EOF |
| Queue + mutações | ConcurrentQueueAndMutations: 48 handlers liberados por barreira |
| History + restart | EveryCommandSQLiteRestart: ledger append-only e guild 2 isolada |
| Autoplay + fim de fila | Pares idle/autoplay e EOF; preferência sem provider não inventa próxima música |

EOF + skip pode cancelar a tentativa que terminou ou descartar o sucessor antes
de seu primeiro frame. Current=c permite histórico [a,c] ou [a,b,c]; current=b
permite [a,b]. Não aceitar [a,b,c] como única história era um erro de oráculo,
não um defeito confirmado do player. Casos com previous sem histórico, posições
fora da fila e comandos pós-leave são rejeições intencionais, não cenários omitidos.

## Falhas e domínios complementares

- 44 cenários ACK/reply (22×2): ACK falho não executa; resposta HTTP falha após
  ACK preserva mutação aplicada e a próxima reprodução. Queue tem segundo PATCH
  limitado para aviso de falha, não retry de playback.
- Announcements: suítes existentes verificam exatamente uma emissão após frame,
  pending, falha, épocas e destination. Não há confirmação de áudio Discord real.
- Streams/processos: FFmpeg real com fontes simuladas, cancelamento, morte de
  subprocessos, EOF, teardown, descritores e stress nas suítes existentes.
- TestQA013CommandPersistenceRollbackAndRecovery injeta ABORT em transação SQLite
  depois do ACK: efeito em memória continua, commit falho não é parcial e EOF
  posterior recupera persistência após remover o fault.
- TestQA013AnnouncementFailureDoesNotBlockNextTrack falha o primeiro anúncio
  e verifica anúncio da sucessora; TestQA013ConcurrentGuildIsolation executa
  play simultâneo em duas guilds e stop em apenas uma.
- Persistência: restart por comando, isolation/artwork/ledger, rollback e interrupção
  transacional, kill de processo em DB temporário, cache e logstore existentes.
- Logging: CommandToTransitionPersistentCorrelation verifica envelopes SQLite,
  interaction_id de receive/ACK, revisão da mutação e attempt nas cinco etapas.
- Fault injection em cleanup/stream/resolução/DB usa as suítes de domínio existentes;
  a campanha nova não finge que reply HTTP cobre essas fronteiras.

## Reprodução

```sh
go test ./internal/discord -run '^TestQA013' -count=1
go test ./internal/player -run '^TestQA013' -count=1
go test ./internal/player -run '^TestQA013DurableModelSequences$/seed=17$' -count=1
python3 scripts/qa013_mutations.py
```

Ver results.md para comandos completos, fuzz/stress, baseline, contagens reais,
limitações e gates da release. Repetições/race e assertions não são somadas como
novos cenários; os 64 interleavings são repetições de um contrato concorrente.
