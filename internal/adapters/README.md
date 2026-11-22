# Adaptadores de ingestão

A Fase 1 usa Trafilatura para extrair texto e metadados de artigos. O adaptador Go valida o endereço da fonte e os redirecionamentos antes de enviar o HTML à CLI pela entrada padrão.

O adaptador de vídeo usa yt-dlp para obter metadados e capítulos de vídeos públicos do YouTube. `inspect-video --captions` consulta legendas para diagnóstico. A fila só passa por essa consulta quando a faixa de áudio original está definitivamente indisponível e marca a origem do fallback. [Instruções](../../README.md#inspecionar-um-vídeo-do-youtube)

A fila baixa a faixa original com yt-dlp, normaliza em segmentos com FFmpeg e usa Whisper local. O áudio e os resultados de cada segmento ficam no diretório de trabalho entre tentativas. Os timestamps de cada trecho são reconstituídos a partir do início do segmento no vídeo. [Roadmap](../../docs/ROADMAP.md)
