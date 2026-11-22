# Handoff do Noema para desenvolvimento no Windows

O fluxo de ingestão de artigos foi validado no Windows em 22 de novembro de 2026, com Docker Desktop, Neon e os dois repositórios privados do GitHub. O desenvolvimento segue no Windows. A escolha da hospedagem contínua ficou para depois.

## Validação concluída

- A migração rodou pelo Docker Compose na branch `production` do projeto Neon `winter-band-81069553`, usando a conexão direta com TLS e `channel_binding=require`. Uma consulta ao banco confirmou as tabelas do Noema e do River, incluindo `noema_ingestion_requests` e `river_job`.
- O ciclo manual processou [Fixing For Loops in Go 1.22](https://go.dev/blog/loopvar-preview) na primeira tentativa. O pedido ficou em `COMPLETED`. A captura, a consolidação e o manifesto foram publicados no repositório de artefatos. A nota no vault aponta para o manifesto e os blocos de evidência. [Manifesto](https://github.com/CesarBraz7/noema-artifacts/blob/main/sources/web-9c90a4a8d65bb59512d6c5943af256cef67c631afc2f106dcfb2d7c15451e453/14cda71ceb05d0fca7392f7f3a7d740422d2bbaaa5ef78781521ce8313277a92/manifest.json) · [Nota no vault](https://github.com/CesarBraz7/obsidian-vault/blob/main/Fontes/Artigos/web-9c90a4a8d65bb59512d6c5943af256cef67c631afc2f106dcfb2d7c15451e453.md)
- O reenvio com `utm_source` manteve uma fonte, um pedido, os mesmos três artefatos e a mesma nota. A [inbox](https://github.com/CesarBraz7/obsidian-vault/blob/main/Inbox/links.md) ficou com a URL canônica do artigo de teste.
- Passaram `go test ./...`, `go vet ./...`, o teste de integração com PostgreSQL 16 descartável e o build Docker AMD64.
- A imagem Docker `linux/arm64` rodou localmente por emulação. O contêiner reportou `aarch64`, e funcionaram `noema version`, `trafilatura --version` e `noema once` com as conexões de produção.

## Continuar no Windows

Use o Docker Desktop para executar o worker e um PostgreSQL descartável para os testes de integração com a API GitHub simulada. [Testes locais](../README.md#verificar-localmente)

O próximo incremento do roadmap é a captura local de vídeos com yt-dlp e FFmpeg, começando por metadados e legendas com timestamps. [Roadmap](ROADMAP.md#fase-2--vídeos)

Use `main` para os commits e pushes deste repositório. Para verificar o código no PowerShell, rode os comandos abaixo na raiz do clone.

```powershell
go test ./...
go vet ./...
docker compose -f deploy/compose.yaml build noema
docker compose -f deploy/compose.yaml run --rm noema version
```

Para um ciclo com Neon e GitHub reais, use `.env.local`, ignorado pelo Git. O arquivo do clone no Windows já contém a conexão direta da branch `production` do Neon. Os ciclos de validação usaram a credencial GitHub de `CesarBraz7` do Git Credential Manager apenas no ambiente do processo. Para executar o worker com uma credencial dedicada, configure um token da conta com `Contents: Read and write` nos dois repositórios de dados. [Instruções de operação](../README.md#executar-a-fase-1)
