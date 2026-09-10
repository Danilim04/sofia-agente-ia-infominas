"""Pipeline de ingestão da base de conhecimento: docs Markdown → chunks → ChromaDB.

Pode ser executado como CLI:

    python -m src.rag.ingest [--docs-dir docs] [--persist-dir ./chroma_db]
"""

from __future__ import annotations

import argparse
import logging
from pathlib import Path
from typing import TYPE_CHECKING

from langchain_core.documents import Document
from langchain_text_splitters import (
    MarkdownHeaderTextSplitter,
    RecursiveCharacterTextSplitter,
)

if TYPE_CHECKING:
    from langchain_chroma import Chroma
    from langchain_core.embeddings import Embeddings


logger = logging.getLogger(__name__)

COLLECTION_NAME = "azapfy_kb"
# Modelo FIXO do Chroma embutido na imagem (rollback offline). É gerado no
# build e consultado com este mesmo modelo, sempre local — não segue
# EMBEDDINGS_PROVIDER/EMBEDDINGS_MODEL (que valem para o pgvector).
CHROMA_EMBEDDINGS_MODEL = "sentence-transformers/all-MiniLM-L6-v2"

# Cabeçalhos Markdown usados para fatiar cada doc em seções citáveis.
_HEADERS = [("#", "h1"), ("##", "h2"), ("###", "h3")]


def _secao_de(metadata: dict) -> str:
    """Monta o caminho de seção legível (ex.: 'Módulo: Pesquisa › 4.3 Botões')."""
    partes = [metadata.get(h) for h in ("h1", "h2", "h3")]
    return " › ".join(p for p in partes if p)


def load_markdown_dir(docs_dir: Path) -> list[Document]:
    """Carrega todos os `.md` de um diretório em `Document`s por seção.

    Cada `Document` traz `metadata['source']` (nome do arquivo) e
    `metadata['secao']` (caminho dos cabeçalhos). Não há "página" como no PDF
    antigo — a seção é o que a resposta cita. Arquivos vazios são ignorados.
    """
    docs_dir = Path(docs_dir)
    if not docs_dir.is_dir():
        raise FileNotFoundError(
            f"Diretório de docs não encontrado em {docs_dir}. "
            "Coloque os arquivos .md de conhecimento em docs/ antes de rodar a ingestão."
        )

    md_files = sorted(docs_dir.glob("*.md"))
    if not md_files:
        raise FileNotFoundError(f"Nenhum arquivo .md encontrado em {docs_dir}.")

    # strip_headers=False mantém o título dentro do texto do chunk (ajuda tanto
    # o embedding quanto a leitura do trecho pelo LLM); a seção também vai p/
    # metadata para a citação.
    splitter = MarkdownHeaderTextSplitter(
        headers_to_split_on=_HEADERS, strip_headers=False
    )
    documents: list[Document] = []
    for md in md_files:
        texto = md.read_text(encoding="utf-8").strip()
        if not texto:
            logger.warning("Pulando %s (arquivo vazio).", md.name)
            continue
        for secao in splitter.split_text(texto):
            secao.metadata = {
                "source": md.name,
                "secao": _secao_de(secao.metadata),
            }
            documents.append(secao)

    logger.info(
        "Carregadas %d seções de %d arquivo(s) Markdown em %s",
        len(documents),
        len(md_files),
        docs_dir,
    )
    return documents


def split_documents(
    documents: list[Document],
    chunk_size: int = 800,
    chunk_overlap: int = 120,
) -> list[Document]:
    """Divide os documentos em chunks com overlap, preservando metadata.

    Seções pequenas cabem em um único chunk; seções longas são quebradas e
    cada pedaço herda `source`/`secao` para a citação.
    """
    splitter = RecursiveCharacterTextSplitter(
        chunk_size=chunk_size,
        chunk_overlap=chunk_overlap,
        separators=["\n\n", "\n", ". ", " ", ""],
    )
    chunks = splitter.split_documents(documents)
    logger.info(
        "Gerados %d chunks (chunk_size=%d, overlap=%d)",
        len(chunks),
        chunk_size,
        chunk_overlap,
    )
    return chunks


def persist_chunks(
    chunks: list[Document],
    persist_dir: Path,
    embeddings: "Embeddings",
    collection_name: str = COLLECTION_NAME,
) -> "Chroma":
    """Persiste os chunks num ChromaDB em disco e devolve o vector store.

    Reseta a coleção antes de gravar para não misturar a ingestão atual com
    conhecimento obsoleto de uma ingestão anterior (ex.: o PDF antigo).
    """
    from langchain_chroma import Chroma

    persist_dir = Path(persist_dir)
    persist_dir.mkdir(parents=True, exist_ok=True)

    try:
        Chroma(
            collection_name=collection_name,
            embedding_function=embeddings,
            persist_directory=str(persist_dir),
        ).delete_collection()
    except Exception as exc:  # noqa: BLE001 — sem coleção prévia para limpar
        logger.debug("Nenhuma coleção anterior para limpar (%s).", exc)

    store = Chroma.from_documents(
        documents=chunks,
        embedding=embeddings,
        persist_directory=str(persist_dir),
        collection_name=collection_name,
    )
    logger.info(
        "ChromaDB persistido em %s (collection=%s, docs=%d)",
        persist_dir,
        collection_name,
        len(chunks),
    )
    return store


def ingest(
    docs_dir: Path,
    persist_dir: Path,
    embeddings: "Embeddings",
    chunk_size: int = 800,
    chunk_overlap: int = 120,
) -> "Chroma":
    """Pipeline completo: carrega os .md → splita → persiste em ChromaDB."""
    documents = load_markdown_dir(docs_dir)
    chunks = split_documents(documents, chunk_size, chunk_overlap)
    return persist_chunks(chunks, persist_dir, embeddings)


def _cli() -> None:
    from src.agent.llm import get_local_embeddings
    from src.config import get_settings

    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s"
    )
    settings = get_settings()

    parser = argparse.ArgumentParser(
        description="Ingere as docs Markdown da Azapfy no ChromaDB persistido "
                    f"(sempre com o modelo local {CHROMA_EMBEDDINGS_MODEL})."
    )
    parser.add_argument(
        "--docs-dir",
        type=Path,
        default=settings.docs_dir,
        help="Diretório com os .md de conhecimento (default: DOCS_DIR do .env).",
    )
    parser.add_argument(
        "--persist-dir",
        type=Path,
        default=settings.chroma_persist_dir,
        help="Diretório de persistência do Chroma (default: CHROMA_PERSIST_DIR).",
    )
    parser.add_argument("--chunk-size", type=int, default=settings.rag_chunk_size)
    parser.add_argument("--chunk-overlap", type=int, default=settings.rag_chunk_overlap)
    args = parser.parse_args()

    embeddings = get_local_embeddings(CHROMA_EMBEDDINGS_MODEL)
    store = ingest(
        args.docs_dir,
        args.persist_dir,
        embeddings,
        args.chunk_size,
        args.chunk_overlap,
    )
    total = store._collection.count()
    print(f"Ingestão concluída. Documentos indexados no Chroma: {total}")


if __name__ == "__main__":
    _cli()
