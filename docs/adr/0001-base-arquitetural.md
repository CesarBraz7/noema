# ADR 0001 — Base arquitetural do Noema

**Status:** aceita · 2026-11-19

Decisões:
1. Orquestração em Go, execução em Docker Compose, compatível com ARM64/AMD64.
2. Neon PostgreSQL externo para estado durável e River OSS para jobs.
3. OCI Always Free como hospedagem inicial, sem dependência permanente de um provedor.
4. Extractor First: reaproveitar OSS e evitar navegador headless como caminho padrão.
5. GitHub privado separado para artefatos e vault, OCI Object Storage para binários preservados.
6. `source-content.json` separa evidência de `knowledge.json`, que registra interpretação.
7. Gratuito primeiro; modelos pagos ficam desativados sem autorização.
8. Atualizações no vault devem preservar autoria humana, sem force-push.

Riscos e justificativas constam na [especificação v1.2](../especificacao-arquitetura-v1.2.md). Mudanças posteriores devem gerar novas ADRs.
