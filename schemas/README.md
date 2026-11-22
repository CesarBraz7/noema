# Schemas de artefatos

O Noema usa JSON Schema 2020-12 para validar `source-content.json`, `knowledge.json` e `manifest.json` antes da gravação. O worker também valida a captura ao reutilizá-la.

- `source-content.schema.json` registra metadados e texto efetivamente capturado, com um ID por bloco. Em vídeos, capítulos e blocos de fala têm `startMs` e `endMs`. A captura preserva a origem do texto. A transcrição por ASR registra também idioma, modelo e evidência da faixa original. Uma captura parcial de vídeo pode guardar somente metadados.
- `transcript-part.schema.json` valida partes ordenadas do texto quando a transcrição excede o tamanho de um arquivo na API de conteúdo. `source-content.json` referencia as partes pelo caminho e SHA-256.
- `knowledge.schema.json` referencia esses blocos na seleção de trechos, com o foco opcional da inbox.
- `artifact-manifest.schema.json` guarda caminho, SHA-256, tamanho e dependências dos artefatos. Capturas são compartilhadas entre consolidações com focos diferentes.
