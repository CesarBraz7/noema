# Schemas de artefatos

A Fase 1 usa JSON Schema 2020-12 para validar `source-content.json`, `knowledge.json` e `manifest.json` antes da gravação. O worker também valida a captura ao reutilizá-la.

- `source-content.schema.json` registra metadados e texto efetivamente capturado, com um ID por bloco.
- `knowledge.schema.json` referencia esses blocos na seleção de trechos, com o foco opcional da inbox.
- `artifact-manifest.schema.json` guarda caminho, SHA-256, tamanho e dependências dos artefatos. Capturas são compartilhadas entre consolidações com focos diferentes.
