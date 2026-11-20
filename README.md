# Noema

**Pipeline pessoal de ingestão e consolidação de conhecimento.**

O Noema transforma links em conhecimento rastreável no Obsidian. O primeiro fluxo cobre artigos web com texto extraível.

> **Fase 1.** A ingestão de artigos usa GitHub, PostgreSQL, River OSS e Trafilatura. A síntese seleciona trechos localmente, com referência ao bloco capturado e sem chamadas de IA.

## Repositórios

| Repositório | Papel |
|---|---|
| [CesarBraz7/noema](https://github.com/CesarBraz7/noema) | Orquestração Go, integração, Docker e documentação |
| [CesarBraz7/noema-artifacts](https://github.com/CesarBraz7/noema-artifacts) | JSONs, transcrições, análise e checkpoints duráveis |
| [CesarBraz7/obsidian-vault](https://github.com/CesarBraz7/obsidian-vault) | Inbox e notas Markdown para abrir no Obsidian |

## Arquitetura de referência

- **Go** como orquestrador enxuto, rodando em Docker Compose em VM ARM64 (OCI Always Free como primeira opção);
- **PostgreSQL externo (Neon) + River OSS** para fontes, jobs, retries e estados;
- **Extractor First:** `yt-dlp`, `gallery-dl`, `Instaloader`, `trafilatura`, FFmpeg e navegador somente em último caso;
- **IA multimodal por API:** Gemini, Groq e fallbacks compatíveis, priorizando níveis gratuitos;
- **Artefatos auditáveis:** `source-content.json`, análise por modalidade, `knowledge.json` e `manifest.json`;
- **Persistência separada:** GitHub privado para JSON/textos; OCI Object Storage para binários selecionados;
- **Saída:** notas de fonte e conceitos para Obsidian, preservando anotações pessoais.

## Documentos

- [Especificação completa de produto e arquitetura v1.2](docs/especificacao-arquitetura-v1.2.md)
- [Roadmap de implementação](docs/ROADMAP.md)
- [Decisões arquiteturais (ADRs)](docs/adr/0001-base-arquitetural.md)

## Executar a Fase 1

1. Copie `.env.example` para `.env` e configure `DATABASE_URL` com a conexão **direta** do Neon (sem `-pooler`), via TLS. Use `sslmode=verify-full` para validar o certificado e o nome do servidor. A conexão direta é necessária para o River e o lock de sessão do worker.
2. Configure um token GitHub da conta `CesarBraz7` com `Contents: Read and write` nos dois repositórios de dados. Use `GITHUB_TOKEN` para um único token, ou `GITHUB_VAULT_TOKEN` e `GITHUB_ARTIFACTS_TOKEN` para tokens separados. Os commits da automação são atribuídos pela API do GitHub à conta autenticada.
3. Adicione um link de artigo ao `Inbox/links.md` no Obsidian e sincronize o vault com o GitHub. O check da caixa é opcional. Use uma linha indentada para acrescentar foco.
4. Construa a imagem, prepare o banco e rode um primeiro ciclo.

```bash
docker compose --env-file .env -f deploy/compose.yaml build noema
docker compose --env-file .env -f deploy/compose.yaml run --rm noema migrate
docker compose --env-file .env -f deploy/compose.yaml run --rm noema once
```

O `once` consulta `Inbox/links.md`, drena os trabalhos elegíveis e encerra. Os comandos abaixo ativam a ingestão contínua e abrem os logs.

```bash
docker compose --env-file .env -f deploy/compose.yaml up -d noema
docker compose --env-file .env -f deploy/compose.yaml logs -f noema
```

A inbox é verificada a cada 15 minutos, com ETag. O serviço verifica a fila na inicialização, quando recebe entradas e a cada duas horas para atender retries. O PostgreSQL fica desconectado entre os ciclos. As solicitações, tentativas e erros ficam em `noema_ingestion_requests`.

A captura `source-content.json` é reutilizada quando o mesmo artigo é reenviado. Um novo foco gera `knowledge.json` e `manifest.json` próprios. O manifesto referencia o caminho e o SHA-256 da captura. A nota em `Fontes/Artigos/` aponta para o manifesto e para os blocos que sustentam cada trecho. Se a nota já tiver sido editada ou removida, o worker preserva a mudança e registra `NEEDS_REVIEW`.

## Verificar localmente

Use Go 1.23+ para rodar os testes e consultar a versão.

```bash
go test ./...
go vet ./...
go run ./cmd/noema version
```

Para incluir os testes de integração, instale Trafilatura com `python -m pip install -r deploy/extractors.requirements.txt`, aponte `NOEMA_INTEGRATION_DATABASE_URL` para um banco PostgreSQL de teste vazio e execute `NOEMA_REQUIRE_TRAFILATURA=1 go test -count=1 ./...`. O teste do fluxo usa uma API GitHub simulada e cria tabelas de Noema e River no banco de teste.

## Segurança

Nunca enviar chaves de API, cookies, sessões, senhas, arquivo `.env` real, MP4s ou dumps de banco ao Git. Não habilitar fallback pago automaticamente. Não sobrescrever conteúdo pessoal do Obsidian. Todos os repositórios de dados devem permanecer privados.
