"""Integração real com Postgres+pgvector — PULADO sem PGVECTOR_TEST_URL.

Usa uma env var PRÓPRIA (não `PGVECTOR_URL`) de propósito: estes testes criam
e DERRUBAM tabelas — nunca devem apontar para o banco de produção. Para rodar:

    docker run -d --name pg-rag-teste -e POSTGRES_PASSWORD=teste \
        -e POSTGRES_DB=rag_teste -p 5433:5432 pgvector/pgvector:pg16
    PGVECTOR_TEST_URL=postgresql://postgres:teste@localhost:5433/rag_teste \
        pytest tests/test_indice_pg.py -v
"""

from __future__ import annotations

import os

import pytest

from src.rag.fonte import DocumentoFonte, ItemListagem

PG_URL = os.environ.get("PGVECTOR_TEST_URL", "")

pytestmark = pytest.mark.skipif(
    not PG_URL, reason="integração pgvector: defina PGVECTOR_TEST_URL"
)

DIM = 8
MODELO = "fake-model"


def _vec(seed: float) -> list[float]:
    return [seed] + [0.0] * (DIM - 1)


@pytest.fixture()
def indice():
    from src.rag.indice_pg import PgIndice

    ind = PgIndice.conectar(PG_URL)
    # Começa do zero: derruba resíduos de execuções anteriores.
    with ind._conn.transaction():
        for tabela in ("rag_chunks", "rag_documentos", "rag_meta"):
            ind._conn.execute(f"DROP TABLE IF EXISTS {tabela}")
    ind.preparar_esquema(MODELO, DIM)
    try:
        yield ind
    finally:
        ind.fechar()


def _indexar(indice, doc_id="a.md", titulo="Doc A", texto="conteúdo", seed=1.0,
             updated_at="v1"):
    from src.rag.chunking import chunkear_documento

    item = ItemListagem(id=doc_id, titulo=titulo, categoria="Frotas",
                        updated_at=updated_at)
    doc = DocumentoFonte(id=doc_id, titulo=titulo, categoria="Frotas",
                         conteudo=f"# {titulo}\n\n{texto}")
    chunks = chunkear_documento(doc)
    indice.substituir_documento(
        item, f"hash-{doc_id}-{updated_at}", chunks, [_vec(seed)] * len(chunks)
    )
    return item


def test_roundtrip_indexar_consultar_substituir_remover(indice):
    from src.rag.indice_pg import PgRetriever

    _indexar(indice, "a.md", "Doc A", "texto sobre cadastro", seed=1.0)
    _indexar(indice, "b.md", "Doc B", "texto sobre avisos", seed=-1.0)

    estado = indice.estado()
    assert set(estado) == {"a.md", "b.md"} and not estado["a.md"].deleted

    class EmbFixa:
        def embed_query(self, _):
            return _vec(1.0)  # cosseno máximo com o Doc A

    docs = PgRetriever(PG_URL, EmbFixa(), k=1).invoke("qualquer pergunta")
    assert len(docs) == 1
    assert docs[0].metadata["source"] == "Doc A"
    assert "cadastro" in docs[0].page_content

    # Substituição transacional: o conteúdo novo troca o antigo por inteiro.
    _indexar(indice, "a.md", "Doc A", "texto REVISADO", seed=1.0, updated_at="v2")
    docs = PgRetriever(PG_URL, EmbFixa(), k=1).invoke("qualquer pergunta")
    assert "REVISADO" in docs[0].page_content
    assert indice.estado()["a.md"].hash_listagem == "hash-a.md-v2"

    indice.remover_documento("a.md", "hash-tombstone")
    assert indice.estado()["a.md"].deleted
    contagens = indice.contagens()
    assert contagens == {"docs_vivos": 1, "tombstones": 1,
                         "chunks": contagens["chunks"]}
    docs = PgRetriever(PG_URL, EmbFixa(), k=5).invoke("qualquer pergunta")
    assert all(d.metadata["source"] != "Doc A" for d in docs)


def test_modelo_registrado_persiste_e_limpar_tudo_reseta(indice):
    assert indice.modelo_registrado() == MODELO
    indice.preparar_esquema("outro-modelo", DIM)   # não sobrescreve
    assert indice.modelo_registrado() == MODELO

    _indexar(indice)
    indice.limpar_tudo("outro-modelo", DIM)
    assert indice.modelo_registrado() == "outro-modelo"
    assert indice.estado() == {}
    assert indice.contagens()["chunks"] == 0


def test_sync_completo_contra_postgres_real(indice):
    """O motor inteiro (sync) contra o índice real — fonte e embeddings fakes."""
    from tests.test_sync import FakeEmbeddings, FakeFonte, _doc
    from src.rag.sync import executar_sync

    fonte = FakeFonte({"a.md": _doc(), "b.md": _doc(titulo="Avisos RNTRC")})
    emb = FakeEmbeddings()

    r1 = executar_sync(fonte, indice, emb, embedding_model=MODELO)
    assert r1.novos == 2 and r1.ok

    r2 = executar_sync(fonte, indice, emb, embedding_model=MODELO)
    assert r2.inalterados == 2 and r2.novos == 0  # idempotente

    fonte.docs["a.md"]["deleted"] = True
    r3 = executar_sync(fonte, indice, emb, embedding_model=MODELO)
    assert r3.removidos == 1
    assert indice.contagens()["docs_vivos"] == 1
