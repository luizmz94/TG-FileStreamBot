// app/components/MediaModal.tsx
'use client';

import { useState, useEffect, useRef } from 'react';
import { XIcon, HeartIcon, StarIcon, PlusIcon, PlayIcon, PauseIcon, WarningIcon, ArrowCounterClockwiseIcon, InstagramLogoIcon, TiktokLogoIcon, XLogoIcon, FastForwardIcon, RewindIcon } from '@phosphor-icons/react';
import { useRouter } from 'next/navigation';
import { useSession } from 'next-auth/react';
import { useMediaCache } from '../contexts/MediaCacheContext';
import { useHasuraToken } from '@/lib/hooks/useHasuraToken';
import { useUserRole } from '@/lib/hooks/useUserRole';
import { markWatched, setMediaValidity } from '@/lib/media-helpers';
import { formatFileSize } from '@/lib/formatters';

type Plyr = any;

// Tipos
interface MediaItem {
  media_id: string;
  media_name?: string;
  media_extension?: string;
  media_size?: number;
  duration?: number;
  width?: number;
  height?: number;
  like_count?: number;
  posted_at?: string;
  global_term?: string;
  term?: string;
  type?: string;
  message_id?: string;
  channel_id?: string;
  media_channels?: Array<{ message_id: string; channel_id: string }>;
  favorite?: Array<{ media_id: string; favorited_at?: string }> | boolean;
  liked?: Array<{ media_id: string; liked_at?: string }> | boolean;
  watched_medias?: Array<{ watched_at: string }>;
  watched_at?: string;
  liked_at?: string;
  favorited_at?: string;
}

interface FallbackItem {
  global_term?: string;
  instagram?: string;
  tiktok?: string;
  twitter?: string;
  instagram_followers?: number;
  terms?: string;
  media_count?: number;
  is_favorite?: Array<{ global_term: string }>;
  favorite?: boolean;
}

interface MediaModalProps {
  isVisible: boolean;
  selectedMedia: MediaItem;
  currentMediaIndex: number;
  mediaList: MediaItem[];
  isBlurred: boolean;
  termName: string;
  fallbackItem: FallbackItem | null;
  onClose: () => void;
  onNavigate: (direction: 'prev' | 'next') => void;
  onLoadMoreMediaForTerm: (termName: string) => void;
  onToggleFavoriteMedia: (mediaId: string) => void;
  onToggleLikeMedia: (mediaId: string) => void;
  onToggleFollow: (termName: string) => void;
  seenIds?: Set<string>;
  onSaveReelsState?: () => void;
  disableHistoryManagement?: boolean;
  onMediaInvalidated?: (mediaId: string) => void;
}

export default function MediaModal({
  isVisible,
  selectedMedia,
  currentMediaIndex,
  mediaList,
  isBlurred,
  termName,
  fallbackItem,
  onClose,
  onNavigate,
  onLoadMoreMediaForTerm,
  onToggleFavoriteMedia,
  onToggleLikeMedia,
  onToggleFollow,
  // seenIds,
  onSaveReelsState,
  disableHistoryManagement = false,
  onMediaInvalidated,
}: MediaModalProps) {
  const [showControls, setShowControls] = useState(true);
  const [isPlaying, setIsPlaying] = useState(false);
  const [isPaused, setIsPaused] = useState(false);
  const [progress, setProgress] = useState(0);
  const [startTime, setStartTime] = useState<number | null>(null);
  const [elapsed, setElapsed] = useState(0);
  const [videoDuration, setVideoDuration] = useState(0);
  const [isLiked, setIsLiked] = useState(false);
  const [isFavorited, setIsFavorited] = useState(false);
  const [isFollowing, setIsFollowing] = useState(false);
  const [likes, setLikes] = useState(0);
  const [touchStartY, setTouchStartY] = useState(0);
  const [touchEndY, setTouchEndY] = useState(0);
  const [showUndoButton, setShowUndoButton] = useState(false);
  const [showInvalidateConfirm, setShowInvalidateConfirm] = useState(false);
  const [isImageLoaded, setIsImageLoaded] = useState(false);
  const [seekAmount, setSeekAmount] = useState(0);
  const [showSeekIndicator, setShowSeekIndicator] = useState(false);
  const [seekSide, setSeekSide] = useState<'left' | 'right' | null>(null);
  const [isIOS, setIsIOS] = useState(false);
  const [plyrReady, setPlyrReady] = useState(false); // Flag para forçar rerender quando Plyr carregar

  const videoRef = useRef<HTMLVideoElement>(null);
  const plyrInstanceRef = useRef<Plyr | null>(null);
  const controlsTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const progressTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const progressIntervalRef = useRef<NodeJS.Timeout | null>(null);
  const imageProgressFrameRef = useRef<number | null>(null);
  const preloadTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const dialogMediaStateRef = useRef({ videoWasPlaying: false, imageWasProgressing: false });
  const hasRequestedMore = useRef(false);
  const mediaContainerRef = useRef<HTMLDivElement>(null);
  const preloadContainerRef = useRef<HTMLDivElement | null>(null); // Container temporário para preload
  const markedRef = useRef<Set<string>>(new Set()); // evita marcar 2x a mesma mídia
  const PlyrConstructorRef = useRef<any>(null); // Usa ref para persistir entre rerenders
  const plyrLoadingRef = useRef<boolean>(false); // Evita carregar Plyr múltiplas vezes
  const lastTapTimeRef = useRef<{ left: number; right: number }>({ left: 0, right: 0 });
  const seekTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const pendingSeekRef = useRef(0);
  const tapCountRef = useRef<{ left: number; right: number }>({ left: 0, right: 0 });
  const tapResetTimeoutRef = useRef<{ left: NodeJS.Timeout | null; right: NodeJS.Timeout | null }>({ left: null, right: null });
  const router = useRouter();
  const { data: session } = useSession();
  const { token } = useHasuraToken(); // Usa o token Hasura já existente
  const { isAdmin } = useUserRole(); // Verifica se usuário é admin
  
  // Detecta se é iOS no cliente
  useEffect(() => {
    setIsIOS(/iPad|iPhone|iPod/.test(navigator.userAgent));
  }, []);
  
  // Carrega Plyr dinamicamente no cliente (apenas uma vez, globalmente)
  useEffect(() => {
    if (typeof window !== 'undefined' && !PlyrConstructorRef.current && !plyrLoadingRef.current) {
      plyrLoadingRef.current = true;
      console.log('[MediaModal] 📦 Iniciando carregamento do PlyrConstructor...');
      
      import('plyr').then((plyrModule) => {
        PlyrConstructorRef.current = plyrModule.default;
        console.log('[MediaModal] ✅ PlyrConstructor carregado e salvo no ref');
        setPlyrReady(true); // Força rerender para processar a mídia inicial
      }).catch((err) => {
        console.error('[MediaModal] ❌ Erro ao carregar Plyr:', err);
        plyrLoadingRef.current = false; // Permite tentar novamente em caso de erro
      });
    } else if (PlyrConstructorRef.current) {
      // Se já está carregado (hot reload), seta como pronto
      setPlyrReady(true);
    }
  }, []);
  
  // Usa o contexto global de cache
  const { 
    preloadedElementsRef, 
    preloadOrderRef, 
    getPlyrInstance, 
    setPlyrInstance,
    removePlyrInstance,
    cleanOldPlyrInstances,
    getPlyrCacheSize
  } = useMediaCache();
  const formattedMediaSize = formatFileSize(selectedMedia?.media_size);

  const STORY_DURATION = 6000; // 6s para imagens
  const SWIPE_THRESHOLD = 50;

  // Funções helper para URLs
  const getStreamUrl = (messageId: string) => `/stream/${messageId}`;
  const getPhotoUrl = (messageId: string) => `/photo/${messageId}`;
  const getThumbnailUrl = (messageId: string) => `/thumb/${messageId}`;
  const getAvatarUrl = (globalTerm: string) => `/avatar/${encodeURIComponent(globalTerm.trim())}`;
  
  // Funções para abrir redes sociais
  const openInstagram = (instagram: string) => {
    const username = instagram.replace(/^@/, '');
    const appUrl = `instagram://user?username=${username}`;
    const fallbackUrl = `https://instagram.com/${username}`;

    window.location.href = appUrl;

    const isMobile = /iPhone|iPad|iPod|Android/i.test(navigator.userAgent);
    if (!isMobile) {
      setTimeout(() => {
        window.open(fallbackUrl, '_blank', 'noopener,noreferrer');
      }, 800);
    }
  };

  const openTikTok = (tiktok: string) => {
    const username = tiktok.replace(/^@/, '');
    const appUrl = `snssdk1233://user/profile/${username}`;
    const fallbackUrl = `https://tiktok.com/@${username}`;

    window.location.href = appUrl;

    const isMobile = /iPhone|iPad|iPod|Android/i.test(navigator.userAgent);
    if (!isMobile) {
      setTimeout(() => {
        window.open(fallbackUrl, '_blank', 'noopener,noreferrer');
      }, 800);
    }
  };

  const openTwitter = (twitter: string) => {
    const username = twitter.replace(/^@/, '');
    const appUrl = `twitter://user?screen_name=${username}`;
    const fallbackUrl = `https://x.com/${username}`;

    window.location.href = appUrl;

    const isMobile = /iPhone|iPad|iPod|Android/i.test(navigator.userAgent);
    if (!isMobile) {
      setTimeout(() => {
        window.open(fallbackUrl, '_blank', 'noopener,noreferrer');
      }, 800);
    }
  };
  
  // Helper para determinar se é vídeo (tudo que não é .jpg é vídeo)
  const isVideoMedia = (media: MediaItem): boolean => {
    return media.media_extension?.toLowerCase() !== '.jpg';
  };

  const isPhotoMedia = (media?: MediaItem): boolean => {
    if (!media) return false;
    return !isVideoMedia(media);
  };

  // marca mídia atual como assistida (idempotente e otimista)
  const markCurrentAsWatched = () => {
    const id = selectedMedia?.media_id;
    const username = session?.user?.email;
    
    if (!id || !username || !token) {
      return;
    }
    if (markedRef.current.has(id)) {
      return;
    }
    
    markedRef.current.add(id);

    markWatched(token, username, id).catch((e) => {
      console.error('[markWatched] ❌ Falhou:', e);
      markedRef.current.delete(id);
    });
  };

  // wrapper de navegação: não precisa mais marcar ao avançar
  const handleNavigate = async (direction: 'prev' | 'next') => {
    onNavigate(direction);
  };

  // --------------------------------------------
  // HISTÓRICO: fechar no voltar e consumir entrada no X/Escape
  // --------------------------------------------
  // Fecha o modal. Se a origem for "user", usamos history.back() para consumir a entrada
  // e o fechamento efetivo acontece no handler de popstate.
  const requestClose = (origin: 'user' | 'back') => {
    if (disableHistoryManagement || origin === 'back') {
      onClose();
      return;
    }
    
    if (origin === 'user') {
      // só volta se a entrada que criamos existir
      try {
        if (typeof window !== 'undefined' && window.history.state?.modalOpen) {
          window.history.back();
          return; // o onClose roda via popstate
        }
      } catch { /* ignore */ }
      onClose(); // fallback
    }
  };

  useEffect(() => {
    // Só gerencia histórico se não foi desabilitado
    if (disableHistoryManagement) return;
    
    // Inserimos uma entrada no histórico quando o modal monta
    // para capturar o botão voltar do mobile/navegador.
    try {
      window.history.pushState({ modalOpen: true }, '');
    } catch { /* ignore */ }

    const handlePopState = () => {
      // Ao voltar, fechamos o modal.
      requestClose('back');
    };

    window.addEventListener('popstate', handlePopState);
    return () => window.removeEventListener('popstate', handlePopState);
  }, [disableHistoryManagement]); // monta/desmonta junto com o modal

  // Marca como assistido quando token estiver disponível
  useEffect(() => {
    if (!selectedMedia?.media_id || !session?.user?.email || !token) return;
    markCurrentAsWatched();
  }, [selectedMedia?.media_id, session?.user?.email, token]);

  useEffect(() => {
    if (!selectedMedia || !mediaContainerRef.current) return;
    
    if (isVideoMedia(selectedMedia) && !PlyrConstructorRef.current) {
      console.log(`[MediaModal] ⏳ Aguardando PlyrConstructor carregar para mídia inicial ${selectedMedia.media_id}`);
      return;
    }

    // Suporta tanto o formato Hasura (array) quanto o formato do backend antigo (boolean)
    setIsLiked(
      Array.isArray(selectedMedia.liked) 
        ? selectedMedia.liked.length > 0 
        : selectedMedia.liked || false
    );
    setIsFavorited(
      Array.isArray(selectedMedia.favorite) 
        ? selectedMedia.favorite.length > 0 
        : selectedMedia.favorite || false
    );
    setLikes(selectedMedia.like_count || 0);

    if (fallbackItem) {
      const isFav = Array.isArray(fallbackItem.is_favorite) 
        ? fallbackItem.is_favorite.length > 0 
        : fallbackItem.favorite || false;
      setIsFollowing(isFav);
    }

    const container = mediaContainerRef.current;
    container.innerHTML = '';

    const messageId = selectedMedia.media_channels?.[0]?.message_id || selectedMedia.media_id;
    
    // Pausa todos os vídeos em background antes de carregar a nova mídia
    pauseAllBackgroundVideos(messageId);
    
    // Para vídeos: busca primeiro no cache de Plyr
    // Para imagens: busca no cache de elementos HTML
    const cachedPlyr = isVideoMedia(selectedMedia) ? getPlyrInstance(messageId) : null;
    const cachedElement = preloadedElementsRef.current.get(messageId);

    // Log para debug
    console.log(`[MediaModal] Loading media ${messageId}`, {
      isVideo: isVideoMedia(selectedMedia),
      hasCachedPlyr: !!cachedPlyr,
      hasCachedElement: !!cachedElement,
      elementTag: cachedElement?.tagName,
      elementCacheSize: preloadedElementsRef.current.size,
      plyrCacheSize: getPlyrCacheSize()
    });

    // Para vídeos com Plyr em cache: reutiliza a instância completa
    if (isVideoMedia(selectedMedia) && cachedPlyr) {
      console.log(`[MediaModal] ✅ Reutilizando instância Plyr do cache para ${messageId}`);
      
      try {
        const plyrMedia = cachedPlyr.media as HTMLVideoElement;
        if (plyrMedia && plyrMedia.parentElement) {
          // Instância válida
          videoRef.current = plyrMedia;
          plyrInstanceRef.current = cachedPlyr;
          
          // Atualiza classes do vídeo
          plyrMedia.className = `${getMediaSizeClasses()} ${isBlurred ? 'blur-md' : ''}`;
          
          // Força preload auto agora que será exibido
          if (plyrMedia.preload !== 'auto') {
            plyrMedia.preload = 'auto';
          }
          
          // IMPORTANTE: Remove event listeners antigos para evitar duplicação
          cachedPlyr.off('ended', handleVideoEnded);
          cachedPlyr.off('play', handleVideoPlay);
          cachedPlyr.off('pause', handleVideoPause);
          cachedPlyr.off('loadedmetadata', handleVideoLoadedMetadata);
          cachedPlyr.off('timeupdate', handleVideoTimeUpdate);
          
          // Adiciona event listeners frescos
          cachedPlyr.on('ended', handleVideoEnded);
          cachedPlyr.on('play', handleVideoPlay);
          cachedPlyr.on('pause', handleVideoPause);
          cachedPlyr.on('loadedmetadata', handleVideoLoadedMetadata);
          cachedPlyr.on('timeupdate', handleVideoTimeUpdate);
          
          console.log(`[MediaModal] 🎧 Event listeners adicionados ao Plyr reutilizado para ${messageId}`);
          
          // Anexa o wrapper do Plyr ao container
          const plyrWrapper = plyrMedia.closest('.plyr');
          if (plyrWrapper) {
            container.appendChild(plyrWrapper);
          } else {
            container.appendChild(plyrMedia);
          }
          
          // Tenta dar play
          const playPromise = cachedPlyr.play();
          if (playPromise !== undefined) {
            playPromise.catch((err: unknown) => console.warn('Erro no autoplay:', err));
          }
          return; // Concluído
        } else {
          console.warn(`[MediaModal] Plyr inválido (elementos destruídos), será recriado para ${messageId}`);
        }
      } catch (e) {
        console.warn(`[MediaModal] Erro ao validar Plyr, será recriado para ${messageId}:`, e);
      }
    }
    
    // Para vídeos sem Plyr mas com elemento HTML: usa elemento e cria Plyr
    if (isVideoMedia(selectedMedia) && !cachedPlyr && cachedElement && cachedElement.tagName === 'VIDEO') {
      console.log(`[MediaModal] ⚠️ Reutilizando elemento de vídeo do cache mas SEM Plyr (falha no preload), criando agora para ${messageId}`);
      const video = cachedElement as HTMLVideoElement;
      video.controls = false;
      video.className = `${getMediaSizeClasses()} ${isBlurred ? 'blur-md' : ''}`;
      
      // Força preload auto
      if (video.preload !== 'auto') {
        video.preload = 'auto';
      }
      
      videoRef.current = video;
      container.appendChild(video);
      
      // Cria instância do Plyr
      if (!PlyrConstructorRef.current) return;
      plyrInstanceRef.current = new PlyrConstructorRef.current(video, {
        controls: ['play-large', 'play', 'progress', 'current-time', 'settings'],
        autoplay: true,
        clickToPlay: true,
        hideControls: true,
        resetOnEnd: false,
      });
      
      // Adiciona ao cache global
      setPlyrInstance(messageId, plyrInstanceRef.current);
      console.log(`[MediaModal] ✅ Plyr criado e salvo no cache global (fallback) para ${messageId}`);
      
      // Event listeners do Plyr
      plyrInstanceRef.current.on('ended', handleVideoEnded);
      plyrInstanceRef.current.on('play', handleVideoPlay);
      plyrInstanceRef.current.on('pause', handleVideoPause);
      plyrInstanceRef.current.on('loadedmetadata', handleVideoLoadedMetadata);
      plyrInstanceRef.current.on('timeupdate', handleVideoTimeUpdate);
      
      // Tenta dar play
      const playPromise = plyrInstanceRef.current.play();
      if (playPromise !== undefined) {
        playPromise.catch((err: unknown) => console.warn('Erro no autoplay:', err));
      }
      return; // Concluído
    }
    
    // Para imagens: busca elemento HTML no cache
    if (!isVideoMedia(selectedMedia) && cachedElement && cachedElement.tagName === 'DIV') {
      console.log(`[MediaModal] ✅ Reutilizando elemento HTML (imagem) do cache para ${messageId}`);
      cachedElement.className = `${getMediaSizeClasses()} select-none ${isBlurred ? 'blur-md' : ''}`;
      
      const img = cachedElement.querySelector('img');
      if (img) {
        img.onclick = () => toggleImageProgress();
      }
      
      container.appendChild(cachedElement);
      setIsImageLoaded(true);
      setTimeout(() => startImageProgress(), 0);
      return; // Concluído
    }
    
    // Se chegou aqui, não tinha cache válido - cria novo elemento
    console.log(`[MediaModal] ⚠️ Criando NOVO elemento para ${messageId} (não encontrado em cache)`);
    const newElement = prepareMediaElement(selectedMedia, isBlurred, {
      onEnded: handleVideoEnded,
      onPlay: handleVideoPlay,
      onPause: handleVideoPause,
      onLoadedMetadata: handleVideoLoadedMetadata,
      onTimeUpdate: handleVideoTimeUpdate,
      onClick: !isVideoMedia(selectedMedia) ? () => toggleImageProgress() : () => { },
      onImageLoad: () => setIsImageLoaded(true),
    });

    if (isVideoMedia(selectedMedia)) {
      const video = newElement as HTMLVideoElement;
      video.controls = false;
      videoRef.current = video;
      container.appendChild(newElement);
      
      // Cria nova instância do Plyr
      if (!PlyrConstructorRef.current) return;
      plyrInstanceRef.current = new PlyrConstructorRef.current(video, {
        controls: ['play-large', 'play', 'progress', 'current-time', 'settings'],
        autoplay: true,
        clickToPlay: true,
        hideControls: true,
        resetOnEnd: false,
      });
      
      // Adiciona ao cache global
      setPlyrInstance(messageId, plyrInstanceRef.current);
      console.log(`[MediaModal] ✅ Plyr criado e salvo no cache global para ${messageId}`);
      
      // Event listeners do Plyr
      plyrInstanceRef.current.on('ended', handleVideoEnded);
      plyrInstanceRef.current.on('play', handleVideoPlay);
      plyrInstanceRef.current.on('pause', handleVideoPause);
      plyrInstanceRef.current.on('loadedmetadata', handleVideoLoadedMetadata);
      plyrInstanceRef.current.on('timeupdate', handleVideoTimeUpdate);
      
      // Tenta dar play
      const playPromise = plyrInstanceRef.current.play();
      if (playPromise !== undefined) {
        playPromise.catch((err: unknown) => console.warn('Erro no autoplay:', err));
      }
    } else {
      // Imagem: salva no cache de elementos
      preloadedElementsRef.current.set(messageId, newElement);
      container.appendChild(newElement);
    }
  }, [selectedMedia, isBlurred, fallbackItem, preloadedElementsRef, plyrReady]); // Adiciona plyrReady como dependência

  const MAX_KEEP = 7; // mantém até 7 mídias em cache
  const delay = (ms: number) => new Promise(r => setTimeout(r, ms));

  const preloadNextMedias = async () => {
    // Carrega as próximas 2 mídias (+1 e +2) de forma sequencial
    // Quando a +1 carregar metadata, dispara o carregamento da +2
    
    console.log(`[Preload] 🚀 Iniciando preload (PlyrConstructor=${!!PlyrConstructorRef.current}, cache size=${getPlyrCacheSize()})`);
    
    const nextIndex1 = currentMediaIndex + 1;
    // const nextIndex2 = currentMediaIndex + 2;
    // const nextIndex3 = currentMediaIndex + 3;
    
    
    // Carrega a primeira mídia (+1)
    if (nextIndex1 < mediaList.length) {
      const nextMedia1 = mediaList[nextIndex1];
      if (nextMedia1) {
        const id1 = nextMedia1.media_channels?.[0]?.message_id || nextMedia1.media_id;
        
        // Verifica se já existe no cache de elementos (vídeos e imagens)
        if (id1 && !preloadedElementsRef.current.has(id1)) {
          console.log(`[Preload] ⏬ Pré-carregando mídia +1: ${id1}`);
          
          const element1 = prepareMediaElement(nextMedia1, false);
          preloadedElementsRef.current.set(id1, element1);
          
          // Atualiza ordem apenas se não existir
          const existingIndex = preloadOrderRef.current.indexOf(id1);
          if (existingIndex === -1) {
            preloadOrderRef.current.push(id1);
          }

          console.log(`[Preload] ✅ Elemento ${id1} adicionado ao cache (size: ${preloadedElementsRef.current.size}, tipo: ${isVideoMedia(nextMedia1) ? 'vídeo' : 'imagem'})`);

          // Para vídeos: tenta criar o Plyr imediatamente no container temporário
          if (isVideoMedia(nextMedia1)) {
            // Garante que o container temporário existe
            if (!preloadContainerRef.current) {
              console.log('[Preload] ⚠️ Container temporário não existe, criando agora...');
              const tempContainer = document.createElement('div');
              tempContainer.style.position = 'fixed';
              tempContainer.style.top = '-9999px';
              tempContainer.style.left = '-9999px';
              tempContainer.style.width = '1px';
              tempContainer.style.height = '1px';
              tempContainer.style.overflow = 'hidden';
              tempContainer.style.pointerEvents = 'none';
              document.body.appendChild(tempContainer);
              preloadContainerRef.current = tempContainer;
            }
            
            // Verifica se PlyrConstructor está disponível (sem aguardar)
            if (PlyrConstructorRef.current && preloadContainerRef.current) {
              const video = element1 as HTMLVideoElement;
              video.controls = false;
              
              // Força preload="auto" para carregar o vídeo agressivamente em background
              video.preload = 'auto';
              
              // Anexa ao container temporário (necessário para Plyr inicializar)
              preloadContainerRef.current.appendChild(video);
              
              console.log(`[Preload] 🎬 Criando instância Plyr para ${id1} (em container temporário)`);
              
              try {
                const plyrInstance = new PlyrConstructorRef.current(video, {
                  controls: ['play-large', 'play', 'progress', 'current-time', 'settings'],
                  autoplay: false,
                  clickToPlay: true,
                  hideControls: true,
                  resetOnEnd: false,
                });
                
                setPlyrInstance(id1, plyrInstance);
                console.log(`[Preload] ✅ Plyr criado e salvo no cache global para ${id1}`);
                
                // NÃO aguarda metadata - deixa carregar em background de forma assíncrona
                // Isso permite que o usuário navegue sem travar
              } catch (err) {
                console.warn(`[Preload] ❌ Erro ao criar Plyr para ${id1}:`, err);
              }
            } else {
              console.warn(`[Preload] ⚠️ PlyrConstructor não disponível ainda para ${id1}, será criado no fallback`);
            }
          } else {
            // Para imagens: aguarda decode
            try { 
              await (element1 as HTMLImageElement).decode?.(); 
            } catch { 
            }
          }

        } else if (id1) {
          console.log(`[Preload] ⏭️ Elemento ${id1} já está no cache`);
        }
      }
    }

    // // Após carregar a primeira, carrega a segunda mídia (+2)
    // if (nextIndex2 < mediaList.length) {
    //   const nextMedia2 = mediaList[nextIndex2];
    //   if (nextMedia2) {
    //     const id2 = nextMedia2.media_channels?.[0]?.message_id || nextMedia2.media_id;
        
    //     if (id2 && !preloadedElementsRef.current.has(id2)) {
          
    //       const element2 = prepareMediaElement(nextMedia2, false);
    //       element2.classList.add('hidden');
    //       preloadedElementsRef.current.set(id2, element2);
    //       preloadOrderRef.current.push(id2);

    //       // Aguarda metadata da segunda mídia
    //       if (isVideoMedia(nextMedia2)) {
    //         await Promise.race([
    //           new Promise<void>(res => { (element2 as HTMLVideoElement).onloadedmetadata = () => res(); }),
    //           delay(4000),
    //         ]);
    //       } else {
    //         try { 
    //           await (element2 as HTMLImageElement).decode?.(); 
    //         } catch { 
    //         }
    //       }

    //     } else if (id2) {
    //     }
    //   }
    // }


    // // Após carregar a primeira, carrega a terceira mídia (+3)
    // if (nextIndex3 < mediaList.length) {
    //   const nextMedia3 = mediaList[nextIndex3];
    //   if (nextMedia3) {
    //     const id3 = nextMedia3.media_channels?.[0]?.message_id || nextMedia3.media_id;
        
    //     if (id3 && !preloadedElementsRef.current.has(id3)) {
          
    //       const element3 = prepareMediaElement(nextMedia3, false);
    //       element3.classList.add('hidden');
    //       preloadedElementsRef.current.set(id3, element3);
    //       preloadOrderRef.current.push(id3);

    //       // Aguarda metadata da terceira mídia
    //       if (isVideoMedia(nextMedia3)) {
    //         await Promise.race([
    //           new Promise<void>(res => { (element3 as HTMLVideoElement).onloadedmetadata = () => res(); }),
    //           delay(4000),
    //         ]);
    //       } else {
    //         try { 
    //           await (element3 as HTMLImageElement).decode?.(); 
    //         } catch { 
    //         }
    //       }

    //     } else if (id3) {
    //     }
    //   }
    // }



    const currentId = selectedMedia?.media_channels?.[0]?.message_id || selectedMedia?.media_id;
    
    // Limpa elementos HTML antigos (mantém MAX_KEEP)
    // Nota: Para vídeos, quando removemos o elemento HTML, também precisamos remover o Plyr
    while (preloadOrderRef.current.length > MAX_KEEP) {
      const oldest = preloadOrderRef.current.shift();
      if (oldest && oldest !== currentId) {
        const hadElement = preloadedElementsRef.current.has(oldest);
        if (hadElement) {
          console.log(`[Cache] 🗑️ Removendo elemento HTML do cache: ${oldest} (limite de ${MAX_KEEP} atingido)`);
          preloadedElementsRef.current.delete(oldest);
          
          // Se tinha Plyr associado a este elemento, remove também
          const hadPlyr = !!getPlyrInstance(oldest);
          if (hadPlyr) {
            console.log(`[Cache] 🗑️ Removendo Plyr associado ao elemento ${oldest}`);
            removePlyrInstance(oldest);
          }
        }
        
        console.log(`[Cache] Estado após remoção - elementos: ${preloadedElementsRef.current.size}, ordem: ${preloadOrderRef.current.length}, plyrCache: ${getPlyrCacheSize()}`);
      }
    }
    
    // Limpa instâncias Plyr órfãs (sem elemento HTML correspondente)
    // Isso pode acontecer se o Plyr foi criado mas o elemento HTML foi removido em outra situação
    const MAX_PLYR_CACHE = MAX_KEEP * 2;
    const plyrCacheSize = getPlyrCacheSize();
    if (plyrCacheSize > MAX_PLYR_CACHE) {
      console.log(`[Cache] 🗑️ Limpando cache de Plyr órfãos (${plyrCacheSize} > ${MAX_PLYR_CACHE})`);
      // Mantém os IDs que estão na ordem atual + mídia atual
      const idsToKeep = [...preloadOrderRef.current];
      if (currentId && !idsToKeep.includes(currentId)) {
        idsToKeep.push(currentId);
      }
      cleanOldPlyrInstances(idsToKeep, MAX_PLYR_CACHE);
      console.log(`[Cache] Estado após limpeza de Plyr órfãos - plyrCache: ${getPlyrCacheSize()}`);
    }
    
  };

  // Função para pausar todos os vídeos em background (prevenção de múltiplos vídeos tocando)
  const pauseAllBackgroundVideos = (exceptMessageId?: string) => {
    const allIds = preloadOrderRef.current;
    let pausedCount = 0;
    
    allIds.forEach(id => {
      if (id !== exceptMessageId) {
        const plyrInstance = getPlyrInstance(id);
        if (plyrInstance) {
          try {
            // Pausa se estiver tocando
            if (plyrInstance.playing) {
              console.log(`[MediaModal] ⏸️ Pausando vídeo em background: ${id}`);
              plyrInstance.pause();
              pausedCount++;
            }
            
            // NÃO cancela o carregamento - deixa continuar em background
            // Isso permite que o vídeo continue carregando para navegação rápida
            const videoElement = plyrInstance.media as HTMLVideoElement;
            if (videoElement) {
              // Apenas garante que preload está configurado para continuar carregando
              if (videoElement.preload !== 'auto' && videoElement.preload !== 'metadata') {
                videoElement.preload = 'metadata';
              }
            }
          } catch (e) {
            console.warn(`[MediaModal] Erro ao pausar vídeo ${id}:`, e);
          }
        }
      }
    });
    
    if (pausedCount > 0) {
      console.log(`[MediaModal] ⏸️ Total de vídeos pausados em background: ${pausedCount}`);
    }
  };

  const prepareMediaElement = (
    media: MediaItem,
    isBlurred: boolean,
    handlers?: Partial<{
      onEnded: () => void;
      onPlay: () => void;
      onPause: () => void;
      onLoadedMetadata: () => void;
      onTimeUpdate: () => void;
      onClick: () => void;
      onImageLoad?: () => void;
    }>
  ): HTMLElement => {
    const messageId = media.media_channels?.[0]?.message_id || media.media_id;
    const mediaType = isVideoMedia(media) ? 'video' : 'image';
    console.log(`[prepareMediaElement] Criando elemento para ${messageId}, tipo: ${mediaType}`);
    
    if (isVideoMedia(media)) {
      const video = document.createElement('video');
      video.src = getStreamUrl(messageId);
      video.poster = getThumbnailUrl(messageId);
      video.preload = 'metadata'; // Carrega apenas metadata, não o vídeo completo
      video.playsInline = true;
      video.controls = false; // Plyr vai adicionar os controles
      video.disableRemotePlayback = true;
      video.className = `${getMediaSizeClasses()} ${isBlurred ? 'blur-md' : ''}`;
      
      // Atributos específicos para iOS
      video.setAttribute('playsinline', '');
      video.setAttribute('webkit-playsinline', '');
      video.setAttribute('x5-playsinline', '');
      video.setAttribute('x5-video-player-type', 'h5');
      video.setAttribute('x5-video-player-fullscreen', 'false');

      if (handlers) {
        video.oncontextmenu = (e) => e.preventDefault();
        // Eventos serão anexados via Plyr
      }

      return video;
    }

    // É imagem (.jpg) - cria wrapper com loading
    const wrapper = document.createElement('div');
    wrapper.className = 'relative w-full h-full flex items-center justify-center';
    
    // Cria o loader
    const loader = document.createElement('div');
    loader.className = 'absolute inset-0 flex items-center justify-center bg-black/50 z-10';
    loader.innerHTML = `
      <div class="animate-spin rounded-full h-12 w-12 border-b-2 border-white"></div>
    `;
    
    const img = document.createElement('img');
    img.src = getPhotoUrl(messageId);
    img.alt = 'Mídia';
    img.draggable = false;
    img.className = `${getMediaSizeClasses()} select-none ${isBlurred ? 'blur-md' : ''}`;

    // Adiciona evento de load para remover o loader
    img.onload = () => {
      loader.remove();
      if (handlers?.onImageLoad) {
        handlers.onImageLoad();
      }
    };

    if (handlers) {
      img.oncontextmenu = (e) => e.preventDefault();
      if (handlers.onClick) img.onclick = handlers.onClick;
    }

    wrapper.appendChild(loader);
    wrapper.appendChild(img);
    return wrapper;
  };

  useEffect(() => {
    if (currentMediaIndex >= mediaList.length - 4 && !hasRequestedMore.current) {
      hasRequestedMore.current = true;
      onLoadMoreMediaForTerm(termName);
    }
  }, [currentMediaIndex, mediaList.length, onLoadMoreMediaForTerm, termName]);

  useEffect(() => {
    hasRequestedMore.current = false;
  }, [mediaList.length]);

  useEffect(() => {
    const scrollY = window.scrollY;

    document.body.style.position = 'fixed';
    document.body.style.top = `-${scrollY}px`;
    document.body.style.width = '100%';
    document.body.style.overflow = 'hidden';
    
    // Cria container temporário invisível para preload de Plyr
    if (!preloadContainerRef.current) {
      const tempContainer = document.createElement('div');
      tempContainer.style.position = 'fixed';
      tempContainer.style.top = '-9999px';
      tempContainer.style.left = '-9999px';
      tempContainer.style.width = '1px';
      tempContainer.style.height = '1px';
      tempContainer.style.overflow = 'hidden';
      tempContainer.style.pointerEvents = 'none';
      document.body.appendChild(tempContainer);
      preloadContainerRef.current = tempContainer;
    }

    return () => {
      document.body.style.position = '';
      document.body.style.top = '';
      document.body.style.width = '';
      document.body.style.overflow = '';
      
      // Remove container temporário
      if (preloadContainerRef.current) {
        preloadContainerRef.current.remove();
        preloadContainerRef.current = null;
      }

      requestAnimationFrame(() => {
        // Usa scrollY capturado no momento da montagem do modal
        window.scrollTo(0, scrollY);
      });
    };
  }, []);

  // reset ao trocar mídia + preload
  useEffect(() => {
    setProgress(0);
    setElapsed(0);
    setStartTime(null);
    setIsPaused(false);
    setIsPlaying(false);
    setVideoDuration(0);
    setIsImageLoaded(false);

    // Mostra os controles ao trocar de mídia
    setShowControls(true);
    if (controlsTimeoutRef.current) clearTimeout(controlsTimeoutRef.current);
    controlsTimeoutRef.current = setTimeout(() => setShowControls(false), 4000);

    if (progressTimeoutRef.current) clearTimeout(progressTimeoutRef.current);
    if (progressIntervalRef.current) clearInterval(progressIntervalRef.current);
    if (imageProgressFrameRef.current !== null) {
      cancelAnimationFrame(imageProgressFrameRef.current);
      imageProgressFrameRef.current = null;
    }
    if (preloadTimeoutRef.current) {
      clearTimeout(preloadTimeoutRef.current);
      preloadTimeoutRef.current = null;
    }

    // Não inicia progresso automático aqui - vai iniciar quando a imagem carregar
    // Para vídeos, o progresso será controlado pelo evento timeupdate
    
    // Delay maior no início para dar tempo do PlyrConstructor inicializar
    // Após o primeiro vídeo, aguarda 2-3 segundos antes de precarregar o próximo
    // Isso permite que o vídeo atual carregue sem competição por banda
    const delayMs = getPlyrCacheSize() === 0 ? 1200 : 3000;
    preloadTimeoutRef.current = setTimeout(() => { void preloadNextMedias(); }, delayMs);

    return () => {
      if (imageProgressFrameRef.current !== null) {
        cancelAnimationFrame(imageProgressFrameRef.current);
        imageProgressFrameRef.current = null;
      }
      if (preloadTimeoutRef.current) {
        clearTimeout(preloadTimeoutRef.current);
        preloadTimeoutRef.current = null;
      }
    };
  }, [selectedMedia]);

  // Inicia progresso da imagem quando ela terminar de carregar
  useEffect(() => {
    if (isImageLoaded && isPhotoMedia(selectedMedia)) {
      // Pequeno delay para garantir que o DOM está pronto
      const timer = setTimeout(() => startImageProgress(), 50);
      return () => clearTimeout(timer);
    }
  }, [isImageLoaded, selectedMedia]);

  // Auto-hide controls
  useEffect(() => {
    const resetControlsTimeout = () => {
      if (controlsTimeoutRef.current) clearTimeout(controlsTimeoutRef.current);
      setShowControls(true);
      controlsTimeoutRef.current = setTimeout(() => setShowControls(false), 4000);
    };

    resetControlsTimeout();
    const handleMouseMove = () => resetControlsTimeout();
    const handleTouchStart = () => resetControlsTimeout();

    document.addEventListener('mousemove', handleMouseMove);
    document.addEventListener('touchstart', handleTouchStart);

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('touchstart', handleTouchStart);
      if (controlsTimeoutRef.current) clearTimeout(controlsTimeoutRef.current);
    };
  }, []);

  // atalhos de teclado (vertical)
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      switch (e.key) {
        case 'Escape':
          e.preventDefault();
          requestClose('user');
          break;
        case 'ArrowUp':
          e.preventDefault();
          if (mediaList.length > 1) void handleNavigate('prev');
          break;
        case 'ArrowDown':
          e.preventDefault();
          if (mediaList.length > 1) void handleNavigate('next');
          break;
        case ' ':
          e.preventDefault();
          if (isVideoMedia(selectedMedia)) {
            toggleVideoPlayPause();
          } else {
            toggleImageProgress();
          }
          break;
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [mediaList.length, selectedMedia]);

  // Touch handlers (swipe)
  const handleTouchStart = (e: React.TouchEvent) => {
    setTouchStartY(e.touches[0].clientY);
  };
  const handleTouchMove = (e: React.TouchEvent) => {
    setTouchEndY(e.touches[0].clientY);
  };
  const handleTouchEnd = () => {
    if (!touchStartY || !touchEndY) return;
    const distance = touchStartY - touchEndY;
    const isSwipe = Math.abs(distance) > SWIPE_THRESHOLD;

    if (!isSwipe && isPhotoMedia(selectedMedia)) {
      toggleImageProgress();
    }

    if (distance > SWIPE_THRESHOLD && mediaList.length > 1) {
      void handleNavigate('next');
    } else if (distance < -SWIPE_THRESHOLD && mediaList.length > 1) {
      void handleNavigate('prev');
    }

    setTouchStartY(0);
    setTouchEndY(0);
  };

  // cleanup
  useEffect(() => {
    return () => {
      if (progressTimeoutRef.current) clearTimeout(progressTimeoutRef.current);
      if (progressIntervalRef.current) clearInterval(progressIntervalRef.current);
      if (controlsTimeoutRef.current) clearTimeout(controlsTimeoutRef.current);
      if (seekTimeoutRef.current) clearTimeout(seekTimeoutRef.current);
      if (tapResetTimeoutRef.current.left) clearTimeout(tapResetTimeoutRef.current.left);
      if (tapResetTimeoutRef.current.right) clearTimeout(tapResetTimeoutRef.current.right);
      if (imageProgressFrameRef.current !== null) {
        cancelAnimationFrame(imageProgressFrameRef.current);
        imageProgressFrameRef.current = null;
      }
      if (preloadTimeoutRef.current) {
        clearTimeout(preloadTimeoutRef.current);
        preloadTimeoutRef.current = null;
      }
      
      // NOTA: Não limpamos o cache global do Plyr aqui para persistência entre navegações
      // O cache será limpo apenas quando necessário através do contexto
      
      if (plyrInstanceRef.current) {
        plyrInstanceRef.current = null;
      }
    };
  }, []);

  const startImageProgress = () => {
  if (!isPhotoMedia(selectedMedia)) return;

    if (progressIntervalRef.current) clearInterval(progressIntervalRef.current);

    const now = Date.now();
    setStartTime(now);
    setElapsed(0);
    setProgress(0);
    setIsPaused(false);

    progressIntervalRef.current = setInterval(() => {
      const currentElapsed = Date.now() - now;
      const currentProgress = Math.min((currentElapsed / STORY_DURATION) * 100, 100);

      setElapsed(currentElapsed);
      setProgress(currentProgress);

      if (currentProgress >= 100) {
        clearInterval(progressIntervalRef.current!);
        onNavigate('next');
      }
    }, 50);
  };

  const formatNumber = (num: number) => {
    if (num >= 1_000_000) return `${(num / 1_000_000).toFixed(1)}M`;
    if (num >= 1_000) return `${(num / 1_000).toFixed(1)}K`;
    return num.toString();
  };

  const pauseImageProgress = () => {
  if (!isPhotoMedia(selectedMedia)) return;
    if (!isPaused && startTime) {
      setIsPaused(true);
      setElapsed(Date.now() - startTime);
      if (progressIntervalRef.current) clearInterval(progressIntervalRef.current);
    }
  };

  const resumeImageProgress = () => {
  if (!isPhotoMedia(selectedMedia)) return;
    if (isPaused) {
      if (progressIntervalRef.current) clearInterval(progressIntervalRef.current);

      const newStart = Date.now() - elapsed;
      setStartTime(newStart);
      setIsPaused(false);

      progressIntervalRef.current = setInterval(() => {
        setElapsed(Date.now() - newStart);
        setProgress(() => {
          const currentElapsed = Date.now() - newStart;
          const newProgress = Math.min((currentElapsed / STORY_DURATION) * 100, 100);
          if (newProgress >= 100) {
            clearInterval(progressIntervalRef.current!);
            onNavigate('next');
          }
          return newProgress;
        });
      }, 50);
    }
  };

  const pauseMediaForDialog = () => {
    dialogMediaStateRef.current = { videoWasPlaying: false, imageWasProgressing: false };

    if (isPhotoMedia(selectedMedia)) {
      dialogMediaStateRef.current.imageWasProgressing = !isPaused;
      if (!isPaused) {
        pauseImageProgress();
      }
    } else if (plyrInstanceRef.current) {
      dialogMediaStateRef.current.videoWasPlaying = !plyrInstanceRef.current.paused;
      if (!plyrInstanceRef.current.paused) {
        plyrInstanceRef.current.pause();
        setIsPlaying(false);
      }
    }
  };

  const resumeMediaAfterDialog = () => {
    if (isPhotoMedia(selectedMedia) && dialogMediaStateRef.current.imageWasProgressing) {
      resumeImageProgress();
    } else if (!isPhotoMedia(selectedMedia) && dialogMediaStateRef.current.videoWasPlaying && plyrInstanceRef.current) {
      const resumePromise = plyrInstanceRef.current.play();
      if (resumePromise !== undefined) {
        resumePromise.then(() => setIsPlaying(true)).catch(() => {});
      }
    }

    dialogMediaStateRef.current = { videoWasPlaying: false, imageWasProgressing: false };
  };

  const removeCurrentMediaFromCache = () => {
    const messageId = selectedMedia.media_channels?.[0]?.message_id || selectedMedia.media_id;
    if (!messageId) return;
    
    console.log(`[Cache] 🗑️ INVALIDAÇÃO manual da mídia ${messageId}`);
    
    // Remove elemento do cache
    const hadElement = preloadedElementsRef.current.has(messageId);
    preloadedElementsRef.current.delete(messageId);
    preloadOrderRef.current = preloadOrderRef.current.filter((id) => id !== messageId);
    
    // Remove e destroi instância do Plyr se existir no cache global
    const plyrInstance = getPlyrInstance(messageId);
    if (plyrInstance) {
      // Pausa o vídeo antes de destruir
      if (plyrInstance.playing) {
        console.log(`[Cache] ⏸️ Pausando vídeo invalidado antes de remover: ${messageId}`);
        try {
          plyrInstance.pause();
        } catch (e) {
          console.warn(`[Cache] Erro ao pausar vídeo invalidado ${messageId}:`, e);
        }
      }
      
      console.log(`[Cache] 🗑️ Destruindo e removendo instância Plyr de mídia invalidada ${messageId}`);
      removePlyrInstance(messageId);
    }
    
    console.log(`[Cache] Estado após invalidação - elementos: ${preloadedElementsRef.current.size}, ordem: ${preloadOrderRef.current.length}`);
  };

  const toggleImageProgress = () => {
    if (isPaused) {
      resumeImageProgress();
    } else {
      pauseImageProgress();
    }
  };

  const toggleVideoPlayPause = () => {
    if (!plyrInstanceRef.current) return;
    plyrInstanceRef.current.togglePlay();
  };

  const handleVideoEnded = async () => {
    setIsPlaying(false);
    onNavigate('next');
  };

  const handleVideoPlay = () => setIsPlaying(true);
  const handleVideoPause = () => setIsPlaying(false);

  const handleVideoLoadedMetadata = () => {
    if (plyrInstanceRef.current) {
      setProgress(0);
    }
  };

  const handleVideoTimeUpdate = () => {
    if (plyrInstanceRef.current && plyrInstanceRef.current.duration  > 0) {
      const currentTime = plyrInstanceRef.current.currentTime;
      const progressPercent = (currentTime / plyrInstanceRef.current.duration) * 100;
      setProgress(progressPercent);
    }
  };

  const handleFavorite = async () => {
    try {
      onToggleFavoriteMedia(selectedMedia.media_id!);
    } catch (err) {
      console.error('Erro ao favoritar mídia:', err);
    }
  };

  const handleLike = async () => {
    try {
      onToggleLikeMedia(selectedMedia.media_id!);
    } catch (err) {
      console.error('Erro ao curtir mídia:', err);
    }
  };

  const handleFollow = () => {
    setIsFollowing(!isFollowing);
    try {
      onToggleFollow(termName);
    } catch (err) {
      console.error('Erro ao seguir termo:', err);
      setIsFollowing(prev => !prev); // rollback
    }
  };

  // Função para invalidar mídia
  const handleInvalidateMedia = async (options: { showUndoButton?: boolean } = {}) => {
    if (!selectedMedia.media_id || !token) return false;
    
    try {
      const result = await setMediaValidity(token, selectedMedia.media_id, true);
      if (result.status === 'ok') {
        if (options.showUndoButton !== false) {
          setShowUndoButton(true);
        } else {
          setShowUndoButton(false);
        }
        return true;
      }
    } catch (err) {
      console.error('Erro ao invalidar mídia:', err);
    }
    return false;
  };

  // Função para desfazer invalidação (validar novamente)
  const handleUndoInvalidation = async () => {
    if (!selectedMedia.media_id || !token) return;
    
    try {
      const result = await setMediaValidity(token, selectedMedia.media_id, false);
      if (result.status === 'ok') {
        setShowUndoButton(false);
      }
    } catch (err) {
      console.error('Erro ao validar mídia:', err);
    }
  };

  // Reset estado de invalidação ao trocar mídia
  useEffect(() => {
    setShowUndoButton(false);
    setShowInvalidateConfirm(false);
  }, [selectedMedia]);

  const handleOpenInvalidateConfirm = () => {
    pauseMediaForDialog();
    setShowInvalidateConfirm(true);
  };

  const handleCancelInvalidateConfirm = () => {
    setShowInvalidateConfirm(false);
    resumeMediaAfterDialog();
  };

  const handleConfirmInvalidate = async () => {
    const success = await handleInvalidateMedia({ showUndoButton: false });
    setShowInvalidateConfirm(false);

    if (success) {
      removeCurrentMediaFromCache();
      dialogMediaStateRef.current = { videoWasPlaying: false, imageWasProgressing: false };

      if (selectedMedia?.media_id) {
        if (onMediaInvalidated) {
          onMediaInvalidated(selectedMedia.media_id);
        } else {
          void handleNavigate('next');
        }
      } else {
        void handleNavigate('next');
      }
    } else {
      resumeMediaAfterDialog();
    }
  };

  const openTermPage = () => {
    if (isPlaying) toggleVideoPlayPause();
    
    // Salva o estado do reels antes de navegar
    if (onSaveReelsState) {
      onSaveReelsState();
    }
    
    // Marca que está navegando para um termo
    sessionStorage.setItem('navigatedFromReels', 'true');
    
    setTimeout(() => {
      router.push(`/term/${encodeURIComponent(termName)}`);
    }, 0);
  };

  // Handle double-click/tap on side zones to seek video
  const handleSideTap = (side: 'left' | 'right', event: React.MouseEvent | React.TouchEvent) => {
    if (!isVideoMedia(selectedMedia) || !plyrInstanceRef.current) return;

    const now = Date.now();
    const lastTap = lastTapTimeRef.current[side];
    const timeSinceLastTap = now - lastTap;

    // Check if this is a double tap (second tap within 300ms)
    const isDoubleTap = lastTap > 0 && timeSinceLastTap < 300;

    if (isDoubleTap) {
      // This is a double tap or continuation - handle seek
      event.preventDefault();
      event.stopPropagation();

      // Clear any existing seek timeout
      if (seekTimeoutRef.current) {
        clearTimeout(seekTimeoutRef.current);
      }

      // Increment seek amount by 5 seconds
      const increment = side === 'right' ? 5 : -5;
      pendingSeekRef.current += increment;
      setSeekAmount(Math.abs(pendingSeekRef.current));
      setSeekSide(side);
      setShowSeekIndicator(true);

      // Keep the last tap time for continuous tapping
      lastTapTimeRef.current[side] = now;

      // Set timeout to actually seek after user stops tapping
      seekTimeoutRef.current = setTimeout(() => {
        if (plyrInstanceRef.current && pendingSeekRef.current !== 0) {
          const currentTime = plyrInstanceRef.current.currentTime;
          const newTime = Math.max(0, Math.min(currentTime + pendingSeekRef.current, plyrInstanceRef.current.duration));
          plyrInstanceRef.current.currentTime = newTime;
          
          // Reset everything
          pendingSeekRef.current = 0;
          setShowSeekIndicator(false);
          setSeekAmount(0);
          setSeekSide(null);
          lastTapTimeRef.current[side] = 0;
        }
      }, 500); // Wait 500ms after last tap
    } else {
      // This is a single tap - just record the time and let the event pass through
      // DO NOT prevent default here - let the controls show
      lastTapTimeRef.current[side] = now;
      
      // Reset after 300ms if no second tap comes
      if (tapResetTimeoutRef.current[side]) {
        clearTimeout(tapResetTimeoutRef.current[side]!);
      }
      tapResetTimeoutRef.current[side] = setTimeout(() => {
        lastTapTimeRef.current[side] = 0;
      }, 300);
    }
  };

  const getMediaSizeClasses = () => {
    if (!selectedMedia.width || !selectedMedia.height) return 'h-full w-auto object-contain';
    const isPortrait = selectedMedia.height > selectedMedia.width;
    const isLandscape = selectedMedia.width > selectedMedia.height;
    if (isPortrait) return 'h-full w-auto object-contain';
    if (isLandscape) return 'h-full w-auto object-contain';
    return 'h-full w-auto object-contain';
  };

  if (!isVisible) return <div className="hidden" />;

  const userData = {
    global_term: selectedMedia.global_term || fallbackItem?.global_term || '',
    username: fallbackItem?.instagram || '',
    followers: fallbackItem?.instagram_followers || 0,
    terms: fallbackItem?.terms || "",
    count: fallbackItem?.media_count || 0,
  };

  return (
    <div className={`fixed inset-0 z-50 bg-black pb-12 sm:pb-0 ${isIOS ? 'pb-[calc(3rem+15px)]' : ''}`} style={{ touchAction: 'none' }}>
      <div
        className="relative w-full h-full flex"
        onTouchStart={handleTouchStart}
        onTouchMove={handleTouchMove}
        onTouchEnd={handleTouchEnd}
      >
        {/* barra de progresso */}
        <div className="absolute top-0 left-0 right-0 z-30 h-1 bg-white/20">
          <div className="h-full bg-white transition-all duration-100 ease-linear" style={{ width: `${progress}%` }} />
        </div>

        {/* fechar */}
        <button
          className={`absolute top-4 left-4 z-30 p-2 rounded-full bg-black/50 text-white hover:bg-black/70 transition-all duration-200 ${showControls ? 'opacity-100' : 'opacity-0'}`}
          onClick={() => requestClose('user')}
        >
          <XIcon size={20} />
        </button>

        {/* contador e tamanho */}
        {(mediaList.length > 1 || formattedMediaSize) && (
          <div className={`absolute top-4 right-4 z-30 flex flex-col items-end gap-2 transition-all duration-200 ${showControls ? 'opacity-100' : 'opacity-0'}`}>
            {mediaList.length > 1 && (
              <div className="bg-black/50 text-white px-3 py-1 rounded-full text-sm font-medium">
                {currentMediaIndex + 1} / {mediaList.length}
              </div>
            )}
            {formattedMediaSize && (
              <div className="bg-black/50 text-white px-2.5 py-0.5 rounded-full text-xs font-medium">
                {formattedMediaSize}
                {isAdmin && typeof window !== 'undefined' && window.location.hostname === 'localhost' && (
                  <span className="ml-1.5 opacity-75">
                    {selectedMedia?.media_channels?.[0]?.message_id && (
                      <> • MsgID: {selectedMedia.media_channels[0].message_id}</>
                    )}
                    {selectedMedia?.media_id && (
                      <> • MediaID: {selectedMedia.media_id}</>
                    )}
                  </span>
                )}
              </div>
            )}
          </div>
        )}

        {/* Botões de invalidar/validar mídia - abaixo do contador */}
        {/* Botões de invalidar/validar mídia - abaixo do contador */}
        <div className={`absolute top-24 right-4 z-30 flex flex-col gap-2 transition-all duration-200 ${showControls ? 'opacity-100' : 'opacity-0'}`}>
          {!showUndoButton ? (
            <button
              className="p-2 rounded-full bg-red-500/80 text-white hover:bg-red-500 transition-all duration-200 hover:scale-110"
              onClick={handleOpenInvalidateConfirm}
              title="Invalidar mídia"
            >
              <WarningIcon size={16} weight="bold" />
            </button>
          ) : (
            <button
              className="p-2 rounded-full bg-green-500/80 text-white hover:bg-green-500 transition-all duration-200 hover:scale-110"
              onClick={handleUndoInvalidation}
              title="Desfazer invalidação"
            >
              <ArrowCounterClockwiseIcon size={16} weight="bold" />
            </button>
          )}
        </div>

        {/* play/pause imagem */}
  {isPhotoMedia(selectedMedia) && (
          <div className={`absolute bottom-4 left-4 z-30 transition-opacity duration-300 ${showControls ? 'opacity-100' : 'opacity-0'}`}>
            <button className="p-3 rounded-full bg-black/50 text-white hover:bg-black/70 transition-colors" onClick={toggleImageProgress}>
              {isPaused ? <PlayIcon size={20} /> : <PauseIcon size={20} />}
            </button>
          </div>
        )}

        {/* termo e data */}
        <div className={`absolute top-4 left-14 z-20 bg-black/50 text-white px-3 py-1 rounded-md text-sm font-medium transition-all duration-200 ${showControls ? 'opacity-100' : 'opacity-0'}`}>
          <div>{selectedMedia?.term}</div>
          <div>{selectedMedia.posted_at ? new Date(selectedMedia.posted_at).toLocaleString('pt-BR') : ''}</div>
        </div>

        {/* área da mídia */}
        <div
          ref={mediaContainerRef}
          className="flex-1 flex items-center justify-center relative"
          onTouchStart={handleTouchStart}
          onTouchMove={handleTouchMove}
          onTouchEnd={handleTouchEnd}
        />

        {/* Left tap zone - always visible, absolute overlay outside media container */}
        {isVideoMedia(selectedMedia) && (
          <div
            className="absolute left-0 top-[20%] bottom-[20%] w-[25%] z-10"
            style={{ pointerEvents: 'auto' }}
            onTouchEnd={(e) => {
              handleSideTap('left', e);
            }}
            onClick={(e) => {
              if (!('ontouchstart' in window)) {
                handleSideTap('left', e);
              }
            }}
          />
        )}

        {/* Right tap zone - only when controls are hidden, absolute overlay outside media container */}
        {isVideoMedia(selectedMedia) && !showControls && (
          <div
            className="absolute right-0 top-[20%] bottom-[20%] w-[25%] z-10"
            style={{ pointerEvents: 'auto' }}
            onTouchEnd={(e) => {
              handleSideTap('right', e);
            }}
            onClick={(e) => {
              if (!('ontouchstart' in window)) {
                handleSideTap('right', e);
              }
            }}
          />
        )}

        {/* Right tap zone over avatar area - only when controls are visible */}
        {isVideoMedia(selectedMedia) && showControls && (
          <div
            className="absolute right-0 top-[20%] bottom-[20%] w-[25%] z-10"
            style={{ pointerEvents: 'auto' }}
            onTouchEnd={(e) => {
              handleSideTap('right', e);
            }}
            onClick={(e) => {
              if (!('ontouchstart' in window)) {
                handleSideTap('right', e);
              }
            }}
          />
        )}

        {/* Seek indicator */}
        {showSeekIndicator && seekSide && (
          <div className={`absolute ${seekSide === 'left' ? 'left-8' : 'right-8'} top-1/2 -translate-y-1/2 z-[110] flex items-center gap-1.5`}
            style={{
              textShadow: '1px 1px 3px rgba(0,0,0,0.9), -1px -1px 2px rgba(0,0,0,0.9), 1px -1px 2px rgba(0,0,0,0.9), -1px 1px 2px rgba(0,0,0,0.9)'
            }}
          >
            {seekSide === 'left' ? (
              <RewindIcon size={24}  className="text-white" />
            ) : (
              <FastForwardIcon size={24} className="text-white" />
            )}
            <span className="text-xl font-bold text-white">{seekAmount}s</span>
          </div>
        )}

        {/* sidebar */}
        <div className={`absolute right-4 bottom-30 z-20 flex flex-col items-center space-y-4 ${showControls ? 'opacity-100' : 'opacity-0'} transition-opacity duration-300`}>
          {/* avatar */}
          <div className="relative">
            <div className="w-12 h-12 rounded-full border-2 border-white bg-gray-600 flex items-center justify-center text-white font-bold text-lg">
              {userData.global_term ? (
                <img
                  src={getAvatarUrl(userData.global_term)}
                  alt={userData.global_term}
                  className="w-full h-full rounded-full object-cover"
                  onClick={openTermPage}
                />
              ) : (
                userData.username.charAt(0).toUpperCase()
              )}
            </div>
            {!isFollowing && (
              <button
                className="absolute -bottom-2 left-1/2 -translate-x-1/2 w-6 h-6 bg-red-500 rounded-full flex items-center justify-center text-white hover:bg-red-600 transition-colors"
                onClick={handleFollow}
              >
                <PlusIcon size={14} />
              </button>
            )}
          </div>

          {/* Botões de redes sociais */}
          {(fallbackItem?.instagram || fallbackItem?.tiktok || fallbackItem?.twitter) && (
            <div className="flex flex-col items-center space-y-2">
              {fallbackItem.instagram && (
                <button
                  className="p-2 rounded-full bg-black/50 text-white hover:bg-pink-600 transition-all duration-200"
                  onClick={() => openInstagram(fallbackItem.instagram!)}
                  title="Abrir Instagram"
                >
                  <InstagramLogoIcon size={20} />
                </button>
              )}
              {fallbackItem.tiktok && (
                <button
                  className="p-2 rounded-full bg-black/50 text-white hover:bg-black transition-all duration-200"
                  onClick={() => openTikTok(fallbackItem.tiktok!)}
                  title="Abrir TikTok"
                >
                  <TiktokLogoIcon size={20} />
                </button>
              )}
              {fallbackItem.twitter && (
                <button
                  className="p-2 rounded-full bg-black/50 text-white hover:bg-blue-500 transition-all duration-200"
                  onClick={() => openTwitter(fallbackItem.twitter!)}
                  title="Abrir X (Twitter)"
                >
                  <XLogoIcon size={20} />
                </button>
              )}
            </div>
          )}

          {/* like */}
          <div className="flex flex-col items-center">
            <button
              className={`p-3 rounded-full transition-all duration-200 ${isLiked ? 'bg-red-500 text-white' : 'bg-black/50 text-white hover:bg-black/70'}`}
              onClick={handleLike}
            >
              <HeartIcon size={24} weight={isLiked ? 'fill' : 'regular'} />
            </button>
            <span className="text-white text-xs mt-1 font-medium">{formatNumber(likes)}</span>
          </div>

          {/* favorito */}
          <button
            className={`p-3 rounded-full transition-all duration-200 ${isFavorited ? 'bg-yellow-500 text-white' : 'bg-black/50 text-white hover:bg-black/70'}`}
            onClick={handleFavorite}
          >
            <StarIcon size={24} weight={isFavorited ? 'fill' : 'regular'} />
          </button>
        </div>
        {showInvalidateConfirm && (
          <div className="modal modal-open cursor-pointer" onClick={handleCancelInvalidateConfirm}>
            <div className="modal-box bg-base-100 text-base-content cursor-default" onClick={(e) => e.stopPropagation()}>
              <h3 className="font-bold text-lg text-error">Invalidar mídia</h3>
              <p className="py-4 text-sm text-base-content/80">
                Tem certeza que deseja invalidar esta mídia? Ela será ocultada para todos os usuários.
              </p>
              <div className="modal-action">
                <button className="btn btn-ghost" onClick={handleCancelInvalidateConfirm}>
                  Cancelar
                </button>
                <button className="btn btn-error text-white" onClick={handleConfirmInvalidate}>
                  Invalidar
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
