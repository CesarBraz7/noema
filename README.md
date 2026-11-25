# Noema

**Pipeline pessoal de ingestão e consolidação de conhecimento.**

O Noema transforma links em notas de fonte e documentos de estudo rastreáveis no Obsidian. Roda localmente em Docker Compose.

## O que já funciona

- **Artigos web** com extração de texto, referências por bloco e publicação de uma nota de fonte.
- **YouTube** com transcrição do áudio original pelo Whisper, checkpoints por segmento, capítulos e análise visual opcional com Gemini.
- **Instagram** com Reels, carrosséis e posts de imagem, preservando a legenda, a ordem das mídias, a transcrição de vídeos e o texto reconhecido nas imagens.
- **Documentos de estudo** com explicações, exemplos e evidências a partir de uma fonte. Uma segunda fonte pode acrescentar contexto. O documento vai para revisão antes de entrar em `Conhecimento/`.

O fluxo no Docker foi validado com artigos, vídeos do YouTube de mais de 20 minutos, três Reels e um carrossel de 11 imagens. O teste de integração cobre um post de imagem simulado. [Roadmap](docs/ROADMAP.md)

O código e a documentação vivem neste repositório. Cada instalação usa dois repositórios privados próprios para os dados, um para a inbox e as notas do Obsidian e outro para os artefatos JSON e as evidências.

## Arquitetura de referência

- **Go** como orquestrador enxuto, rodando em Docker Compose no Windows durante o desenvolvimento e compatível com hosts Linux AMD64 e ARM64;
- **PostgreSQL externo (Neon) + River OSS** para fontes, jobs, retries e estados;
- **Extração:** `yt-dlp`, `gallery-dl`, `trafilatura`, FFmpeg, Whisper e Tesseract;
- **IA opcional via Gemini:** análise visual do YouTube e propostas de documentos de estudo;
- **Artefatos auditáveis:** `source-content.json`, análise por modalidade, `knowledge.json` e `manifest.json`;
- **Persistência separada:** repositórios privados para notas e artefatos, com mídias em armazenamento local;
- **Saída:** notas de fonte e conceitos para Obsidian, preservando anotações pessoais.

## Documentos

- [Especificação auditada v1.3](docs/especificacao-arquitetura-v1.3-auditada.md)
- [Roadmap de implementação](docs/ROADMAP.md)
- [Auditoria de decisões e correções](docs/AUDITORIA-DECISOES-2026-11-22.md)
- [Decisões arquiteturais (ADRs)](docs/adr/0001-base-arquitetural.md)

## Executar localmente

Os comandos abaixo funcionam no Windows com o Docker Desktop iniciado. O worker no contêiner se conecta ao Neon e ao GitHub.

1. Após vincular o projeto com `neon init`, rode `neon deploy` para trazer as variáveis do Postgres. O CLI cria `.env.local` ou atualiza `.env` se ele já existir. O Noema prefere `DATABASE_URL_UNPOOLED` e aceita `DATABASE_URL` como alternativa para uma conexão **direta** via TLS. A conexão direta é necessária para o River e o lock de sessão do worker.
2. No mesmo arquivo, defina `GITHUB_OWNER`, `GITHUB_VAULT_REPO` e `GITHUB_ARTIFACTS_REPO` para os seus repositórios de dados. Configure um token com `Contents: Read and write` nesses repositórios. Use `GITHUB_TOKEN` para um token compartilhado ou `GITHUB_VAULT_TOKEN` e `GITHUB_ARTIFACTS_TOKEN` para tokens separados. O [arquivo de exemplo](.env.example) lista as variáveis.
3. Adicione um link de artigo, vídeo individual do YouTube ou publicação do Instagram a `Inbox/links.md` no GitHub ou no Obsidian. Se editar pelo Obsidian, envie a alteração ao GitHub. O check da caixa é opcional. Use uma linha indentada para acrescentar foco.
4. Construa a imagem, prepare o banco e rode um primeiro ciclo. Se o CLI gravou em `.env`, use esse arquivo no argumento `--env-file`.

```bash
docker compose --env-file .env.local -f deploy/compose.yaml build noema
docker compose --env-file .env.local -f deploy/compose.yaml run --rm noema migrate
docker compose --env-file .env.local -f deploy/compose.yaml run --rm noema once
```

O `once` consulta `Inbox/links.md`, drena os trabalhos elegíveis e encerra. Os comandos abaixo ativam a ingestão contínua e abrem os logs.

```bash
docker compose --env-file .env.local -f deploy/compose.yaml up -d noema
docker compose --env-file .env.local -f deploy/compose.yaml logs -f noema
```

A inbox é verificada a cada 15 minutos, com ETag. O serviço verifica a fila na inicialização, quando recebe entradas e a cada duas horas para atender retries. O PostgreSQL fica desconectado entre os ciclos. As solicitações, tentativas e erros ficam em `noema_ingestion_requests`.

A captura `source-content.json` é reutilizada quando o mesmo artigo é reenviado. Um novo foco gera `knowledge.json` e `manifest.json` próprios. O manifesto referencia o caminho e o SHA-256 da captura. A nota em `Fontes/Artigos/` aponta para o manifesto e para os blocos que sustentam cada trecho. Se a nota já tiver sido editada ou removida, o worker preserva a mudança e registra `NEEDS_REVIEW`.

Para vídeos, o worker guarda os metadados e a origem do texto em `source-content.json`, com início e fim de cada trecho em milissegundos. Novas notas são publicadas em `Fontes/Videos/` e referenciam esses intervalos. Links equivalentes reaproveitam a captura e o mesmo pedido quando o foco é igual.

O worker baixa a faixa original com yt-dlp, normaliza o áudio com FFmpeg e transcreve com Whisper local. O processamento ocorre em partes e retoma do último segmento salvo, sem teto global de duração ou tamanho do áudio. O Docker Compose mantém o áudio e os checkpoints em `data/`. Legendas entram como fallback declarado quando a faixa original estiver definitivamente indisponível. Restrições de acesso ficam registradas, e falhas temporárias entram nas tentativas do River. [Especificação auditada](docs/especificacao-arquitetura-v1.3-auditada.md#82-vídeos-três-responsabilidades-distintas)

`NOEMA_WORK_HOST_DIR` permite escolher o diretório montado pelo Docker Compose. `NOEMA_ASR_CHUNK_DURATION` define o tamanho dos segmentos (padrão `5m`) e `NOEMA_VIDEO_ATTEMPT_TIMEOUT` define o orçamento de tempo por tentativa de vídeo (padrão `15m`). A imagem inclui o modelo local Whisper `tiny-q5_1` para desenvolvimento. `NOEMA_WHISPER_MODEL` permite informar outro modelo acessível no contêiner.

Para retomar um pedido em revisão por falta de legenda, ausência de ASR ou pelo antigo limite de duração, reenfileire o vídeo e rode um ciclo manual.

```powershell
docker compose --env-file .env.local -f deploy/compose.yaml run --build --rm noema retry-video 'https://www.youtube.com/watch?v=VIDEO_ID'
docker compose --env-file .env.local -f deploy/compose.yaml run --rm noema once
```

## Habilitar a análise visual

Use uma chave de um projeto Gemini da camada gratuita, sem faturamento. Configure `GEMINI_API_KEY` e selecione em `NOEMA_GEMINI_MODEL` um modelo gratuito compatível com vídeo, como `gemini-3.1-flash-lite`. Antes de iniciar o worker, rode `migrate` para habilitar os estados da análise e da fila de cotas. [Vídeo no Gemini](https://aistudio.google.com/docs/video-understanding#youtube) · [Modelos e preços](https://aistudio.google.com/docs/pricing)

Para vídeos com visibilidade pública confirmada, o worker envia a URL canônica ao Gemini após preparar a transcrição. `video-visual.json` registra a resposta estruturada, o modelo informado pelo provedor, o uso de tokens quando fornecido, os intervalos de evidência e as lacunas. Marcações além da duração são descartadas e contabilizadas na nota. A nota reúne evidências da transcrição e da imagem com links para os timestamps. O processamento reaproveita a análise salva quando muda apenas o foco da inbox.

Quando a análise aponta um detalhe visual que precisa de confirmação, o worker baixa um trecho curto e extrai o frame correspondente com FFmpeg. O Gemini recebe os frames selecionados e trechos próximos da transcrição do áudio, quando disponíveis. `video-frame-inspection.json` registra a evidência e os pontos que ficaram sem confirmação. Os JPEGs ficam no diretório local de trabalho com SHA-256 e dimensões registrados no artefato.

Se o Gemini indicar que a entrada nativa é incompatível e houver transcrição do áudio original, o worker inspeciona até quatro frames escolhidos pelos capítulos ou distribuídos pela duração do vídeo. A nota identifica o modo parcial e os timestamps que têm evidência. A rejeição por incompatibilidade fica salva para reaproveitamento com novos focos.

Uma resposta HTTP 429 deixa a análise em `WAITING_QUOTA` até o prazo informado pelo provedor, ou até a próxima janela de retry. A configuração padrão usa a captura local de áudio. Quando a visibilidade pública não pode ser confirmada, o worker registra essa limitação na nota e mantém a captura local.

## Inspecionar um vídeo do YouTube

Troque `VIDEO_ID` pelo identificador de um vídeo público e execute no PowerShell.

```powershell
docker compose -f deploy/compose.yaml run --build --rm noema inspect-video 'https://www.youtube.com/watch?v=VIDEO_ID'
```

O comando imprime metadados e capítulos disponíveis em JSON. Os capítulos têm início e fim em milissegundos. Para diagnosticar legendas, passe `inspect-video --captions URL`. Nesse modo, `captionsStatus` informa se a legenda foi obtida (`available`), se nenhuma faixa compatível foi encontrada (`unavailable`) ou se a leitura falhou (`fetch_failed`). Para gravar artefatos e publicar a nota com links para os capítulos, use a inbox e execute `once`.

## Ingerir uma publicação do Instagram

Adicione um link individual `/p/`, `/reel/` ou `/tv/` à inbox e execute `once` pelo Docker Compose. Os formatos de URL do mesmo shortcode apontam para a mesma fonte. A mídia retornada determina se a publicação é um Reel, carrossel ou post de imagem.

O Noema preserva a legenda e a ordem dos itens. Usa o áudio original para transcrever vídeos e o Tesseract em português e inglês para ler texto das imagens. Os arquivos de mídia ficam no diretório local de trabalho. A captura associada ao manifesto registra posição, tipo e hash dos arquivos. A nota é publicada em `Fontes/Instagram/` e indica itens ausentes ou outras lacunas. Uma publicação que exige acesso indisponível recebe `ACCESS_REQUIRED`.

## Propor uma nota conceitual

Escolha um tema e use a URL original de um artigo, vídeo público do YouTube ou publicação do Instagram que já tenha uma nota no vault. Com o Gemini gratuito configurado, rode o comando pelo Docker Compose.

```powershell
docker compose --env-file .env.local -f deploy/compose.yaml run --rm noema draft-concept 'Captura de variáveis de laço em Go' 'https://go.dev/blog/loopvar-preview'
```

O documento explica o tema e inclui exemplos que podem ser entendidos pela própria nota, com preparação, código e saída esperada quando a evidência sustenta esses detalhes. Você pode passar uma segunda URL ao final do comando para acrescentar contexto. Uma conexão entre fontes só aparece quando ajuda a explicar o tema.

O comando confere os artefatos do manifesto e usa o texto, a transcrição e as observações visuais disponíveis. Conjuntos maiores são examinados em partes, na ordem da fonte. Os achados de cada parte são guardados no diretório de trabalho para reutilização em uma nova tentativa. O modelo sintetiza os achados e faz uma segunda leitura das afirmações contra as referências antes de gravar a proposta.

O caminho retornado fica em `Revisao/Conceitos/`. A nota mostra a cobertura da análise e inclui links para os blocos e timestamps citados. Após revisar o conteúdo, copie a versão aprovada para `Conhecimento/`.

Enquanto a proposta estiver em revisão, repetir o mesmo tema com as mesmas URLs abre o mesmo caminho e preserva as edições. Esse ciclo reaproveita os artefatos de captura.

## Verificar localmente

Use Go 1.25+ para compilar, rodar os testes e consultar a versão. `npm run build` executa o mesmo build Go para a validação do setup do Neon.

```bash
go build ./...
go test ./...
go vet ./...
go run ./cmd/noema version
```

Para incluir os testes de integração, instale Trafilatura com `python -m pip install -r deploy/extractors.requirements.txt`, aponte `NOEMA_INTEGRATION_DATABASE_URL` para um banco PostgreSQL de teste vazio e execute `NOEMA_REQUIRE_TRAFILATURA=1 go test -count=1 ./...`. O teste do fluxo usa uma API GitHub simulada e cria tabelas de Noema e River no banco de teste.

## Segurança

Nunca enviar chaves de API, cookies, sessões, senhas, arquivo `.env` real, MP4s ou dumps de banco ao Git. Não habilitar fallback pago automaticamente. Não sobrescrever conteúdo pessoal do Obsidian. Todos os repositórios de dados devem permanecer privados.
