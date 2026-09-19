# eajdias-cast 🎬📖🎵

> Servidor de mídia tudo-em-um (Cinema, Mangás e Músicas) minimalista, rápido e de consumo irrisório de hardware (< 50MB RAM). Projetado para substituir **Jellyfin + Suwayomi + Navidrome** em um único container Docker.

---

## ⚡ Por que o eajdias-cast?

- **Container Único**: Elimina a necessidade de rodar 3 containers separados (.NET, Java/JVM e Node/Go).
- **Consumo Mínimo de Hardware**: Consome entre **25MB a 50MB de RAM** e **0% de CPU em repouso**. Perfeito para VPSs modestas (1GB de RAM).
- **Go Puro + Vanilla JS**: Sem frameworks pesados, sem NodeJS no backend, binário compilado estático com assets embutidos (`embed.FS`).
- **Direct Play Streaming**: Não queima CPU com transcodificação forçada; o navegador do celular ou PC faz o streaming direto com suporte nativo a HTTP Range (`206 Partial Content`).
- **Leitor Nativo de Mangá**: Lê arquivos `.cbz`, `.zip` e diretórios de imagens sem descompactar no disco, suportando modo Webtoon cascata ou página por página.
- **Player de Áudio Sticky Global**: O áudio continua tocando em segundo plano enquanto você navega pelos catálogos ou lê mangás.
- **Suporte a Legendas e Progresso**: Memoriza onde você parou no filme, série ou mangá.

---

## 📂 Estrutura de Pastas Recomendada

```
/seus-dados/
├── filmes/
│   │   └── Matrix (1999)/
│   │       ├── Matrix.mp4
│   │       └── Matrix.srt       # Legenda opcional
├── series/
│       └── Breaking Bad/
│           ├── Temporada 01/
│           │   ├── S01E01.mkv
│           │   └── S01E01.srt
│           └── Temporada 02/
│               └── S02E01.mkv
├── manga/
│   ├── One Piece/
│   │   ├── Capitulo 01.cbz      # Arquivos .cbz ou .zip
│   │   └── Capitulo 02.cbz
│   └── Berserk/
│       └── Capitulo 01/          # Ou pastas de imagens
│           ├── 01.jpg
│           └── 02.jpg
├── musica/
│   └── Daft Punk/
│       └── Discovery/
│           ├── 01 - One More Time.mp3
│           ├── 02 - Aerodynamic.mp3
│           └── cover.jpg        # Opcional (lê de dentro da tag ID3)
└── cache/                       # Miniaturas geradas e banco SQLite
```

---

## 🚀 Como Rodar

### 1. Clonar e configurar o `.env`
```bash
git clone <url-do-repo> eajdias-cast
cd eajdias-cast
cp .env.example .env
# Edite a senha e caminhos dos discos no .env
nano .env
```

### 2. Subir o container
```bash
docker compose up -d --build
```

Acesse no navegador:
`http://seu-ip:8095` (ou a porta configurada no seu `.env`).

---

## 🔧 Variáveis de Ambiente

| Variável | Padrão | Descrição |
| :--- | :--- | :--- |
| `PORT` | `8080` (interno) / `8095` (compose) | Porta do servidor HTTP |
| `PASSWORD` | `cast123` | Senha de login para o painel web |
| `MEDIA_DIR` | `/data/media` | Volume único; detecta `filmes`, `series`, `anime`, `videos`, `manga` e `musica` |
| `CINEMA_DIR` | — | Sobrescreve a descoberta automática de vídeo, se necessário |
| `MANGA_DIR` | — | Sobrescreve o diretório detectado de mangás |
| `MUSIC_DIR` | — | Sobrescreve o diretório detectado de músicas |
| `CACHE_DIR` | `/data/cache` | Onde ficam o SQLite (`cast.db`) e miniaturas |
