# Handoff do Noema para validação na OCI

O fluxo de ingestão de artigos foi validado no Windows em 22 de novembro de 2026, com Docker Desktop, Neon e os dois repositórios privados do GitHub. O próximo passo é executar a imagem em uma VM ARM64 da OCI.

## Validação concluída

- A migração rodou pelo Docker Compose na branch `production` do projeto Neon `winter-band-81069553`, usando a conexão direta com TLS e `channel_binding=require`. Uma consulta ao banco confirmou as tabelas do Noema e do River, incluindo `noema_ingestion_requests` e `river_job`.
- O ciclo manual processou [Fixing For Loops in Go 1.22](https://go.dev/blog/loopvar-preview) na primeira tentativa. O pedido ficou em `COMPLETED`. A captura, a consolidação e o manifesto foram publicados no repositório de artefatos. A nota no vault aponta para o manifesto e os blocos de evidência. [Manifesto](https://github.com/CesarBraz7/noema-artifacts/blob/main/sources/web-9c90a4a8d65bb59512d6c5943af256cef67c631afc2f106dcfb2d7c15451e453/14cda71ceb05d0fca7392f7f3a7d740422d2bbaaa5ef78781521ce8313277a92/manifest.json) · [Nota no vault](https://github.com/CesarBraz7/obsidian-vault/blob/main/Fontes/Artigos/web-9c90a4a8d65bb59512d6c5943af256cef67c631afc2f106dcfb2d7c15451e453.md)
- O reenvio com `utm_source` manteve uma fonte, um pedido, os mesmos três artefatos e a mesma nota. A [inbox](https://github.com/CesarBraz7/obsidian-vault/blob/main/Inbox/links.md) ficou com a URL canônica do artigo de teste.
- Passaram `go test ./...`, `go vet ./...`, o teste de integração com PostgreSQL 16 descartável e o build Docker AMD64.
- A imagem Docker `linux/arm64` rodou localmente por emulação. O contêiner reportou `aarch64`, e funcionaram `noema version`, `trafilatura --version` e `noema once` com as conexões de produção. Falta repetir a validação na VM ARM64 da OCI.

A conexão do Neon foi salva em `.env.local` no clone do Windows, ignorado pelo Git. Os ciclos manuais usaram a credencial GitHub de `CesarBraz7` do Git Credential Manager apenas no ambiente do processo. Para o worker contínuo, configure um token da conta `CesarBraz7` com `Contents: Read and write` nos dois repositórios de dados.

## Validar na OCI ARM64

1. Acesse a VM ARM64 por SSH, sincronize a branch `main` de `CesarBraz7/noema` e confirme que ela tem Docker e Compose. Faça os commits e pushes deste repositório em `main`.
2. Configure `.env.local` na raiz do clone com `DATABASE_URL_UNPOOLED` da branch `production` e o token GitHub do worker. As variáveis aceitas estão no [README](../README.md#executar-a-fase-1).
3. Construa a imagem na VM, confirme o runtime ARM64 e rode a migração idempotente e um ciclo manual para validar as conexões com Neon e GitHub.

```bash
docker compose --env-file .env.local -f deploy/compose.yaml build noema
docker compose --env-file .env.local -f deploy/compose.yaml run --rm noema version
docker compose --env-file .env.local -f deploy/compose.yaml run --rm --entrypoint trafilatura noema --version
docker compose --env-file .env.local -f deploy/compose.yaml run --rm noema migrate
docker compose --env-file .env.local -f deploy/compose.yaml run --rm noema once
```

O worker contínuo pode ser iniciado na VM depois dessa validação. [Instruções de operação](../README.md#executar-a-fase-1)
