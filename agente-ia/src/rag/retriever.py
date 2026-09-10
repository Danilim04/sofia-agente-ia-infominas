"""Wrapper para reabrir o ChromaDB persistido e expor um retriever pronto.

A fábrica de embeddings vive em `src.agent.llm` (Épico 5); este módulo
apenas re-exporta `get_embeddings` para manter o caminho histórico
(`from src.rag.retriever import get_embeddings`) funcionando.
"""

from __future__ import annotations

from pathlib import Path
from typing import TYPE_CHECKING, Optional

from src.agent.llm import get_embeddings, get_local_embeddings
from src.rag.ingest import CHROMA_EMBEDDINGS_MODEL, COLLECTION_NAME

if TYPE_CHECKING:
    from langchain_chroma import Chroma
    from langchain_core.embeddings import Embeddings
    from langchain_core.vectorstores import VectorStoreRetriever


__all__ = ["get_embeddings", "get_vector_store", "get_retriever"]


def get_vector_store(
    persist_dir: Optional[Path] = None,
    embeddings: Optional["Embeddings"] = None,
) -> "Chroma":
    """Reabre o ChromaDB persistido (sem reingerir).

    O Chroma é o índice de ROLLBACK, gerado no build da imagem com o modelo
    local fixo (`CHROMA_EMBEDDINGS_MODEL`) — por isso consulta SEMPRE com esse
    modelo, independente de EMBEDDINGS_PROVIDER/EMBEDDINGS_MODEL (que valem
    para o pgvector). Misturar espaços vetoriais devolveria lixo em silêncio.
    """
    from langchain_chroma import Chroma

    if persist_dir is None:
        from src.config import get_settings

        persist_dir = get_settings().chroma_persist_dir
    if embeddings is None:
        embeddings = get_local_embeddings(CHROMA_EMBEDDINGS_MODEL)

    return Chroma(
        collection_name=COLLECTION_NAME,
        embedding_function=embeddings,
        persist_directory=str(Path(persist_dir)),
    )


def get_retriever(
    k: int = 4,
    persist_dir: Optional[Path] = None,
    embeddings: Optional["Embeddings"] = None,
):
    """Retorna um retriever top-k pronto para `.invoke(pergunta)`.

    O backend vem de `VECTOR_BACKEND` (`chroma` legado | `pgvector`). Os dois
    devolvem o mesmo formato (`Document` com `source`/`secao`), então
    `buscar_chunks` e o grafo não mudam — é o que torna a migração chaveável
    com rollback instantâneo.
    """
    from src.config import get_settings

    settings = get_settings()
    if settings.vector_backend == "pgvector":
        from src.rag.indice_pg import PgRetriever

        return PgRetriever(
            settings.pgvector_url, embeddings or get_embeddings(), k=k
        )
    store = get_vector_store(persist_dir, embeddings)
    return store.as_retriever(search_kwargs={"k": k})
