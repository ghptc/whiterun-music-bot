# Relatório de mutação

Nove mutantes executados em cópias temporárias dos fontes. Baseline passa antes de cada alteração; o runner restaura a cópia em finally. O checkout de produção nunca é modificado. Compile failure e timeout são inconclusivos, não detectados.

| Mutante | Resultado final |
|---|---|
| Bypass de validação de voz | Detectado |
| Move corrompe posição | Detectado |
| Capacidade permite uma posição extra | Detectado |
| Cache ignora expiração | Detectado |
| Cache compartilha slice mutável | Detectado |
| Matcher ignora ISRC contraditório | Detectado |
| Matcher ignora versão incompatível | Detectado após ampliar matriz |
| LoadGuild descarta histórico | Detectado |
| DeleteMapping não invalida | Detectado |

Resultado observado: 9 detectados, 0 sobreviventes finais, 0 equivalentes declarados, 0 inconclusivos finais. Evidência completa com comandos e saídas: evidence/mutations.json.

O mutante de versões sobreviveu na primeira rodada. QA-002 documenta por que títulos curtos mascaravam essa proteção e quais casos foram acrescentados. Não declarar o mutante equivalente.

Escopo limitado: não foram executados neste runner mutantes de cancelamento ignorado, anúncio duplicado, substituição de vídeo Force Play ou remoção da serialização completa de streams. Os testes existentes de Force Play e as barreiras novas de teardown são evidências comportamentais, mas não substituem esses experimentos de mutação. Scripts históricos check_queue_mutations.py/check_ui_mutations.py permanecem disponíveis, sem resultado atribuído a esta execução.
