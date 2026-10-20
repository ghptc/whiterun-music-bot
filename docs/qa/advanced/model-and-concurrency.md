# Modelo independente e concorrência

O oracle de advanced_model_test.go guarda IDs, listas e valores observáveis. Não
usa queueEntry, transforms do player nem tickets de geração para calcular resultados.
A implementação real continua responsável por locks, goroutines e transições.

Campanha: 2.048 seeds PCG, cada uma com duas operações iniciais e 128 escolhas:
266.240 passos. Inclui attach, batch enqueue, remove, move, skip, jump, replay,
previous, loop, pause/resume, EOF/sucesso/falha parcial, clear, stop, leave, autoplay,
seek indisponível, tickets obsoletos e shuffle. Tamanhos 1.000/1.001 provocam
capacidade e overflow. Não encerra cedo quando a fila fica vazia.

Após cada operação e quiescência, compara atual, fila, histórico (limite 100),
identidade completa de Track exceto Timing, loop, pause, autoplay e CurrentStarted;
guild independente deve permanecer intacta. Erro de operação não pode alterar
estado. Para shuffle, verifica multiset e adota a ordem observada, pois a permutação
de produção é deliberadamente aleatória. Não atribuir previsibilidade ao RNG global.

Cada falha salva seed e bytes de comandos em BARD_QA_FAILURE_DIR (default:
 /tmp/bard-qa008-failures). Replay: BARD_QA_MODEL_SEED=N go test ./internal/player
-run '^TestAdvancedModelSequences$' -count=1. Native fuzzing compartilha o interpretador
e salva inputs que falham pelo mecanismo testdata/fuzz do Go. Não houve seed de
modelo falhando nesta campanha.

## Linearizabilidade

128 seeds de argumentos para três operações simultâneas: inserção de batch,
move e remove. Compara resultado e erros contra todas as seis ordenações legais
do modelo, incluindo posições que mudam de validade durante a disputa. A ordem
escolhida pelo scheduler não é presumida. Repetido 20 vezes sob race detector.

## Relógio virtual

testing/synctest já era utilizado no projeto; foi estendido, não introduzido do zero.
Os novos testes cobrem deadline de resolução, deadline do primeiro frame, frame
antes da resolução, frame a 1 ns do deadline e cancelamento; validam que timers de
startup não cancelam áudio iniciado. Vinte assinantes de busca são cancelados em
quatro schedules (0/1/10/20 sobreviventes), preservando trabalho de outras guilds e
cancelando o último assinante.

No Discord, REST real do SDK opera sobre transporte simulado e limiter injetado
para verificar budgets virtuais, 429, 503, desconexão e timeout. Isso não substitui
os testes de contrato existentes do limiter real: o próprio limiter real tem a
falha QA008-D03 e não encerra sua goroutine dentro da bolha.

Tempo em time.Sleep nessas bolhas representa avanço intencional do relógio virtual,
não espera de escalonamento. Canais, WaitGroup e synctest.Wait sincronizam operações.
FFmpeg, SQLite, TCP, processos e filesystem reais ficam fora das bolhas.
