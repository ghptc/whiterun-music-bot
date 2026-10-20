# Native Go fuzzing

Sete targets, 15 segundos nominais por target, dois workers, GOMAXPROCS=2 e
GOMEMLIMIT=256MiB por processo. Timeout interno de 75s e externo de 105s.
GOMEMLIMIT é limite **soft** do runtime, não cgroup/RSS hard; os workers têm
orçamento próprio e o total do job é maior. Para uma campanha de horas, usar
runner/container dedicado com limite de memória do host.

| Target | Execuções | Novos inputs interessantes | Wall time com setup |
|---|---|---|---|
| FuzzAdvancedMediaURLs | 275,637 | 150 | 27.9s |
| FuzzAdvancedRecordingMetadata | 36,962 | 113 | 16.4s |
| FuzzAdvancedDiagnosticBudget | 679,356 | 3 | 16.2s |
| FuzzAdvancedOgg | 665,534 | 8 | 16.3s |
| FuzzAdvancedTimestamp | 13,887 | 8 | 15.7s |
| FuzzAdvancedQueue | 36,736 | 112 | 16.2s |
| FuzzAdvancedQueueArguments | 53,415 | 116 | 45.5s |

Total: **1,761,527 execuções**, sem falha de oracle ou
crash observado. Não confundir execuções, casos de seed, inputs de cobertura e
requisitos independentes. O tempo inclui coleta de seeds/minimização; exec/s não
é benchmark de produção.

Targets cobrem canonicalização e authority de Spotify/YouTube/artwork; versões,
ISRC e explicit sob Unicode/metadados malformados; orçamento de diagnostics;
lacing/continuação e cancelamento do consumidor Ogg; timestamp base-60;
interpretador de fila/estado; argumentos limit e budgets da fila.

Bounds por input: URLs 4KB, metadados 2KB, diagnostics 8KB, Ogg 128KB,
timestamp 1KB, sequência 512 bytes, título da fila 4KB. Fuzzing não acessa rede,
banco operacional, subprocessos nem Discord real.

Dez descobertas de cobertura do cache nativo foram selecionadas para o corpus
versionado; provenance/rationale em evidence/corpus.json. A suíte padrão executa
47 seeds/corpus cases novos, sem campanha aleatória. Nenhum input de crash foi
encontrado pelo fuzzer nesta execução; os três defeitos de findings.md vieram
das reproduções dirigidas e da avaliação de synctest, não de crashes desta campanha.

Oracles deliberadamente não validam o codec Opus completo nem prometem rejeitar
todo JSON incompleto. QA008-D01/D02 demonstram os limites desses oracles; suas
reproduções vermelhas estão separadas até uma correção autorizada de produção.

Replay: go test ./internal/player -run 'FuzzAdvancedOgg/NOME_DO_CORPUS' -count=1.
Nova campanha: python3 -B scripts/qa008_fuzz.py --seconds 60 --workers 2.
O Go persiste falhas novas em internal/PACOTE/testdata/fuzz/TARGET; revisar e
versionar reproduções relevantes. O runner encerra o grupo de processos sob
sua propriedade quando o limite externo expira.
