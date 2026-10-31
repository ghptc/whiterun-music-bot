# YT-027 — Imagem candidata com atualização exclusiva do yt-dlp

Data: 31/10/2026. **Candidata aprovada nas validações locais/offline; não implantada.**
Nenhum SSH, mudança de EC2, acesso a vídeo YouTube, download de mídia, cookies,
credenciais, IP/DNS ou reprodução Discord foi realizado nesta etapa.

## Artefato e escopo

- Base: `d6ddec304c64c6a1170fda9e118061a0ead8814d`, extraída com `git archive`.
- Imagem candidata local: `bard-yt027:2026.10.26-candidate-isolated`.
- ID/digest conferido: `sha256:09f093ea1ac8f4a5f4edc8b8b2463bd035e97030090d3ec0469be7e164f6a3b2`.
- Referência local: `whiterun-music-bot-bard:opt026-d6ddec304c64`,
  `sha256:cfb1a0bffc7d59f04945feb0029d4530e66f16c3df3d1b647e37d3e0c7a62bbd`.

Mudanças de configuração: padrão `YTDLP_VERSION=2026.10.26` no Dockerfile,
`.env.example` e no argumento de build do Compose. Os três pontos precisam
concordar para que Compose não continue sobrescrevendo o novo padrão com o antigo.
Um `.env` existente ainda pode sobrescrever esse argumento; ele não foi editado.
Verificar a versão no artefato, em vez de presumir a partir do default.

Nenhum arquivo Go, builder de argumentos, matcher, SQLite, OPT-026, circuit breaker,
política REL-024/FIX-025, controle Voice ou configuração de runtime foi modificado.
As alterações locais anteriores e relatórios de outras etapas ficam fora deste commit.

## Restrição transitiva necessária

O primeiro build alterou também `websockets` de 17.3 para 17.4, pois a receita
original deixava essa dependência transitiva livre. Esse build foi **rejeitado como
candidata final**, embora compilasse: não atendia à atualização exclusiva solicitada.
A evidência está em `initial-build-transitive-drift.json`.

A correção mínima foi acrescentar `websockets==17.3` à instalação pip no Dockerfile,
preservando a versão da referência. Não é uma atualização de websockets nem uma
nova funcionalidade. Não se adicionou outro gerenciador, serviço ou camada de retry.
A instalação final satisfaz os requisitos do novo yt-dlp (`pip check` aprovado).
O primeiro artefato local `bard-yt027:2026.10.26-candidate` não é o aprovado;
usar exclusivamente a tag `candidate-isolated` e o digest acima.

## Inventário real do runtime Debian

Inventários executados como usuário `bard`, em containers descartáveis com
`--network none`, filesystem somente leitura e `/tmp` temporário. Não foram
montados volumes, banco, configurações ou credenciais de produção.

| Componente | Referência | Candidata | Evidência |
|---|---|---|---|
| yt-dlp | 2026.08.19 | **2026.10.26** | Única diferença de pacote Python |
| Deno | 2.5.6 | 2.5.6 | Mesmo SHA-256 do executável |
| EJS | 0.8.0 | 0.8.0 | Mesma distribuição Python |
| FFmpeg | 5.1.9 | 5.1.9 | Mesmo SHA-256 do executável |
| Python | 3.11.2 | 3.11.2 | Debian Bookworm x86_64 |
| Pacotes Debian | 310 | 310 idênticos | Comparação completa de nomes/versões |
| Demais pacotes Python | 12 | 12 idênticos | Inclui websockets 17.3 |
| Binário Bard | Mesmo | Mesmo | Hash abaixo |
| Fixture local | Mesma | Mesma | Hash abaixo |

SHA-256 de `/usr/local/bin/bard` em ambas:
`a68e5cd2cbb3adb63e2c73bf69f3b39ff322185dc490472b9a561830ce3283d2`.
Fixture:
`0dc3540d8f2c19d09f30fabadbec072259e4f8378b378e2018a8e0691950f3f3`.
Inventários completos e hashes de Deno/FFmpeg em
[evidências](evidence-yt027-candidate/inventory-comparison.json).

O `pip check` não encontrou dependências quebradas. Seu aviso de cache não gravável
é esperado no container de teste readonly; não houve falha. O diretor real EJS
reconheceu Deno disponível nas duas imagens. Uma expressão local `6*7` retornou 42
em ambas, sem imports remotos. O build executou também a verificação FFmpeg→Opus.
Isso valida instalação básica no Debian, não um desafio real entregue pelo YouTube.

## Comparação offline das versões

O runner foi executado dentro das duas imagens Debian, sem rede. Downloads, player
responses e respostas de cache JS são mocks. Não há geração de atestação, token,
CAPTCHA ou autenticação. As URLs de fixture usam domínio `.invalid` e nunca são
acessadas. O runner inclui uma proteção adicional contra conexões Python.

| Fixture / perfil | 2026.08.19 | 2026.10.26 |
|---|---:|---:|
| Initial data via atribuição JS tradicional | Lê | Lê |
| Initial data via `script id="yt-initial-data"` | Não lê | **Lê** |
| Metadados presentes no player fixture | Título e duração 210 s | Título e duração 210 s |
| Perfil implantado: `web;player_skip=js;skip=hls,dash,translated_subs` | Um cliente / um player callback | Um cliente / um player callback |
| Mesmo perfil sem `player_skip=js` — somente comparação | Um cliente | Um cliente |
| Clientes padrão com JS disponível | `visionos, web`: dois callbacks | `tv_samsung, visionos, web`: três callbacks |
| Conexões de rede no replay | Zero | Zero |

A correção de parsing é a mudança demonstrada no
[upstream #17703](https://github.com/yt-dlp/yt-dlp/pull/17703), já investigada em YT-027.
A fixture HTML é sintética baseada nesse formato, não uma suposta captura da EC2.
Os campos título/duração do player fixture são intencionalmente fixos: sua igualdade
não prova completude de metadados de todos os vídeos.

Na fase de formatos, um formato muxado sintético direto é preservado nos três
perfis. Formatos com assinatura cifrada ou `n` ficam ausentes quando JS é pulado,
e aparecem com respostas JS de cache pré-simuladas quando ele é permitido. Não
são formatos reais nem uma demonstração de acesso a áudio-only ou GVS.
**Nenhum perfil do Bard foi alterado**; B/C existem apenas no replay para contraste.

### Número de clientes e requisições

O replay chama `_extract_player_responses` para **todos** os clientes selecionados,
com respostas OK determinísticas; conta os callbacks, não só os nomes dos defaults.
No perfil completo de metadados do Bard, `web` continua forçado: 1→1 nesse cenário.
No perfil padrão, a seleção passa de 2→3. O streamer e buscas não forçam o perfil
completo `web`, então uma atualização pode aumentar consultas internas nesses
caminhos. Busca flat pode não percorrer o mesmo caminho de player dessa fixture.

Um callback simulado não é uma requisição HTTP medida. EJS, player configs,
webpage, manifestos e fallback interno dependem do cenário real. Não há alegação
de redução ou limite total HTTP. Os limites globais OPT-026 continuam limitando
operações; não foram relaxados para compensar a nova seleção de clientes.

## Matching, classificação, fila e recuperação

Testes direcionados executados sobre o código Go intacto:

- Corpus OPT-026: **14 positivos, 31 negativos, zero falsos positivos**. Preserva
  studio/live, remix/produtor, radio/extended, cover/original, remaster/edição,
  featured artists, instrumental/vocal e faixa/álbum.
- Diagnósticos bot/auth, restrição etária, 429, timeout, vídeo indisponível e falha
  de extrator permanecem distintos. Challenge em stderr prevalece mesmo com exit
  zero/JSON parcial. Dados sensíveis não são propagados pelo erro.
- Ausência de duração mantém a rejeição/insuficiência prevista, sem afrouxar prova.
  A inexistência de formatos no perfil metadata não é interpretada como áudio pronto.
- Descoberta para na primeira falha de provedor, sem cascade de candidatos.
- Dois GuildPlayers: uma tentativa inicial e três trials globais; **246 posições
  preservadas**, uma notificação por guild, sem Leave inesperado ou avanço duplo.
- Políticas de progresso testadas em 10%, 49%, 50%, 75% e 99%: retomar abaixo de
  50%; avanço único a partir de 50%. Corpus FIX-025 de 123 ocorrências preservado.
- Horizonte de 123 posições e segunda playlist distante preserva ordem; stop/skip/
  pausa/resume e cancelamento continuam pelos mecanismos existentes.
- Cinco consumidores compartilham prova e renovação; SQLite reaberto reutiliza
  prova persistida e preserva offset. Não foram adicionados bancos ou filas.

Os testes Go usam fixtures/mocks para a interface do extrator. A comparação do
pacote Python real é a seção anterior; não se afirma que os mocks representem
cada nova mensagem possível do upstream. As melhorias de diagnóstico da versão
nova ainda precisam ser observadas em respostas autorizadas reais.

## Validação executada

```sh
go test ./... -count=1
go vet ./...
go test -race ./internal/media ./internal/player ./internal/discord ./internal/storage
docker build -t bard-yt027:2026.10.26-candidate-isolated .
```

**Todos aprovados.** Rodada direcionada de identidade/classificação/fila/provas
aprovada. Nenhum teste Go intermitente falhou nesta execução; não houve repetição
até verde. O ajuste foi de empacotamento após detectar drift, não ocultação de teste.
O build repetido reutilizou as etapas Go correspondentes ao mesmo código, já
validado separadamente com execução nova. O Compose foi validado com credenciais
fictícias e `config --quiet`, sem iniciar serviços.

A comparação final comprova que só yt-dlp mudou nos pacotes do runtime. Os hashes
idênticos do binário também confirmam a preservação de argumentos, matching,
SQLite, OPT-026 e recuperação. Nenhum dado persistente foi usado nos containers
de comparação. Testes live opt-in e audição real não fazem parte desta aprovação.

## Plano mínimo posterior — requer autorização própria

1. Confirmar acesso permitido pelo provedor e autorização para o ensaio. Não usar
   repetição de sondas enquanto o challenge estiver ativo. Não alterar IP/cookies.
2. Usar **esta imagem por digest**, em container de diagnóstico isolado na EC2,
   sem trocar o Bardão, sem seus volumes/secrets e sem iniciar Gateway/Voice.
   Conferir revisão/configuração e ausência de outra extração concorrente.
3. Uma única invocação para o ID público `aqz-KE-bpKQ` (Big Buck Bunny), após
   confirmar publicação/uso autorizado. Sem busca, playlist, download de áudio,
   HEAD/GET de mídia ou comparação automática de clientes/versões.
4. Manter o perfil de metadados implantado. Alterar apenas os limites dessa chamada:
   socket 5 s, timeout total 25 s, retries e extractor-retries zero. Não alterar
   configurações permanentes nem rearmar o breaker do bot.
5. Capturar stdout/stderr somente em memória limitada; interromper o grupo ao
   primeiro auth/bot challenge. Emitir categoria sanitizada, disponibilidade de
   título/duração/formatos e tempo, sem URLs de mídia, cookies ou tokens.
6. Em challenge, encerrar sem fallback. Em metadados válidos, relatar somente esse
   resultado: não comprova mídia GVS, recuperação sustentada ou áudio ouvido.
   Reprodução real exigiria etapa manual/autorizada separada.

O filme possui licença de conteúdo aberta, mas isso não substitui a autorização
aplicável para acessar o serviço YouTube. **Este plano não foi executado.**

## Limitações antes de qualquer deploy

- A atualização corrige o formato de parsing demonstrado. Não há evidência de que
  aquela resposta HTML específica tenha causado o challenge ou a duração ausente
  da EC2. Não se declara acesso restaurado.
- Defaults novos podem aumentar fan-out; sem novo orçamento de retries no Bard.
- O pin websockets 17.3 mantém a referência. A receita ainda usa tags/repositórios
  de sistema que podem evoluir; a igualdade demonstrada vale para estes artefatos.
  Não presumir que um rebuild futuro tenha o mesmo inventário: conferir novamente
  ou usar o digest validado, sem introduzir outra camada de infraestrutura.
- Nenhum deploy está autorizado por esta entrega. A imagem está somente no Docker
  local; não foi enviada à EC2 ou a um registry.

Artefatos, fontes dos probes e checksums: [evidence-yt027-candidate](evidence-yt027-candidate/).
Para reproduzir os três probes dentro da imagem, usar `--network none --read-only`,
`/tmp` tmpfs e `--entrypoint /opt/yt-dlp/bin/python -i <imagem> -`, fornecendo o script
por stdin. Isso não executa o entrypoint do bot nem contata o YouTube.
