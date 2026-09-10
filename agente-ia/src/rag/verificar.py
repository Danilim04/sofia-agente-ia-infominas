"""Verificação de paridade Chroma × pgvector (validação da migração em prod).

Roda as mesmas consultas nos DOIS backends e compara os documentos (`source`)
do top-k. Como o chunking e os embeddings são idênticos, a sobreposição deve
ser alta — divergência grande indica seed incompleto ou índice doente. É o
passo "sombra" do runbook de migração: roda com o bot ainda lendo do Chroma,
ANTES de virar `VECTOR_BACKEND=pgvector`.

    python -m src.rag.verificar                     # consultas padrão
    python -m src.rag.verificar --consultas f.txt   # uma consulta por linha

Exit 0 = paridade ok (sobreposição média >= limiar e contagens plausíveis);
exit 1 = divergência — não vire o backend antes de investigar.
"""

from __future__ import annotations

import argparse
import logging
from pathlib import Path

logger = logging.getLogger(__name__)

# Consultas canônicas: uma por tema recorrente do suporte. Ajuste livre —
# o objetivo é cobrir temas distintos da base, não ser exaustivo.
CONSULTAS_PADRAO = [
    "como cadastrar um novo veículo na frota",
    "o que fazer quando aparece aviso de RNTRC",
    "como abrir um chamado de suporte",
    "como funciona a pesquisa de mercado de frete",
    "como redefinir a senha de acesso à plataforma",
    "como acompanhar a entrega pelo aplicativo do motorista",
]


def _fontes_top_k(retriever, consulta: str) -> list[str]:
    return [d.metadata.get("source", "?") for d in retriever.invoke(consulta)]


def comparar(consultas: list[str], k: int) -> tuple[float, list[str]]:
    """Retorna (sobreposição média, linhas de relatório por consulta)."""
    from src.agent.llm import get_embeddings
    from src.config import get_settings
    from src.rag.indice_pg import PgRetriever
    from src.rag.retriever import get_vector_store

    settings = get_settings()
    embeddings = get_embeddings()
    # Chroma consulta com o modelo local fixo (é assim que foi gerado); o
    # pgvector com o provider configurado. Com modelos diferentes a paridade
    # deixa de ser a métrica — use src.rag.consultar.
    chroma = get_vector_store().as_retriever(search_kwargs={"k": k})
    pg = PgRetriever(settings.pgvector_url, embeddings, k=k)

    linhas: list[str] = []
    soma = 0.0
    for consulta in consultas:
        f_chroma = _fontes_top_k(chroma, consulta)
        f_pg = _fontes_top_k(pg, consulta)
        inter = len(set(f_chroma) & set(f_pg))
        uniao = len(set(f_chroma) | set(f_pg)) or 1
        jaccard = inter / uniao
        soma += jaccard
        marca = "ok " if jaccard >= 0.5 else "DIV"
        linhas.append(
            f"[{marca}] {jaccard:.0%} — {consulta!r}\n"
            f"      chroma:   {f_chroma}\n"
            f"      pgvector: {f_pg}"
        )
    return soma / max(len(consultas), 1), linhas


def _cli() -> None:
    from src.config import get_settings
    from src.rag.indice_pg import PgIndice
    from src.rag.retriever import get_vector_store

    logging.basicConfig(level=logging.WARNING)
    parser = argparse.ArgumentParser(
        description="Compara respostas do Chroma e do pgvector para as mesmas consultas."
    )
    parser.add_argument("--consultas", type=Path, default=None,
                        help="Arquivo com uma consulta por linha (default: lista embutida).")
    parser.add_argument("--k", type=int, default=None,
                        help="Top-k por consulta (default: RAG_TOP_K).")
    parser.add_argument("--limiar", type=float, default=0.6,
                        help="Sobreposição média mínima para passar (default: 0.6).")
    args = parser.parse_args()

    settings = get_settings()
    if not settings.pgvector_url:
        raise SystemExit("PGVECTOR_URL não configurada no .env.")

    consultas = CONSULTAS_PADRAO
    if args.consultas:
        consultas = [
            linha.strip()
            for linha in args.consultas.read_text(encoding="utf-8").splitlines()
            if linha.strip()
        ]
    k = args.k or settings.rag_top_k

    # Contagens: nº de chunks tem que bater entre os dois índices.
    n_chroma = get_vector_store()._collection.count()
    indice = PgIndice.conectar(settings.pgvector_url)
    try:
        contagens = indice.contagens()
    finally:
        indice.fechar()
    print(
        f"Chunks — chroma: {n_chroma} | pgvector: {contagens['chunks']} "
        f"(docs vivos: {contagens['docs_vivos']}, tombstones: {contagens['tombstones']})"
    )

    media, linhas = comparar(consultas, k)
    print("\n".join(linhas))
    print(f"\nSobreposição média: {media:.0%} (limiar: {args.limiar:.0%})")

    if contagens["chunks"] != n_chroma:
        print("ATENÇÃO: contagem de chunks divergente entre os backends.")
    if media < args.limiar:
        raise SystemExit(1)


if __name__ == "__main__":
    _cli()
