# Auditoria de decisões e correções — 22/11/2026

**Objeto:** comparação entre a especificação v1.2, o histórico do planejamento Noema e o código já publicado no GitHub.  
**Documento vigente:** [Noema — Especificação v1.3 auditada](especificacao-arquitetura-v1.3-auditada.md). A [v1.2](especificacao-arquitetura-v1.2.md) permanece apenas como registro histórico.

## Problemas confirmados

| Gravidade | Origem | Constatação | Encaminhamento |
|---|---|---|---|
| **Crítica** | v1.2 §§ 8.2, 11.1, 19 | Priorizar legendas do YouTube não foi aprovado | **Áudio original → ASR como caminho principal**; legendas auxiliares/fallback explícito |
| **Crítica** | `internal/adapters/video/`, README | Código atual implementou captions-first e Whisper `tiny-q5_1` apenas como fallback; teto 20 min / 64 MiB sem autorização | Backlog obrigatório de correção de código, testes para vídeos longos, sem chamar Fase 2 de pronta |
| **Alta** | v1.2 §§ 2, 13, 14, 19 | Consolidação e reescrita automática entre fontes presumidas apesar de pedido anterior por comparações sob demanda | Preservar nota independente; links/sugestões possíveis; mesclagem requer revisão até política definida |
| **Média** | v1.2 § 3 | Tesseract tornado OCR inicial | Tratar como candidato e testar |
| **Média** | v1.2 §§ 6, 10, 16 | Jobs adiados verificados a cada 120 min sem discussão específica | Tratar como ajuste reversível; 15 min permanece intervalo da inbox |
| **Média** | v1.2 §§ 4, 12, 16 | VM OCI tratada como ambiente já disponível | Desenvolvimento local Windows/Docker, cloud futura |
| **Baixa** | v1.2 §§ 9, 10, 12, 17 | JSON de exemplo, tabelas, paths e 30 dias para MP4 próprio soam como requisitos aprovados | Marcar como propostas/contratos versionáveis, não aprovações |
| **Baixa** | v1.2 § 11 | Exemplo de fallback pago US$ 5/mês e US$ 0,50/fonte | Continuar como simulação; cobrança **nunca** automática |

## O que foi preservado

- Usuário escolhe fontes para importar (YouTube, Reels, carrosséis, X, artigos e arquivos próprios) e adiciona links à inbox; nenhum robô busca conteúdos sozinho
- Go + Docker Compose, Neon PostgreSQL + River OSS, fila, checkpoints, deduplicação e recuperação
- Extratores open source do GitHub com navegador apenas em último recurso
- Transcrição auditável do áudio, vídeo multimodal para captar slides, código e demonstrações
- `source-content.json` separado de `knowledge.json` e de Markdown do Obsidian
- GitHub `CesarBraz7/noema`, `noema-artifacts` e `obsidian-vault`
- Persistência por função, gratuito primeiro, pagamento desabilitado, proteção de notas pessoais
- Processamento no Windows por enquanto, migração a host ARM64/AMD64 quando disponível

## Itens pendentes que não podem virar decisões silenciosas

1. **Política de mesclagem entre fontes:** distinguir links automáticos, notas conceituais independentes e comparação/reescrita de conhecimento prévio. Há sinais diferentes em momentos distintos da conversa.
2. **Escolha/limites de ASR:** modelo local, serviço remoto, limite de arquivo, chunking, idioma, precisão e recursos devem ser avaliados; nenhum Whisper `tiny` ou teto de 20 minutos foi validado como padrão.
3. **OCR e cronograma de jobs adiados:** Tesseract e 120 minutos são parâmetros de engenharia propostos, não requisitos.
4. **Upload de mídia própria e retenção:** sem canal de upload no MVP, a política de 30 dias é proposta.
5. **Provedores de IA:** Gemini/Groq/OpenRouter são candidatos aprovados como estratégia geral, mas não há autorização para vincular rigidamente modelos/versões/custos sem validação.
6. **Orçamento pago:** permanecer desabilitado. Valores de referência não autorizam gastar.

**Limite desta auditoria:** ela revisou documentação e verificou o adaptador de vídeo e README; não executou testes funcionais nem alterou código de processamento. A implementação atual continua divergente até uma correção de engenharia e testes.
