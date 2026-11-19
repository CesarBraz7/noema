# Noema

**Pipeline pessoal de ingestão e consolidação de conhecimento.**

O Noema transformará links do YouTube, Instagram, X, páginas, notícias e outras fontes em conhecimento estruturado, rastreável e interligado no Obsidian.

> **Status: bootstrap inicial.** Ainda não existe pipeline funcional, integração com banco, ingestão automática ou consumo de APIs externas.

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

## Como verificar o scaffold

Requer Go 1.23+ ou Docker Compose.

```bash
go test ./...
go run ./cmd/noema version
docker compose -f deploy/compose.yaml --profile bootstrap run --rm bootstrap
```

Esses comandos apenas validam o bootstrap: **não executam ingestão nem chamadas pagas**.

## Próximo incremento

Implementar o fluxo artigo web → inbox GitHub → PostgreSQL/River → Trafilatura → `source-content.json` → `knowledge.json` → nota no vault. Ver [roadmap](docs/ROADMAP.md).

## Segurança

Nunca enviar chaves de API, cookies, sessões, senhas, arquivo `.env` real, MP4s ou dumps de banco ao Git. Não habilitar fallback pago automaticamente. Não sobrescrever conteúdo pessoal do Obsidian. Todos os repositórios de dados devem permanecer privados.
