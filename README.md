# Noema

**Pipeline pessoal de ingestão e consolidação de conhecimento.**

O Noema transforma links em conhecimento rastreável no Obsidian. A ingestão cobre artigos web e vídeos públicos do YouTube.

> **Fase 1.** A ingestão de artigos usa GitHub, PostgreSQL, River OSS e Trafilatura. A síntese seleciona trechos localmente, com referência ao bloco capturado e sem chamadas de IA.

> **Fase 2 em andamento.** Vídeos do YouTube passam pela extração e transcrição do áudio original. A análise visual com Gemini pode ser habilitada no worker local. [Roadmap](docs/ROADMAP.md#fase-2--vídeos)

## Repositórios

| Repositório | Papel |
|---|---|
| [CesarBraz7/noema](https://github.com/CesarBraz7/noema) | Orquestração Go, integração, Docker e documentação |
| [CesarBraz7/noema-artifacts](https://github.com/CesarBraz7/noema-artifacts) | JSONs, transcrições, análise e checkpoints duráveis |
| [CesarBraz7/obsidian-vault](https://github.com/CesarBraz7/obsidian-vault) | Inbox e notas Markdown para abrir no Obsidian |

## Arquitetura de referência

- **Go** como orquestrador enxuto, rodando em Docker Compose no Windows durante o desenvolvimento e compatível com hosts Linux AMD64 e ARM64;
- **PostgreSQL externo (Neon) + River OSS** para fontes, jobs, retries e estados;
- **Extractor First:** `yt-dlp`, `gallery-dl`, `Instaloader`, `trafilatura`, FFmpeg e navegador somente em último caso;
- **IA multimodal por API:** Gemini, Groq e fallbacks compatíveis, priorizando níveis gratuitos;
- **Artefatos auditáveis:** `source-content.json`, análise por modalidade, `knowledge.json` e `manifest.json`;
- **Persistência separada:** GitHub privado para JSON/textos; OCI Object Storage para binários selecionados;
- **Saída:** notas de fonte e conceitos para Obsidian, preservando anotações pessoais.

## Documentos

- [Especificação auditada v1.3](docs/especificacao-arquitetura-v1.3-auditada.md)
- [Roadmap de implementação](docs/ROADMAP.md)
- [Auditoria de decisões e correções](docs/AUDITORIA-DECISOES-2026-11-22.md)
- [Decisões arquiteturais (ADRs)](docs/adr/0001-base-arquitetural.md)

## Executar a Fase 1

Os comandos abaixo funcionam no Windows com o Docker Desktop iniciado. O worker no contêiner se conecta ao Neon e ao GitHub.

1. Após vincular o projeto com `neon init`, rode `neon deploy` para trazer as variáveis do Postgres. O CLI cria `.env.local` ou atualiza `.env` se ele já existir. O Noema prefere `DATABASE_URL_UNPOOLED` e aceita `DATABASE_URL` como alternativa para uma conexão **direta** via TLS. A conexão direta é necessária para o River e o lock de sessão do worker.
2. No mesmo arquivo de ambiente, configure um token GitHub da conta `CesarBraz7` com `Contents: Read and write` nos dois repositórios de dados. Use `GITHUB_TOKEN` para um único token, ou `GITHUB_VAULT_TOKEN` e `GITHUB_ARTIFACTS_TOKEN` para tokens separados. Os commits da automação são atribuídos pela API do GitHub à conta autenticada.
3. Adicione um link de artigo ou vídeo individual do YouTube ao `Inbox/links.md` no GitHub ou no Obsidian. Se editar pelo Obsidian, envie a alteração ao GitHub. O check da caixa é opcional. Use uma linha indentada para acrescentar foco.
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

Use uma chave de um projeto Gemini da camada gratuita, sem faturamento. Configure `GEMINI_API_KEY` e selecione em `NOEMA_GEMINI_MODEL` um modelo gratuito compatível com vídeo, como `gemini-3.5-flash-lite`. Antes de iniciar o worker, rode `migrate` para habilitar os estados da análise e da fila de cotas. [Vídeo no Gemini](https://aistudio.google.com/docs/video-understanding#youtube) · [Modelos e preços](https://aistudio.google.com/docs/pricing)

Para vídeos com visibilidade pública confirmada, o worker envia a URL canônica ao Gemini após preparar a transcrição. `video-visual.json` registra a resposta estruturada, o modelo informado pelo provedor, o uso de tokens quando fornecido, os intervalos de evidência e as lacunas. A nota reúne evidências da transcrição e da imagem com links para os timestamps. O processamento reaproveita a análise salva quando muda apenas o foco da inbox.

Quando a análise aponta um detalhe visual que precisa de confirmação, o worker baixa um trecho curto e extrai o frame correspondente com FFmpeg. O Gemini recebe os frames selecionados e trechos próximos da transcrição do áudio, quando disponíveis. `video-frame-inspection.json` registra a evidência e os pontos que ficaram sem confirmação. Os JPEGs ficam no diretório local de trabalho com SHA-256 e dimensões registrados no artefato.

Uma resposta HTTP 429 deixa a análise em `WAITING_QUOTA` até o prazo informado pelo provedor, ou até a próxima janela de retry. A configuração padrão usa a captura local de áudio. Quando a visibilidade pública não pode ser confirmada, o worker registra essa limitação na nota e mantém a captura local.

## Inspecionar um vídeo do YouTube

Troque `VIDEO_ID` pelo identificador de um vídeo público e execute no PowerShell.

```powershell
docker compose -f deploy/compose.yaml run --build --rm noema inspect-video 'https://www.youtube.com/watch?v=VIDEO_ID'
```

O comando imprime metadados e capítulos disponíveis em JSON. Os capítulos têm início e fim em milissegundos. Para diagnosticar legendas, passe `inspect-video --captions URL`. Nesse modo, `captionsStatus` informa se a legenda foi obtida (`available`), se nenhuma faixa compatível foi encontrada (`unavailable`) ou se a leitura falhou (`fetch_failed`). Para gravar artefatos e publicar a nota com links para os capítulos, use a inbox e execute `once`.

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
