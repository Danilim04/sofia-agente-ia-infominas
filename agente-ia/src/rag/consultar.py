"""Consulta a base de conhecimento pelo retriever configurado — SEM LLM.

É o teste de fumaça do RAG em produção: usa exatamente o `get_retriever()` que
o nó `retrieve` do grafo usa (backend de `VECTOR_BACKEND`, mesmo modelo de
embeddings), e mostra as fontes/seções/trechos do top-k. Não custa token.

Uso:
    python -m src.rag.consultar "como funciona o aplicativo do motorista?"
    python -m src.rag.consultar "documentos do transporte" --k 5 --chars 300

Exit 0 com resultados; exit 1 sem nenhum (índice vazio ou backend errado).
"""

from __future__ import annotations

import argparse
import logging
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from langchain_core.documents import Document


def formatar(pergunta: str, docs: "list[Document]", chars: int = 200) -> str:
    """Relatório legível do top-k (fonte, seção, trecho) — puro, testável."""
    if not docs:
        return f"Nenhum trecho para {pergunta!r} — índice vazio?"
    linhas = [f"{len(docs)} trecho(s) para {pergunta!r}:"]
    for i, d in enumerate(docs, 1):
        meta = d.metadata or {}
        secao = meta.get("secao") or ""
        trecho = " ".join(d.page_content.split())
        if len(trecho) > chars:
            trecho = trecho[: chars - 1] + "…"
        cabecalho = f"[{i}] {meta.get('source', '?')}"
        if secao:
            cabecalho += f" › {secao}"
        linhas.append(cabecalho)
        linhas.append(f"    {trecho}")
    return "\n".join(linhas)


def _cli() -> None:
    from src.config import get_settings
    from src.rag.retriever import get_retriever

    logging.basicConfig(level=logging.WARNING)
    parser = argparse.ArgumentParser(
        description="Consulta o índice do RAG pelo retriever configurado (sem LLM)."
    )
    parser.add_argument("pergunta")
    parser.add_argument("--k", type=int, default=None, help="Top-k (default: RAG_TOP_K).")
    parser.add_argument("--chars", type=int, default=200, help="Tamanho do trecho exibido.")
    args = parser.parse_args()

    settings = get_settings()
    k = args.k or settings.rag_top_k
    print(f"backend={settings.vector_backend} k={k} embeddings={settings.embeddings_model}")
    docs = get_retriever(k=k).invoke(args.pergunta)
    print(formatar(args.pergunta, docs, args.chars))
    if not docs:
        raise SystemExit(1)


if __name__ == "__main__":
    _cli()
