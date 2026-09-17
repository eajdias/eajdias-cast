// Estado Global da Aplicação
const state = {
  activeTab: 'home',
  selectedCinemaCat: 'all',
  overview: null,
  cinema: [],
  manga: [],
  music: [],
  currentVideo: null,
  videoProgInterval: null,
  currentMangaCh: null,
  currentMangaPage: 0,
  mangaMode: 'webtoon',
  playlist: [],
  currentTrackIdx: -1,
  audioEl: document.getElementById('global-audio')
};

// Inicialização
document.addEventListener('DOMContentLoaded', () => {
  setupNavigation();
  setupAudioPlayer();
  setupVideoPlayer();
  setupMangaReader();
  loadOverview();
  
  // Atualizar com base na hash da URL se houver
  const hash = window.location.hash.replace('#', '');
  if (['home', 'cinema', 'manga', 'music'].includes(hash)) {
    switchTab(hash);
  } else {
    switchTab('home');
  }
});

// Navegação entre Abas
function setupNavigation() {
  document.querySelectorAll('.nav-tab, .bnav-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const tab = btn.getAttribute('data-tab');
      switchTab(tab);
    });
  });

  // Filtros de Categoria do Cinema
  document.querySelectorAll('#cinema-filters .cat-chip').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('#cinema-filters .cat-chip').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      state.selectedCinemaCat = btn.getAttribute('data-cat');
      const searchVal = document.getElementById('search-cinema')?.value || '';
      renderCinema(searchVal);
    });
  });

  // Buscas
  document.getElementById('search-cinema')?.addEventListener('input', e => renderCinema(e.target.value));
  document.getElementById('search-manga')?.addEventListener('input', e => renderManga(e.target.value));
  document.getElementById('search-music')?.addEventListener('input', e => renderMusic(e.target.value));
}

function switchTab(tabName) {
  state.activeTab = tabName;
  window.location.hash = tabName;

  document.querySelectorAll('.nav-tab, .bnav-btn').forEach(btn => {
    if (btn.getAttribute('data-tab') === tabName) {
      btn.classList.add('active');
    } else {
      btn.classList.remove('active');
    }
  });

  document.querySelectorAll('.view-tab').forEach(view => {
    view.classList.remove('active');
  });
  document.getElementById(`tab-${tabName}`)?.classList.add('active');

  if (tabName === 'cinema' && state.cinema.length === 0) loadCinema();
  if (tabName === 'manga' && state.manga.length === 0) loadManga();
  if (tabName === 'music' && state.music.length === 0) loadMusic();
}

// ----------------------------------------------------
// 1. Visão Geral & Continuar de Onde Parou
// ----------------------------------------------------
async function loadOverview() {
  try {
    const res = await fetch('/api/overview');
    if (!res.ok) return;
    const data = await res.json();
    state.overview = data;

    document.getElementById('stat-cinema').textContent = data.counts.cinema || 0;
    document.getElementById('stat-manga').textContent = data.counts.manga || 0;
    document.getElementById('stat-music').textContent = data.counts.music || 0;

    renderContinue(data.continue || []);
  } catch (err) {
    console.error('Erro ao carregar overview:', err);
  }
}

function renderContinue(items) {
  const grid = document.getElementById('continue-grid');
  if (!items || items.length === 0) {
    grid.innerHTML = '<div class="empty-state">Nenhum item em andamento no momento.</div>';
    return;
  }

  grid.innerHTML = items.map(it => {
    const pct = it.total > 0 ? Math.min(100, Math.round((it.position / it.total) * 100)) : 0;
    const isManga = it.type === 'manga';
    const sub = isManga ? `Página ${Math.round(it.position)} de ${it.total}` : (it.parent ? `${it.parent}` : 'Filme');

    return `
      <div class="media-card" onclick="resumeMedia('${it.id}', '${it.type}')">
        <div class="card-cover">
          <img src="/api/thumb?path=${encodeURIComponent(it.id)}&w=360" loading="lazy" onerror="this.src='/static/thumb-placeholder.png'">
          <span class="card-badge">${isManga ? 'Mangá' : 'Vídeo'}</span>
        </div>
        <div class="card-details">
          <div class="card-title">${escapeHTML(it.title)}</div>
          <div class="card-subtitle">${escapeHTML(sub)}</div>
          <div class="card-progress">
            <div class="card-progress-bar" style="width: ${pct}%"></div>
          </div>
        </div>
      </div>
    `;
  }).join('');
}

function resumeMedia(id, type) {
  if (type === 'manga') {
    openChapter(id, 0);
  } else {
    openVideo(id);
  }
}

// ----------------------------------------------------
// 2. Cinema & Séries
// ----------------------------------------------------
async function loadCinema() {
  try {
    const res = await fetch('/api/cinema');
    state.cinema = await res.json();
    renderCinema();
  } catch (err) {
    console.error('Erro ao carregar cinema:', err);
  }
}

function renderCinema(filter = '') {
  const grid = document.getElementById('cinema-grid');
  const term = filter.toLowerCase();
  let items = state.cinema.filter(it => it.title.toLowerCase().includes(term));

  if (state.selectedCinemaCat && state.selectedCinemaCat !== 'all') {
    items = items.filter(it => it.category === state.selectedCinemaCat);
  }

  if (items.length === 0) {
    grid.innerHTML = '<div class="empty-state">Nenhum vídeo encontrado nesta categoria.</div>';
    return;
  }

  grid.innerHTML = items.map(it => {
    const isShow = it.type === 'show';
    const thumbPath = isShow ? it.episodes[0]?.path : it.movie?.path;
    const epCount = isShow ? `${it.episodes.length} episódios` : 'Filme';

    let badge = 'Vídeo';
    if (it.category === 'anime') badge = 'Anime';
    else if (it.category === 'serie' || isShow) badge = 'Série';
    else if (it.category === 'filme') badge = 'Filme';

    return `
      <div class="media-card" onclick="handleCinemaClick('${it.id}')">
        <div class="card-cover">
          <img src="/api/thumb?path=${encodeURIComponent(thumbPath)}&w=360" loading="lazy">
          <span class="card-badge">${badge}</span>
        </div>
        <div class="card-details">
          <div class="card-title">${escapeHTML(it.title)}</div>
          <div class="card-subtitle">${epCount}</div>
        </div>
      </div>
    `;
  }).join('');
}

function handleCinemaClick(id) {
  const item = state.cinema.find(x => x.id === id);
  if (!item) return;

  if (item.type === 'show') {
    openEpisodesModal(item);
  } else if (item.movie) {
    openVideo(item.movie.id);
  }
}

function openEpisodesModal(show) {
  const modal = document.getElementById('episodes-modal');
  document.getElementById('episodes-title').textContent = show.title;
  const list = document.getElementById('episodes-list');

  list.innerHTML = show.episodes.map(ep => `
    <div class="item-row" onclick="openVideo('${ep.id}')">
      <div class="item-row-left">
        <span class="item-row-title">T${ep.season}E${ep.episode} - ${escapeHTML(ep.title)}</span>
        <span class="item-row-sub">${ep.subtitles?.length ? 'Legendas disponíveis' : 'Sem legendas'}</span>
      </div>
      <span style="color: var(--accent);">▶ Assistir</span>
    </div>
  `).join('');

  modal.hidden = false;
  document.getElementById('episodes-close').onclick = () => modal.hidden = true;
}

// ----------------------------------------------------
// 3. Player de Vídeo com Progresso
// ----------------------------------------------------
function setupVideoPlayer() {
  const video = document.getElementById('main-video');
  const modal = document.getElementById('video-modal');
  const closeBtn = document.getElementById('video-close');

  closeBtn.addEventListener('click', () => closeVideo());
  window.addEventListener('keydown', e => {
    if (e.key === 'Escape' && !modal.hidden) closeVideo();
  });
}

async function openVideo(mediaID) {
  let media = null;
  for (const grp of state.cinema) {
    if (grp.type === 'movie' && grp.movie?.id === mediaID) {
      media = grp.movie;
      break;
    }
    if (grp.type === 'show') {
      const found = grp.episodes.find(e => e.id === mediaID);
      if (found) {
        media = found;
        break;
      }
    }
  }

  const modal = document.getElementById('video-modal');
  const video = document.getElementById('main-video');
  document.getElementById('video-title').textContent = media ? media.title : 'Reproduzindo';

  // Pausar áudio de música se estiver tocando
  if (!state.audioEl.paused) {
    state.audioEl.pause();
  }

  // Limpar tracks antigas
  while (video.firstChild) {
    video.removeChild(video.firstChild);
  }

  // Adicionar source
  const src = document.createElement('source');
  src.src = `/api/cinema/stream?id=${mediaID}`;
  video.appendChild(src);

  // Adicionar legendas se houver
  if (media && media.subtitles) {
    media.subtitles.forEach((sub, idx) => {
      const tr = document.createElement('track');
      tr.kind = 'subtitles';
      tr.label = sub.label;
      tr.srclang = sub.lang;
      tr.src = `/api/cinema/sub?path=${encodeURIComponent(sub.path)}`;
      if (idx === 0) tr.default = true;
      video.appendChild(tr);
    });
  }

  state.currentVideo = mediaID;
  modal.hidden = false;
  video.load();

  // Restaurar tempo de progresso
  const prog = state.overview?.continue?.find(c => c.id === mediaID);
  if (prog && prog.position > 0) {
    video.currentTime = prog.position;
  }

  video.play().catch(() => {});

  // Salvar progresso a cada 5 segundos
  clearInterval(state.videoProgInterval);
  state.videoProgInterval = setInterval(() => {
    if (video.duration > 0 && !video.paused) {
      saveProgress(mediaID, video.currentTime, video.duration, video.currentTime >= video.duration - 10);
    }
  }, 5000);
}

function closeVideo() {
  const video = document.getElementById('main-video');
  const modal = document.getElementById('video-modal');

  if (state.currentVideo && video.duration > 0) {
    saveProgress(state.currentVideo, video.currentTime, video.duration, video.currentTime >= video.duration - 10);
  }

  clearInterval(state.videoProgInterval);
  video.pause();
  video.src = '';
  modal.hidden = true;
  state.currentVideo = null;
  loadOverview();
}

async function saveProgress(mediaID, position, total, completed) {
  try {
    await fetch('/api/progress', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        media_id: mediaID,
        position: position,
        total: total,
        completed: completed
      })
    });
  } catch (err) {
    console.error('Erro ao salvar progresso:', err);
  }
}

// ----------------------------------------------------
// 4. Mangás & Quadrinhos
// ----------------------------------------------------
async function loadManga() {
  try {
    const res = await fetch('/api/manga');
    state.manga = await res.json();
    renderManga();
  } catch (err) {
    console.error('Erro ao carregar manga:', err);
  }
}

function renderManga(filter = '') {
  const grid = document.getElementById('manga-grid');
  const term = filter.toLowerCase();
  const items = state.manga.filter(it => it.title.toLowerCase().includes(term));

  if (items.length === 0) {
    grid.innerHTML = '<div class="empty-state">Nenhum mangá encontrado.</div>';
    return;
  }

  grid.innerHTML = items.map(m => `
    <div class="media-card" onclick="openChaptersModal('${m.id}')">
      <div class="card-cover">
        <img src="${m.coverURL}" loading="lazy">
        <span class="card-badge">${m.chapters.length} caps</span>
      </div>
      <div class="card-details">
        <div class="card-title">${escapeHTML(m.title)}</div>
        <div class="card-subtitle">${m.chapters.length} capítulos</div>
      </div>
    </div>
  `).join('');
}

function openChaptersModal(mangaID) {
  const manga = state.manga.find(m => m.id === mangaID);
  if (!manga) return;

  const modal = document.getElementById('chapters-modal');
  document.getElementById('chapters-title').textContent = manga.title;
  const list = document.getElementById('chapters-list');

  list.innerHTML = manga.chapters.map(ch => `
    <div class="item-row" onclick="openChapter('${ch.id}', 0)">
      <div class="item-row-left">
        <span class="item-row-title">${escapeHTML(ch.title)}</span>
        <span class="item-row-sub">${ch.page_count} páginas</span>
      </div>
      <span style="color: var(--accent);">📖 Ler</span>
    </div>
  `).join('');

  modal.hidden = false;
  document.getElementById('chapters-close').onclick = () => modal.hidden = true;
}

function setupMangaReader() {
  const readerModal = document.getElementById('reader-modal');
  const modeSelect = document.getElementById('reader-mode');
  const closeBtn = document.getElementById('reader-close');
  const prevBtn = document.getElementById('reader-prev-btn');
  const nextBtn = document.getElementById('reader-next-btn');

  closeBtn.onclick = () => {
    readerModal.hidden = true;
    loadOverview();
  };

  modeSelect.onchange = e => {
    state.mangaMode = e.target.value;
    if (state.currentMangaCh) renderMangaPages();
  };

  prevBtn.onclick = () => {
    if (state.currentMangaPage > 0) {
      state.currentMangaPage--;
      renderMangaPages();
    }
  };

  nextBtn.onclick = () => {
    if (state.currentMangaCh && state.currentMangaPage < state.currentMangaCh.page_count - 1) {
      state.currentMangaPage++;
      renderMangaPages();
    }
  };

  window.addEventListener('keydown', e => {
    if (readerModal.hidden) return;
    if (e.key === 'ArrowRight' || e.key === 'd') nextBtn.click();
    if (e.key === 'ArrowLeft' || e.key === 'a') prevBtn.click();
    if (e.key === 'Escape') closeBtn.click();
  });
}

function openChapter(chID, startPage = 0) {
  document.getElementById('chapters-modal').hidden = true;

  let ch = null;
  for (const m of state.manga) {
    ch = m.chapters.find(c => c.id === chID);
    if (ch) break;
  }
  if (!ch) return;

  state.currentMangaCh = ch;
  state.currentMangaPage = startPage;
  document.getElementById('reader-chapter-title').textContent = ch.title;
  document.getElementById('reader-modal').hidden = false;

  renderMangaPages();
}

function renderMangaPages() {
  const canvas = document.getElementById('reader-canvas');
  const pag = document.getElementById('reader-pagination');
  const ind = document.getElementById('reader-page-indicator');
  const ch = state.currentMangaCh;

  canvas.innerHTML = '';

  if (state.mangaMode === 'webtoon') {
    canvas.classList.remove('single-mode');
    pag.hidden = true;

    for (let i = 0; i < ch.page_count; i++) {
      const img = document.createElement('img');
      img.src = `/api/manga/page?ch=${ch.id}&p=${i}`;
      img.loading = i > 3 ? 'lazy' : 'eager';
      canvas.appendChild(img);
    }
    saveProgress(ch.id, 1, ch.page_count, false);
  } else {
    canvas.classList.add('single-mode');
    pag.hidden = false;
    ind.textContent = `${state.currentMangaPage + 1} / ${ch.page_count}`;

    const img = document.createElement('img');
    img.src = `/api/manga/page?ch=${ch.id}&p=${state.currentMangaPage}`;
    canvas.appendChild(img);

    saveProgress(ch.id, state.currentMangaPage + 1, ch.page_count, state.currentMangaPage >= ch.page_count - 1);
  }
}

// ----------------------------------------------------
// 5. Músicas & Sticky Player
// ----------------------------------------------------
async function loadMusic() {
  try {
    const res = await fetch('/api/music');
    state.music = await res.json();
    renderMusic();
  } catch (err) {
    console.error('Erro ao carregar músicas:', err);
  }
}

function renderMusic(filter = '') {
  const grid = document.getElementById('music-grid');
  const term = filter.toLowerCase();
  const items = state.music.filter(m => m.album.toLowerCase().includes(term) || m.artist.toLowerCase().includes(term));

  if (items.length === 0) {
    grid.innerHTML = '<div class="empty-state">Nenhum álbum ou faixa encontrada.</div>';
    return;
  }

  grid.innerHTML = items.map((alb, idx) => `
    <div class="media-card" onclick="openAlbumModal(${idx})">
      <div class="card-cover square">
        <img src="/api/music/cover?id=${alb.cover_id}" onerror="this.style.display='none'; this.nextElementSibling.style.display='flex';" loading="lazy">
        <div style="display:none; width:100%; height:100%; align-items:center; justify-content:center; font-size:2.5rem;">🎵</div>
      </div>
      <div class="card-details">
        <div class="card-title">${escapeHTML(alb.album)}</div>
        <div class="card-subtitle">${escapeHTML(alb.artist)} &bull; ${alb.tracks.length} faixas</div>
      </div>
    </div>
  `).join('');
}

function openAlbumModal(albIdx) {
  const alb = state.music[albIdx];
  if (!alb) return;

  const modal = document.getElementById('album-modal');
  document.getElementById('album-title').textContent = `${alb.artist} - ${alb.album}`;
  const list = document.getElementById('album-tracks-list');

  list.innerHTML = alb.tracks.map((t, tIdx) => `
    <div class="item-row" onclick="playAlbumTrack(${albIdx}, ${tIdx})">
      <div class="item-row-left">
        <span class="item-row-title">${t.track_number ? t.track_number + '. ' : ''}${escapeHTML(t.title)}</span>
        <span class="item-row-sub">${escapeHTML(t.artist)}</span>
      </div>
      <span style="color: var(--accent);">▶ Tocar</span>
    </div>
  `).join('');

  modal.hidden = false;
  document.getElementById('album-close').onclick = () => modal.hidden = true;
}

function setupAudioPlayer() {
  const audio = state.audioEl;
  const playBtn = document.getElementById('player-play');
  const prevBtn = document.getElementById('player-prev');
  const nextBtn = document.getElementById('player-next');
  const seek = document.getElementById('player-seek');
  const closeBtn = document.getElementById('player-close');

  playBtn.onclick = () => {
    if (audio.paused) audio.play();
    else audio.pause();
  };

  audio.onplay = () => playBtn.textContent = '⏸';
  audio.onpause = () => playBtn.textContent = '▶';

  prevBtn.onclick = () => {
    if (state.currentTrackIdx > 0) {
      playTrack(state.currentTrackIdx - 1);
    }
  };

  nextBtn.onclick = () => {
    if (state.currentTrackIdx < state.playlist.length - 1) {
      playTrack(state.currentTrackIdx + 1);
    }
  };

  audio.onended = () => nextBtn.click();

  audio.ontimeupdate = () => {
    if (audio.duration) {
      const cur = formatTime(audio.currentTime);
      const tot = formatTime(audio.duration);
      document.getElementById('player-time-cur').textContent = cur;
      document.getElementById('player-time-tot').textContent = tot;
      seek.value = (audio.currentTime / audio.duration) * 100;
    }
  };

  seek.oninput = () => {
    if (audio.duration) {
      audio.currentTime = (seek.value / 100) * audio.duration;
    }
  };

  closeBtn.onclick = () => {
    audio.pause();
    document.getElementById('sticky-player').hidden = true;
  };
}

function playAlbumTrack(albIdx, trackIdx) {
  document.getElementById('album-modal').hidden = true;
  const alb = state.music[albIdx];
  state.playlist = alb.tracks;
  playTrack(trackIdx);
}

function playTrack(idx) {
  state.currentTrackIdx = idx;
  const t = state.playlist[idx];
  if (!t) return;

  const player = document.getElementById('sticky-player');
  player.hidden = false;

  document.getElementById('player-title').textContent = t.title;
  document.getElementById('player-artist').textContent = `${t.artist} &bull; ${t.album}`;

  const thumbBox = document.getElementById('player-thumb');
  thumbBox.innerHTML = `<img src="/api/music/cover?id=${t.id}" onerror="this.parentElement.innerHTML='🎵'">`;

  state.audioEl.src = `/api/music/stream?id=${t.id}`;
  state.audioEl.load();
  state.audioEl.play().catch(() => {});
}

// ----------------------------------------------------
// Helpers
// ----------------------------------------------------
function formatTime(secs) {
  const m = Math.floor(secs / 60);
  const s = Math.floor(secs % 60);
  return `${m}:${s < 10 ? '0' : ''}${s}`;
}

function escapeHTML(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
