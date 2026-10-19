# Relatório de concorrência

Baseline race aprovado, mas uma repetição posterior detectou race em recordingRest, helper de testes compartilhado. Falha preservada em race-discovered.txt; diagnóstico e proposta anteriores ao fix em findings.md. Mutex adicionado somente ao recorder; cenário reproduzido em 100 repetições -race e aprovado (race-regression.txt). Não foi silenciado nem removido o teste.

Suíte completa -race aprovada após correção (race.txt). Stress de testes Quality em 20 repetições com race aprovado (stress.txt). A lista contém:
- 20 produtores enfileirando 50 faixas cada, total 1.000; batches atômicos, ordem interna, metadados e rejeição de overflow;
- modelo independente de Move/Remove/Jump/Skip e invariantes para seeds 7, 41 e 20261019, tamanhos 1/2/17/500/1000;
- 20 guilds persistindo dez versões cada em SQLite real;
- skip/replay/jump/stop/leave durante teardown bloqueado por canal; sucessor somente após liberação, nenhum PlayFunc sobreposto e join após Close;
- restauração/cópias defensivas e teste de falha/recuperação do store;
- testes existentes de importação, isolamento, gates de pause, processo e primeiro frame.

Novos testes usam canais, WaitGroup e relógio explícito; timeouts são limites de falha, sem sleeps arbitrários. Shuffle valida invariantes sem fixar ordem aleatória de produção.

Não há garantia universal de ausência de races. O smoke opt-in registra goroutines e FD, mas subprocessos reais, FD por ownership e soak de horas exigem homologação adicional. Não foi encontrado defeito novo de produção nas execuções relatadas; o defeito demonstrado foi no helper.
