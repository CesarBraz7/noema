# Adaptadores de ingestão

A Fase 1 usa Trafilatura para extrair texto e metadados de artigos. O adaptador Go valida o endereço da fonte e os redirecionamentos antes de enviar o HTML à CLI pela entrada padrão.

O adaptador de vídeo usa yt-dlp para obter metadados e uma faixa de legendas com timestamps de um vídeo público do YouTube. A primeira etapa está disponível no comando manual `inspect-video`. [Instruções](../../README.md#inspecionar-um-vídeo-do-youtube)

A ingestão de vídeos pela fila e os adaptadores de redes sociais entram nos próximos incrementos. [Roadmap](../../docs/ROADMAP.md)
