# Handoff do Noema para desenvolvimento no Windows

O fluxo de ingestão de artigos foi validado no Windows em 22 de novembro de 2026, com Docker Desktop, Neon e os dois repositórios privados do GitHub. O desenvolvimento segue no Windows. A escolha da hospedagem contínua ficou para depois.

## Validação concluída

- A migração rodou pelo Docker Compose, usando uma conexão direta com TLS e `channel_binding=require`. Uma consulta ao banco confirmou as tabelas do Noema e do River, incluindo `noema_ingestion_requests` e `river_job`.
- O ciclo manual processou [Fixing For Loops in Go 1.22](https://go.dev/blog/loopvar-preview) na primeira tentativa. O pedido ficou em `COMPLETED`. A captura, a consolidação e o manifesto foram publicados no repositório de artefatos. A nota no vault aponta para o manifesto e os blocos de evidência.
- O reenvio com `utm_source` manteve uma fonte, um pedido, os mesmos três artefatos e a mesma nota.
- Passaram `go test ./...`, `go vet ./...`, o teste de integração com PostgreSQL 16 descartável e o build Docker AMD64.
- A imagem Docker `linux/arm64` rodou localmente por emulação. O contêiner reportou `aarch64`, e funcionaram `noema version`, `trafilatura --version` e `noema once` com as conexões de produção.

## Continuar no Windows

Use o Docker Desktop para executar o worker e um PostgreSQL descartável para os testes de integração com a API GitHub simulada. [Testes locais](../README.md#verificar-localmente)

A ingestão de vídeos do YouTube baixa a faixa de áudio original, normaliza com FFmpeg e transcreve em segmentos retomáveis com Whisper local, sem teto global de duração ou tamanho. A captura preserva timestamps e a origem do texto nas novas notas em `Fontes/Videos/`. Legendas são usadas como fallback declarado quando o áudio original está definitivamente indisponível. O comando `retry-video` retoma pedidos em revisão por falta de legenda, ausência de ASR ou pelo limite de duração anterior. [Instruções](../README.md#executar-localmente)

A análise visual optativa usa a URL pública no Gemini e registra evidências, lacunas e uso em artefato próprio. Os testes com PostgreSQL descartável e APIs simuladas cobrem cota, retomada de checkpoint após falha de publicação e reaproveitamento com novo foco. [Configuração](../README.md#habilitar-a-análise-visual) · [Roadmap](ROADMAP.md#fase-2--vídeos)

A inspeção direcionada extrai frames nos pontos indicados pela análise visual. Um teste no Docker com vídeo público confirmou a extração por trecho com yt-dlp e FFmpeg. O teste com APIs simuladas cobre o envio de um frame com contexto da transcrição, o registro de um frame indisponível e o reaproveitamento da análise e da inspeção em outro foco.

O fallback de frames e transcrição registra a incompatibilidade da entrada nativa, mantém a evidência do áudio original e marca a cobertura parcial na nota. O teste de integração cobre a escolha de frames pelos capítulos e a retomada com um segundo foco.

Em 24 de novembro de 2026, a validação no Windows com PostgreSQL descartável no D: e GitHub simulado processou o vídeo público Go in 100 Seconds. O Gemini real (`gemini-3.1-flash-lite`, camada gratuita) produziu oito evidências visuais válidas e três evidências da inspeção de frames. O Whisper gerou 29 trechos do áudio original. A nota registrou duas evidências e uma solicitação de frame descartadas por excederem a duração. Um segundo foco reutilizou os artefatos com zero chamadas adicionais ao Gemini. [Vídeo de validação](https://www.youtube.com/watch?v=446E-r0rXHI)

No Docker Desktop, com PostgreSQL descartável e API GitHub simulada, o worker processou um vídeo público do TED de 20min03s que tinha legenda disponível. O pedido chegou a `COMPLETED` com cinco segmentos de áudio, 323 trechos transcritos pelo Whisper e zero consultas a legendas pelo worker. Um pedido com outro foco reutilizou a transcrição. O teste de integração também verificou a retomada de um pedido barrado pelo limite antigo, o fallback declarado e o tratamento de restrições de acesso.

As novas capturas preservam os capítulos retornados pelo YouTube, com início e fim em milissegundos. A nota inclui links para abrir cada capítulo no vídeo. `inspect-video` retornou 30 capítulos de um vídeo real no Windows, e o teste de integração verificou a gravação dos capítulos e os links na nota.

Use `main` para os commits e pushes deste repositório. Para verificar o código no PowerShell, rode os comandos abaixo na raiz do clone.

```powershell
go test ./...
go vet ./...
docker compose -f deploy/compose.yaml build noema
docker compose -f deploy/compose.yaml run --rm noema version
```

Para um ciclo com Neon e GitHub reais, use `.env.local`, ignorado pelo Git. Configure a conexão direta do banco, os nomes dos repositórios de dados e um token dedicado com `Contents: Read and write` nesses repositórios. [Instruções de operação](../README.md#executar-localmente)
