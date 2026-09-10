"""Testes do pipeline RAG (Épico 3).

Usamos embeddings *fake* determinísticos (bag-of-words com hash) para validar
o pipeline ponta a ponta sem depender de download do `sentence-transformers`
ou das docs reais em disco.
"""

from __future__ import annotations

import hashlib

import pytest
from langchain_core.documents import Document
from langchain_core.embeddings import Embeddings


# ---------------------------------------------------------------------------
# Fake embeddings — determinístico, sem dependências externas
# ---------------------------------------------------------------------------


class FakeEmbeddings(Embeddings):
    DIM = 64

    def _embed(self, text: str) -> list[float]:
        vec = [0.0] * self.DIM
        for token in text.lower().split():
            h = int(hashlib.md5(token.encode()).hexdigest(), 16)
            vec[h % self.DIM] += 1.0
        norm = sum(v * v for v in vec) ** 0.5 or 1.0
        return [v / norm for v in vec]

    def embed_documents(self, texts: list[str]) -> list[list[float]]:
        return [self._embed(t) for t in texts]

    def embed_query(self, text: str) -> list[float]:
        return self._embed(text)


def _build_documents() -> list[Document]:
    return [
        Document(
            page_content=(
                "Para configurar a integração Bling com a Azapfy, acesse "
                "Configurações > Integrações > Bling. Insira o token da API "
                "e clique em Salvar. A sincronização ocorre a cada 15 minutos."
            ),
            metadata={"source": "azapfy-web.md", "secao": "Integrações › Bling"},
        ),
        Document(
            page_content=(
                "O horário de atendimento do suporte Azapfy é de segunda a "
                "sexta, das 8h às 18h. Fora desse horário, abra um chamado "
                "via painel para que a equipe de suporte responda no próximo "
                "dia útil."
            ),
            metadata={"source": "azapfy-web.md", "secao": "Suporte › Atendimento"},
        ),
        Document(
            page_content=(
                "Para gerar etiquetas em lote, vá em Pedidos > Selecionar "
                "vários > Gerar etiquetas. O sistema suporta até 500 pedidos "
                "por vez. Filtre por transportadora antes de imprimir."
            ),
            metadata={"source": "azapfy-web.md", "secao": "Pedidos › Etiquetas"},
        ),
    ]


# ---------------------------------------------------------------------------
# load_markdown_dir
# ---------------------------------------------------------------------------


def test_load_markdown_dir_extrai_secao_e_source_e_ignora_vazios(tmp_path):
    from src.rag.ingest import load_markdown_dir

    (tmp_path / "azapfy-web.md").write_text(
        "# Plataforma Web\n\nIntro.\n\n## Módulo: Pesquisa\n\n"
        "O coração do backoffice.\n\n### 4.3 Histórico\n\nTracking da nota.\n",
        encoding="utf-8",
    )
    (tmp_path / "vazio.md").write_text("   \n", encoding="utf-8")

    docs = load_markdown_dir(tmp_path)

    # Arquivo vazio é ignorado; só o azapfy-web.md vira seções.
    assert docs, "deveria carregar ao menos uma seção"
    assert all(d.metadata["source"] == "azapfy-web.md" for d in docs)
    secoes = {d.metadata["secao"] for d in docs}
    assert any("Módulo: Pesquisa" in s for s in secoes)
    assert any("Histórico" in s for s in secoes)


def test_load_markdown_dir_sem_arquivos_levanta(tmp_path):
    from src.rag.ingest import load_markdown_dir

    with pytest.raises(FileNotFoundError):
        load_markdown_dir(tmp_path)


# ---------------------------------------------------------------------------
# Splitter
# ---------------------------------------------------------------------------


def test_split_documents_gera_chunks_e_preserva_metadata():
    from src.rag.ingest import split_documents

    docs = _build_documents()
    chunks = split_documents(docs, chunk_size=120, chunk_overlap=30)

    # chunk_size pequeno → pelo menos 1 chunk por doc, frequentemente mais.
    assert len(chunks) >= len(docs)
    assert all("secao" in c.metadata for c in chunks)
    assert all(c.metadata.get("source") == "azapfy-web.md" for c in chunks)


# ---------------------------------------------------------------------------
# Pipeline ponta a ponta: persist → reabrir → query
# ---------------------------------------------------------------------------


@pytest.fixture
def chroma_dir(tmp_path):
    return tmp_path / "chroma"


def test_persist_e_retriever_round_trip(chroma_dir):
    from src.rag.ingest import persist_chunks, split_documents
    from src.rag.retriever import get_retriever

    embeddings = FakeEmbeddings()
    chunks = split_documents(_build_documents(), chunk_size=200, chunk_overlap=40)
    persist_chunks(chunks, chroma_dir, embeddings)

    retriever = get_retriever(k=2, persist_dir=chroma_dir, embeddings=embeddings)
    resultados = retriever.invoke("integração Bling token API")

    assert len(resultados) >= 1
    # Pelo menos um dos resultados top-2 deve ser o trecho do Bling.
    assert any("Bling" in d.page_content for d in resultados)


def test_retriever_devolve_chunk_relevante_para_horario(chroma_dir):
    from src.rag.ingest import persist_chunks, split_documents
    from src.rag.retriever import get_retriever

    embeddings = FakeEmbeddings()
    chunks = split_documents(_build_documents(), chunk_size=200, chunk_overlap=40)
    persist_chunks(chunks, chroma_dir, embeddings)

    retriever = get_retriever(k=2, persist_dir=chroma_dir, embeddings=embeddings)
    resultados = retriever.invoke("qual o horário de atendimento do suporte")

    assert any("horário" in d.page_content.lower() for d in resultados)


# ---------------------------------------------------------------------------
# Tool consultar_base_conhecimento
# ---------------------------------------------------------------------------


def test_consultar_base_conhecimento_pergunta_vazia():
    from src.tools.rag_tool import consultar_base_conhecimento

    out = consultar_base_conhecimento.invoke({"pergunta": "  "})
    assert out["encontrado"] is False
    assert out["total"] == 0
    assert "erro" in out


def test_consultar_base_conhecimento_retorna_secao_e_source(monkeypatch, chroma_dir):
    """A tool expõe `secao` + `source` de cada chunk para a citação (LLM09)."""
    from src.rag.ingest import persist_chunks, split_documents
    from src.rag.retriever import get_retriever
    from src.tools import rag_tool

    embeddings = FakeEmbeddings()
    chunks = split_documents(_build_documents(), chunk_size=200, chunk_overlap=40)
    persist_chunks(chunks, chroma_dir, embeddings)

    # Patch o get_retriever interno da tool para usar nosso Chroma de teste.
    monkeypatch.setattr(
        rag_tool,
        "get_retriever",
        lambda k=4: get_retriever(k=k, persist_dir=chroma_dir, embeddings=embeddings),
    )

    out = rag_tool.consultar_base_conhecimento.invoke(
        {"pergunta": "como configurar integração Bling"}
    )
    assert out["encontrado"] is True
    assert out["total"] >= 1
    primeiro = out["chunks"][0]
    assert primeiro["source"] == "azapfy-web.md"
    assert isinstance(primeiro["secao"], str) and primeiro["secao"]
    assert isinstance(primeiro["texto"], str) and primeiro["texto"]


def test_consultar_base_conhecimento_propaga_falha_do_retriever(monkeypatch):
    from src.tools import rag_tool

    def _retriever_quebrado(k=4):
        raise RuntimeError("simulando ChromaDB indisponível")

    monkeypatch.setattr(rag_tool, "get_retriever", _retriever_quebrado)

    out = rag_tool.consultar_base_conhecimento.invoke({"pergunta": "qualquer"})
    assert out["encontrado"] is False
    assert "erro" in out
