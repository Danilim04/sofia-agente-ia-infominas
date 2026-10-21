"""Fontes de documentação para o pipeline de sincronização (Contrato B).

Uma "fonte" é quem fornece a documentação a indexar. O contrato de dados é o
do Contrato B herdado (listagem barata — metadados, sem conteúdo — + busca de
documento completo por id; tombstones para remoção). Implementações:

- `FonteDocsLocais`: os `agente-ia/docs/base/*.md` apresentados por essa
  interface — é a fonte do seed inicial e do período até a API/portal de
  documentos da Infominas existir (pendência P6). Assim a carga das docs
  atuais usa exatamente o mesmo pipeline (mesmo chunking, mesmas travas) que
  a fonte de produção usará. Os docs atuais são PLACEHOLDER (front-matter
  `placeholder: true`): conteúdo genérico de suporte de TI, sem nenhuma
  afirmação sobre a Infominas.
- `FonteAPIInfominas`: stub configurável (`DOCS_API_*`) da API/portal de
  documentos da Infominas. TODO(P6): URL, autenticação e formato reais ainda
  não foram definidos; o stub assume o formato do Contrato B (hipótese).

Qualquer resposta fora do contrato vira `ErroDeFonte` — o sync aborta e o
índice do dia anterior continua servindo (nunca aceitar listagem
possivelmente parcial). O sync (`src.rag.sync`) só conhece o protocolo
`FonteDocumentacao`; testes usam fakes.
"""

from __future__ import annotations

import hashlib
import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Protocol
from urllib.parse import quote

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

logger = logging.getLogger(__name__)

# agente-ia/docs/base — fonte local padrão (placeholder até a P6).
DIR_DOCS_BASE = Path(__file__).resolve().parents[2] / "docs" / "base"


class ErroDeFonte(RuntimeError):
    """Fonte indisponível ou resposta fora do contrato — aborta o ciclo."""


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


def separar_front_matter(texto: str) -> tuple[dict[str, str], str]:
    """Separa o front-matter `---` (pares `chave: valor`) do corpo Markdown.

    Sem front-matter válido devolve `({}, texto)` intacto. O front-matter é
    metadado de curadoria (ex.: `placeholder: true`) e NÃO entra nos chunks.
    """
    linhas = texto.lstrip("﻿").splitlines()
    if not linhas or linhas[0].strip() != "---":
        return {}, texto
    for i, linha in enumerate(linhas[1:], start=1):
        if linha.strip() == "---":
            meta: dict[str, str] = {}
            for bruta in linhas[1:i]:
                chave, sep, valor = bruta.partition(":")
                if sep and chave.strip():
                    meta[chave.strip().lower()] = valor.strip().strip("\"'")
            return meta, "\n".join(linhas[i + 1 :]).lstrip("\n")
    return {}, texto


class FonteDocumentacao(Protocol):
    def listar(self) -> list[ItemListagem]: ...

    def obter(self, doc_id: str) -> DocumentoFonte: ...


class FonteDocsLocais:
    """Docs Markdown locais (`docs/base/*.md`) atrás da interface do Contrato B.

    - `id` = nome do arquivo (estável); `titulo` = `titulo:` do front-matter
      ou, na falta, o nome do arquivo — é o rótulo de citação (`source`).
    - O front-matter (ex.: `placeholder: true`) é removido do conteúdo; docs
      placeholder geram um aviso no log a cada listagem.
    - `updated_at` = fingerprint sha256 do conteúdo: determinístico e imune a
      mtime trocado por checkout de git (mtime reprocessaria a base inteira a
      cada deploy à toa). Como o arquivo é local, ler para fingerprint é grátis.
    - Arquivo removido do diretório apenas SOME da listagem (não há tombstone
      em filesystem); a remoção do índice exige a flag `--remover-ausentes`
      do sync — deleção nunca é implícita.
    """

    def __init__(self, docs_dir: Path = DIR_DOCS_BASE, categoria: str = "Base de conhecimento"):
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
            meta, _ = separar_front_matter(texto)
            if meta.get("placeholder", "").lower() == "true":
                logger.warning("fonte_local doc placeholder (sem conteúdo oficial) %s", md.name)
            fingerprint = hashlib.sha256(texto.encode("utf-8")).hexdigest()[:16]
            itens.append(
                ItemListagem(
                    id=md.name,
                    titulo=meta.get("titulo") or md.name,
                    categoria=self._categoria,
                    updated_at=fingerprint,
                )
            )
        return itens

    def obter(self, doc_id: str) -> DocumentoFonte:
        caminho = self._docs_dir / doc_id
        if caminho.parent != self._docs_dir or not caminho.is_file():
            raise ErroDeFonte(f"documento não encontrado na fonte local: {doc_id}")
        meta, corpo = separar_front_matter(caminho.read_text(encoding="utf-8"))
        return DocumentoFonte(
            id=doc_id,
            titulo=meta.get("titulo") or doc_id,
            categoria=self._categoria,
            conteudo=corpo,
        )


# ---------------------------------------------------------------------------
# API/portal de documentos da Infominas (stub até a P6)
# ---------------------------------------------------------------------------


class ConfigFonteAPI(BaseSettings):
    """`DOCS_API_*` — config da fonte da Infominas (lida do ambiente/.env).

    TODO(P6): nomes e formato dependem da API/portal real; hipótese de trabalho
    até a pendência ser respondida. Vazio = fonte não configurada.
    """

    model_config = SettingsConfigDict(
        env_file=".env", env_file_encoding="utf-8", extra="ignore", case_sensitive=False
    )

    base_url: str = Field("", alias="DOCS_API_BASE_URL")
    key: str = Field("", alias="DOCS_API_KEY")
    timeout: float = Field(30.0, alias="DOCS_API_TIMEOUT")


def _campo(raw: dict[str, Any], nome: str) -> Any:
    """Campo obrigatório de um item da API; ausência = resposta fora do contrato."""
    if nome not in raw or raw[nome] is None:
        raise ErroDeFonte(f"item da API sem campo obrigatório '{nome}': {raw.get('id')!r}")
    return raw[nome]


class FonteAPIInfominas:
    """API/portal de documentos da Infominas — STUB configurável (TODO(P6)).

    Mesma interface de `FonteDocumentacao`. Hipótese de trabalho, a confirmar
    quando a P6 for respondida (URL, autenticação e formato reais):

    - `GET {base}`      → inventário paginado `{"itens": [...], "next_cursor": ...}`
      com `id`, `titulo`, `categoria`, `updated_at` e `deleted` opcional;
    - `GET {base}/{id}` → `{"id", "titulo", "categoria", "conteudo"}` (Markdown);
    - credencial em `Authorization: Bearer <DOCS_API_KEY>` quando houver.

    Sem `DOCS_API_BASE_URL` o construtor levanta `ErroDeFonte` — antes de
    qualquer chamada de rede. `transport` é injetável (httpx.MockTransport).
    """

    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
        timeout: float | None = None,
        limite_pagina: int = 100,
        transport: Any | None = None,
    ):
        if base_url is None:
            cfg = ConfigFonteAPI()  # type: ignore[call-arg]  # campos vêm do ambiente
            base_url = cfg.base_url
            api_key = cfg.key if api_key is None else api_key
            timeout = cfg.timeout if timeout is None else timeout
        if not base_url:
            raise ErroDeFonte(
                "DOCS_API_* não configurado: defina DOCS_API_BASE_URL (e DOCS_API_KEY) "
                "ou use --fonte local. A API de documentos da Infominas é a pendência P6."
            )
        import httpx

        self._base = base_url.rstrip("/")
        self._limite_pagina = max(1, int(limite_pagina))
        headers = {"Authorization": f"Bearer {api_key}"} if api_key else {}
        self._client = httpx.Client(headers=headers, timeout=timeout or 30.0, transport=transport)

    def close(self) -> None:
        self._client.close()

    def _get(self, url: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
        try:
            resp = self._client.get(url, params=params)
        except Exception as exc:
            raise ErroDeFonte(f"falha de rede na fonte ({url}): {exc}") from exc
        if resp.status_code != 200:
            # Erro explícito aborta o ciclo; nunca aproveitar resposta parcial.
            raise ErroDeFonte(f"fonte respondeu {resp.status_code} em {url}")
        try:
            data = resp.json()
        except Exception as exc:
            raise ErroDeFonte(f"resposta não-JSON da fonte em {url}") from exc
        if not isinstance(data, dict):
            raise ErroDeFonte(f"resposta fora do contrato em {url}")
        return data

    def listar(self) -> list[ItemListagem]:
        itens: list[ItemListagem] = []
        cursor: str | None = None
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
        data = self._get(f"{self._base}/{quote(doc_id, safe='')}")
        return DocumentoFonte(
            id=str(_campo(data, "id")),
            titulo=str(_campo(data, "titulo")),
            categoria=str(_campo(data, "categoria")),
            conteudo=str(_campo(data, "conteudo")),
        )
