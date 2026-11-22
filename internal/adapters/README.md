# Adaptadores de ingestão

A Fase 1 usa Trafilatura para extrair texto e metadados de artigos. O adaptador Go valida o endereço da fonte e os redirecionamentos antes de enviar o HTML à CLI pela entrada padrão.

O adaptador de vídeo usa yt-dlp para obter metadados e uma faixa de legendas com timestamps de um vídeo público do YouTube. O comando manual `inspect-video` e a ingestão pela fila usam a mesma captura. [Instruções](../../README.md#inspecionar-um-vídeo-do-youtube)

Quando faltam legendas, a ingestão pela fila baixa a faixa de áudio, normaliza com FFmpeg e usa Whisper local. O áudio fica no diretório temporário da tentativa. A análise visual e os adaptadores de redes sociais entram nos próximos incrementos. [Roadmap](../../docs/ROADMAP.md)
