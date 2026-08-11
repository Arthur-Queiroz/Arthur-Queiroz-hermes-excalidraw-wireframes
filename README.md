# Hermes Excalidraw Wireframes

Serviço Go que preserva a aplicação Excalidraw na raiz do domínio e acrescenta URLs móveis para wireframes gerados pelo Hermes.

## Rotas

- `GET /` — frontend Excalidraw oficial.
- `GET /healthz` — healthcheck.
- `POST /api/wireframes` — cria um documento (Bearer token obrigatório).
- `PUT /api/wireframes/{id}` — atualiza o mesmo link (Bearer token obrigatório).
- `GET /w/{id}` — preview responsivo em SVG.
- `GET /w/{id}.excalidraw` — download do documento original.

Os IDs combinam um slug legível com 80 bits aleatórios. Conhecer o link concede acesso de leitura; criação e atualização exigem o token da API.

Cada wireframe é persistido como um único registro JSON substituído atomicamente. Documento, título e preview pertencem sempre à mesma geração; o download `.excalidraw` é servido a partir desse registro. O preview é gerado somente na criação/atualização, nunca durante visitas públicas.

## Limites

- corpo HTTP: 5 MiB;
- 500 elementos e 10.000 pontos;
- SVG derivado: 2 MiB, com buffer limitado durante a geração;
- uma escrita/renderização por vez; excesso recebe HTTP 503 sem formar fila ilimitada;
- registro persistido: aproximadamente 9 MiB, verificado antes da alocação; no máximo duas leituras públicas concorrentes, com excesso respondendo HTTP 503;
- tipos de preview: `rectangle`, `ellipse`, `diamond`, `text`, `label`, `arrowLabel`, `line` e `arrow` sem rotação;
- coordenadas, dimensões, fontes, traços e opacidade são validados antes da renderização.

## Configuração

| Variável | Padrão | Uso |
|---|---|---|
| `WIREFRAME_API_TOKEN` | obrigatório | autenticação Bearer da API |
| `WIREFRAME_ADDRESS` | `:8080` | endereço HTTP |
| `WIREFRAME_DATA_DIR` | `/data` | persistência dos documentos |
| `WIREFRAME_PUBLIC_URL` | `https://excalidraw.devarthur.com.br` | URLs retornadas pela API |
| `EXCALIDRAW_STATIC_DIR` | `/app/excalidraw` | build estático do Excalidraw |

## Criar um wireframe

```bash
curl -X POST https://excalidraw.devarthur.com.br/api/wireframes \
  -H "Authorization: Bearer $WIREFRAME_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data @request.json
```

`request.json`:

```json
{
  "title": "Login mobile",
  "slug": "login-mobile",
  "document": {
    "type": "excalidraw",
    "version": 2,
    "source": "hermes-agent",
    "elements": [],
    "appState": { "viewBackgroundColor": "#ffffff" },
    "files": {}
  }
}
```

A resposta contém `id`, `viewUrl` e `downloadUrl`. Para atualizar, envie `title` e `document` com `PUT /api/wireframes/{id}`.

## Desenvolvimento

```bash
go test ./...
go vet ./...
go build ./...
```

O `Dockerfile` copia o frontend da imagem oficial `excalidraw/excalidraw` e o serve no mesmo processo Go. Isso permite substituir a instância estática atual sem perder a interface da raiz.

## Publicação

O container executa como `65532:65532`. Antes do primeiro release, o diretório persistente deve existir no host com esse proprietário:

```text
/var/lib/vps-apps/excalidraw-wireframes/data
```

O serviço recusa a inicialização quando o diretório não é gravável. O token de produção deve ser fornecido por `fromSecret: api-token`; nunca deve ser incluído na imagem, no manifesto ou em URLs.
