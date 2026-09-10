"""Fontes de documentação para o pipeline de sincronização (Contrato B).

Uma "fonte" é quem fornece a documentação a indexar. O contrato de dados é o
do `contrato-b-api-documentacao-v1.md` (raiz do repo): listagem barata
(metadados, sem conteúdo) + busca de documento completo por id. Duas
implementações:

- `FonteDocsLocais`: os `docs/*.md` do repositório apresentados pela MESMA
  interface — é a fonte do seed inicial e do período até a API do cliente
  existir. Assim a carga das docs atuais usa exatamente o mesmo pipeline
  (mesmo chunking, mesmas travas) que a fonte de produção usará.
- `FonteAPIDocumentacao`: a API do cliente (AzapDocs), seguindo o Contrato B
  à risca. Qualquer resposta fora do contrato (status != 200, item sem campo
  obrigatório) vira `ErroDeFonte` — o sync aborta e o índice do dia anterior
  continua servindo (§4.2: nunca aceitar listagem possivelmente parcial).
  `diagnosticar()` (`GET /me`) é o preflight: chave, escopos e espaços.

O sync (`src.rag.sync`) só conhece o protocolo `FonteDocumentacao`; testes
usam fakes.
"""

from __future__ import annotations

import hashlib
import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Optional, Protocol
from urllib.parse import quote

logger = logging.getLogger(__name__)


class ErroDeFonte(RuntimeError):
    """Fonte indisponível ou resposta fora do Contrato B — aborta o ciclo."""


@dataclass(frozen=True)
class ItemListagem:
    """Um item do inventário (`GET /docs`) — metadados sem conteúdo."""

    id: str
    titulo: str
    categoria: str
    updated_at: str
    deleted: bool = False


@dataclass(frozen=True)
class DocumentoFonte:
    """Um documento completo (`GET /docs/{id}`)."""

    id: str
    titulo: str
    categoria: str
    conteudo: str


@dataclass(frozen=True)
class RegistroDoc:
    """O que o índice lembra de um documento (tabela de controle)."""

    hash_listagem: str
    deleted: bool


class FonteDocumentacao(Protocol):
    def listar(self) -> list[ItemListagem]: ...

    def obter(self, doc_id: str) -> DocumentoFonte: ...


class FonteDocsLocais:
    """Docs Markdown locais (`docs/*.md`) atrás da interface do Contrato B.

    - `id` = nome do arquivo (estável) e `titulo` = nome do arquivo — preserva
      o rótulo de citação (`source`) que o índice Chroma atual usa.
    - `updated_at` = fingerprint sha256 do conteúdo: determinístico e imune a
      mtime trocado por checkout de git (mtime reprocessaria a base inteira a
      cada deploy à toa). Como o arquivo é local, ler para fingerprint é grátis.
    - Arquivo removido do diretório apenas SOME da listagem (não há tombstone
      em filesystem); a remoção do índice exige a flag `--remover-ausentes`
      do sync — deleção nunca é implícita.
    """

    def __init__(self, docs_dir: Path, categoria: str = "Documentação Azapfy"):
        self._docs_dir = Path(docs_dir)
        self._categoria = categoria

    def _arquivos(self) -> list[Path]:
        if not self._docs_dir.is_dir():
            raise ErroDeFonte(f"diretório de docs não encontrado: {self._docs_dir}")
        return sorted(self._docs_dir.glob("*.md"))

    def listar(self) -> list[ItemListagem]:
        itens: list[ItemListagem] = []
        for md in self._arquivos():
            texto = md.read_text(encoding="utf-8").strip()
            if not texto:
                logger.warning("fonte_local pulando %s (arquivo vazio)", md.name)
                continue
            fingerprint = hashlib.sha256(texto.encode("utf-8")).hexdigest()[:16]
            itens.append(
                ItemListagem(
                    id=md.name,
                    titulo=md.name,
                    categoria=self._categoria,
                    updated_at=fingerprint,
                )
            )
        return itens

    def obter(self, doc_id: str) -> DocumentoFonte:
        caminho = self._docs_dir / doc_id
        if caminho.parent != self._docs_dir or not caminho.is_file():
            raise ErroDeFonte(f"documento não encontrado na fonte local: {doc_id}")
        return DocumentoFonte(
            id=doc_id,
            titulo=doc_id,
            categoria=self._categoria,
            conteudo=caminho.read_text(encoding="utf-8"),
        )


def _campo(raw: dict[str, Any], nome: str) -> Any:
    """Campo obrigatório de um item da API; ausência = resposta fora do contrato."""
    if nome not in raw or raw[nome] is None:
        raise ErroDeFonte(f"item da API sem campo obrigatório '{nome}': {raw.get('id')!r}")
    return raw[nome]


class DocumentoRemovido(ErroDeFonte):
    """`GET {base}/{id}` respondeu 410 Gone: o documento virou tombstone entre a
    listagem e a leitura. A remoção acontece no PRÓXIMO ciclo, pela listagem —
    nunca por este erro (deleção só por tombstone)."""


@dataclass(frozen=True)
class EspacoAPI:
    """Um espaço do AzapDocs visto pela chave (`GET {base}/me`)."""

    id: str
    nome: str
    escopos: tuple[str, ...]
    ativo: bool
    api_habilitada: bool
    arquivado: bool
    dono_tem_acesso: bool

    @property
    def motivo_bloqueio(self) -> str:
        """Primeira condição que impede o sync neste espaço ('' = utilizável)."""
        if not self.ativo:
            return "espaço inativo"
        if self.arquivado:
            return "espaço arquivado"
        if not self.api_habilitada:
            return "API desabilitada no espaço"
        if not self.dono_tem_acesso:
            return "dono da chave sem acesso ao espaço"
        faltam = ESCOPOS_NECESSARIOS - set(self.escopos)
        if faltam:
            return "escopos ausentes no espaço: " + ", ".join(sorted(faltam))
        return ""

    @property
    def utilizavel(self) -> bool:
        return self.motivo_bloqueio == ""


ESCOPOS_NECESSARIOS = frozenset({"DOCS_LIST", "DOCS_READ"})


@dataclass(frozen=True)
class DiagnosticoAPI:
    """Resposta de `GET {base}/me`: o que a chave alcança."""

    usuario: str
    escopos_chave: tuple[str, ...]
    espacos: tuple[EspacoAPI, ...]
    contrato_versao: str
    retencao_tombstone_dias: Optional[int]

    def problemas(self) -> list[str]:
        """Motivos pelos quais o sync NÃO deve rodar ([] = pronto)."""
        out: list[str] = []
        faltam = ESCOPOS_NECESSARIOS - set(self.escopos_chave)
        if faltam:
            out.append("chave sem os escopos: " + ", ".join(sorted(faltam)))
        if not self.espacos:
            out.append("a chave não alcança nenhum espaço")
        elif not any(e.utilizavel for e in self.espacos):
            out.extend(f"{e.nome}: {e.motivo_bloqueio}" for e in self.espacos)
        return out

    @property
    def pronto(self) -> bool:
        return not self.problemas()


class FonteAPIDocumentacao:
    """API de documentação do cliente — AzapDocs, implementação do Contrato B.

    Formas da API (`https://intranet.azapfy.com.br/api/v1/integrations/docs`):

    - chave `azk_…` no header `X-API-Key` (a API também aceita
      `Authorization: Bearer`, a forma escrita no contrato);
    - `GET {base}`      → inventário paginado (`limit` 1–500, `cursor` opaco);
    - `GET {base}/{id}` → documento em Markdown; `410` = tombstone
      (`DocumentoRemovido`), `404` = fora dos espaços da chave;
    - `GET {base}/me`   → diagnóstico (chave, espaços, escopos) — preflight do
      sync: diz qual das três condições caiu (chave, API do espaço, acesso do
      dono) antes de tocar no índice.

    `base_url` é a URL da COLEÇÃO, exatamente como na documentação da API.
    `transport` é injetável para testes (httpx.MockTransport) — sem rede.
    """

    def __init__(
        self,
        base_url: str,
        api_key: str = "",
        timeout: float = 30.0,
        limite_pagina: int = 500,
        transport: Optional[Any] = None,
    ):
        import httpx

        self._base = base_url.rstrip("/")
        self._limite_pagina = max(1, min(int(limite_pagina), 500))
        headers = {"X-API-Key": api_key} if api_key else {}
        self._client = httpx.Client(headers=headers, timeout=timeout, transport=transport)

    def _request(self, url: str, params: Optional[dict[str, Any]] = None) -> Any:
        try:
            return self._client.get(url, params=params)
        except Exception as exc:  # noqa: BLE001 — rede/timeout viram ErroDeFonte
            raise ErroDeFonte(f"falha de rede na fonte ({url}): {exc}") from exc

    @staticmethod
    def _json(resp: Any, url: str) -> dict[str, Any]:
        try:
            return resp.json()
        except Exception as exc:  # noqa: BLE001
            raise ErroDeFonte(f"resposta não-JSON da fonte em {url}") from exc

    def _get(self, url: str, params: Optional[dict[str, Any]] = None) -> dict[str, Any]:
        resp = self._request(url, params)
        if resp.status_code != 200:
            # §4.2: erro explícito (401/5xx etc.) aborta o ciclo; nunca
            # "aproveitar" uma resposta que não seja 200 completo.
            raise ErroDeFonte(f"fonte respondeu {resp.status_code} em {url}")
        return self._json(resp, url)

    def diagnosticar(self) -> DiagnosticoAPI:
        """`GET {base}/me` — o que esta chave alcança (preflight do sync)."""
        data = self._get(f"{self._base}/me")
        espacos = tuple(
            EspacoAPI(
                id=str(raw.get("id", "")),
                nome=str(raw.get("nome", "")),
                escopos=tuple(raw.get("escopos") or ()),
                ativo=bool(raw.get("ativo", False)),
                api_habilitada=bool(raw.get("apiHabilitada", False)),
                arquivado=bool(raw.get("espacoArquivado", False)),
                dono_tem_acesso=bool(raw.get("donoTemAcesso", False)),
            )
            for raw in data.get("espacos") or []
        )
        contrato = data.get("contrato") or {}
        retencao = contrato.get("retencaoTombstoneDias")
        return DiagnosticoAPI(
            usuario=str((data.get("user") or {}).get("name", "")),
            escopos_chave=tuple((data.get("apiKey") or {}).get("scopes") or ()),
            espacos=espacos,
            contrato_versao=str(contrato.get("versao", "")),
            retencao_tombstone_dias=int(retencao) if retencao is not None else None,
        )

    def listar(self) -> list[ItemListagem]:
        itens: list[ItemListagem] = []
        cursor: Optional[str] = None
        while True:
            params: dict[str, Any] = {"limit": self._limite_pagina}
            if cursor:
                params["cursor"] = cursor
            data = self._get(self._base, params)
            for raw in data.get("itens", []):
                itens.append(
                    ItemListagem(
                        id=str(_campo(raw, "id")),
                        titulo=str(_campo(raw, "titulo")),
                        categoria=str(_campo(raw, "categoria")),
                        updated_at=str(_campo(raw, "updated_at")),
                        deleted=bool(raw.get("deleted", False)),
                    )
                )
            cursor = data.get("next_cursor")
            if not cursor:
                return itens

    def obter(self, doc_id: str) -> DocumentoFonte:
        url = f"{self._base}/{quote(doc_id, safe='')}"
        resp = self._request(url)
        if resp.status_code == 410:
            raise DocumentoRemovido(
                f"documento {doc_id} já é tombstone na fonte (410); a remoção "
                "vem pela listagem do próximo ciclo"
            )
        if resp.status_code != 200:
            raise ErroDeFonte(f"fonte respondeu {resp.status_code} em {url}")
        data = self._json(resp, url)
        return DocumentoFonte(
            id=str(_campo(data, "id")),
            titulo=str(_campo(data, "titulo")),
            categoria=str(_campo(data, "categoria")),
            conteudo=str(_campo(data, "conteudo")),
        )
