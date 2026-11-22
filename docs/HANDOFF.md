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

A Fase 2 aceita vídeos do YouTube na inbox. O worker preserva o texto de legendas ou da transcrição local com timestamps e publica uma nota em `Fontes/Vídeos/`. Quando faltam legendas, a imagem Docker usa FFmpeg e Whisper com vídeos de até 20 minutos. O comando `retry-video` retoma pedidos antigos que ficaram em revisão pela ausência de legenda. O próximo incremento da Fase 2 é a análise visual com evidências temporais. [Instruções](../README.md#executar-a-fase-1) · [Roadmap](ROADMAP.md#fase-2--vídeos)

O teste local no Docker usou um vídeo público do TED, PostgreSQL descartável e a API GitHub simulada. O pedido ficou em `COMPLETED`, com 427 trechos de legenda, três artefatos e a nota de vídeo.

O teste de transcrição e retomada usou áudio real do YouTube, PostgreSQL descartável e API GitHub simulada. Rodou no Windows com FFmpeg e Whisper portátil 1.8.6. O pedido retomado ficou em `COMPLETED`, com quatro artefatos e uma nota. A imagem AMD64 com Whisper 1.9.5 foi construída. A validação de execução dessa versão no Docker ficou pendente porque o serviço WSL parou de responder no Windows.

Use `main` para os commits e pushes deste repositório. Para verificar o código no PowerShell, rode os comandos abaixo na raiz do clone.

```powershell
go test ./...
go vet ./...
docker compose -f deploy/compose.yaml build noema
docker compose -f deploy/compose.yaml run --rm noema version
```

Para um ciclo com Neon e GitHub reais, use `.env.local`, ignorado pelo Git. O arquivo do clone no Windows já contém a conexão direta da branch `production` do Neon. Os ciclos de validação usaram a credencial GitHub de `CesarBraz7` do Git Credential Manager apenas no ambiente do processo. Para executar o worker com uma credencial dedicada, configure um token da conta com `Contents: Read and write` nos dois repositórios de dados. [Instruções de operação](../README.md#executar-a-fase-1)
