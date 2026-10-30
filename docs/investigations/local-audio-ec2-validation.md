# Fonte local de áudio e homologação EC2

Data: 30/10/2026. Resultado: **pipeline local validado com FFmpeg real e Voice simulado; áudio real na EC2 ainda não executado**. Não houve SSH, deploy, restart, alteração de SQLite ou mudança de produção nesta implementação.

## 1. Implementação

A fonte interna usa `RequestedSource: "local"`. `media.LocalTrack(id, duration)` constrói um descritor finito; `URL` armazena apenas um identificador de arquivo, nunca um caminho absoluto. O diretório é configuração administrativa: `Streamer.LocalAudioDir`, ou `Bot.ConfigureLocalAudio()` antes de iniciar playback. Diretório vazio desabilita a abertura local.

Nenhum comando musical novo foi criado. Nenhum resolver Discord aceita caminhos locais. O caminho local de `Bot.play` ignora resoluções/verificações Spotify e YouTube, inclusive se um descritor interno carregar flags residuais desses provedores. Os demais tipos de fonte mantêm o fluxo existente. A execução normal do bot não habilita uma interface administrativa de enqueue: o harness explícito de homologação faz essa inserção apenas no player do bot dedicado.

Arquivos principais:

- [Descritor](../../internal/media/local.go).
- [Abertura restrita](../../internal/player/local.go).
- [Streamer compartilhado](../../internal/player/stream.go).
- [Configuração administrativa](../../internal/discord/local_audio.go).
- [Harness real optativo](../../internal/discord/local_audio_live_test.go).

## 2. Arquitetura reutilizada

```text
arquivo autorizado -> descriptor aberto uma vez
  -> FFmpeg produtor: remux/copy para Matroska em pipe
  -> FFmpeg decoder existente: libopus, 48 kHz, stereo, 96 kbit/s
  -> parser Ogg existente -> pacing de 20 ms -> Voice existente
```

O produtor FFmpeg substitui o subprocesso yt-dlp nessa fonte. Ele apenas remuxa o conteúdo do descriptor; não é um segundo player. Essa escolha mantém a supervisão existente de produtor/decoder, backpressure, encerramento de grupos de processos, deadlines de primeiro áudio, watchdog de atividade e drenagem após EOF. Há dois subprocessos FFmpeg locais: produtor sem recodificação e encoder Opus. A origem local não executa yt-dlp, Deno ou chamadas de metadados externas.

O `GuildPlayer` continua sendo dono de fila, pausa, progresso confirmado, cancelamento e recuperação. Pausa bloqueia o mesmo parser, com backpressure limitado, sem reiniciar subprocessos; retomada continua a posição. Retomada de checkpoint usa o seek por decodificação/descarte já existente. Skip avança uma posição; stop mantém a semântica explícita atual de limpar a fila do player alvo. Nenhum desses controles solicita Voice Leave. A fonte não implementa uma nova política de retries nem altera FIX-025/REL-024.

## 3. Fixture original

[validation.flac](../../internal/player/testdata/local-audio/validation.flac): tom matemático original, audível, com fade de entrada/saída, sem samples ou músicas comerciais. Duração 30,000 s, FLAC, stereo, 48.000 Hz, 1.558.460 bytes.

SHA-256:

```text
0dc3540d8f2c19d09f30fabadbec072259e4f8378b378e2018a8e0691950f3f3
```

O comando FFmpeg de geração e as condições de uso estão no [README da fixture](../../internal/player/testdata/local-audio/README.md). [Manifesto da fixture](evidence-local-audio/fixture.json). Começar a audição com volume baixo.

## 4. Segurança de caminhos e formatos

- Identificador ASCII com primeiro caractere alfanumérico, no máximo 128 caracteres; somente letras, números, ponto, underscore e hífen. Sem barras, caminho absoluto, espaços, argumentos ou metacaracteres de shell.
- Diretório absoluto, aberto com `O_DIRECTORY | O_NOFOLLOW`; diretório gravável por grupo/outros é rejeitado.
- `openat` usa o descriptor do diretório e `O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC`. Como o identificador não contém separadores, não há componentes intermediários controlados pelo usuário. Symlinks internos e externos são rejeitados atomicamente; não há janela de validação por `EvalSymlinks` seguida de abertura livre.
- `f.Stat()` exige arquivo regular e bits de leitura. FIFO não bloqueia a abertura e é rejeitado; diretórios/dispositivos também não são aceitos.
- Lista inicial limitada a `.wav`/RIFF WAVE e `.flac`/fLaC, com cabeçalho compatível. O decoder valida o conteúdo restante; um cabeçalho aceito sozinho não comprova áudio válido.
- FFmpeg recebe o descriptor em stdin, formato explícito e protocolo permitido somente `pipe`. O decoder aceita somente o container interno Matroska/WebM. Não recebe caminho/URL do arquivo, playlist ou comando derivado do nome.
- Unlink/rename posterior não redireciona um descriptor já aberto. Remover o arquivo durante reprodução pode permitir concluir a leitura do inode existente; uma nova abertura falhará. Truncamento/corrupção do conteúdo não é uma substituição segura de arquivo e pode terminar em falha/EOF antecipado.
- Logs registram ID validado, correlação e métricas, sem diretório absoluto. IDs inválidos são reduzidos a `invalid` na correlação.

O diretório deve conter somente arquivos administrados e autorizados, com permissões restritas, e ser montado somente leitura no harness. Não é um sandbox para arquivos arbitrários enviados por usuários nem uma API de upload. Administradores devem controlar também hardlinks e alterações no conteúdo; a configuração do diretório é uma fronteira de confiança.

## 5. Testes e evidências

A validação foi repetida numa árvore isolada: commit base `c05a4fca149c0ddc9ace72b09bf38733d0c77fec` mais apenas os arquivos desta implementação. Alterações anteriores de resolver, logstore, scripts e relatórios não entraram nessa árvore. Os hashes estão no [manifesto de fontes](evidence-local-audio/source-manifest.json).

| Verificação | Resultado e evidência |
|---|---|
| `go test ./...` | Aprovado na árvore isolada; [log](evidence-local-audio/go-test.txt) |
| `go vet ./...` | Aprovado, sem diagnósticos; [log](evidence-local-audio/go-vet.txt) |
| `go test -race -p 1 ./internal/media ./internal/player ./internal/discord -count=1` | Aprovado; [log final](evidence-local-audio/race-final.txt) |
| `go test ./internal/player ./internal/media ./internal/discord -run '^TestLocal' -count=1 -v` | Aprovado; [log direcionado](evidence-local-audio/targeted-tests.txt) |
| Build Docker padrão | Aprovado, incluindo vet/testes do Dockerfile e compilação do Bardão; [log](evidence-local-audio/docker-build.txt) |
| Build do harness real | Aprovado; [log](evidence-local-audio/harness-build.txt) |
| Harness sem autorização, rede desabilitada | Teste real explicitamente `SKIP`, sem áudio; [log](evidence-local-audio/harness-authorization-gate.txt) |

No ensaio direcionado isolado, o arquivo de 30 s completou em 30,171 s: primeiro frame em 69 ms, 1.501 pacotes de áudio e cinco pacotes finais de silêncio; progresso calculado de 30.020 ms. A diferença de um pacote é padding do encoder, dentro da tolerância do pipeline. São tempos observados neste host, não um SLA para EC2. O teste exige EOF, evento terminal único e término dos dois PIDs observados. Não interpreta envio a Voice simulado como UDP ou audição real.

Cobertura direcionada:

- FLAC completo de 30 s e WAV real; geração de Opus e entrega de frames.
- Pausa/retomada no mesmo track sem avanço de progresso ou fila durante pausa.
- Skip/stop pelo GuildPlayer e pelos handlers reais de `/pause`, `/resume`, `/skip`, `/stop`, com Voice/REST simulados; sem Voice Leave.
- Seek de checkpoint, cancelamento enquanto pausado, EOF após unlink e descritor mantido após remoção.
- Arquivo inexistente, inválido/corrompido, extensão não suportada, conteúdo incompatível com extensão, ausência de permissão, path traversal, metacaracteres, symlink interno/externo e FIFO.
- Encerramento inesperado do encoder; ausência de processos órfãos após EOF, falha e cancelamento, verificada pelos PIDs registrados nos testes.
- Bypass dos provedores mesmo com flags de verificação antigas no descritor local.

### Intermitência preexistente de REL-024

A primeira execução do race detector com verificações concorrentes falhou em `TestREL024HealthDoesNotWaitForDeadlockedWorker`: após liberar o mutex, a asserção exige que um `TryLock` imediato tenha sucesso, embora o worker também possa adquirir esse mutex. Não houve diagnóstico de data race nessa execução.

A mesma falha foi reproduzida **no HEAD original, sem esta implementação**, executando `go test -race ./internal/player -run '^TestREL024HealthDoesNotWaitForDeadlockedWorker$' -count=100`. [Primeira execução](evidence-local-audio/race-initial.txt) e [reprodução no baseline](evidence-local-audio/baseline-health-intermittency.txt) foram preservadas. Não alteramos nem desabilitamos o teste ou a lógica de health. A execução final dos três pacotes com `-race -p 1` passou. A intermitência permanece um risco de confiabilidade da suíte; não foi escondida como falha corrigida pela fonte local.

Imagens construídas **somente localmente**:

| Imagem | ID registrado |
|---|---|
| `whiterun-music-bot-bard:local-audio-validated` | `sha256:520d4d5e5222c3186c73d1c434462e5ae25c556fa575cdd148a36a2905005b3c` |
| `whiterun-music-bot-bard:local-audio-live-validation` | `sha256:750bfb08804c3ad3c62f4f260f4a04fd2a6d95aae20ad571838a6fd4998346e5` |

Os IDs correspondem ao snapshot validado, identificado pelo manifesto. Nenhuma tag foi publicada ou implantada na EC2.

## 6. Procedimento de homologação EC2 — NÃO EXECUTADO

**Exige autorização explícita separada antes de transferir/carregar imagens ou executar o harness na EC2.** Informar ao autorizador: host, canal de homologação, bot de teste, duração aproximada de 30 s, áudio audível, consumo limitado de CPU/memória e encerramento da sessão do bot de teste ao final. Qualquer futura implantação no serviço principal também exige autorização própria.

### Preparação para revisão

1. Usar uma **aplicação/bot Discord dedicada**, com token próprio, diferente do Bardão de produção. Convidá-la somente à guild de homologação necessária, com View Channel, Connect e Speak no canal escolhido. O harness não sincroniza ou altera slash commands.
2. Conferir externamente os dois IDs de aplicação e a identidade do token. Não usar o token do Bardão atual e não abrir uma segunda Gateway com a identidade de produção. A guarda compara o ID de teste com o ID de produção fornecido pelo operador; um ID de produção informado incorretamente não substitui essa conferência.
3. Verificar recursos atuais da EC2 e sessões ativas. Não parar serviços, limpar fila, desconectar ouvintes, alterar bancos ou remover checkpoints para abrir espaço. Adiar se não houver recursos ou canal adequado. A fila de produção não é aberta pelo harness.
4. Guardar as variáveis abaixo em arquivo de secrets temporário, modo 0600, fora do repositório e da stack; não imprimir seus valores:

```text
BARD_LOCAL_AUDIO_LIVE=I_AUTHORIZE_TEST_BOT_AUDIO
BARD_TEST_DISCORD_TOKEN=<token do bot dedicado>
BARD_TEST_APPLICATION_ID=<aplicacao de teste>
BARD_PRODUCTION_APPLICATION_ID=<aplicacao real de producao conferida>
BARD_TEST_GUILD_ID=<guild de homologacao>
BARD_TEST_VOICE_CHANNEL_ID=<canal de homologacao>
```

### Construção local do artefato aprovado

Usar um checkout limpo do commit entregue, sem incorporar alterações não relacionadas. Os comandos abaixo são preparação local; não executam Voice:

```sh
docker build -t whiterun-music-bot-bard:local-audio-validated .
docker build --target test-env -t whiterun-music-bot-bard:local-audio-test-env .
docker build -f docker/local-audio-validation.Dockerfile \
  -t whiterun-music-bot-bard:local-audio-live-validation .
```

O Dockerfile adicional preserva o Dockerfile padrão e seu target final. Copia um binário de teste estático e a fixture para a imagem runtime, executada como `bard`. Não adiciona Go/compilação ou bancos ao procedimento EC2. Conferir checksums, commit e IDs das imagens antes de solicitar autorização. Um rebuild pode produzir IDs diferentes; registrar o ID efetivamente transferido.

### Execução única, somente após autorização

Transferir/carregar a imagem pelo mecanismo autorizado, sem substituir Compose ou o serviço principal. Registrar horário UTC, SHA do commit, ID da imagem e checksum da fixture. Não montar volumes persistentes, secrets ou SQLite da aplicação.

Exemplo de execução isolada a ser revisado antes de uso (caminho de secrets é apenas exemplo):

```sh
set -o pipefail
docker run --rm --read-only --cap-drop ALL \
  --security-opt no-new-privileges \
  --memory 256m --cpus 0.5 --pids-limit 128 \
  --tmpfs /tmp:rw,nosuid,noexec,size=32m \
  --env-file /caminho-administrativo-seguro/local-audio-validation.env \
  whiterun-music-bot-bard:local-audio-live-validation \
  2>&1 | tee local-audio-validation-authorized.log
```

A rede precisa permitir DNS, Gateway Discord e Voice/UDP. Não usar `--network none` na homologação real. O diretório de áudio `/opt/bard-validation` e a fixture já estão na imagem; filesystem fica somente leitura. O teste recusa identidade igual à aplicação de produção, Voice já ocupada pelo bot de teste e player de teste com fila/trabalho existente. Não carrega Store nem Spotify e não usa yt-dlp. Não há retry automático do harness: se falhar, preservar o log e analisar antes de outra tentativa.

O teste entra no canal configurado, enfileira uma única fixture no GuildPlayer existente, observa submissão UDP, primeiro frame, progresso e EOF, e encerra somente a conexão do bot de teste ao finalizar. Esse encerramento é limpeza explícita do harness, não Voice Leave causado por pausa, skip, stop ou falha de provedor. Sessões de produção pertencem a outra identidade e permanecem fora do teste.

## 7. Observabilidade e critérios de sucesso

Eventos locais: `local_audio_opened`, `local_audio_first_frame`, `local_audio_completed` ou `local_audio_failed`. Trazem `local_correlation_id`, ID validado, duração, progresso e tempo até o primeiro frame; o terminal traz contagem de frames. O logger registra timestamp; o harness também escreve `validation_started_at` e `validation_finished_at`. O evento `first_discord_send` comprova submissão UDP, não recepção/audição.

A homologação exige todos os itens:

1. Execução autorizada no canal correto, ID de aplicação de teste conferido, sem interrupção de sessão existente e sem alteração de fila/catalog/checkpoints de produção.
2. Teste realmente executado: **`SKIP` não é homologação aprovada**, mesmo se o processo retorna zero.
3. Um opened, um first frame, um terminal completed, nenhum failed; correlação consistente e primeiro UDP observado.
4. Progressão até aproximadamente 30 s e EOF; nenhum travamento, subprocesso persistente ou repetição automática. Registrar recursos/resultado da execução e verificar que o container temporário terminou.
5. Uma pessoa no canal confirma tom audível e continuidade até o fim. Confirmar essa observação no relatório com horário UTC; métricas não substituem audição.

## 8. Limitações e estado final

A implementação comprova localmente o pipeline de arquivo até a fronteira Voice, com encoder real. O ensaio dedicado na EC2 verificará essa infraestrutura/rota com uma identidade de teste, sem diagnosticar automaticamente o estado de uma sessão antiga do Bardão. Ainda não há áudio real confirmado nesta entrega.

A fonte suporta WAV/FLAC administrados e duração fornecida pelo operador; não é um catálogo comercial, interface de upload ou fallback silencioso da playlist bloqueada. Não resolve o desafio YouTube, não substitui músicas existentes e não concede direitos musicais. A fixture foi gerada especificamente para não depender desses provedores.

Conclusão local: implementação e procedimento preparados; nenhuma autorização de deploy ou de áudio real foi presumida. O incidente de produção permanece aberto até homologação efetiva, e eventual restauração da playlist original continua dependente de uma fonte autorizada e disponível.

## 9. Interface administrativa para homologação manual

A autorização posterior de deploy pediu acionamento pelo proprietário no Discord. A revisão complementar adiciona `/audio-check`, **sem opções de arquivo**, publicado somente na guild configurada. O harness com bot dedicado continua disponível, mas não é necessário para esse acionamento manual e não será executado pelo deploy.

Configuração administrativa antes do startup:

```text
BARD_LOCAL_VALIDATION_GUILD_ID=404344088691867649
BARD_LOCAL_VALIDATION_CHANNEL_ID=921489232374140978
```

Esse destino foi autorizado pelo usuário e verificado pela API Discord como Voice da guild indicada. O runtime inclui a fixture em `/opt/bard-validation/validation.flac`. Sem configuração de destino, a interface fica desabilitada. No startup, o bot consulta a aplicação/canal por REST, verifica guild e tipo de canal, resolve o proprietário real da aplicação (ou da equipe proprietária) e registra apenas esse comando administrativo na guild. Nenhuma dessas ações entra em Voice ou enfileira áudio.

Uso manual: entrar no canal Voice autorizado e executar `/audio-check` com a conta proprietária da aplicação. A permissão padrão do comando exige administrador da guild; a autorização de execução também exige o ID do proprietário obtido do Discord, sem confiar em roles ou argumentos enviados pelo usuário. A resposta é efêmera. Se a conta proprietária não tiver permissão de ver o comando, a administração da guild deve conceder acesso ao comando exclusivamente a essa conta; isso não amplia a verificação no servidor.

O comando recusa guild/canal divergentes, Voice já ocupada, faixa atual, fila pendente, resolução em andamento, estado bloqueado/halted, loop ou autoplay. Não limpa dados para tornar o teste possível. Uma invocação aceita conecta o mesmo bot e enfileira somente a fixture no GuildPlayer existente. Repetições são recusadas enquanto a sessão está anexada. Não há repetição automática da fixture ou comando de arquivo arbitrário. Depois da audição/EOF, `/leave` pode ser executado manualmente pelo usuário para encerrar a sessão de homologação; não é executado pelo deploy.

Essa extensão muda o procedimento de preparação, não o estado da homologação: nenhum áudio real é considerado aprovado apenas pela implantação ou registro do comando. O SHA complementar e o estado operacional efetivo constam no relatório de deploy.
