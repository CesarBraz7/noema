# Roadmap incremental — Noema

A pipeline é construída por incrementos testáveis, sem ativar cobranças por padrão.

O desenvolvimento e a validação funcionam no Windows com Docker Desktop. A escolha do host e a validação de acesso, cotas e armazenamento ficam para a implantação contínua.

## Fase 0 — Base de desenvolvimento e segurança
- Manter os três repositórios privados e documentar permissões.
- Preparar Go, ambiente Docker ARM64/AMD64, CI e configuração por ambiente.
- Validar a conexão com o Neon a partir do Docker local.
- Criar contratos iniciais e ferramentas de desenvolvimento local.
- **Aceite:** testes Go e build Docker passam; nenhum segredo versionado.

## Fase 1 — Primeiro fluxo ponta a ponta (artigo)
- Ler `Inbox/links.md` no repositório do vault, com polling condicional.
- Normalizar URL, deduplicar fontes, persistir estados no PostgreSQL e usar River OSS.
- Integrar Trafilatura como CLI para extrair artigo textual.
- Gerar e validar `source-content.json` e `knowledge.json`, publicar artefatos.
- Gerar nota Markdown por fonte com evidências, publicar no vault sem sobrescrever notas humanas.
- **Aceite:** link de artigo produz nota; reenvio não repete captura desnecessária.

## Fase 2 — Vídeos
- **Implementado no YouTube** — baixar a faixa de áudio original, normalizar com FFmpeg e transcrever localmente em segmentos retomáveis. Legendas entram como fallback declarado quando a faixa está indisponível.
- **Implementado no YouTube** — preservar metadados, áudio, transcrição e timestamps em checkpoints distintos e publicar links para os capítulos fornecidos pelo YouTube. Falhas e restrições ficam registradas.
- Avaliar a qualidade e o custo de modelos ASR em materiais longos e em português. A imagem local usa Whisper `tiny-q5_1` nesta etapa.
- Criar análise multimodal com Gemini quando disponível e inspeção adicional de frames quando necessária.
- Persistir resultados e custos por etapa e reutilizar artefatos compatíveis.
- **Aceite:** vídeo técnico com fala, código e slides gera conhecimento rastreável.

## Fase 3 — Instagram e X
- Integrar gallery-dl, Instaloader e adaptadores textuais/mídia.
- Preservar carrosséis ordenados, legendas e contexto; sinalizar capturas incompletas.
- **Aceite:** Reel, carrossel e post com mídia funcionam ou registram acesso/captura parcial com clareza.

## Fase 4 — Resiliência, cotas e recuperação
- Roteador de IA por capacidade com fallback gratuito antes do pago.
- Limites de gasto, WAITING_QUOTA, retry, deduplicação, reconciliação, retenção e limpeza.
- Testes de reinício e falhas entre persistência de artefato e confirmação de job.
- **Aceite:** retomar do checkpoint durável sem repetir operações custosas evitáveis.

## Fase 5 — Consolidação do segundo cérebro
- Classificar conceitos sem taxonomia rígida, gerar links entre notas de fontes.
- Proteger notas pessoais e sinalizar contradições para revisão.
- **Aceite:** fontes de plataformas diferentes contribuem para conceitos associados.

Referência vigente: [Especificação v1.3 auditada](especificacao-arquitetura-v1.3-auditada.md) · [Auditoria](AUDITORIA-DECISOES-2026-11-22.md).
