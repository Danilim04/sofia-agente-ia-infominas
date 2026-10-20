"""Fontes de documentação para o pipeline de sincronização (Contrato B).

Uma "fonte" é quem fornece a documentação a indexar. O contrato de dados é o
do Contrato B herdado (listagem barata — metadados, sem conteúdo — + busca de
documento completo por id; tombstones para remoção). Implementação atual:

- `FonteDocsLocais`: os `docs/*.md` do repositório apresentados por essa
  interface — é a fonte do seed inicial e do período até a API/portal de
  documentos da Infominas existir (pendência P6). Assim a carga das docs
  atuais usa exatamente o mesmo pipeline (mesmo chunking, mesmas travas) que
  a fonte de produção usará. E3.2 adiciona `FonteAPIInfominas` (stub até P6).

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
from typing import Protocol

logger = logging.getLogger(__name__)


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

    def __init__(self, docs_dir: Path, categoria: str = "Base de conhecimento"):
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
