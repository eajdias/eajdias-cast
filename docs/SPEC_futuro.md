# SPEC: eajdias-cast com peer opcional (standalone-first)

> **Status**: Planejamento
> **Data**: 2026-09-22 (revisão 4: correções da revisão — standalone garantido, peer explícito)
> **Visão**: o projeto segue 100% auto-contido por padrão (roda em
> qualquer lugar: homelab direto, VPS boa, VPS modesta). Se houver um outro
> "PC" conectado (outra instância eajdias-cast acessível na rede), o container
> usa os recursos dele conforme necessário — compute (ffmpeg/ffprobe) e/ou
> armazenamento externo via HTTP para poupar disco local.

---

## 1. Visão geral

### Princípio central

**Standalone é o padrão; peer é extensão explícita e aditiva.** Nenhuma
feature nova pode quebrar ou exigir rede. O modo peer só ativa com
`PEER_ENABLE != "off"` (padrão: `off`); com ele em `off`, nenhum código de
peer executa — nem health check, nem job de sync — e o binário comporta-se
exatamente como hoje, mesmo que outras envs de peer estejam definidas.

### Arquitetura

```
 Modo standalone (default, zero config nova)
┌──────────────────────────────────┐
│  eajdias-cast (qualquer host)    │
│  ├── mídia local (disco)         │
│  ├── ffmpeg/ffprobe locais       │
│  └── SQLite + thumbs locais      │
└──────────────────────────────────┘

 Modo peer (opcional, mesmo binário nos dois lados)
┌──────────────────────────────────┐         ┌──────────────────────────────────┐
│  Instância A (client)            │         │  Instância B (server)            │
│  eajdias-cast (:8095)            │   rede   │  eajdias-cast (:8095)            │
│  ├── mídia local leve            │◄───────►│  ├── acervo completo local       │
│  ├── mídia remota via proxy      │  HTTP +  │  ├── ffmpeg/ffprobe (+VAAPI)     │
│  ├── cache parcial (p/ economia) │  token   │  └── SQLite + thumbs locais      │
│  └── catálogo = local + remoto   │         │  (UI local segue funcionando!)   │
└──────────────────────────────────┘         └──────────────────────────────────┘
```

Pontos-chave:
- **Mesmo binário, mesma imagem Docker, nos dois lados.** Sem codebase `./worker/`
  separada para manter. O "worker" vira um conjunto de endpoints dentro do
  próprio eajdias-cast, habilitado quando `PEER_ENABLE` inclui `server`/`mesh`.
- **A UI do server continua funcionando normalmente.** Ele não vira "slave":
  segue standalone para uso local E serve o peer em paralelo.
- **Sem NFS obrigatório.** Bytes trafegam via HTTP com Range (proxy). NFS vira
  alternativa avançada (apêndice A), não pré-requisito.

### Modos de operação

| `PEER_ENABLE` | Significado |
|---|---|
| `off` (padrão) | **Standalone**: zero código de peer executa, mesmo com outras envs definidas |
| `server` | Serve UI local + endpoints `/api/peer/*` (autenticados por `PEER_TOKEN`) |
| `client` | Consome o peer (`PEER_URL` + `PEER_REMOTE_TOKEN`); não serve nada |
| `mesh` | Consome o peer E serve os próprios dados (requer `PEER_URL`, `PEER_REMOTE_TOKEN` e `PEER_TOKEN`) |

---

## 2. Peer API (no mesmo binário)

Habilitada quando `PEER_ENABLE` é `server` ou `mesh`. Auth
**machine-to-machine** via header `X-Peer-Token` (compara com `PEER_TOKEN` do
server, em tempo constante). O client autentica-se com `PEER_REMOTE_TOKEN`
(token distinto por direção; nunca reutilizar o mesmo nos dois lados).
**Não usa cookie de sessão** (o peer não faz login na UI do outro lado).
`GET /api/health` (humano/monitor) é público e nunca exige sessão;
`GET /api/peer/health` exige `X-Peer-Token`.

### Endpoints

| Método | Path | Auth | Descrição |
|---|---|---|---|
| `GET` | `/api/health` | nenhuma (público) | Status local: `{status, version, peer, media:{...}}`. Base para Kuma; criado na task 1 |
| `GET` | `/api/peer/health` | token | `{status, version, counts:{music,manga,video}}` |
| `GET` | `/api/peer/catalog` | token | DTOs `{id,title,size,mtime,source}` por tipo (cinema/mangá/música); **sem `path`**. Páginas de mangá e "latest N" são projeções deste catálogo + `/api/peer/file` (sem endpoints dedicados no MVP) |
| `GET` | `/api/peer/file?id=` | token | Bytes do arquivo com suporte a `Range` (`http.ServeContent`). Serve vídeo, música, CBZ |
| `GET` | `/api/peer/thumb?id=&width=` | token | JPEG gerado com ffmpeg local do peer (VAAPI se disponível) |
| `GET` | `/api/peer/probe?id=` | token | `{audio, subs}` via ffprobe local do peer |
| `GET` | `/api/peer/remux?id=&audio=` | token | MP4 remuxado (áudio selecionado) |
| `GET` | `/api/peer/subtitle?id=&stream=` | token | Legenda em VTT |

### Regras de segurança da API

- **IDs por conteúdo, nunca paths.** `id = hash(relative_path + size + mtime)`
  com `relative_path` relativo a `MEDIA_DIR` (os IDs atuais são
  `hashString(path)` absoluto e mudam entre hosts — ver `main.go:52`,
  `manga.go:176`, `cinema.go:258`, `music.go:90`). O client referencia itens
  por `id`; o server resolve `id → path local` internamente. Nenhum endpoint
  aceita path do wire (previne path traversal). O JSON do catálogo peer usa
  DTOs sem `path` (`VideoMedia`/`MangaChapter`/`Track` serializam `Path` hoje
  e precisam de DTO dedicado com `source`).
- **Rate limiting** com limitador stdlib (sem nova dependência; `golang.org/x/time/rate` só com justificativa).
- **Concorrência de ffmpeg** reutiliza `FF_SLOTS`/`ffSlots()` existente (sem `MAX_CONCURRENT` paralelo).
- Bind segue o padrão do compose local (`${PORT:-8095}:8080`, sem `user:` override — a imagem já roda como uid 20000); restrição de rede via UFW (ver §6).

---

## 3. Comportamento do peer client

Ativo quando `PEER_ENABLE` é `client` ou `mesh` (requer `PEER_URL` +
`PEER_REMOTE_TOKEN`). Com `PEER_ENABLE=off`, esta seção inteira não existe em
runtime.

### 3.1 Merge de catálogo

- No `rescanIfNeeded`: scan local (inalterado) + `GET /api/peer/catalog` (se peer
  online). Itens remotos entram nas mesmas listas com campo `source`
  (novo campo nos DTOs + tratamento no frontend; progresso continua
  por-instância, chaveado no ID namespaced — não há sync de progresso entre lados).
- **Namespace de IDs**: `peer:<peerName>:<contentID>` onde `contentID` é o ID
  por conteúdo do §2 (relativo + size + mtime). Evita colisão com IDs locais
  e mantém o ID estável entre rescans/hosts.
- **Catálogo stale**: se o peer ficar offline, manter o último catálogo remoto
  em memória marcado como `stale` — a UI mostra os itens esmaecidos com selo
  "indisponível" em vez de fazê-los sumir (menos confuso). Música/mangá local
  seguem 100% funcionais.

### 3.2 Regra de localidade (onde processar)

> **Processa onde o arquivo mora.** Se o item é local, usa ffmpeg local como
> hoje. Se é remoto (`source == peer`), delega ao peer. Nunca o contrário.

| Operação | Item local | Item remoto |
|---|---|---|
| Stream vídeo/música | handler atual (`ServeContent` no disco) | proxy: repassa `Range` ao `/api/peer/file`, faz streaming da resposta sem bufferizar o arquivo inteiro |
| Página de mangá | `serveMangaPage` atual | proxy a `/api/peer/file` (CBZ) com extração da página no client OU leitura local (modo cache, ver §4) |
| Thumb | `handleThumb` atual | `GET /api/peer/thumb`, cacheia o JPEG localmente (chave = ID namespaced) |
| Probe | `probeTracks` atual | `GET /api/peer/probe` |
| Remux áudio | `serveSelectedAudio` atual | `GET /api/peer/remux`, faz streaming da resposta |
| Legenda | handler atual | `GET /api/peer/subtitle` |

### 3.3 Resiliência

- **Health check**: `GET /api/peer/health` a cada 30s. 3 falhas → peer offline.
  1 sucesso → online de novo.
- **Circuit breaker**: 3 erros consecutivos no caminho de request → circuito
  aberto por 5 min (requests de itens remotos retornam 503 imediato, sem tentar
  o peer). Após 5 min → half-open (1 request teste). Se OK → fecha.
- **Timeouts**: `PEER_TIMEOUT` (padrão 10s) em chamadas de metadados; streaming
  usa timeout maior (sem deadline agressiva — vídeo de 2h não pode cair por
  timeout de 10s).

---

## 4. Estratégias de armazenamento remoto (para poupar disco)

O client escolhe por tipo de mídia como consome o acervo do peer:

| Modo | Como funciona | Disco local | Offline | Quando usar |
|---|---|---|---|---|
| `proxy` | Tudo via HTTP sob demanda, zero cópia | ~0 | Nada remoto funciona | **Vídeo (padrão)**: arquivos grandes, streaming sob demanda basta |
| `cache` | Mantém os últimos N itens localmente, sincroniza periódico | ~N × tamanho médio | Últimos N funcionam | **Mangá (padrão, N=5)**: capítulos recentes para leitura rápida + offline |
| `full` | Cópia integral (rsync ou pull inicial) | Tamanho total | Tudo funciona | **Música (padrão)**: 20MB, cabe em qualquer VPS |

Envs:

| Env | Padrão | Descrição |
|---|---|---|
| `PEER_ENABLE` | `off` | `off/server/client/mesh`; gate de todo código peer |
| `PEER_URL` | — | URL base do peer B (usado em `client`/`mesh`) |
| `PEER_TOKEN` | — | Segredo para SERVIR `/api/peer/*` (`server`/`mesh`) |
| `PEER_REMOTE_TOKEN` | — | Segredo para CONSUMIR o peer (`client`/`mesh`) |
| `PEER_VIDEO_MODE` | `proxy` | `proxy` (recomendado) |
| `PEER_MANGA_MODE` | `cache` | `proxy` (zero disco) ou `cache` (últimos N locais) |
| `PEER_MANGA_KEEP` | `5` | Nº de capítulos recentes por série no modo `cache` |
| `PEER_MUSIC_MODE` | `full` | `full` (cópia única) |
| `PEER_SYNC_INTERVAL` | `6h` | Intervalo do job de cache sync do mangá |

### 4.1 Cache sync de mangá (dentro do app, sem script externo)

> **Pré-requisito de mount**: o modo `cache`/`full` escreve em disco local,
> logo o path de destino NÃO pode estar sob mount `:ro` (ver §5). `proxy`
> puro funciona com mídia `:ro`.

Job em Go (`peerCacheSync`, roda a cada `PEER_SYNC_INTERVAL` + sob demanda via
`POST /api/cache/peer-sync`; só existe com `PEER_ENABLE=client|mesh`):
1. `GET /api/peer/catalog` e filtra localmente os últimos N capítulos por série (projeção "latest"; sem endpoint dedicado)
2. Para cada capítulo listado: se o CBZ não existe localmente, baixa via
   `GET /api/peer/file?id=` para o diretório de cache gravável
3. Remove CBZs locais da série que não estão na lista (rodízio)
4. Rescan local detecta as mudanças no próximo ciclo (30s)

Isso **substitui o `manga-sync.sh`** como mecanismo principal: sem ssh, sem
bash, sem problemas com espaços em nomes, sem cron externo. O script rsync
permanece documentado como **bootstrap alternativo** (carga inicial em bulk é
mais rápida via rsync com `--progress` do que via HTTP item a item).

### 4.2 Seed inicial (bulk, opcional — ver Apêndice B para o exemplo concreto)

```bash
# Música (one-shot; origem = acervo da instância B, destino = mídia da A)
rsync -avz --progress <origem>:/media/musica/ <destino>:/data/media/musica/
```

---

## 5. Diretórios (instância client com peer)

```
/data/
├── media/
│   ├── musica/          # PEER_MUSIC_MODE=full (cópia; mount RW)
│   ├── manga/           # PEER_MANGA_MODE=cache (últimos N/série; mount RW)
│   └── video/           # PEER_VIDEO_MODE=proxy → pode nem existir (mount :ro ou ausente)
└── cache/
    ├── cast.db          # SQLite LOCAL (progresso, tags, probe cache) — nunca remoto
    └── thumbs/          # Thumbs locais + thumbs de itens remotos cacheados
```

> **Regra**: SQLite (`cast.db`) sempre em storage LOCAL. Nunca em NFS, nunca
> compartilhado entre instâncias.

Na instância server nada muda: segue com o acervo completo local e a
estrutura atual.

---

## 6. Compose

> Mesma imagem nos dois lados. Nenhum `build: ./worker` separado.

### 6.1 Instância B — peer server (+ UI local inalterada)

```yaml
name: eajdias-cast

services:
  cast:
    build: .
    container_name: eajdias-cast
    ports:
      - "${PORT:-8095}:8080"
    environment:
      PASSWORD: ${PASSWORD}
      MEDIA_DIR: /data/media
      CACHE_DIR: /data/cache
      PEER_ENABLE: server
      PEER_NAME: b
      PEER_TOKEN: ${PEER_TOKEN}      # segredo SÓ para servir; distinto do token de consumo
      FF_SLOTS: ${FF_SLOTS:-}
    devices:
      - /dev/dri:/dev/dri
    group_add:
      - video
    volumes:
      - ${MEDIA_BIND:-./media}:/data/media:ro
      - ${CACHE_BIND:-./data/cache}:/data/cache
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/api/health"]
      interval: 30s
      timeout: 5s
      retries: 3
    restart: unless-stopped
```

UFW (B): liberar a porta do app para o IP do peer A (exemplo no Apêndice B).
Isolamento por IP do peer só é possível com porta dedicada; como UI e peer
API dividem a mesma porta, a regra abaixo libera UI+API para a rede de
gerência e a peer API segue protegida por `X-Peer-Token` (ver §8).

### 6.2 Instância A — peer client (poupa disco)

```yaml
name: eajdias-cast

services:
  cast:
    build: .
    container_name: eajdias-cast
    ports:
      - "${PORT:-8095}:8080"
    environment:
      PASSWORD: ${PASSWORD}
      MEDIA_DIR: /data/media
      CACHE_DIR: /data/cache
      PEER_ENABLE: client
      PEER_NAME: a
      PEER_URL: http://peer-b:8095     # hostname/IP do peer B (exemplo no Apêndice B)
      PEER_REMOTE_TOKEN: ${PEER_REMOTE_TOKEN}  # token que B provisionou para A
      PEER_TIMEOUT: "10"
      PEER_VIDEO_MODE: proxy
      PEER_MANGA_MODE: cache
      PEER_MANGA_KEEP: "5"
      PEER_MUSIC_MODE: full
      PEER_SYNC_INTERVAL: 6h
      FF_SLOTS: "1"
    volumes:
      # ATENÇÃO: cache/full escrevem em disco — estes paths precisam ser RW.
      # Proxy puro aceita :ro. Não montar /data/media inteiro como :ro quando
      # PEER_MANGA_MODE=cache ou PEER_MUSIC_MODE=full.
      - /data/media/musica:/data/media/musica:rw
      - /data/media/manga:/data/media/manga:rw
      - /data/cache:/data/cache
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/api/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
    restart: unless-stopped
    labels:
      - freshdock.enable=true
      - freshdock.mode=nightly
```

UFW (A): mesma regra — porta do app liberada para a rede de gerência; peer
API (se `mesh` um dia) protegida por token. Ver exemplo concreto no
Apêndice B.

> Portas: `:8095` nos dois lados por padrão (`PEER_URL` aponta para a porta
> real do peer). Se a porta local já estiver em uso, mudar `PORT` no deploy —
> o SPEC não impõe número, só exige que `PEER_URL` reflita o real.

---

## 7. Casos de uso

### 7.1 Standalone (sem peer — comportamento atual, garantido)

1. `PEER_ENABLE=off` (padrão) → nenhum código de peer executa (nem health check).
2. Catálogo = só mídia local. ffmpeg local como hoje.
3. Vale para: homelab direto, VPS com disco/GPU suficientes, testes.

### 7.2 Peer online (A consome B)

1. Usuário abre a UI da instância A
2. Catálogo = mídia local + cache + itens do peer (via proxy)
3. Clica num vídeo → A repassa `Range` ao peer → B lê do disco local dele
   e responde → A faz streaming ao browser (sem salvar o arquivo)
4. Thumb de vídeo → B gera com VAAPI → A cacheia o JPEG
5. UI da instância B continua 100% funcional e independente

### 7.3 Peer offline (B desligado)

1. Mídia local + cache funcionam normalmente
2. Itens remotos aparecem esmaecidos com selo "indisponível" (catálogo stale)
3. Thumbs já cacheados continuam visíveis
4. Ao ligar B: health check detecta em ≤30s, catálogo remoto volta sozinho

### 7.4 Novo capítulo de mangá em B

1. Capítulo chega em B (qualquer método atual)
2. Job `peerCacheSync` em A (a cada `PEER_SYNC_INTERVAL` ou via `POST /api/cache/peer-sync`)
   puxa o novo + remove o mais antigo (rodízio, mantém N)
3. Rescan local (30s) atualiza o catálogo

---

## 8. Segurança

| Camada | Medida |
|---|---|
| Peer API | `X-Peer-Token` vs `PEER_TOKEN` do server, comparação em tempo constante, sem sessão; tokens distintos por direção |
| Rede | Mesma porta para UI + peer API: UFW libera a porta para a rede de gerência; isolamento por IP exclusivo do peer exigiria porta dedicada (não adotado no MVP) |
| Wire | IDs por conteúdo (relativo + size + mtime), nunca paths absolutos; `id → path` resolvido só no server; catálogo peer usa DTOs sem `path` |
| Dados | Mounts RW só onde `cache`/`full` escrevem; `:ro` só no proxy puro; SQLite sempre local |
| Recursos | Reutiliza `FF_SLOTS` existente; rate limit stdlib na peer API |

---

## 8.1 Observabilidade

| O que monitorar | Como |
|---|---|
| App (cada lado) | Kuma: HTTP `/api/health` público (sem sessão) com `peer` + contadores |
| Peer | Campo `peer` no `/api/health`: `off/server/client/mesh` + `online/offline/stale`, último sucesso |
| Circuit breaker | Log `[peer] circuit open — 503 for remote items` |
| Cache sync | Log + `POST /api/cache/peer-sync` manual; `GET /api/cache` mostra última sync |

**`/api/health`:**
```json
{
  "status": "ok",
  "version": "2.0.0",
  "peer": "online",
  "uptime": 86400,
  "media": { "music": 12, "manga": 1, "video": 45, "remote": 120 }
}
```

---

## 8.2 Backup

- `/data/cache/cast.db` (progresso, tags — por instância, sem sync entre lados)
  → cópia periódica para o outro host como backup frio (exemplo no Apêndice B).
- Thumbs são regeneráveis; `cast.db` é o que vale backup.

---

## 9. Pré-requisitos (sem NFS no caminho principal)

### Nos dois lados
- [ ] Mesma versão do binário/imagem (`/api/health` e `/api/peer/health` retornam `version`; client loga warning em mismatch)
- [ ] Segredos distintos por direção (`PEER_TOKEN` em quem serve, `PEER_REMOTE_TOKEN` em quem consome; gerar com `openssl rand -hex 32`)
- [ ] Resolução de nome/IP do peer B alcançável a partir de A (exemplo no Apêndice B)
- [ ] UFW liberando a porta do app para a rede de gerência (ver §6; peer API protegida por token)
- [ ] Conectividade direta entre os lados confirmada (exemplo no Apêndice B)

### Na instância client (A)
- [ ] Paths de mídia com escrita onde `cache`/`full` atuam; `/data/cache` em disco local
- [ ] Seed inicial quando `PEER_MUSIC_MODE=full` (cópia one-shot ou pull via peer)

Nada de `nfs-kernel-server`, `/etc/exports`, `/etc/fstab` no caminho principal.

---

## 10. Ordem de implementação

| # | Tarefa | Dependências | Verificação |
|---|---|---|---|
| 0 | Versionar peer API (`version` em `/api/health` + `/api/peer/health` + check de mismatch) | Nenhuma | Client loga warning com server de versão diferente |
| 1 | `/api/health` público + endpoints peer no mesmo binário (`/peer/health`, `/catalog` em DTO sem `path`, `/file` com Range, `/thumb`, `/probe`, `/remux`, `/subtitle`) + auth `X-Peer-Token` vs `PEER_TOKEN` | Nenhuma | `curl` sem sessão em `/api/health` → 200; `curl -H X-Peer-Token` → dados; sem header no peer → 401; IDs por conteúdo estáveis entre hosts |
| 2 | Envs `PEER_ENABLE/PEER_NAME/PEER_URL/PEER_TOKEN/PEER_REMOTE_TOKEN/PEER_TIMEOUT` + health poll (30s, 3 strikes), tudo gated por `PEER_ENABLE` | Task 1 | Com `PEER_ENABLE=off`, zero comportamento novo (teste standalone) |
| 3 | Merge de catálogo com IDs `peer:<nome>:<contentID>` + `source` + flag stale + UI (selo "indisponível", itens esmaecidos) | Task 1, 2 | UI lista local + remoto; peer off → remoto esmaecido |
| 4 | Proxy de streaming (`cinema/music stream`, página de mangá via CBZ do `/peer/file`) com repasse de `Range` | Task 3 | Vídeo remoto toca no browser; seek funciona (206) |
| 5 | Delegação de compute (`thumb/probe/remux/subtitle`) por localidade (reusa `FF_SLOTS`) + cache local de thumbs remotos | Task 3 | Thumb remoto aparece; 2º acesso não bate no peer (cache) |
| 6 | Circuit breaker no client do peer (3 erros → open 5min → half-open) | Task 2 | Peer instável → 503 rápido em vez de timeout por request |
| 7 | Job `peerCacheSync` (mangá `cache`, `PEER_MANGA_KEEP`, `PEER_SYNC_INTERVAL`) + `POST /api/cache/peer-sync` manual | Task 1, 3 | Novo cap em B → após sync, A tem N últimos e remove o excedente |
| 8 | Testes: atualizar `catalog_test.go` + cobrir peer (401 sem token, DTO sem `path`, estabilidade de ID entre hosts, `206` no proxy, stale, rodízio do cache, `PEER_ENABLE=off` sem efeito) | Tasks 1-5, 7 | `go test ./...` verde incluindo novos casos |
| 9 | Seed inicial + teste ponta a ponta peer online | Tasks 1-8 | Música/mangá/vídeo funcionam via peer |
| 10 | Teste standalone (`PEER_ENABLE=off`) + teste degradação (peer off) | Task 9 | Standalone idêntico ao atual; peer off → local OK, remoto "indisponível" |
| 11 | Monitores Kuma + backup `cast.db` | Task 9 | Status visível; restore testado |
| 12 | Atualizar docs do homelab-privado | Task 9 | Docs refletem arquitetura peer-opcional |

---

## 11. Fora do escopo (futuro)

- Múltiplos peers (lista `PEER_URLS`; merge de N catálogos)
- Sync de música via peer pull (hoje: cópia one-shot; `PEER_MUSIC_MODE=full` já prevê o modo)
- Transcoding adaptativo no peer (hoje: direct play + remux de áudio)
- Rotação automática de tokens (`PEER_TOKEN`/`PEER_REMOTE_TOKEN`)
- Sync de progresso/tags entre instâncias (hoje: por instância)

---

## Apêndice A — NFS como alternativa avançada (opcional)

Se um dia o proxy HTTP virar gargalo (múltiplos streams 4K simultâneos), NFS
sobre Tailscale continua possível **como otimização, sem mudar o app**:
export `ro` no PC, mount `soft,nfsvers=4` na VPS, bind mount no compose. Nesse
caso os arquivos "remotos" viram "locais" para o app (lê do mount) e só o
compute (ffmpeg) continua delegado ao peer. Ver histórico do SPEC (revisão 2)
para os mount options validados (`timeo=30,retrans=5,rsize=1048576`,
`x-systemd.automount`, GSO bug em kernels 7.0–7.1.4). Não é o caminho padrão.

---

## Apêndice B — Exemplo concreto (VPS modesta + PC do homelab)

> Informativo, não normativo. O corpo do SPEC usa A/B; aqui vão os valores
> reais do deploy atual.

- A = VPS-1 (`100.124.142.46`), `PEER_ENABLE=client`, `PEER_NAME=a`,
  `PEER_URL=http://homelab:8095` (AdGuard rewrite `homelab` → IP Tailscale do PC)
- B = PC (`eajdias-old`), `PEER_ENABLE=server`, `PEER_NAME=b`, acervo em
  `/mnt/homelab/media` (`MEDIA_BIND=/mnt/homelab/media`), ambos na porta `:8095`
- DNS: `homelab` → PC; `vps.homelab` → VPS-1
- UFW exemplo — B: `sudo ufw allow from 100.124.142.46 to any port 8095 proto tcp`
- Música (~20MB, `full`); mangá em cache (últimos 5/série; ex.: Onepunch-Man)
- Seed: `rsync -avz --progress /mnt/homelab/media/musica/ vps-oracle-1:/data/media/musica/`
- Backup frio: `rsync -az vps-oracle-1:/data/cache/cast.db /mnt/homelab/backups/eajdias-cast/`
- Rede: `tailscale ping` para confirmar conexão direta (não via DERP)
