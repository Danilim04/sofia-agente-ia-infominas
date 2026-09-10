"""Índice vetorial em PostgreSQL + pgvector (SQL puro, sem ORM).

Por que SQL direto e não `langchain-postgres`: o Contrato B promete transação
por documento (uma pergunta no meio do sync vê a versão velha OU a nova, nunca
metade) — com controle explícito de transação isso é literal: DELETE dos chunks
antigos + INSERT dos novos + upsert da tabela de controle no MESMO commit.

Esquema (criado idempotentemente em `preparar_esquema`):
- `rag_meta`      — chave/valor: `embedding_model`, `embedding_dim`. Incremental
  com modelo diferente do registrado é RECUSADO pelo sync (espaços vetoriais
  não se misturam); trocar modelo/chunking exige `--full`.
- `rag_documentos`— tabela de controle do sync: hash da listagem + tombstones.
- `rag_chunks`    — chunks com embedding `vector(dim)`; id determinístico
  `{doc_id}#{ordem:04d}`; índice HNSW por cosseno (embeddings normalizados).

`PgRetriever` expõe `.invoke(pergunta)` no mesmo formato do retriever Chroma
(lista de `Document` com `source`/`secao`) — `buscar_chunks` e o grafo não
mudam. Conexão por consulta (volume de suporte é baixo; falha vira o fail-soft
do `buscar_chunks`).
"""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING, Any, Optional

from src.rag.fonte import ItemListagem, RegistroDoc

if TYPE_CHECKING:
    from langchain_core.documents import Document
    from langchain_core.embeddings import Embeddings

logger = logging.getLogger(__name__)


def _vector(valores: list[float]) -> Any:
    try:
        from pgvector import Vector
    except ImportError:  # pgvector-python < 0.4 expunha em .utils
        from pgvector.utils import Vector
    return Vector(valores)


def _conectar(url: str, criar_extensao: bool = False) -> Any:
    """Abre conexão psycopg com o tipo `vector` registrado.

    `criar_extensao` só no caminho de escrita (sync/bootstrap) — o caminho de
    leitura não roda DDL.
    """
    import psycopg
    from pgvector.psycopg import register_vector

    # autocommit=True é o padrão psycopg3 para "leituras commitam na hora,
    # escritas atômicas usam bloco conn.transaction()". Sem isso, uma leitura
    # fora de bloco abre transação implícita e o transaction() seguinte vira
    # SAVEPOINT — a escrita nunca commita e outra conexão não a enxerga.
    conn = psycopg.connect(url, connect_timeout=10, autocommit=True)
    if criar_extensao:
        with conn.transaction():
            conn.execute("CREATE EXTENSION IF NOT EXISTS vector")
    register_vector(conn)
    return conn


class PgIndice:
    """Escrita do índice (usada pelo sync). Leitura fica no `PgRetriever`."""

    def __init__(self, conn: Any):
        self._conn = conn

    @classmethod
    def conectar(cls, url: str) -> "PgIndice":
        return cls(_conectar(url, criar_extensao=True))

    def fechar(self) -> None:
        try:
            self._conn.close()
        except Exception:  # noqa: BLE001 — fechar nunca mascara o erro real
            pass

    # -- esquema -----------------------------------------------------------

    def _criar_tabela_chunks(self, dim: int) -> None:
        dim = int(dim)
        self._conn.execute(
            f"""
            CREATE TABLE IF NOT EXISTS rag_chunks (
                id TEXT PRIMARY KEY,
                doc_id TEXT NOT NULL,
                ordem INTEGER NOT NULL,
                texto TEXT NOT NULL,
                secao TEXT,
                source TEXT NOT NULL,
                categoria TEXT,
                embedding vector({dim}) NOT NULL
            )
            """
        )
        self._conn.execute(
            "CREATE INDEX IF NOT EXISTS rag_chunks_doc_idx ON rag_chunks (doc_id)"
        )
        self._conn.execute(
            "CREATE INDEX IF NOT EXISTS rag_chunks_hnsw_idx ON rag_chunks "
            "USING hnsw (embedding vector_cosine_ops)"
        )

    def preparar_esquema(self, embedding_model: str, dim: int) -> None:
        with self._conn.transaction():
            self._conn.execute(
                """
                CREATE TABLE IF NOT EXISTS rag_meta (
                    chave TEXT PRIMARY KEY,
                    valor TEXT NOT NULL
                )
                """
            )
            self._conn.execute(
                """
                CREATE TABLE IF NOT EXISTS rag_documentos (
                    id TEXT PRIMARY KEY,
                    hash_listagem TEXT NOT NULL,
                    titulo TEXT NOT NULL DEFAULT '',
                    categoria TEXT NOT NULL DEFAULT '',
                    updated_at_fonte TEXT NOT NULL DEFAULT '',
                    deleted BOOLEAN NOT NULL DEFAULT FALSE,
                    chunk_count INTEGER NOT NULL DEFAULT 0,
                    ingested_at TIMESTAMPTZ NOT NULL DEFAULT now()
                )
                """
            )
            self._criar_tabela_chunks(dim)
            # Registra modelo/dim só na primeira vez — divergência posterior é
            # detectada pelo sync (ErroModeloIncompativel), nunca sobrescrita.
            self._conn.execute(
                "INSERT INTO rag_meta (chave, valor) VALUES ('embedding_model', %s) "
                "ON CONFLICT (chave) DO NOTHING",
                (embedding_model,),
            )
            self._conn.execute(
                "INSERT INTO rag_meta (chave, valor) VALUES ('embedding_dim', %s) "
                "ON CONFLICT (chave) DO NOTHING",
                (str(int(dim)),),
            )

    def modelo_registrado(self) -> Optional[str]:
        row = self._conn.execute(
            "SELECT valor FROM rag_meta WHERE chave = 'embedding_model'"
        ).fetchone()
        return row[0] if row else None

    def limpar_tudo(self, embedding_model: str, dim: int) -> None:
        """Reset para `--full` com troca de modelo (a dimensão pode mudar)."""
        with self._conn.transaction():
            self._conn.execute("DROP TABLE IF EXISTS rag_chunks")
            self._conn.execute("DELETE FROM rag_documentos")
            self._criar_tabela_chunks(dim)
            self._conn.execute(
                "INSERT INTO rag_meta (chave, valor) VALUES ('embedding_model', %s) "
                "ON CONFLICT (chave) DO UPDATE SET valor = EXCLUDED.valor",
                (embedding_model,),
            )
            self._conn.execute(
                "INSERT INTO rag_meta (chave, valor) VALUES ('embedding_dim', %s) "
                "ON CONFLICT (chave) DO UPDATE SET valor = EXCLUDED.valor",
                (str(int(dim)),),
            )
        logger.warning("indice_pg limpar_tudo modelo=%s dim=%d", embedding_model, dim)

    # -- estado / escrita ---------------------------------------------------

    def estado(self) -> dict[str, RegistroDoc]:
        rows = self._conn.execute(
            "SELECT id, hash_listagem, deleted FROM rag_documentos"
        ).fetchall()
        return {r[0]: RegistroDoc(hash_listagem=r[1], deleted=r[2]) for r in rows}

    def substituir_documento(
        self,
        item: ItemListagem,
        hash_listagem: str,
        chunks: "list[Document]",
        vetores: list[list[float]],
    ) -> None:
        with self._conn.transaction():
            self._conn.execute("DELETE FROM rag_chunks WHERE doc_id = %s", (item.id,))
            for ordem, (chunk, vetor) in enumerate(zip(chunks, vetores)):
                meta = chunk.metadata or {}
                self._conn.execute(
                    """
                    INSERT INTO rag_chunks
                        (id, doc_id, ordem, texto, secao, source, categoria, embedding)
                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s)
                    """,
                    (
                        f"{item.id}#{ordem:04d}",
                        item.id,
                        ordem,
                        chunk.page_content,
                        meta.get("secao") or None,
                        meta.get("source") or item.titulo,
                        meta.get("categoria") or item.categoria,
                        _vector(vetor),
                    ),
                )
            self._conn.execute(
                """
                INSERT INTO rag_documentos
                    (id, hash_listagem, titulo, categoria, updated_at_fonte,
                     deleted, chunk_count, ingested_at)
                VALUES (%s, %s, %s, %s, %s, FALSE, %s, now())
                ON CONFLICT (id) DO UPDATE SET
                    hash_listagem = EXCLUDED.hash_listagem,
                    titulo = EXCLUDED.titulo,
                    categoria = EXCLUDED.categoria,
                    updated_at_fonte = EXCLUDED.updated_at_fonte,
                    deleted = FALSE,
                    chunk_count = EXCLUDED.chunk_count,
                    ingested_at = now()
                """,
                (item.id, hash_listagem, item.titulo, item.categoria,
                 item.updated_at, len(chunks)),
            )

    def remover_documento(self, doc_id: str, hash_listagem: str) -> None:
        """Remove os chunks e grava o tombstone na tabela de controle."""
        with self._conn.transaction():
            self._conn.execute("DELETE FROM rag_chunks WHERE doc_id = %s", (doc_id,))
            self._conn.execute(
                """
                INSERT INTO rag_documentos
                    (id, hash_listagem, deleted, chunk_count, ingested_at)
                VALUES (%s, %s, TRUE, 0, now())
                ON CONFLICT (id) DO UPDATE SET
                    hash_listagem = EXCLUDED.hash_listagem,
                    deleted = TRUE,
                    chunk_count = 0,
                    ingested_at = now()
                """,
                (doc_id, hash_listagem),
            )

    # -- diagnóstico (usado por src.rag.verificar) --------------------------

    def contagens(self) -> dict[str, int]:
        vivos = self._conn.execute(
            "SELECT count(*) FROM rag_documentos WHERE NOT deleted"
        ).fetchone()[0]
        tombstones = self._conn.execute(
            "SELECT count(*) FROM rag_documentos WHERE deleted"
        ).fetchone()[0]
        chunks = self._conn.execute("SELECT count(*) FROM rag_chunks").fetchone()[0]
        return {"docs_vivos": vivos, "tombstones": tombstones, "chunks": chunks}


class PgRetriever:
    """Leitura top-k por cosseno, com a interface que `buscar_chunks` espera."""

    def __init__(self, url: str, embeddings: "Embeddings", k: int = 4):
        self._url = url
        self._embeddings = embeddings
        self._k = k

    def invoke(self, pergunta: str) -> "list[Document]":
        from langchain_core.documents import Document

        vetor = self._embeddings.embed_query(pergunta)
        conn = _conectar(self._url)
        try:
            rows = conn.execute(
                """
                SELECT texto, secao, source, categoria
                FROM rag_chunks
                ORDER BY embedding <=> %s
                LIMIT %s
                """,
                (_vector(vetor), self._k),
            ).fetchall()
        finally:
            conn.close()
        return [
            Document(
                page_content=texto,
                metadata={"secao": secao or "", "source": source, "categoria": categoria},
            )
            for texto, secao, source, categoria in rows
        ]
