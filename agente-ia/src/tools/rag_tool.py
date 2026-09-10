"""Consulta à base de conhecimento interna (RAG).

`buscar_chunks()` é a função pura usada pelo nó `retrieve` do grafo
(retrieval-first: TODA mensagem que chega ao agente passa pela recuperação
antes da chamada de LLM — o modelo não decide mais "se" consulta a base, o
que elimina a classe de falha F4 do laudo: responder de memória citando
fonte fabricada).

A tool `consultar_base_conhecimento` continua existindo como wrapper para
compatibilidade (testes/experimentos), mas NÃO entra mais em
`get_default_tools()`.
"""

from __future__ import annotations

import logging
from typing import Any

from langchain_core.tools import tool

from src.config import get_settings
from src.rag.retriever import get_retriever


logger = logging.getLogger(__name__)


def buscar_chunks(pergunta: str) -> dict[str, Any]:
    """Recupera os top-k chunks da base para uma pergunta em linguagem natural.

    Returns:
        dict: encontrado (bool), total (int), chunks (list com texto, secao,
        source). Em falha, `erro` traz uma mensagem GENÉRICA — o detalhe
        técnico vai só para o log (A2: nunca vazar erro interno ao cliente).
    """
    pergunta = (pergunta or "").strip()
    if not pergunta:
        return {
            "encontrado": False,
            "total": 0,
            "chunks": [],
            "erro": "pergunta vazia",
        }

    settings = get_settings()
    logger.info("rag_query top_k=%s pergunta=%r", settings.rag_top_k, pergunta)
    try:
        retriever = get_retriever(k=settings.rag_top_k)
        docs = retriever.invoke(pergunta)
    except Exception as exc:  # noqa: BLE001 — fail-soft com log
        logger.warning("rag_falhou pergunta=%r erro=%s", pergunta, exc)
        return {
            "encontrado": False,
            "total": 0,
            "chunks": [],
            "erro": "base de conhecimento indisponível no momento",
        }

    chunks = []
    for d in docs:
        chunks.append(
            {
                "texto": d.page_content,
                "secao": d.metadata.get("secao") or None,
                "source": d.metadata.get("source", "desconhecido"),
            }
        )

    logger.info(
        "rag_resultado total=%d fontes=%s",
        len(chunks),
        [c["source"] for c in chunks],
    )
    return {
        "encontrado": len(chunks) > 0,
        "total": len(chunks),
        "chunks": chunks,
    }


@tool
def consultar_base_conhecimento(pergunta: str) -> dict[str, Any]:
    """Consulta a base de conhecimento técnico interna da Azapfy (docs indexadas).

    Fonte de verdade primária (e única) para dúvidas técnicas/operacionais sobre
    os produtos Azapfy (plataforma Web, app do motorista, módulos, mercado).

    Args:
        pergunta: Pergunta do usuário em linguagem natural (em português).

    Returns:
        dict: encontrado (bool), total (int), chunks (list com texto, secao,
        source) para citar a fonte.
    """
    return buscar_chunks(pergunta)
