# Handoff do Noema para validação no Windows

O primeiro fluxo do Noema está implementado. A próxima sessão deve aplicar a migração no Neon a partir do computador de César e processar um artigo real. O Windows já alcançou o endpoint do banco na porta `5432` por IPv6. A conexão autenticada a partir do Docker ainda precisa ser testada.

## Estado do projeto

- Repositório principal privado `CesarBraz7/noema`. Faça os commits e pushes na branch `main`. Os commits `17ef36d` e `10217c6` contêm a ingestão e a integração com o Neon.
- O worker lê `Inbox/links.md`, deduplica URLs, extrai o texto com Trafilatura e seleciona citações localmente. O River usa PostgreSQL. Os artefatos JSON vão para `CesarBraz7/noema-artifacts`, e as notas Markdown vão para `CesarBraz7/obsidian-vault`. A publicação preserva edições manuais. [README](../README.md)
- O projeto Neon `winter-band-81069553`, branch `production`, já existe. O `neon deploy` confirmou o Postgres e baixou as variáveis para o `.env.local` do Codex Cloud. Na última consulta ao Neon por HTTPS, as tabelas `river_job` e `noema_ingestion_requests` ainda não existiam. A porta `5432` foi recusada no Cloud.
- O fluxo passou com PostgreSQL 16, River e Trafilatura reais e uma API GitHub simulada. Também passaram a migração local com TLS e `channel_binding=require` e o build Docker AMD64. O primeiro ciclo com o Neon e os repositórios privados será a validação de produção.

## Preparar a sessão local

Se for clonar o repositório no Windows, execute no PowerShell dentro da pasta onde quer guardar o projeto e abra a pasta `noema` em uma sessão **Local** do Codex Desktop.

```powershell
git clone --branch main https://github.com/CesarBraz7/noema.git
cd noema
```

Se já tiver uma cópia, confirme que está com a árvore limpa, execute `git switch main` e atualize com `git pull --ff-only origin main`.

No painel do Neon, abra **Connect** na branch `production` com **Connection pooling** desativado. Crie `.env.local` na raiz do clone e salve a URL direta inteira na variável `DATABASE_URL_UNPOOLED`, incluindo os parâmetros TLS e channel binding fornecidos pelo Neon. O arquivo e as credenciais do Codex Cloud ficam no ambiente anterior. Mantenha a URL real no arquivo local ignorado pelo Git. [Conexão no Neon](https://neon.com/docs/connect/connect-from-any-app)

## Aplicar a migração

Com o Docker Desktop iniciado, execute na raiz do Noema.

```powershell
docker compose --env-file .env.local -f deploy/compose.yaml run --build --rm noema migrate
```

O comando deve sair com código `0` e criar as tabelas do Noema e do River. A migração precisa apenas do banco. Caso o Docker falhe ao conectar e o PowerShell continue alcançando o endpoint, teste a migração no Windows com Go 1.25 ou posterior. Carregue a mesma `DATABASE_URL_UNPOOLED` no ambiente do processo sem imprimir a credencial e execute `go run ./cmd/noema migrate`. Se funcionar no host, confira a conectividade do Docker Desktop antes de iniciar o worker nele.

## Validar o primeiro artigo

1. Configure no `.env.local` `GITHUB_TOKEN`, ou `GITHUB_VAULT_TOKEN` e `GITHUB_ARTIFACTS_TOKEN`, com permissão de leitura e escrita de conteúdo nos repositórios privados de dados. O token deve ser da conta `CesarBraz7` para atribuir os commits da aplicação a ela.
2. Adicione um link de artigo público a `Inbox/links.md` no vault e sincronize o arquivo com o GitHub.
3. Execute um ciclo manual. Verifique a saída do comando, os três artefatos e a nota em `Fontes/Artigos/`. Reenvie a mesma URL com um parâmetro `utm_` e confirme que ele reaproveita a captura.

```powershell
docker compose --env-file .env.local -f deploy/compose.yaml run --rm noema once
```

Depois desse teste, valide a execução da imagem na VM OCI ARM64 e use o modo contínuo quando estiver pronto para deixar a ingestão ativa. [Instruções de operação](../README.md#executar-a-fase-1)
